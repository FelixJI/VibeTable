using System;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using Microsoft.Win32;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

public enum PluginPackagePickKind
{
    PackageOrFolder,
    Package,
    Folder,
}

/// <summary>
/// Native-only selection boundary for plugin packages and development folders.
/// The selected path may cross the local RPC pipe but never the WebView bridge.
/// </summary>
public interface IPluginPackageSourcePicker
{
    Task<string?> PickAsync(PluginPackagePickKind kind, CancellationToken token);
}

public interface IPluginFilePicker
{
    Task<string?> PickAsync(PluginRuntimeFileRequest request, CancellationToken token);
}

public static class PluginPackageSourceTokens
{
    public const string Picker = "host-picker";
    public const string PackagePicker = "host-picker:package";
    public const string FolderPicker = "host-picker:folder";

    public static bool TryGetPickKind(string? token, out PluginPackagePickKind kind)
    {
        kind = token switch
        {
            Picker => PluginPackagePickKind.PackageOrFolder,
            PackagePicker => PluginPackagePickKind.Package,
            FolderPicker => PluginPackagePickKind.Folder,
            _ => default,
        };
        return token is Picker or PackagePicker or FolderPicker;
    }
}

public sealed class WindowsPluginPackageSourcePicker : IPluginPackageSourcePicker
{
    public async Task<string?> PickAsync(PluginPackagePickKind kind, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        var application = Application.Current
            ?? throw new InvalidOperationException("WPF application is unavailable.");
        return await application.Dispatcher.InvokeAsync(() =>
        {
            token.ThrowIfCancellationRequested();
            PluginPackagePickKind resolvedKind = kind;
            if (kind == PluginPackagePickKind.PackageOrFolder)
            {
                MessageBoxResult choice = MessageBox.Show(
                    application.MainWindow,
                    "选择“是”安装 .vtplugin 包，选择“否”加载插件文件夹。",
                    "选择插件来源",
                    MessageBoxButton.YesNoCancel,
                    MessageBoxImage.Question);
                if (choice == MessageBoxResult.Cancel)
                {
                    return null;
                }
                resolvedKind = choice == MessageBoxResult.Yes
                    ? PluginPackagePickKind.Package
                    : PluginPackagePickKind.Folder;
            }

            string? selected = resolvedKind == PluginPackagePickKind.Folder
                ? PickFolder(application.MainWindow)
                : PickPackage(application.MainWindow);
            if (string.IsNullOrWhiteSpace(selected))
            {
                return null;
            }
            string fullPath = Path.GetFullPath(selected);
            bool exists = resolvedKind == PluginPackagePickKind.Folder
                ? Directory.Exists(fullPath)
                : File.Exists(fullPath);
            return exists ? fullPath : null;
        });
    }

    private static string? PickPackage(Window? owner)
    {
        var dialog = new OpenFileDialog
        {
            AddExtension = true,
            CheckFileExists = true,
            CheckPathExists = true,
            DefaultExt = ".vtplugin",
            DereferenceLinks = false,
            Filter = "VibeTable 插件包 (*.vtplugin)|*.vtplugin",
            Multiselect = false,
            RestoreDirectory = true,
            Title = "安装 VibeTable 插件包",
        };
        bool? accepted = owner is null ? dialog.ShowDialog() : dialog.ShowDialog(owner);
        return accepted == true ? dialog.FileName : null;
    }

    private static string? PickFolder(Window? owner)
    {
        var dialog = new OpenFolderDialog
        {
            DereferenceLinks = false,
            Multiselect = false,
            Title = "加载 VibeTable 插件文件夹",
        };
        bool? accepted = owner is null ? dialog.ShowDialog() : dialog.ShowDialog(owner);
        return accepted == true ? dialog.FolderName : null;
    }
}

/// <summary>
/// Fixed file-protocol picker used only by the explicit desktop test mode.
/// It reads one path from <c>plugin-source.txt</c>; it does not execute or
/// interpret any control-file content.
/// </summary>
public sealed class TestModePluginPackageSourcePicker : IPluginPackageSourcePicker
{
    private readonly string _controlsDirectory;

    public TestModePluginPackageSourcePicker(string controlsDirectory)
    {
        _controlsDirectory = Path.GetFullPath(
            controlsDirectory
            ?? throw new ArgumentNullException(nameof(controlsDirectory)));
    }

    public Task<string?> PickAsync(
        PluginPackagePickKind kind,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        string control = Path.Combine(_controlsDirectory, "plugin-source.txt");
        if (!File.Exists(control))
        {
            throw new InvalidOperationException(
                "Missing test-mode control file: plugin-source.txt");
        }
        string selected = Path.GetFullPath(File.ReadAllText(control).Trim());
        bool exists = kind switch
        {
            PluginPackagePickKind.Folder => Directory.Exists(selected),
            PluginPackagePickKind.Package => File.Exists(selected),
            _ => Directory.Exists(selected) || File.Exists(selected),
        };
        if (!exists)
        {
            throw new FileNotFoundException(
                "The test-mode plugin source does not exist.",
                selected);
        }
        return Task.FromResult<string?>(selected);
    }
}

/// <summary>Native file boundary used only for an active, declared plugin capability.</summary>
public sealed class WindowsPluginFilePicker : IPluginFilePicker
{
    public async Task<string?> PickAsync(
        PluginRuntimeFileRequest request,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        var application = Application.Current
            ?? throw new InvalidOperationException("WPF application is unavailable.");
        return await application.Dispatcher.InvokeAsync(() =>
        {
            token.ThrowIfCancellationRequested();
            if (string.Equals(request.Direction, "read", StringComparison.Ordinal))
            {
                var dialog = new OpenFileDialog
                {
                    CheckFileExists = true,
                    CheckPathExists = true,
                    DereferenceLinks = false,
                    Filter = "Files|*.*",
                    Multiselect = false,
                    RestoreDirectory = true,
                    Title = "选择插件要读取的文件",
                };
                bool? accepted = application.MainWindow is null
                    ? dialog.ShowDialog()
                    : dialog.ShowDialog(application.MainWindow);
                return accepted == true ? Path.GetFullPath(dialog.FileName) : null;
            }

            var saveDialog = new SaveFileDialog
            {
                AddExtension = true,
                CheckPathExists = true,
                DereferenceLinks = false,
                FileName = request.SuggestedName ?? "plugin-output",
                Filter = "Files|*.*",
                RestoreDirectory = true,
                Title = "选择插件输出文件",
            };
            bool? saved = application.MainWindow is null
                ? saveDialog.ShowDialog()
                : saveDialog.ShowDialog(application.MainWindow);
            return saved == true ? Path.GetFullPath(saveDialog.FileName) : null;
        });
    }
}
