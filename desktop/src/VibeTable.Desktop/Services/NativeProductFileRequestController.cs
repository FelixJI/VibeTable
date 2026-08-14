using System.IO;
using System.Text.Json;
using System.Windows;
using Microsoft.Win32;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

public sealed record NativeAttachmentSelection(
    IReadOnlyList<string> Paths,
    bool PickerWasShown);

/// <summary>
/// Native capabilities used by product file requests. The production adapter
/// owns Windows dialogs, WebView native file objects, test-mode controls, and
/// preview dispatch; controller tests use the same closed seam in memory.
/// </summary>
public interface INativeProductFileHost
{
    string? SelectImportSource();

    string? SelectExportTarget(string format, string defaultName);

    NativeAttachmentSelection SelectAttachmentSources(bool replacement);

    string? SelectAttachmentTarget(string suggestedName);

    string CreateAttachmentPreviewPath(string suggestedName);

    Task PreviewAttachmentAsync(string fullPath);
}

/// <summary>
/// Narrow RPC port required by native product file requests. It prevents the
/// request state machine from depending on the full product-data gateway.
/// </summary>
public interface IProductFileRpcGateway
{
    bool IsAvailable { get; }

    Task<JsonElement> RegisterImportSourceAsync(
        JsonElement parameters,
        CancellationToken cancellationToken);

    Task<JsonElement> RegisterExportTargetAsync(
        JsonElement parameters,
        CancellationToken cancellationToken);

    Task<JsonElement> ApplyAttachmentChangeAsync(
        JsonElement parameters,
        CancellationToken cancellationToken);

    Task<JsonElement> SaveAttachmentAsync(
        JsonElement parameters,
        CancellationToken cancellationToken);
}

/// <summary>
/// Owns the renderer-to-native file request state machine: native selection,
/// trusted path materialization, payload validation, correlation, cancellation,
/// and stable failure mapping. The WPF composition root only routes requests.
/// </summary>
public sealed class NativeProductFileRequestController
{
    private readonly IWebReplySink _reply;
    private readonly IProductFileRpcGateway _gateway;
    private readonly INativeProductFileHost _host;
    private readonly Func<CancellationToken> _sessionToken;
    private readonly Action<string>? _trace;

    public NativeProductFileRequestController(
        IWebReplySink reply,
        IProductFileRpcGateway gateway,
        INativeProductFileHost host,
        Func<CancellationToken>? sessionToken = null,
        Action<string>? trace = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _host = host ?? throw new ArgumentNullException(nameof(host));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
        _trace = trace;
    }

    public static bool Handles(string requestType)
        => requestType is
            "data.importSourceRequested" or
            "data.exportTargetRequested" or
            "file.uploadRequested" or
            "file.replaceRequested" or
            "file.removeRequested" or
            "file.previewRequested" or
            "file.downloadRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "data.importSourceRequested" => PickImportSourceAsync(request),
            "data.exportTargetRequested" => PickExportTargetAsync(request),
            "file.uploadRequested" => UploadAttachmentsAsync(request),
            "file.replaceRequested" => ReplaceAttachmentAsync(request),
            "file.removeRequested" => RemoveAttachmentAsync(request),
            "file.previewRequested" => PreviewAttachmentAsync(request),
            "file.downloadRequested" => DownloadAttachmentAsync(request),
            _ => RejectUnknownAsync(request),
        };

    private Task PickImportSourceAsync(RoutedWebRequest request)
    {
        if (!RequireGateway(request.RequestId))
            return Task.CompletedTask;
        string? selectedPath = _host.SelectImportSource();
        if (selectedPath is null)
        {
            _reply.PostOperationFailed(request.RequestId, "Import cancelled.", "CANCELLED");
            return Task.CompletedTask;
        }
        var info = new FileInfo(selectedPath);
        return RegisterPickedPathAsync(
            _gateway.RegisterImportSourceAsync,
            request,
            new
            {
                path = info.FullName,
                sizeBytes = info.Length,
                mimeType = (string?)null,
            });
    }

    private Task PickExportTargetAsync(RoutedWebRequest request)
    {
        if (!RequireGateway(request.RequestId))
            return Task.CompletedTask;
        string format = ReadString(request.Payload, "format") == "xlsx" ? "xlsx" : "csv";
        string defaultName = SafeFileName(ReadString(request.Payload, "defaultName"))
            ?? $"vibetable-export.{format}";
        string? selectedPath = _host.SelectExportTarget(format, defaultName);
        if (selectedPath is null)
        {
            _reply.PostOperationFailed(request.RequestId, "Export cancelled.", "CANCELLED");
            return Task.CompletedTask;
        }
        return RegisterPickedPathAsync(
            _gateway.RegisterExportTargetAsync,
            request,
            new { path = selectedPath });
    }

    private Task UploadAttachmentsAsync(RoutedWebRequest request)
    {
        NativeAttachmentSelection selection = _host.SelectAttachmentSources(
            replacement: false);
        if (selection.Paths.Count == 0)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                selection.PickerWasShown
                    ? "已取消选择附件。"
                    : "上传请求没有携带有效的原生文件对象。",
                selection.PickerWasShown
                    ? "CANCELLED"
                    : "ATTACHMENT_UPLOAD_OBJECTS_MISSING");
            return Task.CompletedTask;
        }
        _trace?.Invoke(
            $"Attachment upload request accepted; files={selection.Paths.Count}; " +
            $"requestIdPresent={!string.IsNullOrWhiteSpace(request.RequestId)}");
        return ApplyAttachmentChangeAsync(request, selection.Paths, []);
    }

    private Task ReplaceAttachmentAsync(RoutedWebRequest request)
    {
        NativeAttachmentSelection selection = _host.SelectAttachmentSources(
            replacement: true);
        string? storedName = ReadString(request.Payload, "storedName");
        if (selection.Paths.Count != 1 || string.IsNullOrWhiteSpace(storedName))
        {
            bool cancelled = selection.PickerWasShown && selection.Paths.Count == 0;
            _reply.PostOperationFailed(
                request.RequestId,
                cancelled
                    ? "已取消选择替换文件。"
                    : "附件替换必须携带一个原生文件和已有附件标识。",
                cancelled ? "CANCELLED" : "ATTACHMENT_REPLACE_INVALID");
            return Task.CompletedTask;
        }
        return ApplyAttachmentChangeAsync(request, selection.Paths, [storedName]);
    }

    private Task RemoveAttachmentAsync(RoutedWebRequest request)
    {
        string? storedName = ReadString(request.Payload, "storedName");
        if (string.IsNullOrWhiteSpace(storedName))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少托管附件标识。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        return ApplyAttachmentChangeAsync(request, [], [storedName]);
    }

    private async Task ApplyAttachmentChangeAsync(
        RoutedWebRequest request,
        IReadOnlyList<string> hostPaths,
        IReadOnlyList<string> removeStoredNames)
    {
        _trace?.Invoke(
            $"Attachment change starting; type={request.Type}; " +
            $"uploads={hostPaths.Count}; removals={removeStoredNames.Count}");
        if (!_gateway.IsAvailable
            || !TryReadAttachmentContext(
                request.Payload,
                requireDigest: true,
                out Dictionary<string, object?> context))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "缺少最新附件行版本，请刷新后重试。",
                "ATTACHMENT_CONTEXT_INVALID");
            return;
        }
        context["hostPaths"] = hostPaths;
        context["removeStoredNames"] = removeStoredNames;
        CancellationToken token = _sessionToken();
        try
        {
            JsonElement result = await _gateway.ApplyAttachmentChangeAsync(
                JsonSerializer.SerializeToElement(context),
                token);
            _trace?.Invoke($"Attachment change completed; type={request.Type}");
            _reply.PostResponse(request.Type, request.RequestId, result);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (Exception exception)
        {
            _trace?.Invoke(
                $"Attachment change failed; type={request.Type}; " +
                $"exception={exception.GetType().Name}");
            _reply.PostOperationFailed(
                request.RequestId,
                "托管附件变更失败，请刷新记录后重试。",
                "ATTACHMENT_CHANGE_FAILED");
        }
    }

    private async Task PreviewAttachmentAsync(RoutedWebRequest request)
    {
        if (!TryPrepareAttachmentSave(
                request,
                "附件预览参数无效。",
                out Dictionary<string, object?> context,
                out string suggestedName))
        {
            return;
        }
        string previewPath = _host.CreateAttachmentPreviewPath(suggestedName);
        context["outputPath"] = previewPath;
        CancellationToken token = _sessionToken();
        try
        {
            await _gateway.SaveAttachmentAsync(
                JsonSerializer.SerializeToElement(context),
                token);
            await _host.PreviewAttachmentAsync(previewPath);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (DocumentPreviewException exception)
        {
            _reply.PostOperationFailed(request.RequestId, exception.Message, exception.Code);
        }
        catch (Exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "托管附件预览失败，请稍后重试。",
                "ATTACHMENT_PREVIEW_FAILED");
        }
    }

    private async Task DownloadAttachmentAsync(RoutedWebRequest request)
    {
        if (!TryPrepareAttachmentSave(
                request,
                "附件下载参数无效。",
                out Dictionary<string, object?> context,
                out string suggestedName))
        {
            return;
        }
        string? outputPath = _host.SelectAttachmentTarget(suggestedName);
        if (outputPath is null)
            return;
        context["outputPath"] = outputPath;
        CancellationToken token = _sessionToken();
        try
        {
            await _gateway.SaveAttachmentAsync(
                JsonSerializer.SerializeToElement(context),
                token);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (Exception)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "托管附件下载失败，请重试。",
                "ATTACHMENT_DOWNLOAD_FAILED");
        }
    }

    private bool TryPrepareAttachmentSave(
        RoutedWebRequest request,
        string invalidContextMessage,
        out Dictionary<string, object?> context,
        out string suggestedName)
    {
        suggestedName = string.Empty;
        context = [];
        if (!_gateway.IsAvailable
            || !TryReadAttachmentContext(
                request.Payload,
                requireDigest: false,
                out context))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                invalidContextMessage,
                "ATTACHMENT_CONTEXT_INVALID");
            return false;
        }
        string? storedName = ReadString(request.Payload, "storedName");
        string? originalName = ReadString(request.Payload, "originalName");
        if (string.IsNullOrWhiteSpace(storedName))
        {
            _reply.PostOperationFailed(request.RequestId, "缺少托管附件标识。", "BAD_PAYLOAD");
            return false;
        }
        suggestedName = SafeFileName(originalName)
            ?? SafeFileName(storedName)
            ?? "attachment.bin";
        context["storedName"] = storedName;
        return true;
    }

    private async Task RegisterPickedPathAsync(
        Func<JsonElement, CancellationToken, Task<JsonElement>> register,
        RoutedWebRequest request,
        object parameters)
    {
        CancellationToken token = _sessionToken();
        try
        {
            JsonElement grant = await register(
                JsonSerializer.SerializeToElement(parameters),
                token);
            _reply.PostResponse(request.Type, request.RequestId, grant);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested)
        {
        }
        catch (Exception exception)
        {
            _trace?.Invoke(
                $"Path grant failed; type={request.Type}; " +
                $"exception={exception.GetType().Name}");
            _reply.PostOperationFailed(
                request.RequestId,
                "无法使用所选位置，请重新选择后重试。",
                "PATH_GRANT_FAILED");
        }
    }

    private bool RequireGateway(string? requestId)
    {
        if (_gateway.IsAvailable)
            return true;
        _reply.PostOperationFailed(
            requestId,
            "Local data service is unavailable.",
            "BACKEND_UNAVAILABLE");
        return false;
    }

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(
            request.RequestId,
            "原生文件请求类型无效。",
            "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    private static bool TryReadAttachmentContext(
        JsonElement payload,
        bool requireDigest,
        out Dictionary<string, object?> context)
    {
        context = new Dictionary<string, object?>(StringComparer.Ordinal);
        foreach (string name in new[] { "tableId", "recordId", "fieldId" })
        {
            string? value = ReadString(payload, name);
            if (string.IsNullOrWhiteSpace(value))
            {
                context.Clear();
                return false;
            }
            context[name] = value;
        }
        if (!requireDigest)
            return true;
        string? schemaRevision = ReadString(payload, "schemaRevision");
        string? expectedDigest = ReadString(payload, "expectedDigest");
        if (string.IsNullOrWhiteSpace(schemaRevision) || !IsRowDigest(expectedDigest))
        {
            context.Clear();
            return false;
        }
        context["schemaRevision"] = schemaRevision;
        context["expectedDigest"] = expectedDigest;
        return true;
    }

    private static bool IsRowDigest(string? value)
        => value is { Length: 71 }
            && value.StartsWith("sha256:", StringComparison.Ordinal)
            && value.AsSpan(7).IndexOfAnyExcept("0123456789abcdef".AsSpan()) < 0;

    private static string? SafeFileName(string? value)
    {
        if (string.IsNullOrWhiteSpace(value))
            return null;
        string name = Path.GetFileName(value);
        return name.Length is > 0 and <= 255
            && name.IndexOfAny(Path.GetInvalidFileNameChars()) < 0
                ? name
                : null;
    }

    private static string? ReadString(JsonElement value, string name)
        => value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty(name, out JsonElement item)
            && item.ValueKind == JsonValueKind.String
                ? item.GetString()
                : null;
}

internal sealed class ProductFileRpcGatewayAdapter(
    Func<JsonRpcProductDataGateway?> gateway) : IProductFileRpcGateway
{
    public bool IsAvailable => gateway() is not null;

    public Task<JsonElement> RegisterImportSourceAsync(
        JsonElement parameters,
        CancellationToken cancellationToken)
        => RequireGateway().RegisterImportSourceAsync(parameters, cancellationToken);

    public Task<JsonElement> RegisterExportTargetAsync(
        JsonElement parameters,
        CancellationToken cancellationToken)
        => RequireGateway().RegisterExportTargetAsync(parameters, cancellationToken);

    public Task<JsonElement> ApplyAttachmentChangeAsync(
        JsonElement parameters,
        CancellationToken cancellationToken)
        => RequireGateway().ApplyHostAttachmentChangeAsync(parameters, cancellationToken);

    public Task<JsonElement> SaveAttachmentAsync(
        JsonElement parameters,
        CancellationToken cancellationToken)
        => RequireGateway().SaveAttachmentToHostAsync(parameters, cancellationToken);

    private JsonRpcProductDataGateway RequireGateway()
        => gateway() ?? throw new InvalidOperationException(
            "The product file RPC gateway is unavailable.");
}

internal sealed class WindowsNativeProductFileHost : INativeProductFileHost
{
    private readonly Window _owner;
    private readonly Func<IReadOnlyList<string>> _nativePaths;
    private readonly string? _e2eControlsDir;
    private readonly string _attachmentPreviewRoot;
    private readonly ILocalDocumentPreview _attachmentPreview;

    public WindowsNativeProductFileHost(
        Window owner,
        Func<IReadOnlyList<string>> nativePaths,
        string? e2eControlsDir,
        string attachmentPreviewRoot,
        ILocalDocumentPreview attachmentPreview)
    {
        _owner = owner ?? throw new ArgumentNullException(nameof(owner));
        _nativePaths = nativePaths ?? throw new ArgumentNullException(nameof(nativePaths));
        _e2eControlsDir = e2eControlsDir;
        _attachmentPreviewRoot = Path.GetFullPath(attachmentPreviewRoot);
        _attachmentPreview = attachmentPreview
            ?? throw new ArgumentNullException(nameof(attachmentPreview));
    }

    public string? SelectImportSource()
    {
        string? testPath = ReadE2eControlPath(
            "import-source.txt",
            requireExistingFile: true);
        if (testPath is not null)
            return testPath;
        var dialog = new OpenFileDialog
        {
            CheckFileExists = true,
            Multiselect = false,
            Title = "Import table data",
            Filter = "Supported data|*.xlsx;*.xlsm;*.csv|Excel workbook|*.xlsx;*.xlsm|CSV file|*.csv",
        };
        return dialog.ShowDialog(_owner) == true ? dialog.FileName : null;
    }

    public string? SelectExportTarget(string format, string defaultName)
    {
        string? testPath = ReadE2eControlPath(
            "export-target.txt",
            requireExistingFile: false);
        if (testPath is not null)
            return testPath;
        var dialog = new SaveFileDialog
        {
            FileName = defaultName,
            AddExtension = true,
            DefaultExt = $".{format}",
            OverwritePrompt = true,
            Title = "Export table data",
            Filter = format == "xlsx" ? "Excel workbook|*.xlsx" : "CSV file|*.csv",
        };
        return dialog.ShowDialog(_owner) == true ? dialog.FileName : null;
    }

    public NativeAttachmentSelection SelectAttachmentSources(bool replacement)
    {
        IReadOnlyList<string> paths = _nativePaths();
        if (paths.Count > 0)
            return new NativeAttachmentSelection(paths, PickerWasShown: false);
        if (_e2eControlsDir is not null)
        {
            string controlName = replacement
                ? "attachment-replacement-source.txt"
                : "attachment-source.txt";
            return new NativeAttachmentSelection(
                [ReadE2eControlPath(controlName, requireExistingFile: true)!],
                PickerWasShown: false);
        }
        var dialog = new OpenFileDialog
        {
            CheckFileExists = true,
            Multiselect = !replacement,
            Title = replacement ? "Replace attachment" : "Add attachments",
            Filter = "All files|*.*",
        };
        IReadOnlyList<string> selected = dialog.ShowDialog(_owner) == true
            ? dialog.FileNames
            : [];
        return new NativeAttachmentSelection(selected, PickerWasShown: true);
    }

    public string? SelectAttachmentTarget(string suggestedName)
    {
        var dialog = new SaveFileDialog
        {
            FileName = suggestedName,
            AddExtension = false,
            CheckPathExists = true,
            OverwritePrompt = true,
            Title = "保存托管附件",
            Filter = "所有文件|*.*",
        };
        return dialog.ShowDialog(_owner) == true ? dialog.FileName : null;
    }

    public string CreateAttachmentPreviewPath(string suggestedName)
        => Path.Combine(
            _attachmentPreviewRoot,
            $"{Guid.NewGuid():N}-{suggestedName}");

    public Task PreviewAttachmentAsync(string fullPath)
        => _owner.Dispatcher.InvokeAsync(() =>
        {
            if (!_attachmentPreview.CanPreview(fullPath))
            {
                throw new DocumentPreviewException(
                    "系统没有为此文件类型注册安全预览器。",
                    "PREVIEW_HANDLER_UNAVAILABLE");
            }
            _attachmentPreview.Show(fullPath);
        }).Task;

    private string? ReadE2eControlPath(
        string controlName,
        bool requireExistingFile)
    {
        if (_e2eControlsDir is null)
            return null;
        string controlPath = Path.Combine(_e2eControlsDir, controlName);
        if (!File.Exists(controlPath))
        {
            throw new InvalidOperationException(
                $"Missing test-mode control file: {controlName}");
        }
        string value = File.ReadAllText(controlPath).Trim();
        if (string.IsNullOrWhiteSpace(value))
        {
            throw new InvalidOperationException(
                $"Empty test-mode control file: {controlName}");
        }
        string fullPath = Path.GetFullPath(value);
        if (requireExistingFile && !File.Exists(fullPath))
        {
            throw new FileNotFoundException(
                $"Test-mode selected file does not exist: {controlName}",
                fullPath);
        }
        return fullPath;
    }
}
