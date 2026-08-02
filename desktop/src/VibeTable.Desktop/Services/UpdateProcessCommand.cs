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

internal static class UpdateProcessCommand
{
    internal const string SmokeTokenEnvironmentVariable =
        "VIBETABLE_SELF_UPDATE_SMOKE_TOKEN";
    internal const string SmokeCompletionFileName =
        ".self-update-smoke-complete";
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

    public static bool TryScheduleCleanup(IReadOnlyList<string> arguments)
    {
        if (!TryParseCleanup(arguments, out string? stageRoot, out int updaterProcessId,
                out string? token, out bool smokeArgument))
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
            if (!PathsEqual(plan.TargetRoot, AppContext.BaseDirectory)
                || !FixedTimeTokenEquals(plan.Token, token!)
                || plan.SmokeTest != smokeArgument)
            {
                return false;
            }
            if (plan.SmokeTest)
            {
                CleanupAfterUpdaterExit(plan, updaterProcessId);
                File.WriteAllText(
                    Path.Combine(plan.TargetRoot, SmokeCompletionFileName),
                    plan.Token,
                    Encoding.ASCII);
                return true;
            }
            _ = Task.Run(() => CleanupAfterUpdaterExit(plan, updaterProcessId));
        }
        catch (Exception)
        {
            // A retained update directory is safer than deleting an unverified path.
        }
        return false;
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
        }
        using Process? process = Process.Start(start);
        if (process is null)
        {
            throw new ReleaseUpdateException(
                "更新已写入，但无法重新启动 VibeTable。",
                "UPDATE_RESTART_FAILED");
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
        if (arguments.Count is not (6 or 7)
            || arguments[0] != "--cleanup-update"
            || arguments[2] != "--updater-pid"
            || arguments[4] != "--update-token"
            || (arguments.Count == 7 && arguments[6] != "--self-update-smoke")
            || !int.TryParse(arguments[3], out updaterProcessId)
            || updaterProcessId <= 0)
        {
            return false;
        }
        stageRoot = arguments[1];
        token = arguments[5];
        smokeTest = arguments.Count == 7;
        return true;
    }

    private static void CleanupAfterUpdaterExit(UpdateApplyPlan plan, int updaterProcessId)
    {
        try
        {
            using Process updater = Process.GetProcessById(updaterProcessId);
            if (!updater.WaitForExit((int)TimeSpan.FromMinutes(2).TotalMilliseconds)) return;
        }
        catch (ArgumentException)
        {
            // The updater already exited.
        }
        try
        {
            RejectReparsePointsRecursively(plan.StagingRoot);
            Directory.Delete(plan.StagingRoot, recursive: true);
        }
        catch (IOException)
        {
            // Keep the exact staging directory for the next diagnostic/cleanup pass.
        }
        catch (UnauthorizedAccessException)
        {
            // Keep the exact staging directory for the next diagnostic/cleanup pass.
        }
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
