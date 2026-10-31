using System;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Text.Json;
using System.Threading.Tasks;
using System.Windows;
using VibeTable.Infrastructure;
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
        bool previewHost = e.Args is ["--preview-host", ..];
        if (previewHost)
        {
            InstallDesktopDiagnosticTrace(Services.HostStartupOptions.Current(), previewHost: true);
            PreviewHostEntry.TraceStage(PreviewHostStage.ManagedEntry);
        }
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
        Services.UpdateActivationStartupResolution updateStartup =
            Services.PendingUpdateActivationJournal.ResolveStartup(
                e.Args,
                AppContext.BaseDirectory,
                Services.PendingUpdateActivationJournal.CurrentProcessIdentity(),
                new Services.WpfUpdateHostLifetimeAdapter(this),
                Services.PendingUpdateActivationJournal.WaitForUpdaterExitAsync,
                Services.UpdateProcessCommand.TryCleanupStage);
        if (updateStartup.Disposition ==
            Services.UpdateActivationStartupDisposition.Blocked)
        {
            if (string.IsNullOrEmpty(Environment.GetEnvironmentVariable(
                    Services.UpdateProcessCommand.SmokeTokenEnvironmentVariable)))
            {
                MessageBox.Show(
                    "检测到未完成或无效的更新恢复事务，已阻止普通启动。",
                    "VibeTable 更新恢复",
                    MessageBoxButton.OK,
                    MessageBoxImage.Error);
            }
            Shutdown(2);
            return;
        }
        Services.IUpdateActivationSettlement? updateActivation = updateStartup.Settlement;
        _ = SetCurrentProcessExplicitAppUserModelID(
            ApplicationUserModelId);
        base.OnStartup(e);
        var options = Services.HostStartupOptions.Current();
        if (!previewHost && !ApplyPendingDataRootMigration(options))
        {
            return;
        }
        if (!previewHost && !EnsurePortableDataRootWritable(options))
        {
            return;
        }
        if (!previewHost) InstallDesktopDiagnosticTrace(options);
        InstallCrashDiagnostics();
        if (previewHost)
        {
            PreviewHostEntry.TraceStage(PreviewHostStage.DesktopStartupCompleted);
            int exitCode = PreviewHostEntry.Start(
                this,
                e.Args[1..]);
            if (exitCode != 0)
            {
                Shutdown(exitCode);
            }
            return;
        }

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
        var window = new MainWindow(updateActivation);
        if (options.TestMode)
            Services.TestModeReadinessWriter.Trace(options.ReadinessDir, "App: MainWindow constructed");
        MainWindow = window;
        // Show before Hide: with the default ShutdownMode=OnLastWindowClose,
        // hiding a window that was never shown can let WPF tear down the app
        // before it starts. Showing once registers the HWND, then Hide parks
        // it in the tray for a silent auto-start launch.
        if (options.TestMode)
            Services.TestModeReadinessWriter.Trace(options.ReadinessDir, "App: showing MainWindow");
        window.Show();
        if (options.TestMode)
            Services.TestModeReadinessWriter.Trace(options.ReadinessDir, "App: MainWindow Show returned");
        if (window.StartHidden)
        {
            window.Hide();
        }
        window.ReportTestModeStartupVisibility();
    }

    /// <summary>
    /// Portable mode keeps every runtime file beside the executable, so the
    /// data directory must be writable; a read-only location fails fast with
    /// actionable guidance instead of scattering files elsewhere. Standard,
    /// test, and development roots are skipped (their locations are writable
    /// by construction or explicitly supplied).
    /// </summary>
    private static bool EnsurePortableDataRootWritable(
        Services.HostStartupOptions startup)
    {
        if (startup.TestMode
            || !string.IsNullOrWhiteSpace(startup.DevelopmentDataRoot)
            || !LaunchPaths.IsPortableMode(AppContext.BaseDirectory))
        {
            return true;
        }
        string root = LaunchPaths.ResolvePortableDataRoot(AppContext.BaseDirectory);
        try
        {
            Directory.CreateDirectory(root);
            string probe = Path.Combine(
                root,
                ".write-probe-" + Guid.NewGuid().ToString("N"));
            File.WriteAllText(probe, string.Empty);
            File.Delete(probe);
            return true;
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or System.Security.SecurityException)
        {
            MessageBox.Show(
                "便携模式的数据目录不可写：\n"
                    + root
                    + "\n\n请将程序移动到可写位置后重试；或删除程序目录下 data\\"
                    + "portable.marker 文件以回到标准模式（数据保存到用户目录）。",
                "VibeTable 便携模式",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            Current.Shutdown(4);
            return false;
        }
    }

    private static void InstallCrashDiagnostics()
    {
        AppDomain.CurrentDomain.UnhandledException += (_, args) =>
            WriteCrashDiagnostic("desktop.appdomain_unhandled", args.ExceptionObject as Exception);
        System.Windows.Threading.Dispatcher.CurrentDispatcher.UnhandledException += (_, args) =>
            WriteCrashDiagnostic("desktop.dispatcher_unhandled", args.Exception);
    }

    /// <summary>
    /// Runs the pending standard⇄portable data-root switch before anything
    /// opens a handle inside either root. Returns false (after shutting down)
    /// when the migration fails; the source root is preserved for retry.
    /// </summary>
    private static bool ApplyPendingDataRootMigration(
        Services.HostStartupOptions startup)
    {
        try
        {
            Services.DataRootMigrationService.ApplyForLaunch(
                startup,
                AppContext.BaseDirectory,
                Environment.GetFolderPath(
                    Environment.SpecialFolder.LocalApplicationData));
            return true;
        }
        catch (Services.DataRootMigrationService.DataRootMigrationException exception)
        {
            MessageBox.Show(
                "切换数据目录模式时迁移失败，已保留原有数据，下次启动将自动重试。\n"
                    + $"{exception.Message}\n（错误码 {exception.ErrorCode}）",
                "VibeTable 数据迁移",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            Current.Shutdown(3);
            return false;
        }
    }

    private static void InstallDesktopDiagnosticTrace(
        Services.HostStartupOptions startup,
        bool previewHost = false)
    {
        try
        {
            string logName = previewHost ? $"preview-{Environment.ProcessId}.log" : "desktop.log";
            string logPath = Path.Combine(DesktopLogDirectory(startup, previewHost), logName);
            if (previewHost && startup.TestMode)
                Services.UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(logPath);
            Trace.Listeners.Add(new RotatingDiagnosticTraceListener(logPath));
        }
        catch
        {
            // Diagnostics must not prevent application startup.
        }
    }

    internal static string DesktopLogDirectory()
        => DesktopLogDirectory(Services.HostStartupOptions.Current(), IsPreviewHostProcess());

    private static bool IsPreviewHostProcess()
        => Environment.GetCommandLineArgs() is [_, "--preview-host", ..];

    /// <summary>
    /// All runtime logs share the active data root's logs directory; only the
    /// preview-host test process keeps its own scenario-isolated directory.
    /// </summary>
    internal static string DesktopLogDirectory(
        Services.HostStartupOptions startup,
        bool previewHost = false)
    {
        if (previewHost && startup.TestMode)
        {
            ArgumentException.ThrowIfNullOrWhiteSpace(startup.E2eControlsDir);
            return Path.Combine(
                Path.GetFullPath(startup.E2eControlsDir), "preview-host", "desktop-logs");
        }
        return Services.ProductDataRoot.LogsDirectory(startup);
    }

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
            if (IsPreviewHostProcess())
            {
                Trace.TraceError(line);
                return;
            }
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
