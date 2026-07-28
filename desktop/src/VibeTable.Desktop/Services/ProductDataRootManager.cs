using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.Json;
using System.Windows;
using Microsoft.Win32;

namespace VibeTable.Desktop.Services;

internal sealed record ProductDataRootStatus(
    string DataRoot,
    string DefaultDataRoot,
    bool MigrationPending,
    string? PendingDataRoot);

internal sealed record ProductDataRootMigrationSelection(
    bool Selected,
    string? TargetDataRoot,
    bool RequiresRestart);

/// <summary>
/// Owns the stable product data-root preference and startup-only migration.
/// Database files are never copied while PocketBase or Python is running.
/// </summary>
internal static class ProductDataRootManager
{
    private const int PreferenceSchema = 1;
    private const string ProductFolderName = "VibeTableData";
    private const string PreferenceFileName = "vibetable-data-root.json";
    private const string PendingFileName = "vibetable-data-root.pending.json";
    private const string MigrationMarkerFileName = ".vibetable-data-root.json";

    private sealed record ProductDataRootPreference(int Schema, string DataRoot);
    private sealed record PendingMigration(
        int Schema,
        string SourceRoot,
        string TargetRoot,
        string RequestedAt);
    private sealed record MigrationMarker(
        int Schema,
        string SourceRoot,
        string MigratedAt,
        long FileCount,
        long TotalBytes);

    /// <summary>
    /// Resolve the production root before constructing any writable runtime.
    /// A pending settings migration is applied here while all services are
    /// stopped. Development and test roots bypass this method.
    /// </summary>
    public static string Resolve(string programDirectory)
    {
        string defaultRoot = DefaultDataRoot(programDirectory);
        string? configured = ReadPreference();
        if (!string.IsNullOrWhiteSpace(configured))
        {
            configured = Path.GetFullPath(configured);
            configured = TryApplyPendingMigration(configured);
            if (CanWrite(configured))
            {
                WritePreference(configured);
                return configured;
            }

            ShowMessage(
                "已配置的数据目录不可写，请重新选择一个位置。",
                "数据目录不可用",
                MessageBoxImage.Warning);
        }

        string selected = PromptForFirstRunRoot(defaultRoot);
        EnsureWritableRoot(selected);
        WritePreference(selected);
        return selected;
    }

    public static ProductDataRootStatus GetStatus(
        string currentRoot,
        string programDirectory)
    {
        PendingMigration? pending = ReadPendingMigration();
        return new ProductDataRootStatus(
            Path.GetFullPath(currentRoot),
            DefaultDataRoot(programDirectory),
            pending is not null,
            pending?.TargetRoot);
    }

    /// <summary>
    /// Let the user choose a parent folder and persist a startup migration
    /// request. The current process keeps using the old root until restart.
    /// </summary>
    public static ProductDataRootMigrationSelection ChooseAndScheduleMigration(
        string currentRoot)
    {
        string source = Path.GetFullPath(currentRoot);
        var dialog = new OpenFolderDialog
        {
            Title = "选择新的 VibeTable 数据存储位置（迁移将在下次启动时执行）",
            InitialDirectory = Directory.GetParent(source)?.FullName ?? source,
            Multiselect = false,
        };
        if (dialog.ShowDialog() != true)
        {
            return new ProductDataRootMigrationSelection(false, null, false);
        }

        string target = DataRootForSelectedFolder(dialog.FolderName);
        ValidateMigrationTarget(source, target);
        WritePendingMigration(new PendingMigration(
            PreferenceSchema,
            source,
            target,
            DateTimeOffset.UtcNow.ToString("O")));
        return new ProductDataRootMigrationSelection(true, target, true);
    }

    public static void ConfigureProcessEnvironment(
        IDictionary<string, string> environment,
        string dataRoot)
    {
        string runtimeRoot = Path.GetFullPath(dataRoot);
        environment["LOCALAPPDATA"] = runtimeRoot;
        environment["VIBETABLE_STATE_DIR"] = Path.Combine(runtimeRoot, "state");
    }

    public static string ResolveSidecarLogPath(string dataRoot)
        => Path.Combine(Path.GetFullPath(dataRoot), "logs", "backend.log");

    internal static string DefaultDataRoot(string programDirectory)
        => Path.Combine(Path.GetFullPath(programDirectory), ProductFolderName);

    internal static string DataRootForSelectedFolder(string selectedFolder)
    {
        string selected = Path.GetFullPath(selectedFolder);
        return string.Equals(
            Path.GetFileName(selected.TrimEnd(
                Path.DirectorySeparatorChar,
                Path.AltDirectorySeparatorChar)),
            ProductFolderName,
            StringComparison.OrdinalIgnoreCase)
                ? selected
                : Path.Combine(selected, ProductFolderName);
    }

    private static string PromptForFirstRunRoot(string defaultRoot)
    {
        string fallback = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "VibeTable",
            "Data");
        if (!CanWrite(defaultRoot))
        {
            ShowMessage(
                "程序运行目录不可写，默认位置已切换为“本机应用数据目录”。"
                + $"{Environment.NewLine}你仍可在下一步选择其他位置。",
                "数据目录检查",
                MessageBoxImage.Information);
            defaultRoot = fallback;
        }

        var dialog = new OpenFolderDialog
        {
            Title = "首次启动：请选择 VibeTable 数据存储位置",
            InitialDirectory = Directory.GetParent(defaultRoot)?.FullName ?? defaultRoot,
            Multiselect = false,
        };
        if (dialog.ShowDialog() != true)
        {
            return defaultRoot;
        }

        string selected = DataRootForSelectedFolder(dialog.FolderName);
        if (CanWrite(selected))
        {
            return selected;
        }

        ShowMessage(
            "所选位置不可写，将使用“本机应用数据目录”。",
            "数据目录不可写",
            MessageBoxImage.Warning);
        EnsureWritableRoot(fallback);
        return fallback;
    }

    private static void ValidateMigrationTarget(string source, string target)
    {
        string normalizedSource = WithSeparator(Path.GetFullPath(source));
        string normalizedTarget = WithSeparator(Path.GetFullPath(target));
        if (string.Equals(
                normalizedSource,
                normalizedTarget,
                StringComparison.OrdinalIgnoreCase)
            || normalizedTarget.StartsWith(
                normalizedSource,
                StringComparison.OrdinalIgnoreCase)
            || normalizedSource.StartsWith(
                normalizedTarget,
                StringComparison.OrdinalIgnoreCase))
        {
            throw new InvalidOperationException(
                "新旧数据目录不能相同，也不能互相嵌套。");
        }

        if (Directory.Exists(target)
            && Directory.EnumerateFileSystemEntries(target).Any())
        {
            throw new InvalidOperationException(
                "目标位置已包含 VibeTableData 数据，请选择空位置。");
        }
        EnsureWritableRoot(target);
    }

    private static bool CanWrite(string root)
    {
        try
        {
            Directory.CreateDirectory(root);
            string token = Guid.NewGuid().ToString("N");
            string probe = Path.Combine(root, $".vibetable-probe-{token}.tmp");
            string moved = probe + ".ok";
            using (var stream = new FileStream(
                probe,
                FileMode.CreateNew,
                FileAccess.Write,
                FileShare.None,
                4096,
                FileOptions.WriteThrough))
            {
                stream.WriteByte(1);
                stream.Flush(flushToDisk: true);
            }
            File.Move(probe, moved);
            File.Delete(moved);
            return true;
        }
        catch
        {
            return false;
        }
    }

    private static void EnsureWritableRoot(string root)
    {
        if (!CanWrite(root))
        {
            throw new InvalidOperationException(
                "The selected data root is not writable: " + root);
        }
    }

    private static string TryApplyPendingMigration(string currentRoot)
    {
        PendingMigration? pending = ReadPendingMigration();
        if (pending is null)
        {
            return currentRoot;
        }
        if (!string.Equals(
                Path.GetFullPath(pending.SourceRoot),
                Path.GetFullPath(currentRoot),
                StringComparison.OrdinalIgnoreCase))
        {
            ArchivePendingMigration("source-mismatch");
            return currentRoot;
        }

        try
        {
            MigrateDirectoryTransactional(currentRoot, pending.TargetRoot);
            WritePreference(pending.TargetRoot);
            File.Delete(PendingFilePath());
            return Path.GetFullPath(pending.TargetRoot);
        }
        catch (Exception exception)
        {
            ArchivePendingMigration("failed");
            ShowMessage(
                "数据目录迁移失败，应用将继续使用原目录。"
                + $"{Environment.NewLine}{exception.Message}",
                "迁移未完成",
                MessageBoxImage.Warning);
            return currentRoot;
        }
    }

    internal static void MigrateDirectoryTransactional(
        string sourceRoot,
        string targetRoot)
    {
        string source = Path.GetFullPath(sourceRoot);
        string target = Path.GetFullPath(targetRoot);
        ValidateMigrationTarget(source, target);
        if (!Directory.Exists(source))
        {
            throw new DirectoryNotFoundException(
                "The current VibeTable data root does not exist.");
        }

        string stage = StagePath(target);
        try
        {
            CopyDirectory(source, stage);
            DirectorySummary sourceSummary = Summarize(source);
            DirectorySummary stageSummary = Summarize(stage);
            if (sourceSummary != stageSummary)
            {
                throw new IOException(
                    "The staged data does not match the current data root.");
            }
            ActivateStage(stage, target, source);
        }
        catch
        {
            TryDeleteDirectory(stage);
            throw;
        }
    }

    private static void ActivateStage(
        string stage,
        string target,
        string source)
    {
        DirectorySummary summary = Summarize(stage);
        WriteMarker(stage, source, summary);
        if (Directory.Exists(target))
        {
            if (Directory.EnumerateFileSystemEntries(target).Any())
            {
                throw new IOException(
                    "The target data root changed while migration was running.");
            }
            Directory.Delete(target);
        }
        Directory.Move(stage, target);
    }

    private static void CopyDirectory(string source, string destination)
    {
        var info = new DirectoryInfo(source);
        if (info.Attributes.HasFlag(FileAttributes.ReparsePoint))
        {
            throw new IOException(
                "Data-root migration does not follow directory reparse points.");
        }
        Directory.CreateDirectory(destination);
        foreach (DirectoryInfo directory in info.GetDirectories())
        {
            CopyDirectory(
                directory.FullName,
                Path.Combine(destination, directory.Name));
        }
        foreach (FileInfo file in info.GetFiles())
        {
            if (file.Attributes.HasFlag(FileAttributes.ReparsePoint))
            {
                throw new IOException(
                    "Data-root migration does not follow file reparse points.");
            }
            CopyFile(file.FullName, Path.Combine(destination, file.Name));
        }
    }

    private static void CopyFile(string source, string destination)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(destination)!);
        using var input = new FileStream(
            source,
            FileMode.Open,
            FileAccess.Read,
            FileShare.Read,
            1024 * 1024,
            FileOptions.SequentialScan);
        using var output = new FileStream(
            destination,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            1024 * 1024,
            FileOptions.WriteThrough);
        input.CopyTo(output);
        output.Flush(flushToDisk: true);
    }

    private static DirectorySummary Summarize(string root)
    {
        long count = 0;
        long bytes = 0;
        foreach (string file in Directory.EnumerateFiles(
            root,
            "*",
            SearchOption.AllDirectories))
        {
            count++;
            bytes += new FileInfo(file).Length;
        }
        return new DirectorySummary(count, bytes);
    }

    private static string? ReadPreference()
        => ReadJson<ProductDataRootPreference>(PreferenceFilePath()) is
            { Schema: PreferenceSchema, DataRoot.Length: > 0 } preference
                ? preference.DataRoot
                : null;

    private static PendingMigration? ReadPendingMigration()
        => ReadJson<PendingMigration>(PendingFilePath()) is
            { Schema: PreferenceSchema } pending
                ? pending
                : null;

    private static T? ReadJson<T>(string path)
    {
        try
        {
            return File.Exists(path)
                ? JsonSerializer.Deserialize<T>(File.ReadAllText(path))
                : default;
        }
        catch
        {
            return default;
        }
    }

    private static void WritePreference(string dataRoot)
        => WriteJsonAtomically(
            PreferenceFilePath(),
            new ProductDataRootPreference(
                PreferenceSchema,
                Path.GetFullPath(dataRoot)));

    private static void WritePendingMigration(PendingMigration migration)
        => WriteJsonAtomically(PendingFilePath(), migration);

    private static void WriteMarker(
        string target,
        string source,
        DirectorySummary summary)
        => WriteJsonAtomically(
            Path.Combine(target, MigrationMarkerFileName),
            new MigrationMarker(
                PreferenceSchema,
                Path.GetFullPath(source),
                DateTimeOffset.UtcNow.ToString("O"),
                summary.FileCount,
                summary.TotalBytes));

    private static void WriteJsonAtomically<T>(string path, T value)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        string temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try
        {
            File.WriteAllText(
                temporary,
                JsonSerializer.Serialize(value),
                new UTF8Encoding(encoderShouldEmitUTF8Identifier: false));
            File.Move(temporary, path, overwrite: true);
        }
        finally
        {
            File.Delete(temporary);
        }
    }

    private static void ArchivePendingMigration(string suffix)
    {
        string pending = PendingFilePath();
        if (!File.Exists(pending)) return;
        string archived = pending + "." + suffix + "." +
            DateTimeOffset.UtcNow.ToString("yyyyMMddHHmmss");
        try
        {
            File.Move(pending, archived, overwrite: true);
        }
        catch
        {
            File.Delete(pending);
        }
    }

    private static string PreferenceFilePath()
        => Path.Combine(PreferenceDirectory(), PreferenceFileName);

    private static string PendingFilePath()
        => Path.Combine(PreferenceDirectory(), PendingFileName);

    private static string PreferenceDirectory()
        => Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "VibeTable",
            "desktop");

    private static string StagePath(string target)
        => Path.GetFullPath(target)
            + ".vibetable-staging-"
            + Guid.NewGuid().ToString("N");

    private static string WithSeparator(string path)
        => path.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar)
            + Path.DirectorySeparatorChar;

    private static void TryDeleteDirectory(string path)
    {
        try
        {
            if (Directory.Exists(path))
            {
                Directory.Delete(path, recursive: true);
            }
        }
        catch
        {
            // A staging directory never becomes the active preference.
        }
    }

    private static void ShowMessage(
        string message,
        string title,
        MessageBoxImage image)
        => System.Windows.MessageBox.Show(
            message,
            title,
            MessageBoxButton.OK,
            image);

    private sealed record DirectorySummary(long FileCount, long TotalBytes);
}
