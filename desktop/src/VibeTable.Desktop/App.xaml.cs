using System;
using System.IO;
using System.Windows;

namespace VibeTable.Desktop;

/// <summary>
/// Interaction logic for App.xaml. Kept deliberately minimal: the
/// <see cref="MainWindow"/> ctor builds its own production object graph
/// (supervisor, router, ViewModel, adapters); the startup sequence itself is
/// driven from <see cref="MainWindow.OnLoaded"/>, NOT from here.
/// </summary>
public partial class App : Application
{
    /// <summary>
    /// Early-startup trace (test mode only). Writes to
    /// <c>&lt;readiness-dir&gt;/vibetable-trace.log</c> so the smoke test can see
    /// how far the host got. Harmless when <c>--test-mode</c> is absent.
    /// </summary>
    protected override void OnStartup(StartupEventArgs e)
    {
        var options = Services.HostStartupOptions.Current();
        if (options.TestMode)
        {
            var dir = string.IsNullOrWhiteSpace(options.ReadinessDir)
                ? Path.GetTempPath() : options.ReadinessDir!;
            try
            {
                Directory.CreateDirectory(dir);
                File.AppendAllText(
                    Path.Combine(dir, "vibetable-trace.log"),
                    $"[{DateTimeOffset.UtcNow:O}] App.OnStartup: test-mode=1 args={string.Join("|", e.Args)}{Environment.NewLine}");
                // Catch any unhandled exception so the trace records it instead
                // of the process dying silently (WPF swallows startup faults).
                AppDomain.CurrentDomain.UnhandledException += (_, ue) =>
                {
                    try
                    {
                        File.AppendAllText(
                            Path.Combine(dir, "vibetable-trace.log"),
                            $"[{DateTimeOffset.UtcNow:O}] UNHANDLED: {ue.ExceptionObject}{Environment.NewLine}");
                    }
                    catch { /* best-effort */ }
                };
                System.Windows.Threading.Dispatcher.CurrentDispatcher.UnhandledException += (_, de) =>
                {
                    try
                    {
                        File.AppendAllText(
                            Path.Combine(dir, "vibetable-trace.log"),
                            $"[{DateTimeOffset.UtcNow:O}] DISPATCHER-EX: {de.Exception}{Environment.NewLine}");
                    }
                    catch { /* best-effort */ }
                    de.Handled = true;
                };
            }
            catch
            {
                // Best-effort trace.
            }
        }
        base.OnStartup(e);
    }
}
