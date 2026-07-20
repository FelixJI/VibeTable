using System.IO;
using System.Windows;
using System.Windows.Threading;

namespace VibeTable.PreviewHost;

public partial class App : Application
{
    private bool _showingFailure;

    protected override void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);
        ShutdownMode = ShutdownMode.OnExplicitShutdown;
        DispatcherUnhandledException += OnDispatcherUnhandledException;

        if (!PreviewHostArguments.TryParse(e.Args, out var arguments))
        {
            Shutdown(2);
            return;
        }
        if (!File.Exists(arguments.FilePath))
        {
            ShowSafeFailure("文件不存在，无法预览。");
            Shutdown(3);
            return;
        }

        try
        {
            var window = new ShellPreviewWindow(arguments.FilePath, arguments.HandlerClsid);
            MainWindow = window;
            ShutdownMode = ShutdownMode.OnMainWindowClose;
            window.Show();
            window.Activate();
        }
        catch
        {
            ShowSafeFailure("系统预览器无法加载此文件，请使用默认应用打开。");
            Shutdown(4);
        }
    }

    private void OnDispatcherUnhandledException(
        object sender,
        DispatcherUnhandledExceptionEventArgs e)
    {
        e.Handled = true;
        ShowSafeFailure("系统预览器运行失败，请使用默认应用打开。");
        Shutdown(4);
    }

    private void ShowSafeFailure(string message)
    {
        if (_showingFailure) return;
        _showingFailure = true;
        MessageBox.Show(
            message,
            "VibeTable 预览",
            MessageBoxButton.OK,
            MessageBoxImage.Warning);
    }
}
