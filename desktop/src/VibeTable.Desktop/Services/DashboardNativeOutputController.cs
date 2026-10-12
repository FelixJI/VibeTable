using System.IO;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Native capabilities for dashboard output: the owner-bound save dialog and
/// the WebView2 browser print preview.
/// </summary>
internal interface INativeDashboardOutputHost
{
    /// <summary>Returns the user-selected absolute path, or null when cancelled.</summary>
    string? SelectExportTarget(string format, string defaultName);

    /// <summary>Opens the WebView2 browser print preview. Void only: the SDK
    /// reports neither completion nor cancellation, and a repeated trigger
    /// does not open a second dialog.</summary>
    void ShowPrintDialog();
}

/// <summary>
/// Owns the renderer's dashboard output requests: host-owned save-target
/// selection bound to the session file broker, and native print preview.
/// Each request must carry the current workspace scope and captures its
/// epoch lease before any native dialog opens, so a stale session can
/// never trigger native output in a newer workspace. The renderer never
/// receives a native path.
/// </summary>
internal sealed class DashboardNativeOutputController
{
    public const long MaxExportBytes = DashboardExportDownloadGate.MaxExportBytes;
    internal const string BlobUrlPrefix = "blob:https://app.vibetable.local/";

    private readonly IWebReplySink _reply;
    private readonly WorkspaceSessionEnvelopeFilter _sessions;
    private readonly INativeDashboardOutputHost _host;
    private readonly Func<HostSessionFileBroker?> _files;
    private readonly DashboardExportDownloadGate _gate;
    private readonly Action<string>? _trace;

    public DashboardNativeOutputController(
        IWebReplySink reply,
        WorkspaceSessionEnvelopeFilter sessions,
        INativeDashboardOutputHost host,
        Func<HostSessionFileBroker?> files,
        DashboardExportDownloadGate gate,
        Action<string>? trace = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _sessions = sessions ?? throw new ArgumentNullException(nameof(sessions));
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _files = files ?? throw new ArgumentNullException(nameof(files));
        _gate = gate ?? throw new ArgumentNullException(nameof(gate));
        _trace = trace;
    }

    public static bool Handles(string requestType)
        => requestType is "dashboard.exportTargetRequested" or "dashboard.exportRetireRequested"
            or "dashboard.printRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "dashboard.exportTargetRequested" => PrepareExportAsync(request),
            "dashboard.exportRetireRequested" => RetireExportAsync(request),
            "dashboard.printRequested" => PrintAsync(request),
            _ => RejectUnknownAsync(request),
        };

    private async Task PrepareExportAsync(RoutedWebRequest request)
    {
        if (!TryReadExportRequest(
                request.Payload,
                out string? fileName,
                out string? format,
                out string? blobUrl,
                out long byteLength)
            || format is null || blobUrl is null)
        {
            PostFailure(request, "仪表盘导出请求无效。", "BAD_PAYLOAD");
            return;
        }
        if (byteLength <= 0 || byteLength > MaxExportBytes)
        {
            PostFailure(request, "仪表盘导出超出字节预算。", "EXPORT_TOO_LARGE");
            return;
        }
        // Capture the workspace epoch BEFORE any native dialog: a request
        // without the current workspace scope, or from a session that a
        // workspace switch already retired, must not trigger native output.
        if (request.Scope is not { } scope
            || !_sessions.TryCapture(scope, out WorkspaceRequestEpochLease? lease)
            || lease is null)
        {
            PostFailure(request, "工作区已切换或会话无效，无法导出仪表盘。", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        {
            HostSessionFileBroker? broker;
            try
            {
                // Capture the binding BEFORE the modal dialog: a workspace switch
                // while the dialog is open must retire this export (the target is
                // never replaced), not rebind it to the new session's broker.
                broker = _files();
            }
            catch (BackendUnavailableException)
            {
                broker = null;
            }
            if (broker is null)
            {
                PostFailure(request, "当前工作区文件授权不可用。", "BACKEND_UNAVAILABLE");
                return;
            }
            // The modal dialog runs before any broker lock is taken, so no broker
            // gate is ever held across the wait.
            try
            {
                string defaultName = $"{SafeFileName(fileName)}.{format}";
                string? path = _host.SelectExportTarget(format, defaultName);
                if (string.IsNullOrWhiteSpace(path))
                {
                    PostFailure(request, "已取消保存仪表盘输出。", "CANCELLED");
                    return;
                }
                HostDownloadTarget target = await broker
                    .OpenDownloadTargetAsync(path)
                    .ConfigureAwait(true);
                if (!_gate.TryRegister(target, broker, blobUrl, byteLength))
                {
                    await broker.CompleteDownloadAsync(target.Id, commit: false)
                        .ConfigureAwait(true);
                    PostFailure(request, "仪表盘导出请求无效。", "BAD_PAYLOAD");
                    return;
                }
                _trace?.Invoke(
                    $"Dashboard export target granted; format={format}; bytes={byteLength}");
                _reply.PostResponse(
                    "dashboard.exportTargetGranted",
                    request.RequestId,
                    new { token = target.Id, displayName = target.DisplayName });
            }
            catch (OperationCanceledException)
            {
                _trace?.Invoke("Dashboard export target retired; reason=epoch-cancelled");
                PostFailure(request, "工作区已切换，已取消保存仪表盘输出。", "CANCELLED");
            }
            catch (Exception exception)
            {
                _trace?.Invoke(
                    $"Dashboard export target failed; exception={exception.GetType().Name}");
                PostFailure(request, "无法使用所选位置，请重新选择后重试。", "PATH_GRANT_FAILED");
            }
        }
    }

    private async Task RetireExportAsync(RoutedWebRequest request)
    {
        JsonElement payload = request.Payload;
        if (payload.ValueKind != JsonValueKind.Object || payload.EnumerateObject().Count() != 1
            || !payload.TryGetProperty("token", out JsonElement node)
            || node.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(node.GetString()))
        {
            PostFailure(request, "导出退休请求无效。", "BAD_PAYLOAD");
            return;
        }
        if (request.Scope is not { } scope
            || !_sessions.TryCapture(scope, out WorkspaceRequestEpochLease? lease) || lease is null)
        {
            PostFailure(request, "工作区会话无效，无法退休导出。", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        {
            try
            {
                HostSessionFileBroker? broker = _files();
                string token = node.GetString()!;
                bool? retired = broker is null ? null : await _gate.RetireAsync(token, broker).ConfigureAwait(true);
                if (retired is null)
                {
                    PostFailure(request, "导出授权已结束或不属于当前会话。", "BAD_EXPORT_TOKEN");
                    return;
                }
                _reply.PostResponse("dashboard.exportRetired", request.RequestId, new { token, retired = retired.Value });
            }
            catch (Exception error)
            {
                _trace?.Invoke($"Dashboard export retirement failed; exception={error.GetType().Name}");
                PostFailure(request, "无法完成导出退休。", "EXPORT_RETIRE_FAILED");
            }
        }
    }

    private Task PrintAsync(RoutedWebRequest request)
    {
        if (request.Payload.ValueKind != JsonValueKind.Object
            || request.Payload.EnumerateObject().Any())
        {
            PostFailure(request, "打印请求无效。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        // The print preview is native output too: it belongs to the current
        // workspace epoch, and a stale session must not open it.
        if (request.Scope is not { } scope
            || !_sessions.TryCapture(scope, out WorkspaceRequestEpochLease? lease)
            || lease is null)
        {
            PostFailure(request, "工作区已切换或会话无效，无法打开打印预览。", "BAD_WORKSPACE_SCOPE");
            return Task.CompletedTask;
        }
        using (lease)
        {
            try
            {
                _host.ShowPrintDialog();
            }
            catch (Exception exception)
            {
                // The dialog state is unknown only when the host call itself threw;
                // surface it instead of pretending the preview opened.
                _trace?.Invoke(
                    $"Dashboard print dialog failed; exception={exception.GetType().Name}");
                PostFailure(
                    request,
                    $"打印预览打开失败：{exception.GetType().Name}。",
                    "PRINT_FAILED");
                return Task.CompletedTask;
            }
            _reply.PostResponse(
                "dashboard.printDialogOpened",
                request.RequestId,
                new { opened = true });
            return Task.CompletedTask;
        }
    }

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        PostFailure(request, "仪表盘输出请求类型无效。", "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    internal static bool TryReadExportRequest(
        JsonElement payload,
        out string? fileName,
        out string? format,
        out string? blobUrl,
        out long byteLength)
    {
        fileName = null;
        format = null;
        blobUrl = null;
        byteLength = 0;
        if (payload.ValueKind != JsonValueKind.Object) return false;
        int properties = 0;
        foreach (JsonProperty property in payload.EnumerateObject())
        {
            properties++;
            switch (property.Name)
            {
                case "fileName" when property.Value.ValueKind == JsonValueKind.String:
                    fileName = property.Value.GetString();
                    break;
                case "format" when property.Value.ValueKind == JsonValueKind.String:
                    format = property.Value.GetString();
                    break;
                case "blobUrl" when property.Value.ValueKind == JsonValueKind.String:
                    blobUrl = property.Value.GetString();
                    break;
                case "byteLength" when property.Value.ValueKind == JsonValueKind.Number:
                    if (!property.Value.TryGetInt64(out byteLength)) return false;
                    break;
                default:
                    return false;
            }
        }
        return properties == 4
            && SafeFileName(fileName) is not null
            && format is "csv" or "png"
            && blobUrl is not null
            && blobUrl.StartsWith(BlobUrlPrefix, StringComparison.Ordinal)
            && blobUrl.Length <= BlobUrlPrefix.Length + 128
            && !blobUrl.Contains(' ');
    }

    private void PostFailure(RoutedWebRequest request, string message, string code)
        => _reply.PostOperationFailed(request.RequestId, message, code, request.Type);

    private static string? SafeFileName(string? value)
    {
        if (string.IsNullOrWhiteSpace(value)) return null;
        string name = Path.GetFileName(value.Trim());
        return name.Length is > 0 and <= 255
            && name.IndexOfAny(Path.GetInvalidFileNameChars()) < 0
                ? name
                : null;
    }
}

internal sealed class WindowsNativeDashboardOutputHost(
    System.Windows.Window owner,
    Func<Microsoft.Web.WebView2.Core.CoreWebView2?> core,
    string? e2eControlsDir = null) : INativeDashboardOutputHost
{
    private readonly System.Windows.Window _owner = owner
        ?? throw new ArgumentNullException(nameof(owner));
    private readonly Func<Microsoft.Web.WebView2.Core.CoreWebView2?> _core = core
        ?? throw new ArgumentNullException(nameof(core));

    public string? SelectExportTarget(string format, string defaultName)
    {
        if (ReadE2eCancelControl())
            return null;
        string? testPath = ReadE2eControlPath();
        if (testPath is not null)
            return testPath;
        var dialog = new Microsoft.Win32.SaveFileDialog
        {
            FileName = defaultName,
            AddExtension = true,
            DefaultExt = $".{format}",
            OverwritePrompt = true,
            Title = "保存仪表盘输出",
            Filter = format == "png" ? "PNG 图像|*.png" : "CSV 文件|*.csv",
        };
        return dialog.ShowDialog(_owner) == true ? dialog.FileName : null;
    }

    public void ShowPrintDialog()
    {
        Microsoft.Web.WebView2.Core.CoreWebView2? view = _core()
            ?? throw new InvalidOperationException("The product renderer is unavailable.");
        // Browser preview of the current page with the app print CSS. The SDK
        // does not open a second dialog on repeated calls and returns no
        // completion state; callers must not report one.
        view.ShowPrintUI();
    }

    private string? ReadE2eControlPath()
    {
        if (e2eControlsDir is null)
            return null;
        string controlPath = Path.Combine(e2eControlsDir, "dashboard-export-target.txt");
        if (!File.Exists(controlPath))
            return null;
        string value = File.ReadAllText(controlPath).Trim();
        if (string.IsNullOrWhiteSpace(value))
            throw new InvalidOperationException(
                $"Empty test-mode control file: dashboard-export-target.txt");
        return Path.GetFullPath(value);
    }

    /// <summary>
    /// Explicitly synthesized E2E cancel seam: the presence of this control
    /// file makes the test-mode picker behave exactly like a user cancelling
    /// the native save dialog. Production is unaffected.
    /// </summary>
    private bool ReadE2eCancelControl()
        => e2eControlsDir is not null
            && File.Exists(Path.Combine(e2eControlsDir, "dashboard-export-cancel.txt"));
}
