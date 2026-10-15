using System.Diagnostics;
using System.IO;
using System.Text.Json;
using System.Windows;

namespace VibeTable.PreviewHost;

internal enum PreviewHostStage
{
    ManagedEntry,
    DesktopStartupCompleted,
    PreviewEntry,
    ArgumentsAccepted,
    WindowShowStarted,
    WindowShowReturned,
    ComActivationStarted,
    ComActivationReturned,
    HandlerInitializeStarted,
    HandlerInitializeReturned,
    HandlerPreviewQueryStarted,
    HandlerPreviewQueryReturned,
    HandlerStreamQueryStarted,
    HandlerStreamQueryReturned,
    HandlerFileQueryStarted,
    HandlerFileQueryReturned,
    HandlerStreamCreateStarted,
    HandlerStreamCreateReturned,
    HandlerStreamInitializeStarted,
    HandlerStreamInitializeReturned,
    HandlerFileInitializeStarted,
    HandlerFileInitializeReturned,
    HandlerSetWindowStarted,
    HandlerSetWindowReturned,
    HandlerDoPreviewStarted,
    HandlerDoPreviewReturned,
    PreviewSessionFailed,
    PreviewWindowFailed,
}

public static class PreviewHostEntry
{
    internal static void TraceStage(PreviewHostStage stage, Exception? exception = null,
        Guid? handlerClsid = null, int? resultHr = null)
    {
        try
        {
            // Typed facts use the existing closed event field; paths and COM messages never enter logs.
            string eventName = $"preview.host.phase.{stage}";
            if (handlerClsid is { } clsid)
                eventName += $".handler.{clsid:N}.apartment.{Thread.CurrentThread.GetApartmentState()}";
            if (resultHr is { } hr) eventName += $".hr.0x{hr:X8}";
            TraceEvent(eventName, exception);
        }
        catch
        {
            // Diagnostics must not change preview startup or failure handling.
        }
        PreviewHostResourceDiagnostics.Trace(stage);
    }

    internal static void TraceEvent(string eventName, Exception? exception = null)
    {
        try
        {
            Trace.TraceInformation(JsonSerializer.Serialize(new
            {
                timestamp = DateTimeOffset.UtcNow,
                level = exception is null ? "info" : "error",
                module = "document-preview",
                @event = eventName,
                errorCode = exception is null
                    ? null : $"{exception.GetType().Name}(0x{exception.HResult:X8})",
                requestId = (string?)null,
                operationId = (string?)null,
                workspaceId = (string?)null,
                sessionEpoch = (long?)null,
                jobId = (string?)null,
                durationMs = (double?)null,
            }));
        }
        catch
        {
            // Diagnostics must not change preview startup or failure handling.
        }
    }

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
        TraceStage(PreviewHostStage.PreviewEntry);
        application.ShutdownMode = ShutdownMode.OnExplicitShutdown;
        if (!PreviewHostArguments.TryParse(args, out var arguments)) return 2;
        TraceStage(PreviewHostStage.ArgumentsAccepted, handlerClsid: arguments.HandlerClsid);

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
            TraceStage(PreviewHostStage.PreviewWindowFailed, eventArgs.Exception);
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
            TraceStage(PreviewHostStage.WindowShowStarted);
            window.Show();
            TraceStage(PreviewHostStage.WindowShowReturned);
            window.Activate();
            return 0;
        }
        catch (Exception exception)
        {
            TraceStage(PreviewHostStage.PreviewWindowFailed, exception);
            WriteEvidence("failed");
            ShowSafeFailure("系统预览器无法加载此文件，请使用默认应用打开。");
            return 4;
        }
    }
}
