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

internal interface IUpdateActivationGate
{
    bool ExitAfterConfirmation { get; }

    Task<bool> Completion { get; }

    void ConfirmShellReady();
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
    private static readonly string[] OwnedRootEntries =
    [
        "VibeTable.Next.exe",
        "release.json",
        "resources",
    ];

    public static bool TryApply(
        IReadOnlyList<string> arguments,
        out int exitCode,
        out string? errorMessage)
    {
        exitCode = 0;
        errorMessage = null;
        if (arguments.Count != 2 || arguments[0] != "--apply-update") return false;

        UpdateApplyPlan? plan = null;
        try
        {
            plan = ReadAndValidatePlan(arguments[1], requireCurrentSource: true);
            WaitForParentExit(plan.ParentProcessId);
            ApplyPackageOwnedFiles(plan);
            StartUpdatedApplication(plan);
        }
        catch (Exception exception)
        {
            exitCode = 1;
            errorMessage = exception.Message;
            if (plan is not null)
            {
                WriteFailureEvidence(plan.StagingRoot, exception);
                TryRestartExistingApplication(plan.TargetRoot);
            }
        }
        return true;
    }

    internal static bool TryCreateActivationGate(
        IReadOnlyList<string> arguments,
        out IUpdateActivationGate? gate,
        string? runningRoot = null,
        Func<int, Task<bool>>? waitForUpdaterExit = null,
        Func<UpdateApplyPlan, bool>? cleanupStage = null)
    {
        gate = null;
        if (!TryParseCleanup(
                arguments,
                out string? stageRoot,
                out int updaterProcessId,
                out string? token,
                out bool smokeArgument))
        {
            return false;
        }
        try
        {
            string planPath = Path.Combine(stageRoot!, "update-plan.json");
            UpdateApplyPlan plan = ReadAndValidatePlan(
                planPath,
                requireCurrentSource: false,
                targetAlreadyUpdated: true);
            if (!PathsEqual(plan.TargetRoot, runningRoot ?? AppContext.BaseDirectory)
                || !FixedTimeTokenEquals(plan.Token, token!)
                || plan.SmokeTest != smokeArgument)
            {
                return false;
            }
            gate = new PendingUpdateActivationGate(
                plan,
                updaterProcessId,
                waitForUpdaterExit ?? WaitForUpdaterExitAsync,
                cleanupStage ?? TryCleanupStage);
            return true;
        }
        catch (Exception)
        {
            // An invalid activation must retain the exact staging directory.
            return false;
        }
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
            foreach (string name in OwnedRootEntries)
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

            foreach (string name in OwnedRootEntries)
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

    private static void StartUpdatedApplication(UpdateApplyPlan plan)
    {
        string executable = Path.Combine(plan.TargetRoot, "VibeTable.Next.exe");
        string? smokeReadinessRoot = plan.SmokeTest
            ? Path.Combine(
                Directory.GetParent(plan.StagingRoot.TrimEnd(
                    Path.DirectorySeparatorChar,
                    Path.AltDirectorySeparatorChar))!.FullName,
                "self-update-readiness")
            : null;
        var start = new ProcessStartInfo
        {
            FileName = executable,
            WorkingDirectory = plan.TargetRoot,
            UseShellExecute = false,
        };
        start.ArgumentList.Add("--cleanup-update");
        start.ArgumentList.Add(plan.StagingRoot);
        start.ArgumentList.Add("--updater-pid");
        start.ArgumentList.Add(Environment.ProcessId.ToString(System.Globalization.CultureInfo.InvariantCulture));
        start.ArgumentList.Add("--update-token");
        start.ArgumentList.Add(plan.Token);
        if (plan.SmokeTest)
        {
            start.ArgumentList.Add("--self-update-smoke");
            start.ArgumentList.Add("--test-mode");
            start.ArgumentList.Add("--readiness-dir");
            start.ArgumentList.Add(smokeReadinessRoot!);
        }
        using Process? process = Process.Start(start);
        if (process is null)
        {
            throw new ReleaseUpdateException(
                "更新已写入，但无法重新启动 VibeTable。",
                "UPDATE_RESTART_FAILED");
        }
        if (plan.SmokeTest)
        {
            try
            {
                Directory.CreateDirectory(smokeReadinessRoot!);
                WriteJsonAtomically(
                    Path.Combine(smokeReadinessRoot!, SmokeProcessFileName),
                    new SmokeProcessEvidence(
                        plan.Token,
                        plan.TargetVersion,
                        process.Id));
            }
            catch
            {
                try
                {
                    process.Kill(entireProcessTree: true);
                }
                catch (Exception)
                {
                    // Preserve the original evidence-write failure.
                }
                throw;
            }
        }
    }

    private static bool TryParseCleanup(
        IReadOnlyList<string> arguments,
        out string? stageRoot,
        out int updaterProcessId,
        out string? token,
        out bool smokeTest)
    {
        stageRoot = null;
        updaterProcessId = 0;
        token = null;
        smokeTest = false;
        string? updaterProcessIdText = null;
        for (int index = 0; index < arguments.Count; index++)
        {
            string argument = arguments[index];
            switch (argument)
            {
                case "--cleanup-update":
                    if (stageRoot is not null || index + 1 >= arguments.Count)
                    {
                        return false;
                    }
                    stageRoot = arguments[++index];
                    break;
                case "--updater-pid":
                    if (updaterProcessIdText is not null || index + 1 >= arguments.Count)
                    {
                        return false;
                    }
                    updaterProcessIdText = arguments[++index];
                    break;
                case "--update-token":
                    if (token is not null || index + 1 >= arguments.Count)
                    {
                        return false;
                    }
                    token = arguments[++index];
                    break;
                case "--self-update-smoke":
                    if (smokeTest)
                    {
                        return false;
                    }
                    smokeTest = true;
                    break;
            }
        }
        return stageRoot is not null
            && token is not null
            && int.TryParse(updaterProcessIdText, out updaterProcessId)
            && updaterProcessId > 0;
    }

    private static async Task<bool> WaitForUpdaterExitAsync(int updaterProcessId)
    {
        if (updaterProcessId == Environment.ProcessId)
        {
            return false;
        }
        try
        {
            using Process updater = Process.GetProcessById(updaterProcessId);
            using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(2));
            await updater.WaitForExitAsync(timeout.Token).ConfigureAwait(false);
            return true;
        }
        catch (ArgumentException)
        {
            // The updater already exited.
            return true;
        }
        catch (OperationCanceledException)
        {
            return false;
        }
    }

    private static bool TryCleanupStage(UpdateApplyPlan plan)
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

    private sealed class PendingUpdateActivationGate(
        UpdateApplyPlan plan,
        int updaterProcessId,
        Func<int, Task<bool>> waitForUpdaterExit,
        Func<UpdateApplyPlan, bool> cleanupStage) : IUpdateActivationGate
    {
        private const string ConfirmationLockSuffix =
            ".activation.lock";
        private readonly TaskCompletionSource<bool> _completion =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _confirmationStarted;

        public bool ExitAfterConfirmation => plan.SmokeTest;

        public Task<bool> Completion => _completion.Task;

        public void ConfirmShellReady()
        {
            if (Interlocked.Exchange(ref _confirmationStarted, 1) != 0)
            {
                return;
            }
            _ = Task.Run(ConfirmAsync);
        }

        private async Task ConfirmAsync()
        {
            try
            {
                if (!await waitForUpdaterExit(updaterProcessId).ConfigureAwait(false))
                {
                    _completion.TrySetResult(false);
                    return;
                }
                UpdateApplyPlan current = ReadAndValidatePlan(
                    Path.Combine(plan.StagingRoot, "update-plan.json"),
                    requireCurrentSource: false,
                    targetAlreadyUpdated: true);
                if (current != plan)
                {
                    _completion.TrySetResult(false);
                    return;
                }
                string claimPath = plan.StagingRoot.TrimEnd(
                    Path.DirectorySeparatorChar,
                    Path.AltDirectorySeparatorChar) + ConfirmationLockSuffix;
                FileStream? claim = null;
                try
                {
                    if (File.Exists(claimPath))
                    {
                        RejectReparsePoint(claimPath);
                    }
                    claim = new FileStream(
                        claimPath,
                        FileMode.OpenOrCreate,
                        FileAccess.ReadWrite,
                        FileShare.None,
                        bufferSize: 4096,
                        FileOptions.WriteThrough);
                    claim.SetLength(0);
                    using (var writer = new StreamWriter(
                               claim,
                               Encoding.ASCII,
                               bufferSize: 1024,
                               leaveOpen: true))
                    {
                        writer.Write(plan.Token);
                        writer.Flush();
                    }
                    claim.Flush(flushToDisk: true);
                    if (!cleanupStage(plan))
                    {
                        _completion.TrySetResult(false);
                        return;
                    }
                    if (plan.SmokeTest)
                    {
                        WriteJsonAtomically(
                            Path.Combine(plan.TargetRoot, SmokeCompletionFileName),
                            new SmokeCompletionEvidence(
                                plan.Token,
                                plan.TargetVersion,
                                Environment.ProcessId,
                                DateTimeOffset.UtcNow));
                    }
                    _completion.TrySetResult(true);
                }
                finally
                {
                    if (claim is not null)
                    {
                        claim.Dispose();
                        try
                        {
                            File.Delete(claimPath);
                        }
                        catch (IOException)
                        {
                            // A regular stale lock file is recoverable because
                            // the next process must acquire its OS handle anew.
                        }
                        catch (UnauthorizedAccessException)
                        {
                            // Keep the exact lock path for diagnostics.
                        }
                    }
                }
            }
            catch (Exception exception)
            {
                try
                {
                    WriteActivationFailureEvidence(plan.StagingRoot, exception);
                }
                catch (Exception)
                {
                    // The activation result remains failed even when its
                    // diagnostic directory cannot be written safely.
                }
                _completion.TrySetResult(false);
            }
        }
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

    private sealed record SmokeCompletionEvidence(
        string Token,
        string TargetVersion,
        int ProcessId,
        DateTimeOffset ConfirmedAt);

    private static void WriteActivationFailureEvidence(
        string stageRoot,
        Exception exception)
    {
        string path = stageRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar) + ".activation-error.txt";
        File.WriteAllText(
            path,
            $"[{DateTimeOffset.UtcNow:O}] {exception}{Environment.NewLine}");
    }

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

    private static void RejectReparsePointsRecursively(string root)
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

    private static void RejectReparsePoint(string path)
    {
        if ((File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0)
        {
            throw new ReleaseUpdateException(
                "更新目录包含重解析点，已拒绝继续。",
                "UPDATE_REPARSE_POINT_REJECTED");
        }
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
