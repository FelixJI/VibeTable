using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Workspace.Domain;

namespace VibeTable.Desktop.Services;

/// <summary>
/// JSON-RPC adapter for provider-neutral document index metadata. Local paths
/// and file content never cross this boundary.
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
    {
        request = request with
        {
            CreatedAt = UtcRfc3339Timestamp.Canonicalize(
                request.CreatedAt,
                nameof(request.CreatedAt)),
        };
        return _client.InvokeAsync<RegisterDocumentParams, RegisterDocumentResult>(
            "workspace.registerDocument",
            request,
            token);
    }

    public Task<PublishIndexBatchResult> PublishIndexBatchAsync(
        PublishIndexBatchParams request,
        CancellationToken token)
    {
        request = request with
        {
            Revisions = request.Revisions
                .Select(revision => revision with
                {
                    CreatedAt = UtcRfc3339Timestamp.Canonicalize(
                        revision.CreatedAt,
                        nameof(revision.CreatedAt)),
                })
                .ToList(),
        };
        return _client.InvokeAsync<PublishIndexBatchParams, PublishIndexBatchResult>(
            "workspace.publishIndexBatch",
            request,
            token);
    }

    public async Task UnlinkAsync(string linkId, CancellationToken token)
    {
        await _client.InvokeAsync<UnlinkDocumentParams, UnlinkDocumentResult>(
            "workspace.unlinkDocument",
            new UnlinkDocumentParams(linkId),
            token).ConfigureAwait(false);
    }

    private sealed record UnlinkDocumentResult(string Deleted);
}
