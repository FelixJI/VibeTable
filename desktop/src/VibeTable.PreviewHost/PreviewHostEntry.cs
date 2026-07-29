using System.IO;
using System.Windows;

namespace VibeTable.PreviewHost;

public static class PreviewHostEntry
{
    public static int Start(
        Application application,
        IReadOnlyList<string> args)
    {
        ArgumentNullException.ThrowIfNull(application);
        ArgumentNullException.ThrowIfNull(args);
        application.ShutdownMode = ShutdownMode.OnExplicitShutdown;

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
            ShowSafeFailure("系统预览器运行失败，请使用默认应用打开。");
            application.Shutdown(4);
        };

        if (!PreviewHostArguments.TryParse(args, out var arguments))
        {
            return 2;
        }
        if (!File.Exists(arguments.FilePath))
        {
            ShowSafeFailure("文件不存在，无法预览。");
            return 3;
        }

        try
        {
            var window = new ShellPreviewWindow(
                arguments.FilePath,
                arguments.HandlerClsid);
            application.MainWindow = window;
            application.ShutdownMode = ShutdownMode.OnMainWindowClose;
            window.Show();
            window.Activate();
            return 0;
        }
        catch
        {
            ShowSafeFailure("系统预览器无法加载此文件，请使用默认应用打开。");
            return 4;
        }
    }
}
