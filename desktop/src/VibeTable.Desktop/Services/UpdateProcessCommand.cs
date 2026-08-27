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

internal static class UpdatePackageOwnedEntries
{
    internal static IReadOnlyList<string> InInstallOrder { get; } =
        ["resources", "release.json", "VibeTable.Next.exe"];

    internal static bool AllExistAt(string root) => InInstallOrder.All(name =>
        File.Exists(Path.Combine(root, name))
        || Directory.Exists(Path.Combine(root, name)));

    internal static bool IsFullyRestored(IReadOnlyList<UpdateOwnedEntryLedger> ledger) =>
        ledger.Count == InInstallOrder.Count
        && InInstallOrder.All(name => ledger.SingleOrDefault(
            entry => entry.Name == name)?.Phase == "restored");

    internal static void ValidatePackageAt(string root, string expectedVersion)
    {
        try
        {
            if (!AllExistAt(root))
            {
                throw new InvalidDataException("Owned package entry is missing.");
            }
            foreach (string name in InInstallOrder)
            {
                string path = Path.Combine(root, name);
                if (Directory.Exists(path))
                {
                    UpdateProcessCommand.RejectReparsePointsRecursively(path);
                }
                else
                {
                    UpdateProcessCommand.RejectReparsePoint(path);
                }
            }
            if (InstalledPackageIdentity.Read(root).Version != expectedVersion)
            {
                throw new InvalidDataException("Installed package version drifted.");
            }
        }
        catch (Exception exception) when (exception is
            IOException or UnauthorizedAccessException or ReleaseUpdateException
            or InvalidDataException)
        {
            throw new ReleaseUpdateException(
                "当前安装形状无法作为完整回退基线。",
                "UPDATE_CURRENT_PACKAGE_INVALID",
                exception);
        }
    }
}

internal interface IUpdateActivationGate
{
    bool ExitAfterConfirmation { get; }

    Task<bool> Completion { get; }

    void ConfirmActivation();

    void FailActivation();
}

internal static class UpdateProcessCommand
{
    internal const string SmokeTokenEnvironmentVariable =
        "VIBETABLE_SELF_UPDATE_SMOKE_TOKEN";
    internal const string SmokeCompletionFileName =
        ".self-update-smoke-complete";
    internal const string SmokeProcessFileName =
        ".self-update-smoke-process.json";
    private static readonly JsonSerializerOptions WebJson =
        new(JsonSerializerDefaults.Web);
    private static readonly Encoding Utf8NoBom =
        new UTF8Encoding(encoderShouldEmitUTF8Identifier: false);
    public static bool TryApply(
        IReadOnlyList<string> arguments,
        out int exitCode,
        out string? errorMessage)
    {
        exitCode = 0;
        errorMessage = null;
        if (arguments is ["--rollback-update", string targetRoot,
                "--worker-nonce", string workerNonce])
        {
            try
            {
                PendingUpdateActivationJournal.RunRollbackWorker(
                    targetRoot,
                    workerNonce,
                    PendingUpdateActivationJournal.CurrentProcessIdentity());
            }
            catch (Exception)
            {
                exitCode = 1;
                // The retained journal is the worker's stable diagnostic
                // surface; a process-out worker must never display WPF UI.
                errorMessage = null;
            }
            return true;
        }
        if (arguments.Count != 2 || arguments[0] != "--apply-update") return false;

        UpdateApplyPlan? plan = null;
        UpdateProcessIdentity? updaterIdentity = null;
        try
        {
            plan = ReadAndValidatePlan(arguments[1], requireCurrentSource: true);
            updaterIdentity = PendingUpdateActivationJournal.CurrentProcessIdentity();
            PendingUpdateActivationJournal.Publish(plan, updaterIdentity);
            WaitForParentExit(plan.ParentProcessId);
            ApplyPackageOwnedFiles(plan);
            new UpdateRecoveryWatchdog(new WindowsUpdateRecoveryProcessAdapter())
                .RunUpdatedPackageAsync(plan, CancellationToken.None)
                .GetAwaiter()
                .GetResult();
        }
        catch (Exception exception)
        {
            exitCode = 1;
            errorMessage = exception.Message;
            if (plan is not null)
            {
                WriteFailureEvidence(plan.StagingRoot, exception);
                if (updaterIdentity is not null)
                {
                    _ = TryRestartAfterApplyFailure(
                        plan,
                        updaterIdentity,
                        TryRestartExistingApplication);
                }
            }
        }
        return true;
    }

    internal static bool TryRestartAfterApplyFailure(
        UpdateApplyPlan plan,
        UpdateProcessIdentity updaterIdentity,
        Action<string> restart)
    {
        ArgumentNullException.ThrowIfNull(restart);
        if (!PendingUpdateActivationJournal.TryAbandonPrepared(plan, updaterIdentity))
        {
            return false;
        }
        restart(plan.TargetRoot);
        return true;
    }

    internal static bool TryCreateActivationGate(
        IReadOnlyList<string> arguments,
        out IUpdateActivationGate? gate,
        string? runningRoot = null,
        Func<UpdateProcessIdentity, Task<bool>>? waitForUpdaterExit = null,
        Func<UpdateApplyPlan, bool>? cleanupStage = null)
    {
        return PendingUpdateActivationJournal.TryLoad(
            arguments,
            runningRoot ?? AppContext.BaseDirectory,
            out gate,
            waitForUpdaterExit,
            cleanupStage);
    }

    internal static UpdateApplyPlan ReadAndValidatePlan(
        string planPath,
        bool requireCurrentSource = false,
        bool targetAlreadyUpdated = false)
    {
        string fullPlanPath = Path.GetFullPath(planPath);
        UpdateApplyPlan? plan;
        using (FileStream stream = File.OpenRead(fullPlanPath))
        {
            plan = JsonSerializer.Deserialize<UpdateApplyPlan>(
                stream,
                new JsonSerializerOptions { PropertyNameCaseInsensitive = true });
        }
        if (plan is null
            || plan.SchemaVersion != 1
            || plan.ParentProcessId <= 0
            || string.IsNullOrWhiteSpace(plan.TargetRoot)
            || string.IsNullOrWhiteSpace(plan.SourceRoot)
            || string.IsNullOrWhiteSpace(plan.StagingRoot)
            || string.IsNullOrWhiteSpace(plan.CurrentVersion)
            || string.IsNullOrWhiteSpace(plan.TargetVersion)
            || plan.Token is null
            || plan.Token.Length != 64
            || plan.Token.Any(character => !Uri.IsHexDigit(character))
            || (plan.SmokeTest
                && !FixedTimeTokenEquals(
                    plan.Token,
                    Environment.GetEnvironmentVariable(SmokeTokenEnvironmentVariable))))
        {
            throw new ReleaseUpdateException(
                "更新计划格式无效。",
                "UPDATE_PLAN_INVALID");
        }

        string targetRoot = NormalizeDirectory(plan.TargetRoot);
        string sourceRoot = NormalizeDirectory(plan.SourceRoot);
        string stageRoot = NormalizeDirectory(plan.StagingRoot);
        DirectoryInfo? targetParent = Directory.GetParent(
            targetRoot.TrimEnd(Path.DirectorySeparatorChar));
        DirectoryInfo? stageParent = Directory.GetParent(
            stageRoot.TrimEnd(Path.DirectorySeparatorChar));
        string expectedSource = NormalizeDirectory(
            Path.Combine(stageRoot, "package", "VibeTable"));
        string expectedPlan = Path.Combine(stageRoot, "update-plan.json");
        if (targetParent is null
            || stageParent is null
            || !PathsEqual(targetParent.FullName, stageParent.FullName)
            || !Path.GetFileName(stageRoot.TrimEnd(Path.DirectorySeparatorChar))
                .StartsWith(".VibeTable.Next.update-", StringComparison.Ordinal)
            || !PathsEqual(sourceRoot, expectedSource)
            || !PathsEqual(fullPlanPath, expectedPlan)
            || PathsEqual(targetRoot, sourceRoot)
            || (requireCurrentSource && !PathsEqual(sourceRoot, AppContext.BaseDirectory)))
        {
            throw new ReleaseUpdateException(
                "更新计划的目录边界无效。",
                "UPDATE_PLAN_PATH_INVALID");
        }

        InstalledPackageIdentity target = InstalledPackageIdentity.Read(targetRoot);
        InstalledPackageIdentity source = InstalledPackageIdentity.Read(sourceRoot);
        string expectedTargetVersion = targetAlreadyUpdated
            ? plan.TargetVersion
            : plan.CurrentVersion;
        if (target.Version != expectedTargetVersion
            || source.Version != plan.TargetVersion
            || !StableReleaseVersion.TryParse(plan.CurrentVersion, out StableReleaseVersion current)
            || !StableReleaseVersion.TryParse(plan.TargetVersion, out StableReleaseVersion next)
            || next <= current)
        {
            throw new ReleaseUpdateException(
                "更新计划中的版本身份不一致。",
                "UPDATE_PLAN_IDENTITY_MISMATCH");
        }
        ReleasePackageStager.ValidatePackageRoot(sourceRoot, plan.TargetVersion);
        return plan with
        {
            TargetRoot = targetRoot,
            SourceRoot = sourceRoot,
            StagingRoot = stageRoot,
        };
    }

    internal static void ApplyPackageOwnedFiles(UpdateApplyPlan plan)
    {
        string targetRoot = NormalizeDirectory(plan.TargetRoot);
        string sourceRoot = NormalizeDirectory(plan.SourceRoot);
        string backupRoot = NormalizeDirectory(Path.Combine(plan.StagingRoot, "backup"));
        RejectReparsePointChainsToVolumeRoot(
            targetRoot,
            plan.StagingRoot,
            sourceRoot,
            backupRoot);
        UpdatePackageOwnedEntries.ValidatePackageAt(targetRoot, plan.CurrentVersion);
        if (Directory.Exists(backupRoot) || File.Exists(backupRoot))
        {
            throw new ReleaseUpdateException(
                "更新备份目录已存在，已拒绝覆盖。",
                "UPDATE_BACKUP_ALREADY_EXISTS");
        }
        Directory.CreateDirectory(backupRoot);

        var moved = new List<string>();
        var copied = new List<string>();
        try
        {
            foreach (string name in UpdatePackageOwnedEntries.InInstallOrder)
            {
                string target = Path.Combine(targetRoot, name);
                string backup = Path.Combine(backupRoot, name);
                if (File.Exists(target))
                {
                    RejectReparsePoint(target);
                    File.Move(target, backup);
                    moved.Add(name);
                }
                else if (Directory.Exists(target))
                {
                    RejectReparsePointsRecursively(target);
                    Directory.Move(target, backup);
                    moved.Add(name);
                }
            }

            foreach (string name in UpdatePackageOwnedEntries.InInstallOrder)
            {
                string source = Path.Combine(sourceRoot, name);
                string target = Path.Combine(targetRoot, name);
                copied.Add(name);
                if (File.Exists(source))
                {
                    RejectReparsePoint(source);
                    File.Copy(source, target, overwrite: false);
                }
                else if (Directory.Exists(source))
                {
                    CopyDirectory(source, target);
                }
                else
                {
                    throw new ReleaseUpdateException(
                        $"更新包缺少 {name}。",
                        "UPDATE_PACKAGE_LAYOUT_INVALID");
                }
            }
        }
        catch (Exception applyException)
        {
            Exception? rollbackException = null;
            try
            {
                foreach (string name in copied.AsEnumerable().Reverse())
                {
                    DeleteExactEntry(Path.Combine(targetRoot, name));
                }
                foreach (string name in moved.AsEnumerable().Reverse())
                {
                    string backup = Path.Combine(backupRoot, name);
                    string target = Path.Combine(targetRoot, name);
                    if (File.Exists(backup)) File.Move(backup, target);
                    else if (Directory.Exists(backup)) Directory.Move(backup, target);
                }
            }
            catch (Exception exception)
            {
                rollbackException = exception;
            }
            if (rollbackException is not null)
            {
                throw new AggregateException(
                    "更新失败，且旧版本自动恢复未能完整完成。",
                    applyException,
                    rollbackException);
            }
            throw;
        }
    }

    private static void WaitForParentExit(int parentProcessId)
    {
        if (parentProcessId == Environment.ProcessId)
        {
            throw new ReleaseUpdateException(
                "更新计划不能等待当前进程。",
                "UPDATE_PLAN_INVALID");
        }
        try
        {
            using Process parent = Process.GetProcessById(parentProcessId);
            if (!parent.WaitForExit((int)TimeSpan.FromMinutes(2).TotalMilliseconds))
            {
                throw new ReleaseUpdateException(
                    "VibeTable 未能在两分钟内退出，更新尚未应用。",
                    "UPDATE_PARENT_EXIT_TIMEOUT");
            }
        }
        catch (ArgumentException)
        {
            // The original process already exited.
        }
    }

    internal static bool TryCleanupStage(UpdateApplyPlan plan)
    {
        try
        {
            RejectReparsePointsRecursively(plan.StagingRoot);
            Directory.Delete(plan.StagingRoot, recursive: true);
            return true;
        }
        catch (IOException)
        {
            return false;
        }
        catch (UnauthorizedAccessException)
        {
            return false;
        }
    }

    internal static void WriteSmokeProcessEvidence(UpdateApplyPlan plan, int processId)
    {
        if (!plan.SmokeTest)
        {
            return;
        }
        string readinessRoot = Path.Combine(
            Directory.GetParent(plan.StagingRoot.TrimEnd(
                Path.DirectorySeparatorChar,
                Path.AltDirectorySeparatorChar))!.FullName,
            "self-update-readiness");
        Directory.CreateDirectory(readinessRoot);
        WriteJsonAtomically(
            Path.Combine(readinessRoot, SmokeProcessFileName),
            new SmokeProcessEvidence(plan.Token, plan.TargetVersion, processId));
    }

    private static void WriteJsonAtomically(string path, object value)
    {
        string temporary = path + $".tmp-{Environment.ProcessId}";
        File.WriteAllText(
            temporary,
            JsonSerializer.Serialize(value, WebJson),
            Utf8NoBom);
        File.Move(temporary, path, overwrite: true);
    }

    private sealed record SmokeProcessEvidence(
        string Token,
        string TargetVersion,
        int ProcessId);

    private static void CopyDirectory(string source, string destination)
    {
        RejectReparsePointsRecursively(source);
        Directory.CreateDirectory(destination);
        foreach (string directory in Directory.EnumerateDirectories(
                     source,
                     "*",
                     SearchOption.AllDirectories))
        {
            string relative = Path.GetRelativePath(source, directory);
            Directory.CreateDirectory(Path.Combine(destination, relative));
        }
        foreach (string file in Directory.EnumerateFiles(
                     source,
                     "*",
                     SearchOption.AllDirectories))
        {
            string relative = Path.GetRelativePath(source, file);
            string target = Path.Combine(destination, relative);
            Directory.CreateDirectory(Path.GetDirectoryName(target)!);
            File.Copy(file, target, overwrite: false);
        }
    }

    internal static void RejectReparsePointsRecursively(string root)
    {
        RejectReparsePoint(root);
        foreach (string path in Directory.EnumerateFileSystemEntries(
                     root,
                     "*",
                     SearchOption.AllDirectories))
        {
            RejectReparsePoint(path);
        }
    }

    internal static void RejectReparsePoint(string path)
    {
        if ((File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0)
        {
            throw new ReleaseUpdateException(
                "更新目录包含重解析点，已拒绝继续。",
                "UPDATE_REPARSE_POINT_REJECTED");
        }
    }

    internal static void RejectReparsePointChain(string path, string boundary)
    {
        string current = NormalizeDirectory(path);
        string normalizedBoundary = NormalizeDirectory(boundary);
        string relative = Path.GetRelativePath(normalizedBoundary, current);
        if (Path.IsPathRooted(relative)
            || relative == ".."
            || relative.StartsWith($"..{Path.DirectorySeparatorChar}",
                StringComparison.Ordinal))
        {
            throw new ReleaseUpdateException(
                "更新目录超出受控边界。",
                "UPDATE_PLAN_PATH_INVALID");
        }
        while (true)
        {
            if (File.Exists(current) || Directory.Exists(current))
            {
                RejectReparsePoint(current);
            }
            if (PathsEqual(current, normalizedBoundary))
            {
                return;
            }
            DirectoryInfo? parent = Directory.GetParent(
                current.TrimEnd(Path.DirectorySeparatorChar));
            if (parent is null)
            {
                throw new ReleaseUpdateException(
                    "更新目录超出受控边界。",
                    "UPDATE_PLAN_PATH_INVALID");
            }
            current = NormalizeDirectory(parent.FullName);
        }
    }

    internal static string RejectReparsePointChainsToVolumeRoot(params string[] paths)
    {
        if (paths.Length == 0)
        {
            throw new ArgumentException("At least one controlled path is required.", nameof(paths));
        }
        string? volumeRoot = null;
        foreach (string path in paths)
        {
            string normalized = NormalizeDirectory(path);
            string currentVolume = Path.GetPathRoot(normalized)
                ?? throw new ReleaseUpdateException(
                    "无法确定更新目录卷边界。",
                    "UPDATE_PLAN_PATH_INVALID");
            currentVolume = NormalizeDirectory(currentVolume);
            if (volumeRoot is not null && !PathsEqual(volumeRoot, currentVolume))
            {
                throw new ReleaseUpdateException(
                    "更新目标与阶段目录不在同一卷。",
                    "UPDATE_PLAN_PATH_INVALID");
            }
            volumeRoot ??= currentVolume;
            RejectReparsePointChain(normalized, volumeRoot);
        }
        return volumeRoot!;
    }

    private static void DeleteExactEntry(string path)
    {
        if (File.Exists(path)) File.Delete(path);
        else if (Directory.Exists(path)) Directory.Delete(path, recursive: true);
    }

    private static string NormalizeDirectory(string path) =>
        ReleasePackageStager.NormalizeDirectory(path);

    private static bool PathsEqual(string left, string right) =>
        string.Equals(
            NormalizeDirectory(left),
            NormalizeDirectory(right),
            StringComparison.OrdinalIgnoreCase);

    private static bool FixedTimeTokenEquals(string expected, string? actual)
    {
        if (actual is null || expected.Length != actual.Length) return false;
        return CryptographicOperations.FixedTimeEquals(
            Encoding.ASCII.GetBytes(expected),
            Encoding.ASCII.GetBytes(actual));
    }

    private static void WriteFailureEvidence(string stageRoot, Exception exception)
    {
        try
        {
            File.WriteAllText(
                Path.Combine(stageRoot, "update-error.txt"),
                $"[{DateTimeOffset.UtcNow:O}] {exception}{Environment.NewLine}");
        }
        catch (Exception evidenceException) when (evidenceException is
            IOException or UnauthorizedAccessException)
        {
            // Best-effort evidence; never mask the update error.
        }
    }

    private static void TryRestartExistingApplication(string targetRoot)
    {
        string executable = Path.Combine(targetRoot, "VibeTable.Next.exe");
        if (!File.Exists(executable)) return;
        try
        {
            using Process? process = Process.Start(new ProcessStartInfo
            {
                FileName = executable,
                WorkingDirectory = targetRoot,
                UseShellExecute = false,
            });
        }
        catch (Exception restartException) when (restartException is
            InvalidOperationException or System.ComponentModel.Win32Exception)
        {
            // The retained evidence explains the original failure.
        }
    }
}
