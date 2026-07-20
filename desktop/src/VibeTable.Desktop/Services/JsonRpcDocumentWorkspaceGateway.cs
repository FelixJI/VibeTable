using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// JSON-RPC adapter for document index metadata. The Python broker talks to
/// Directus with the current user token; this adapter never receives that token.
/// </summary>
public sealed class JsonRpcDocumentWorkspaceGateway : IDocumentWorkspaceRpcGateway
{
    private readonly JsonRpcClient _client;

    public JsonRpcDocumentWorkspaceGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
    }

    public Task<DocumentListResult> ReadDocumentsAsync(
        int limit,
        int offset,
        CancellationToken token)
        => _client.InvokeAsync<ReadDocumentsParams, DocumentListResult>(
            "workspace.readDocuments",
            new ReadDocumentsParams(limit, offset),
            token);

    public Task<FolderResult> ReadFolderAsync(
        string collection,
        string itemId,
        CancellationToken token)
        => _client.InvokeAsync<ReadFolderParams, FolderResult>(
            "workspace.readFolder",
            new ReadFolderParams(collection, itemId),
            token);

    public Task<DocumentHistoryResult> ReadHistoryAsync(
        string documentId,
        int limit,
        int offset,
        CancellationToken token)
        => _client.InvokeAsync<ReadDocumentHistoryParams, DocumentHistoryResult>(
            "workspace.readDocumentHistory",
            new ReadDocumentHistoryParams(documentId, limit, offset),
            token);

    public Task<RegisterDocumentResult> RegisterDocumentAsync(
        RegisterDocumentParams request,
        CancellationToken token)
        => _client.InvokeAsync<RegisterDocumentParams, RegisterDocumentResult>(
            "workspace.registerDocument",
            request,
            token);

    public async Task UnlinkAsync(string linkId, CancellationToken token)
    {
        await _client.InvokeAsync<UnlinkDocumentParams, UnlinkDocumentResult>(
            "workspace.unlinkDocument",
            new UnlinkDocumentParams(linkId),
            token).ConfigureAwait(false);
    }

    private sealed record UnlinkDocumentResult(string Deleted);
}
