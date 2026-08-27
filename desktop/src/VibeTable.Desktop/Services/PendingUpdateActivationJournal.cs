using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record UpdateProcessIdentity(
    int ProcessId,
    DateTimeOffset StartedAtUtc);

internal static class PendingUpdateActivationJournal
{
    private const int SchemaVersion = 1;
    private const int MaximumPointerBytes = 16 * 1024;
    private const string PreparedState = "prepared";
    private const string ConfirmedState = "confirmed";
    private const string PointerFileName = ".VibeTable.Next.update-pending.json";
    private const string LockFileName = ".VibeTable.Next.update-pending.lock";
    private static readonly Encoding Utf8NoBom =
        new UTF8Encoding(encoderShouldEmitUTF8Identifier: false);
    private static readonly JsonSerializerOptions WebJson =
        new(JsonSerializerDefaults.Web);
    private static readonly HashSet<string> PointerProperties =
        new(StringComparer.Ordinal)
        {
            "schemaVersion",
            "state",
            "targetRoot",
            "stagingRoot",
            "currentVersion",
            "targetVersion",
            "token",
            "smokeTest",
            "updaterProcessId",
            "updaterStartedAtUtc",
            "createdAtUtc",
            "confirmedAt",
        };

    internal static string GetPointerPath(string runningRoot) =>
        Path.Combine(GetTargetParent(runningRoot), PointerFileName);

    internal static UpdateProcessIdentity CurrentProcessIdentity()
    {
        using Process current = Process.GetCurrentProcess();
        return new UpdateProcessIdentity(
            current.Id,
            new DateTimeOffset(current.StartTime.ToUniversalTime(), TimeSpan.Zero));
    }

    internal static void Publish(UpdateApplyPlan plan, UpdateProcessIdentity updater)
    {
        if (updater.ProcessId <= 0)
        {
            throw InvalidPointer("更新激活进程身份无效。");
        }
        UpdateApplyPlan normalized = NormalizePlanPaths(plan);
        ValidatePlanShape(normalized);
        string pointerPath = GetPointerPath(normalized.TargetRoot);
        string lockPath = GetLockPath(normalized.TargetRoot);
        using FileStream claim = AcquireLock(lockPath);
        try
        {
            if (File.Exists(pointerPath) || Directory.Exists(pointerPath))
            {
                UpdateProcessCommand.RejectReparsePoint(pointerPath);
                throw new ReleaseUpdateException(
                    "已有尚未完成的更新激活记录，已拒绝覆盖。",
                    "UPDATE_ACTIVATION_PENDING");
            }
            var pending = new PendingUpdateActivation(
                SchemaVersion,
                PreparedState,
                normalized.TargetRoot,
                normalized.StagingRoot,
                normalized.CurrentVersion,
                normalized.TargetVersion,
                normalized.Token,
                normalized.SmokeTest,
                updater.ProcessId,
                updater.StartedAtUtc,
                DateTimeOffset.UtcNow,
                null);
            WriteNew(pointerPath, pending);
        }
        finally
        {
            ReleaseLock(claim, lockPath);
        }
    }

    internal static bool TryAbandonPrepared(
        UpdateApplyPlan plan,
        UpdateProcessIdentity updater)
    {
        UpdateApplyPlan normalized;
        string pointerPath;
        string lockPath;
        try
        {
            normalized = NormalizePlanPaths(plan);
            ValidatePlanShape(normalized);
            pointerPath = GetPointerPath(normalized.TargetRoot);
            lockPath = GetLockPath(normalized.TargetRoot);
        }
        catch (Exception)
        {
            return false;
        }
        FileStream? claim = null;
        try
        {
            claim = AcquireLock(lockPath);
            if (!File.Exists(pointerPath))
            {
                return false;
            }
            PendingUpdateActivation pending = Read(pointerPath);
            _ = ValidatePointerShape(
                pending,
                normalized.TargetRoot,
                null,
                CleanupArgumentsStatus.None);
            if (pending.State != PreparedState
                || InstalledPackageIdentity.Read(normalized.TargetRoot).Version
                    != pending.CurrentVersion
                || !MatchesPlan(pending, normalized)
                || !MatchesUpdater(pending, updater))
            {
                return false;
            }
            UpdateProcessCommand.RejectReparsePoint(pointerPath);
            File.Delete(pointerPath);
            return true;
        }
        catch (Exception)
        {
            return false;
        }
        finally
        {
            if (claim is not null)
            {
                ReleaseLock(claim, lockPath);
            }
        }
    }

    internal static bool TryLoad(
        IReadOnlyList<string> arguments,
        string runningRoot,
        out IUpdateActivationGate? gate,
        Func<UpdateProcessIdentity, Task<bool>>? waitForUpdaterExit = null,
        Func<UpdateApplyPlan, bool>? cleanupStage = null)
    {
        gate = null;
        CleanupArgumentsStatus argumentStatus = TryParseCleanupArguments(
            arguments,
            out CleanupArguments? cleanupArguments);
        if (argumentStatus == CleanupArgumentsStatus.Invalid)
        {
            return false;
        }
        string normalizedRunningRoot;
        string pointerPath;
        try
        {
            normalizedRunningRoot = ReleasePackageStager.NormalizeDirectory(runningRoot);
            string targetParent = GetTargetParent(normalizedRunningRoot);
            UpdateProcessCommand.RejectReparsePoint(targetParent);
            UpdateProcessCommand.RejectReparsePoint(normalizedRunningRoot);
            pointerPath = Path.Combine(targetParent, PointerFileName);
            if (!File.Exists(pointerPath))
            {
                return false;
            }
            PendingUpdateActivation pending = Read(pointerPath);
            ValidatedPending validated = ValidatePending(
                pending,
                normalizedRunningRoot,
                cleanupArguments,
                argumentStatus);
            var activationGate = new JournalActivationGate(
                pending,
                validated.Plan,
                validated.FailedApply,
                pointerPath,
                waitForUpdaterExit ?? WaitForUpdaterExitAsync,
                cleanupStage ?? UpdateProcessCommand.TryCleanupStage);
            gate = activationGate;
            if (pending.State == ConfirmedState)
            {
                activationGate.ResumeConfirmedCleanup();
            }
            return true;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private static ValidatedPending ValidatePending(
        PendingUpdateActivation pending,
        string runningRoot,
        CleanupArguments? arguments,
        CleanupArgumentsStatus argumentStatus)
    {
        PointerShape pointer = ValidatePointerShape(
            pending,
            runningRoot,
            arguments,
            argumentStatus);
        UpdateApplyPlan shape = pointer.Plan;
        bool stageMissing = pointer.StageMissing;
        string targetRoot = shape.TargetRoot;
        string stagingRoot = shape.StagingRoot;
        string installedVersion = InstalledPackageIdentity.Read(targetRoot).Version;
        if (pending.State == ConfirmedState)
        {
            if (installedVersion != pending.TargetVersion)
            {
                throw InvalidPointer("已确认更新的当前安装身份不一致。");
            }
            return new ValidatedPending(shape, StageAlreadyAbsent: stageMissing);
        }
        if (stageMissing)
        {
            throw InvalidPointer("未确认更新的阶段目录缺失。");
        }
        string planPath = Path.Combine(stagingRoot, "update-plan.json");
        UpdateApplyPlan plan = UpdateProcessCommand.ReadAndValidatePlan(
            planPath,
            requireCurrentSource: false,
            targetAlreadyUpdated: installedVersion == pending.TargetVersion);
        if (!MatchesPlan(pending, plan))
        {
            throw InvalidPointer("更新计划与持久化激活记录不一致。");
        }
        bool failedApply = installedVersion == pending.CurrentVersion;
        if (!failedApply && installedVersion != pending.TargetVersion)
        {
            throw InvalidPointer("当前安装身份与更新激活记录不一致。");
        }
        return new ValidatedPending(
            plan,
            StageAlreadyAbsent: false,
            FailedApply: failedApply);
    }

    private static PointerShape ValidatePointerShape(
        PendingUpdateActivation pending,
        string runningRoot,
        CleanupArguments? arguments,
        CleanupArgumentsStatus argumentStatus)
    {
        if (pending.SchemaVersion != SchemaVersion
            || pending.State is not (PreparedState or ConfirmedState)
            || pending.UpdaterProcessId <= 0
            || !StableReleaseVersion.TryParse(
                pending.CurrentVersion,
                out StableReleaseVersion currentVersion)
            || !StableReleaseVersion.TryParse(
                pending.TargetVersion,
                out StableReleaseVersion targetVersion)
            || targetVersion <= currentVersion
            || pending.Token.Length != 64
            || pending.Token.Any(character =>
                !Uri.IsHexDigit(character) || char.IsUpper(character))
            || (pending.State == PreparedState && pending.ConfirmedAt is not null)
            || (pending.State == ConfirmedState && pending.ConfirmedAt is null))
        {
            throw InvalidPointer("更新激活记录格式无效。");
        }
        string targetRoot = ReleasePackageStager.NormalizeDirectory(pending.TargetRoot);
        string stagingRoot = ReleasePackageStager.NormalizeDirectory(pending.StagingRoot);
        if (!PathsEqual(targetRoot, runningRoot))
        {
            throw InvalidPointer("更新激活目标与当前程序不一致。");
        }
        var shape = new UpdateApplyPlan(
            1,
            targetRoot,
            Path.Combine(stagingRoot, "package", "VibeTable"),
            stagingRoot,
            pending.UpdaterProcessId,
            pending.CurrentVersion,
            pending.TargetVersion,
            pending.Token,
            pending.SmokeTest);
        bool stageMissing = !Directory.Exists(stagingRoot) && !File.Exists(stagingRoot);
        ValidatePlanShape(shape, allowMissingStagingRoot: stageMissing);
        if (argumentStatus == CleanupArgumentsStatus.Valid)
        {
            if (arguments is null
                || !PathsEqual(arguments.StagingRoot, stagingRoot)
                || arguments.UpdaterProcessId != pending.UpdaterProcessId
                || !FixedTimeTokenEquals(pending.Token, arguments.Token)
                || arguments.SmokeTest != pending.SmokeTest)
            {
                throw InvalidPointer("更新激活参数与持久化记录不一致。");
            }
        }
        return new PointerShape(shape, stageMissing);
    }

    private static void ValidatePlanShape(
        UpdateApplyPlan plan,
        bool allowMissingStagingRoot = false)
    {
        string targetRoot = ReleasePackageStager.NormalizeDirectory(plan.TargetRoot);
        string stagingRoot = ReleasePackageStager.NormalizeDirectory(plan.StagingRoot);
        string sourceRoot = ReleasePackageStager.NormalizeDirectory(plan.SourceRoot);
        string targetParent = GetTargetParent(targetRoot);
        DirectoryInfo? stagingParent = Directory.GetParent(
            stagingRoot.TrimEnd(Path.DirectorySeparatorChar));
        if (stagingParent is null
            || !PathsEqual(stagingParent.FullName, targetParent)
            || !Path.GetFileName(stagingRoot.TrimEnd(Path.DirectorySeparatorChar))
                .StartsWith(".VibeTable.Next.update-", StringComparison.Ordinal)
            || !PathsEqual(
                sourceRoot,
                Path.Combine(stagingRoot, "package", "VibeTable"))
            || plan.Token.Length != 64
            || plan.Token.Any(character =>
                !Uri.IsHexDigit(character) || char.IsUpper(character)))
        {
            throw InvalidPointer("更新激活记录的目录或身份边界无效。");
        }
        UpdateProcessCommand.RejectReparsePoint(targetParent);
        UpdateProcessCommand.RejectReparsePoint(targetRoot);
        if (!allowMissingStagingRoot)
        {
            UpdateProcessCommand.RejectReparsePoint(stagingRoot);
        }
    }

    private static PendingUpdateActivation Read(string pointerPath)
    {
        UpdateProcessCommand.RejectReparsePoint(pointerPath);
        var info = new FileInfo(pointerPath);
        if (info.Length <= 0 || info.Length > MaximumPointerBytes)
        {
            throw InvalidPointer("更新激活记录大小无效。");
        }
        byte[] bytes = File.ReadAllBytes(pointerPath);
        if (bytes.AsSpan().StartsWith(new byte[] { 0xef, 0xbb, 0xbf }))
        {
            throw InvalidPointer("更新激活记录编码无效。");
        }
        using JsonDocument document = JsonDocument.Parse(bytes);
        JsonElement root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object)
        {
            throw InvalidPointer("更新激活记录必须是 JSON 对象。");
        }
        string[] names = root.EnumerateObject().Select(property => property.Name).ToArray();
        if (names.Length != PointerProperties.Count
            || names.Distinct(StringComparer.Ordinal).Count() != names.Length
            || names.Any(name => !PointerProperties.Contains(name)))
        {
            throw InvalidPointer("更新激活记录字段不完整或包含未知字段。");
        }
        PendingUpdateActivation? pending = JsonSerializer.Deserialize<PendingUpdateActivation>(
            bytes,
            WebJson);
        return pending ?? throw InvalidPointer("更新激活记录格式无效。");
    }

    private static void WriteNew(string pointerPath, PendingUpdateActivation pending)
    {
        string temporary = TemporaryPath(pointerPath);
        try
        {
            WriteTemporary(temporary, pending);
            File.Move(temporary, pointerPath, overwrite: false);
        }
        finally
        {
            TryDeleteExactFile(temporary);
        }
    }

    private static void Replace(string pointerPath, PendingUpdateActivation pending)
    {
        string temporary = TemporaryPath(pointerPath);
        try
        {
            WriteTemporary(temporary, pending);
            UpdateProcessCommand.RejectReparsePoint(pointerPath);
            File.Replace(temporary, pointerPath, null, ignoreMetadataErrors: true);
        }
        finally
        {
            TryDeleteExactFile(temporary);
        }
    }

    private static void WriteTemporary(string path, PendingUpdateActivation pending)
    {
        using var stream = new FileStream(
            path,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            bufferSize: 4096,
            FileOptions.WriteThrough);
        using var writer = new StreamWriter(stream, Utf8NoBom, bufferSize: 4096, leaveOpen: true);
        writer.Write(JsonSerializer.Serialize(pending, WebJson));
        writer.Flush();
        stream.Flush(flushToDisk: true);
    }

    private static string TemporaryPath(string pointerPath) =>
        pointerPath + $".tmp-{Environment.ProcessId}-{Guid.NewGuid():N}";

    private static string GetLockPath(string runningRoot) =>
        Path.Combine(GetTargetParent(runningRoot), LockFileName);

    private static string GetTargetParent(string runningRoot)
    {
        string targetRoot = ReleasePackageStager.NormalizeDirectory(runningRoot);
        DirectoryInfo? parent = Directory.GetParent(
            targetRoot.TrimEnd(Path.DirectorySeparatorChar));
        if (parent is null)
        {
            throw InvalidPointer("无法确定更新激活记录目录。");
        }
        return ReleasePackageStager.NormalizeDirectory(parent.FullName);
    }

    private static FileStream AcquireLock(string lockPath)
    {
        if (File.Exists(lockPath) || Directory.Exists(lockPath))
        {
            UpdateProcessCommand.RejectReparsePoint(lockPath);
        }
        return new FileStream(
            lockPath,
            FileMode.OpenOrCreate,
            FileAccess.ReadWrite,
            FileShare.None,
            bufferSize: 4096,
            FileOptions.WriteThrough);
    }

    private static void ReleaseLock(FileStream claim, string lockPath)
    {
        claim.Dispose();
        TryDeleteExactFile(lockPath);
    }

    private static void TryDeleteExactFile(string path)
    {
        try
        {
            if (File.Exists(path))
            {
                UpdateProcessCommand.RejectReparsePoint(path);
                File.Delete(path);
            }
        }
        catch (IOException)
        {
            // A regular stale lock or temporary file is recoverable.
        }
        catch (UnauthorizedAccessException)
        {
            // Keep the exact path for diagnostics.
        }
    }

    private static CleanupArgumentsStatus TryParseCleanupArguments(
        IReadOnlyList<string> arguments,
        out CleanupArguments? result)
    {
        result = null;
        string? stagingRoot = null;
        string? updaterProcessIdText = null;
        string? token = null;
        bool smokeTest = false;
        bool sawUpdateArgument = false;
        for (int index = 0; index < arguments.Count; index++)
        {
            switch (arguments[index])
            {
                case "--cleanup-update":
                    sawUpdateArgument = true;
                    if (stagingRoot is not null || index + 1 >= arguments.Count)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    stagingRoot = arguments[++index];
                    break;
                case "--updater-pid":
                    sawUpdateArgument = true;
                    if (updaterProcessIdText is not null || index + 1 >= arguments.Count)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    updaterProcessIdText = arguments[++index];
                    break;
                case "--update-token":
                    sawUpdateArgument = true;
                    if (token is not null || index + 1 >= arguments.Count)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    token = arguments[++index];
                    break;
                case "--self-update-smoke":
                    sawUpdateArgument = true;
                    if (smokeTest)
                    {
                        return CleanupArgumentsStatus.Invalid;
                    }
                    smokeTest = true;
                    break;
            }
        }
        if (!sawUpdateArgument)
        {
            return CleanupArgumentsStatus.None;
        }
        if (stagingRoot is null
            || token is null
            || !int.TryParse(updaterProcessIdText, out int updaterProcessId)
            || updaterProcessId <= 0)
        {
            return CleanupArgumentsStatus.Invalid;
        }
        result = new CleanupArguments(stagingRoot, updaterProcessId, token, smokeTest);
        return CleanupArgumentsStatus.Valid;
    }

    internal static async Task<bool> WaitForUpdaterExitAsync(UpdateProcessIdentity expected)
    {
        if (expected.ProcessId == Environment.ProcessId)
        {
            return false;
        }
        try
        {
            using Process updater = Process.GetProcessById(expected.ProcessId);
            DateTimeOffset actualStartedAt;
            try
            {
                actualStartedAt = new DateTimeOffset(
                    updater.StartTime.ToUniversalTime(),
                    TimeSpan.Zero);
            }
            catch (Exception exception) when (exception is InvalidOperationException
                                               or System.ComponentModel.Win32Exception)
            {
                return false;
            }
            if (actualStartedAt != expected.StartedAtUtc)
            {
                // The PID was reused; the exact updater process has exited.
                return true;
            }
            using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(2));
            await updater.WaitForExitAsync(timeout.Token).ConfigureAwait(false);
            return true;
        }
        catch (ArgumentException)
        {
            return true;
        }
        catch (OperationCanceledException)
        {
            return false;
        }
    }

    private static UpdateApplyPlan NormalizePlanPaths(UpdateApplyPlan plan) => plan with
    {
        TargetRoot = ReleasePackageStager.NormalizeDirectory(plan.TargetRoot),
        SourceRoot = ReleasePackageStager.NormalizeDirectory(plan.SourceRoot),
        StagingRoot = ReleasePackageStager.NormalizeDirectory(plan.StagingRoot),
    };

    private static bool PathsEqual(string left, string right) =>
        string.Equals(
            ReleasePackageStager.NormalizeDirectory(left),
            ReleasePackageStager.NormalizeDirectory(right),
            StringComparison.OrdinalIgnoreCase);

    private static bool FixedTimeTokenEquals(string expected, string? actual)
    {
        if (actual is null || expected.Length != actual.Length)
        {
            return false;
        }
        return CryptographicOperations.FixedTimeEquals(
            Encoding.ASCII.GetBytes(expected),
            Encoding.ASCII.GetBytes(actual));
    }

    private static bool MatchesUpdater(
        PendingUpdateActivation pending,
        UpdateProcessIdentity updater) =>
        pending.UpdaterProcessId == updater.ProcessId
        && pending.UpdaterStartedAtUtc == updater.StartedAtUtc;

    private static bool MatchesPlan(
        PendingUpdateActivation pending,
        UpdateApplyPlan plan) =>
        PathsEqual(pending.TargetRoot, plan.TargetRoot)
        && PathsEqual(pending.StagingRoot, plan.StagingRoot)
        && pending.CurrentVersion == plan.CurrentVersion
        && pending.TargetVersion == plan.TargetVersion
        && FixedTimeTokenEquals(pending.Token, plan.Token)
        && pending.SmokeTest == plan.SmokeTest;

    private static ReleaseUpdateException InvalidPointer(string message) =>
        new(message, "UPDATE_ACTIVATION_INVALID");

    private enum CleanupArgumentsStatus
    {
        None,
        Valid,
        Invalid,
    }

    private sealed record CleanupArguments(
        string StagingRoot,
        int UpdaterProcessId,
        string Token,
        bool SmokeTest);

    private sealed record ValidatedPending(
        UpdateApplyPlan Plan,
        bool StageAlreadyAbsent,
        bool FailedApply = false);

    private sealed record PointerShape(
        UpdateApplyPlan Plan,
        bool StageMissing);

    private sealed record PendingUpdateActivation(
        int SchemaVersion,
        string State,
        string TargetRoot,
        string StagingRoot,
        string CurrentVersion,
        string TargetVersion,
        string Token,
        bool SmokeTest,
        int UpdaterProcessId,
        DateTimeOffset UpdaterStartedAtUtc,
        DateTimeOffset CreatedAtUtc,
        DateTimeOffset? ConfirmedAt);

    private sealed class JournalActivationGate(
        PendingUpdateActivation pending,
        UpdateApplyPlan plan,
        bool failedApply,
        string pointerPath,
        Func<UpdateProcessIdentity, Task<bool>> waitForUpdaterExit,
        Func<UpdateApplyPlan, bool> cleanupStage) : IUpdateActivationGate
    {
        private readonly TaskCompletionSource<bool> _completion =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _confirmationStarted;

        public bool ExitAfterConfirmation => plan.SmokeTest;

        public Task<bool> Completion => _completion.Task;

        public void ConfirmActivation()
        {
            if (Interlocked.Exchange(ref _confirmationStarted, 1) != 0)
            {
                return;
            }
            _ = Task.Run(() => CompleteAsync(confirm: true));
        }

        public void ResumeConfirmedCleanup()
        {
            if (Interlocked.Exchange(ref _confirmationStarted, 1) != 0)
            {
                return;
            }
            _ = Task.Run(() => CompleteAsync(confirm: false));
        }

        private async Task CompleteAsync(bool confirm)
        {
            string lockPath = GetLockPath(plan.TargetRoot);
            FileStream? claim = null;
            try
            {
                var updater = new UpdateProcessIdentity(
                    pending.UpdaterProcessId,
                    pending.UpdaterStartedAtUtc);
                if (!await waitForUpdaterExit(updater).ConfigureAwait(false))
                {
                    _completion.TrySetResult(false);
                    return;
                }
                claim = AcquireLock(lockPath);
                PendingUpdateActivation current = Read(pointerPath);
                ValidatedPending currentValidated = ValidatePending(
                    current,
                    plan.TargetRoot,
                    null,
                    CleanupArgumentsStatus.None);
                if (!SameAttempt(current, pending)
                    || currentValidated.Plan != plan
                    || currentValidated.FailedApply != failedApply)
                {
                    _completion.TrySetResult(false);
                    return;
                }
                if (currentValidated.FailedApply)
                {
                    if (current.State != PreparedState)
                    {
                        _completion.TrySetResult(false);
                        return;
                    }
                    UpdateProcessCommand.RejectReparsePoint(pointerPath);
                    File.Delete(pointerPath);
                    _completion.TrySetResult(true);
                    return;
                }
                if (confirm)
                {
                    if (current.State != PreparedState)
                    {
                        _completion.TrySetResult(false);
                        return;
                    }
                    current = current with
                    {
                        State = ConfirmedState,
                        ConfirmedAt = DateTimeOffset.UtcNow,
                    };
                    Replace(pointerPath, current);
                }
                else if (current.State != ConfirmedState)
                {
                    _completion.TrySetResult(false);
                    return;
                }
                if (!currentValidated.StageAlreadyAbsent && !cleanupStage(plan))
                {
                    _completion.TrySetResult(false);
                    return;
                }
                if (plan.SmokeTest)
                {
                    WriteSmokeCompletion(plan);
                }
                UpdateProcessCommand.RejectReparsePoint(pointerPath);
                File.Delete(pointerPath);
                _completion.TrySetResult(true);
            }
            catch (Exception exception)
            {
                WriteActivationFailureEvidence(plan.StagingRoot, exception);
                _completion.TrySetResult(false);
            }
            finally
            {
                if (claim is not null)
                {
                    ReleaseLock(claim, lockPath);
                }
            }
        }

        private static bool SameAttempt(
            PendingUpdateActivation left,
            PendingUpdateActivation right) =>
            PathsEqual(left.TargetRoot, right.TargetRoot)
            && PathsEqual(left.StagingRoot, right.StagingRoot)
            && left.CurrentVersion == right.CurrentVersion
            && left.TargetVersion == right.TargetVersion
            && FixedTimeTokenEquals(left.Token, right.Token)
            && left.SmokeTest == right.SmokeTest
            && left.UpdaterProcessId == right.UpdaterProcessId
            && left.UpdaterStartedAtUtc == right.UpdaterStartedAtUtc;
    }

    private static void WriteSmokeCompletion(UpdateApplyPlan plan)
    {
        string path = Path.Combine(
            plan.TargetRoot,
            UpdateProcessCommand.SmokeCompletionFileName);
        string temporary = TemporaryPath(path);
        try
        {
            using (var stream = new FileStream(
                       temporary,
                       FileMode.CreateNew,
                       FileAccess.Write,
                       FileShare.None,
                       bufferSize: 4096,
                       FileOptions.WriteThrough))
            {
                JsonSerializer.Serialize(
                    stream,
                    new SmokeCompletionEvidence(
                        plan.Token,
                        plan.TargetVersion,
                        Environment.ProcessId,
                        DateTimeOffset.UtcNow),
                    WebJson);
                stream.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally
        {
            TryDeleteExactFile(temporary);
        }
    }

    private static void WriteActivationFailureEvidence(string stageRoot, Exception exception)
    {
        try
        {
            string path = stageRoot.TrimEnd(
                Path.DirectorySeparatorChar,
                Path.AltDirectorySeparatorChar) + ".activation-error.txt";
            File.WriteAllText(
                path,
                $"[{DateTimeOffset.UtcNow:O}] {exception}{Environment.NewLine}");
        }
        catch (Exception)
        {
            // Diagnostics must never replace the activation result.
        }
    }

    private sealed record SmokeCompletionEvidence(
        string Token,
        string TargetVersion,
        int ProcessId,
        DateTimeOffset ConfirmedAt);
}
