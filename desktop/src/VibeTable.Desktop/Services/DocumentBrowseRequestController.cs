using System.Collections.Concurrent;
using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Serialization;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Owns document browsing, host actions, and correlated revision comparison.
/// Capability validation, cancellation, and safe error mapping stay behind
/// the same dispatch interface used by production and interface tests.
/// </summary>
public sealed class DocumentBrowseRequestController
{
    private readonly IWebReplySink _reply;
    private readonly ConcurrentDictionary<string, DiffRequestState> _diffRequests = [];
    private WorkspaceDocumentOsAdapter? _documents;

    public DocumentBrowseRequestController(IWebReplySink reply)
        => _reply = reply ?? throw new ArgumentNullException(nameof(reply));

    public static bool Handles(string requestType)
        => requestType is
            "document.listRequested" or
            "document.openRequested" or
            "document.revealRequested" or
            "document.previewRequested" or
            "document.diffRequested" or
            "document.diffCancelRequested" or
            "document.pickRequested" or
            "document.relinkRequested";

    public void SetWorkspace(WorkspaceDocumentOsAdapter documents)
    {
        CancelDiffRequests();
        _documents = documents ?? throw new ArgumentNullException(nameof(documents));
    }

    public void RotateCapabilityEpoch()
    {
        CancelDiffRequests();
        _documents?.RotateCapabilityEpoch();
    }

    public Task DispatchAsync(RoutedWebRequest request)
        => request.Type switch
        {
            "document.listRequested" => ListAsync(request),
            "document.openRequested" => RunActionAsync(request, "open", value => value.Open),
            "document.revealRequested" => RunActionAsync(
                request,
                "reveal",
                value => value.Reveal),
            "document.previewRequested" => RunActionAsync(
                request,
                "preview",
                value => value.Preview),
            "document.diffRequested" => DiffAsync(request),
            "document.diffCancelRequested" => CancelDiffAsync(request),
            "document.pickRequested" => RejectAsync(
                request,
                "文件导入协议尚未就绪。",
                "DOCUMENT_IMPORT_NOT_READY"),
            "document.relinkRequested" => RejectAsync(
                request,
                "文件重新定位协议尚未就绪。",
                "DOCUMENT_RELINK_NOT_READY"),
            _ => RejectAsync(request, "文档请求类型无效。", "UNKNOWN_TYPE"),
        };

    private async Task ListAsync(RoutedWebRequest request)
    {
        WorkspaceDocumentOsAdapter? documents = RequireWorkspace(request);
        if (documents is null)
            return;
        string? authority = GetString(request.Payload, "authority");
        if (!string.Equals(authority, "workspace", StringComparison.Ordinal))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                "当前入口仅支持本地工作区文档。",
                "DOCUMENT_AUTHORITY_UNSUPPORTED");
            return;
        }
        if (!TryGetProperty(request.Payload, "scope", out JsonElement scope)
            || scope.ValueKind != JsonValueKind.Object)
        {
            Reject(request, "缺少文件范围。", "BAD_PAYLOAD");
            return;
        }

        try
        {
            DocumentListPayload result = GetString(scope, "kind") switch
            {
                "global" => await ListGlobalAsync(
                    documents,
                    request).ConfigureAwait(false),
                "record" => await ListRecordAsync(
                    documents,
                    request,
                    scope).ConfigureAwait(false),
                _ => throw new DocumentRequestPayloadException(
                    "未知文件范围。",
                    "BAD_PAYLOAD"),
            };
            _reply.PostResponse("document.listLoaded", request.RequestId, result);
        }
        catch (DocumentRequestPayloadException exception)
        {
            Reject(request, exception.Message, exception.Code);
        }
        catch (Exception exception)
        {
            PostFailure(request, exception, "DOCUMENT_LIST_FAILED");
        }
    }

    private static async Task<DocumentListPayload> ListGlobalAsync(
        WorkspaceDocumentOsAdapter documents,
        RoutedWebRequest request)
    {
        DocumentQueryInput query = DocumentQueryInput.Default;
        if (TryGetProperty(request.Payload, "query", out JsonElement raw))
        {
            try
            {
                query = JsonSerializer.Deserialize<DocumentQueryInput>(
                    raw.GetRawText(),
                    new JsonSerializerOptions
                    {
                        PropertyNameCaseInsensitive = false,
                        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
                    }) ?? throw new JsonException();
            }
            catch (JsonException)
            {
                throw new DocumentRequestPayloadException(
                    "文件查询参数无效。",
                    "FILE_QUERY_INVALID");
            }
        }
        return await documents.ListGlobalAsync(CancellationToken.None, query)
            .ConfigureAwait(false);
    }

    private static async Task<DocumentListPayload> ListRecordAsync(
        WorkspaceDocumentOsAdapter documents,
        RoutedWebRequest request,
        JsonElement scope)
    {
        string? collection = GetString(scope, "collection");
        string? itemId = GetScalarString(scope, "itemId");
        if (string.IsNullOrWhiteSpace(collection)
            || string.IsNullOrWhiteSpace(itemId))
        {
            throw new DocumentRequestPayloadException(
                "记录文件范围缺少 collection 或 itemId。",
                "BAD_PAYLOAD");
        }
        return await documents.ListRecordAsync(
            collection,
            itemId,
            CancellationToken.None).ConfigureAwait(false);
    }

    private Task RunActionAsync(
        RoutedWebRequest request,
        string action,
        Func<WorkspaceDocumentOsAdapter, Action<string>> selectAction)
        => Task.Run(() => RunAction(request, action, selectAction));

    private void RunAction(
        RoutedWebRequest request,
        string action,
        Func<WorkspaceDocumentOsAdapter, Action<string>> selectAction)
    {
        WorkspaceDocumentOsAdapter? documents = RequireWorkspace(request);
        if (documents is null)
            return;
        string? handle = GetString(request.Payload, "entryHandle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            Reject(request, "缺少文档授权。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            selectAction(documents)(handle);
            _reply.PostResponse(
                "document.actionCompleted",
                request.RequestId,
                new { entryHandle = handle, action });
        }
        catch (Exception exception)
        {
            PostFailure(request, exception, "DOCUMENT_ACTION_FAILED");
        }
    }

    private async Task DiffAsync(RoutedWebRequest request)
    {
        WorkspaceDocumentOsAdapter? documents = RequireWorkspace(request);
        if (documents is null)
            return;
        if (!TryCreateDiffState(request, out string operationId, out DiffRequestState state))
            return;
        try
        {
            DocumentDiffPayload result = await documents.CompareAsync(
                state.EntryHandle,
                state.HistoricalRevisionId,
                state.ExpectedEffectiveRevisionId,
                state.Token).ConfigureAwait(false);
            _reply.PostResponse("document.diffCompleted", request.RequestId, result);
        }
        catch (Exception exception)
        {
            PostFailure(request, exception, "DOCUMENT_DIFF_FAILED");
        }
        finally
        {
            _diffRequests.TryRemove(operationId, out _);
            state.Dispose();
        }
    }

    private bool TryCreateDiffState(
        RoutedWebRequest request,
        out string operationId,
        out DiffRequestState state)
    {
        operationId = string.Empty;
        state = null!;
        if (!HasExactProperties(
            request.Payload,
            "entryHandle",
            "historicalRevisionId",
            "expectedEffectiveRevisionId",
            "operationId"))
        {
            Reject(request, "文档比较参数无效。", "BAD_PAYLOAD");
            return false;
        }
        string? handle = GetString(request.Payload, "entryHandle");
        string? historicalRevisionId = GetString(
            request.Payload,
            "historicalRevisionId");
        string? expectedEffectiveRevisionId = GetString(
            request.Payload,
            "expectedEffectiveRevisionId");
        string? rawOperationId = GetString(request.Payload, "operationId");
        if (string.IsNullOrWhiteSpace(handle)
            || string.IsNullOrWhiteSpace(historicalRevisionId)
            || string.IsNullOrWhiteSpace(expectedEffectiveRevisionId)
            || !Guid.TryParseExact(rawOperationId, "D", out Guid parsedOperationId))
        {
            Reject(request, "文档比较参数无效。", "BAD_PAYLOAD");
            return false;
        }
        operationId = parsedOperationId.ToString("D");
        var candidate = new DiffRequestState(
            handle,
            historicalRevisionId,
            expectedEffectiveRevisionId,
            new CancellationTokenSource());
        if (!_diffRequests.TryAdd(operationId, candidate))
        {
            candidate.Dispose();
            Reject(
                request,
                "文档比较请求身份重复。",
                "DOCUMENT_DIFF_REQUEST_DUPLICATE");
            return false;
        }
        state = candidate;
        return true;
    }

    private Task CancelDiffAsync(RoutedWebRequest request)
    {
        if (!HasExactProperties(request.Payload, "entryHandle", "operationId"))
        {
            Reject(request, "文档比较取消参数无效。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        string? entryHandle = GetString(request.Payload, "entryHandle");
        string? rawOperationId = GetString(request.Payload, "operationId");
        if (string.IsNullOrWhiteSpace(entryHandle)
            || !Guid.TryParseExact(rawOperationId, "D", out Guid parsedOperationId))
        {
            Reject(request, "文档比较取消参数无效。", "BAD_PAYLOAD");
            return Task.CompletedTask;
        }
        bool cancelled = _diffRequests.TryGetValue(
                parsedOperationId.ToString("D"),
                out DiffRequestState? state)
            && string.Equals(
                state.EntryHandle,
                entryHandle,
                StringComparison.Ordinal);
        if (cancelled)
            state!.TryCancel();
        _reply.PostResponse(
            "document.diffCancelCompleted",
            request.RequestId,
            new { entryHandle, cancelled });
        return Task.CompletedTask;
    }

    private void CancelDiffRequests()
    {
        foreach (DiffRequestState state in _diffRequests.Values)
            state.TryCancel();
    }

    private WorkspaceDocumentOsAdapter? RequireWorkspace(RoutedWebRequest request)
    {
        if (_documents is null)
        {
            Reject(
                request,
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
        }
        return _documents;
    }

    private void PostFailure(
        RoutedWebRequest request,
        Exception exception,
        string fallbackCode)
    {
        string candidateCode = exception switch
        {
            DocumentCapabilityException value => value.Code,
            DocumentPreviewException value => value.Code,
            _ => fallbackCode,
        };
        (string code, string message) = GetSafeFailure(
            candidateCode,
            fallbackCode);
        Trace.TraceError(DiagnosticEvent.Failure(
            "VibeTable.Desktop.DocumentBrowseRequestController",
            "document",
            code));
        _reply.PostOperationFailed(request.RequestId, message, code);
    }

    private static (string Code, string Message) GetSafeFailure(
        string candidateCode,
        string fallbackCode)
        => candidateCode switch
        {
            "DOCUMENT_HANDLE_INVALID" =>
                (candidateCode, "文档授权已失效，请刷新文件列表后重试。"),
            "DOCUMENT_HANDLE_EXPIRED" =>
                (candidateCode, "文档授权已过期，请刷新文件列表后重试。"),
            "DOCUMENT_CAPABILITY_DENIED" =>
                (candidateCode, "当前文档不允许执行此操作。"),
            "REVISION_HANDLE_INVALID" =>
                (candidateCode, "版本授权已失效，请重新打开版本历史。"),
            "REVISION_HANDLE_EXPIRED" =>
                (candidateCode, "版本授权已过期，请重新打开版本历史。"),
            "REVISION_CAPABILITY_DENIED" =>
                (candidateCode, "当前版本不允许执行此操作。"),
            "DOCUMENT_LINK_UNAVAILABLE" =>
                (candidateCode, "当前文档没有可解除的记录关联。"),
            "WORKSPACE_UNMOUNTED" =>
                (candidateCode, "此工作区尚未挂载到本机。"),
            "DOCUMENT_MISSING" =>
                (candidateCode, "文件已移动或删除，请重新定位。"),
            "PREVIEW_HANDLER_UNAVAILABLE" =>
                (candidateCode, "系统没有可用的文件预览器，请使用默认应用打开。"),
            "PREVIEW_HOST_CREATE_FAILED" =>
                (candidateCode, "无法创建文件预览窗口，请稍后重试。"),
            "PREVIEW_HANDLER_FAILED" =>
                (candidateCode, "文件预览失败，请使用默认应用打开。"),
            "DOCUMENT_LIST_FAILED" =>
                (candidateCode, "文件列表加载失败，请稍后重试。"),
            "DOCUMENT_HISTORY_FAILED" =>
                (candidateCode, "版本历史加载失败，请稍后重试。"),
            "DOCUMENT_ACTION_FAILED" =>
                (candidateCode, "文档操作失败，请稍后重试。"),
            _ => GetSafeFallback(fallbackCode),
        };

    private static (string Code, string Message) GetSafeFallback(string fallbackCode)
        => fallbackCode switch
        {
            "DOCUMENT_LIST_FAILED" =>
                (fallbackCode, "文件列表加载失败，请稍后重试。"),
            "DOCUMENT_HISTORY_FAILED" =>
                (fallbackCode, "版本历史加载失败，请稍后重试。"),
            "DOCUMENT_ACTION_FAILED" =>
                (fallbackCode, "文档操作失败，请稍后重试。"),
            _ => ("DOCUMENT_OPERATION_FAILED", "文档操作失败，请稍后重试。"),
        };

    private Task RejectAsync(
        RoutedWebRequest request,
        string message,
        string code)
    {
        Reject(request, message, code);
        return Task.CompletedTask;
    }

    private void Reject(RoutedWebRequest request, string message, string code)
        => _reply.PostOperationFailed(request.RequestId, message, code);

    private static bool HasExactProperties(
        JsonElement value,
        params string[] expected)
        => value.ValueKind == JsonValueKind.Object
            && value.EnumerateObject()
                .Select(property => property.Name)
                .Order(StringComparer.Ordinal)
                .SequenceEqual(
                    expected.Order(StringComparer.Ordinal),
                    StringComparer.Ordinal);

    private static string? GetString(JsonElement payload, string propertyName)
        => payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(propertyName, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
                ? value.GetString()
                : null;

    private static string? GetScalarString(
        JsonElement payload,
        string propertyName)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || !payload.TryGetProperty(propertyName, out JsonElement value))
        {
            return null;
        }
        return value.ValueKind switch
        {
            JsonValueKind.String => value.GetString(),
            JsonValueKind.Number => value.GetRawText(),
            _ => null,
        };
    }

    private static bool TryGetProperty(
        JsonElement payload,
        string propertyName,
        out JsonElement value)
    {
        if (payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty(propertyName, out value))
        {
            value = value.Clone();
            return true;
        }
        value = default;
        return false;
    }

    private sealed class DiffRequestState(
        string entryHandle,
        string historicalRevisionId,
        string expectedEffectiveRevisionId,
        CancellationTokenSource cancellation) : IDisposable
    {
        public string EntryHandle { get; } = entryHandle;
        public string HistoricalRevisionId { get; } = historicalRevisionId;
        public string ExpectedEffectiveRevisionId { get; } = expectedEffectiveRevisionId;
        public CancellationToken Token => cancellation.Token;

        public void TryCancel()
        {
            try
            {
                cancellation.Cancel();
            }
            catch (ObjectDisposedException)
            {
            }
        }

        public void Dispose() => cancellation.Dispose();
    }

    private sealed class DocumentRequestPayloadException(
        string message,
        string code) : Exception(message)
    {
        public string Code { get; } = code;
    }
}
