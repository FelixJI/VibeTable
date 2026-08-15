using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// The document commands required by the renderer request state machine.
/// <see cref="WorkspaceDocumentOsAdapter"/> is the production adapter; tests
/// use an in-memory adapter at the same seam.
/// </summary>
public interface IWorkspaceDocumentCommands
{
    Task<WorkspaceDocumentImportResult?> ImportFromPickerAsync(
        CancellationToken cancellationToken);

    Task<WorkspaceDocumentImportResult> ImportFromHostPathAsync(
        string sourcePath,
        CancellationToken cancellationToken);

    Task<WorkspaceDocumentRelinkResult?> RelinkFromPickerAsync(
        string handle,
        CancellationToken cancellationToken);

    string ResolveDragOutPath(string handle);
}

/// <summary>
/// Owns the complete renderer-to-host FileDocument command lifecycle behind
/// one closed interface. The WPF composition root supplies the current
/// workspace adapter, native drop objects, session cancellation, and the
/// actual Windows drag gesture; it owns none of the validation or result
/// state machine.
/// </summary>
public sealed class DocumentRequestController
{
    private const int MaxExternalDropFiles = 100;

    private readonly IWebReplySink _reply;
    private readonly Func<IWorkspaceDocumentCommands?> _workspace;
    private readonly Func<IReadOnlyList<string>> _nativePaths;
    private readonly Action<string> _dragOut;
    private readonly Func<CancellationToken> _sessionToken;

    public DocumentRequestController(
        IWebReplySink reply,
        Func<IWorkspaceDocumentCommands?> workspace,
        Func<IReadOnlyList<string>> nativePaths,
        Action<string> dragOut,
        Func<CancellationToken>? sessionToken = null)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _workspace = workspace ?? throw new ArgumentNullException(nameof(workspace));
        _nativePaths = nativePaths ?? throw new ArgumentNullException(nameof(nativePaths));
        _dragOut = dragOut ?? throw new ArgumentNullException(nameof(dragOut));
        _sessionToken = sessionToken ?? (() => CancellationToken.None);
    }

    public static bool Handles(string requestType)
        => requestType is
            "document.importRequested" or
            "document.externalDropRequested" or
            "document.relinkRequested" or
            "document.dragOutRequested";

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "document.importRequested" => ImportFromPickerAsync(),
            "document.externalDropRequested" => ImportExternalDropAsync(),
            "document.relinkRequested" => RelinkAsync(request.Payload),
            "document.dragOutRequested" => DragOutAsync(request.Payload),
            _ => RejectUnknownAsync(request),
        };

    private async Task ImportFromPickerAsync()
    {
        IWorkspaceDocumentCommands? workspace = RequireWorkspace();
        if (workspace is null)
            return;
        try
        {
            WorkspaceDocumentImportResult? result = await workspace
                .ImportFromPickerAsync(_sessionToken());
            if (result is not null)
                PostWorkspaceChanged("import", 1);
        }
        catch (OperationCanceledException)
        {
        }
        catch (Exception exception)
        {
            PostFailure(exception, "DOCUMENT_IMPORT_FAILED");
        }
    }

    private async Task ImportExternalDropAsync()
    {
        IReadOnlyList<string> paths = _nativePaths();
        if (paths.Count == 0)
        {
            PostFailure(
                "拖入请求没有携带有效的原生文件对象。",
                "DOCUMENT_DROP_OBJECTS_MISSING");
            return;
        }
        IWorkspaceDocumentCommands? workspace = RequireWorkspace();
        if (workspace is null)
            return;

        int imported = 0;
        try
        {
            foreach (string path in paths.Take(MaxExternalDropFiles))
            {
                await workspace.ImportFromHostPathAsync(path, _sessionToken());
                imported++;
            }
            if (imported > 0)
                PostWorkspaceChanged("import", imported);
        }
        catch (OperationCanceledException)
        {
        }
        catch (Exception exception)
        {
            if (imported > 0)
                PostWorkspaceChanged("import", imported);
            PostFailure(exception, "DOCUMENT_IMPORT_FAILED");
        }
    }

    private async Task RelinkAsync(JsonElement payload)
    {
        IWorkspaceDocumentCommands? workspace = RequireWorkspace();
        if (workspace is null)
            return;
        string? handle = ReadString(payload, "handle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            PostFailure("缺少文档授权。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            WorkspaceDocumentRelinkResult? result = await workspace
                .RelinkFromPickerAsync(handle, _sessionToken());
            if (result is not null)
                PostWorkspaceChanged("relink", 1);
        }
        catch (OperationCanceledException)
        {
        }
        catch (Exception exception)
        {
            PostFailure(exception, "DOCUMENT_RELINK_FAILED");
        }
    }

    private Task DragOutAsync(JsonElement payload)
    {
        IWorkspaceDocumentCommands? workspace = RequireWorkspace();
        if (workspace is null)
            return Task.CompletedTask;
        string? handle = ReadString(payload, "handle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            PostFailure("缺少文档授权。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        try
        {
            _dragOut(workspace.ResolveDragOutPath(handle));
        }
        catch (Exception exception)
        {
            PostFailure(exception, "DOCUMENT_DRAG_OUT_FAILED");
        }
        return Task.CompletedTask;
    }

    private IWorkspaceDocumentCommands? RequireWorkspace()
    {
        IWorkspaceDocumentCommands? workspace = _workspace();
        if (workspace is null)
        {
            PostFailure(
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
        }
        return workspace;
    }

    private void PostWorkspaceChanged(string reason, int affectedCount)
        => _reply.PostNotification(
            "document.workspaceChanged",
            new { reason, affectedCount });

    private void PostFailure(Exception exception, string fallbackCode)
    {
        string message = exception switch
        {
            DocumentFileOperationException value => value.Message,
            DocumentCapabilityException value => value.Message,
            _ => "文件操作未完成，请重试。",
        };
        string code = exception switch
        {
            DocumentFileOperationException value => value.Code,
            DocumentCapabilityException value => value.Code,
            _ => fallbackCode,
        };
        PostFailure(message, code);
    }

    private void PostFailure(string message, string code)
        => _reply.PostNotification(
            "document.operationFailed",
            new DocumentOperationFailedPayload(message, code));

    private Task RejectUnknownAsync(RoutedWebRequest request)
    {
        _reply.PostOperationFailed(
            request.RequestId,
            "文档请求类型无效。",
            "UNKNOWN_TYPE");
        return Task.CompletedTask;
    }

    private static string? ReadString(JsonElement value, string name)
        => value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty(name, out JsonElement item)
            && item.ValueKind == JsonValueKind.String
                ? item.GetString()
                : null;
}
