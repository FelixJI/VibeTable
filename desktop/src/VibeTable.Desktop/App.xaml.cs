using System;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Text.Json;
using System.Windows;
using VibeTable.Infrastructure.Diagnostics;
using VibeTable.PreviewHost;

namespace VibeTable.Desktop;

/// <summary>
/// Interaction logic for App.xaml. Kept deliberately minimal: the
/// <see cref="MainWindow"/> ctor builds its own production object graph
/// (supervisor, router, ViewModel, adapters); the startup sequence itself is
/// driven from <see cref="MainWindow.OnLoaded"/>, NOT from here.
/// </summary>
public partial class App : Application
{
    private const string ApplicationUserModelId = "VibeTable.Next";

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern int SetCurrentProcessExplicitAppUserModelID(
        string applicationId);

    /// <summary>
    /// Early-startup trace (test mode only). Writes to
    /// <c>&lt;readiness-dir&gt;/vibetable-trace.log</c> so the smoke test can see
    /// how far the host got. Harmless when <c>--test-mode</c> is absent.
    /// </summary>
    protected override void OnStartup(StartupEventArgs e)
    {
        if (Services.UpdateProcessCommand.TryApply(
                e.Args,
                out int updateExitCode,
                out string? updateError))
        {
            if (updateError is not null
                && string.IsNullOrEmpty(Environment.GetEnvironmentVariable(
                    Services.UpdateProcessCommand.SmokeTokenEnvironmentVariable)))
            {
                MessageBox.Show(
                    updateError,
                    "VibeTable 更新失败",
                    MessageBoxButton.OK,
                    MessageBoxImage.Error);
            }
            Shutdown(updateExitCode);
            return;
        }
        if (Services.UpdateProcessCommand.TryScheduleCleanup(e.Args))
        {
            Shutdown(0);
            return;
        }
        _ = SetCurrentProcessExplicitAppUserModelID(
            ApplicationUserModelId);
        base.OnStartup(e);
        InstallDesktopDiagnosticTrace();
        InstallCrashDiagnostics();
        if (e.Args is ["--preview-host", .. var previewArguments])
        {
            int exitCode = PreviewHostEntry.Start(
                this,
                previewArguments);
            if (exitCode != 0)
            {
                Shutdown(exitCode);
            }
            return;
        }

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
        var window = new MainWindow();
        MainWindow = window;
        // Show before Hide: with the default ShutdownMode=OnLastWindowClose,
        // hiding a window that was never shown can let WPF tear down the app
        // before it starts. Showing once registers the HWND, then Hide parks
        // it in the tray for a silent auto-start launch.
        window.Show();
        if (window.StartHidden)
        {
            window.Hide();
        }
        window.ReportTestModeStartupVisibility();
    }

    private static void InstallCrashDiagnostics()
    {
        AppDomain.CurrentDomain.UnhandledException += (_, args) =>
            WriteCrashDiagnostic("desktop.appdomain_unhandled", args.ExceptionObject as Exception);
        System.Windows.Threading.Dispatcher.CurrentDispatcher.UnhandledException += (_, args) =>
            WriteCrashDiagnostic("desktop.dispatcher_unhandled", args.Exception);
    }

    private static void InstallDesktopDiagnosticTrace()
    {
        try
        {
            Trace.Listeners.Add(new RotatingDiagnosticTraceListener(
                Path.Combine(DesktopLogDirectory(), "desktop.log")));
        }
        catch
        {
            // Diagnostics must not prevent application startup.
        }
    }

    internal static string DesktopLogDirectory() => Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "VibeTable",
        "logs");

    private static void WriteCrashDiagnostic(string eventName, Exception? exception)
    {
        try
        {
            string root = DesktopLogDirectory();
            string line = JsonSerializer.Serialize(new
            {
                timestamp = DateTimeOffset.UtcNow,
                level = "critical",
                module = "VibeTable.Desktop",
                @event = eventName,
                errorCode = exception?.GetType().Name ?? "UnknownException",
                requestId = (string?)null,
                operationId = (string?)null,
                workspaceId = (string?)null,
                sessionEpoch = (long?)null,
                jobId = (string?)null,
                durationMs = (double?)null,
            });
            var sink = new RotatingLogSink(Path.Combine(root, "desktop.log"));
            sink.WriteLineAsync(line).AsTask().GetAwaiter().GetResult();
            sink.DisposeAsync().AsTask().GetAwaiter().GetResult();
        }
        catch
        {
            // A crash reporter must never mask or replace the original fault.
        }
    }
}
