using System.IO;
using System.Text.Json;
using System.Windows;

namespace VibeTable.PreviewHost;

public static class PreviewHostEntry
{
    internal static void WriteEvidence(
        PreviewHostArguments arguments, string outcome, IntPtr hwnd = default)
    {
        if (arguments.TestEvidenceDirectory is null) return;
        string root = arguments.TestEvidenceDirectory;
        for (DirectoryInfo? directory = new(root); directory is not null; directory = directory.Parent)
            if (directory.Exists && directory.Attributes.HasFlag(FileAttributes.ReparsePoint))
                throw new IOException("Preview evidence directory contains a reparse point.");
        string destination = Path.Combine(root, "document-native-preview-result.json");
        if ((File.Exists(destination) || Directory.Exists(destination)) &&
            File.GetAttributes(destination).HasFlag(FileAttributes.ReparsePoint))
            throw new IOException("Preview evidence file is a reparse point.");
        string temporary = destination + ".tmp";
        if ((File.Exists(temporary) || Directory.Exists(temporary)) &&
            File.GetAttributes(temporary).HasFlag(FileAttributes.ReparsePoint))
            throw new IOException("Preview evidence temporary file is a reparse point.");
        File.WriteAllText(temporary, JsonSerializer.Serialize(new
        {
            outcome,
            source = arguments.FilePath,
            handlerClsid = arguments.HandlerClsid,
            processId = Environment.ProcessId,
            hwnd = hwnd.ToInt64(),
        }));
        File.Move(temporary, destination, overwrite: true);
    }

    public static int Start(
        Application application,
        IReadOnlyList<string> args)
    {
        ArgumentNullException.ThrowIfNull(application);
        ArgumentNullException.ThrowIfNull(args);
        application.ShutdownMode = ShutdownMode.OnExplicitShutdown;
        if (!PreviewHostArguments.TryParse(args, out var arguments)) return 2;

        void WriteEvidence(string outcome, IntPtr hwnd = default)
            => PreviewHostEntry.WriteEvidence(arguments, outcome, hwnd);

        bool showingFailure = false;
        void ShowSafeFailure(string message)
        {
            if (showingFailure) return;
            showingFailure = true;
            MessageBox.Show(
                message,
                "VibeTable 预览",
                MessageBoxButton.OK,
                MessageBoxImage.Warning);
        }

        application.DispatcherUnhandledException += (_, eventArgs) =>
        {
            eventArgs.Handled = true;
            WriteEvidence("failed");
            ShowSafeFailure("系统预览器运行失败，请使用默认应用打开。");
            application.Shutdown(4);
        };

        if (!File.Exists(arguments.FilePath))
        {
            WriteEvidence("missing");
            ShowSafeFailure("文件不存在，无法预览。");
            return 3;
        }

        try
        {
            var window = new ShellPreviewWindow(
                arguments.FilePath,
                arguments.HandlerClsid,
                hwnd => WriteEvidence("do-preview-returned", hwnd));
            application.MainWindow = window;
            application.ShutdownMode = ShutdownMode.OnMainWindowClose;
            window.Show();
            window.Activate();
            return 0;
        }
        catch
        {
            WriteEvidence("failed");
            ShowSafeFailure("系统预览器无法加载此文件，请使用默认应用打开。");
            return 4;
        }
    }
}
