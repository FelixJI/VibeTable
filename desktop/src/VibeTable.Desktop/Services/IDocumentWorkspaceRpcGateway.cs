using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Typed metadata boundary for the G3 document workspace RPC surface.
/// Local paths and file contents never cross this interface.
/// </summary>
public interface IDocumentWorkspaceRpcGateway
{
    Task<DocumentListResult> ReadDocumentsAsync(
        int limit,
        int offset,
        CancellationToken token);

    Task<FolderResult> ReadFolderAsync(
        string collection,
        string itemId,
        CancellationToken token);

    Task<DocumentHistoryResult> ReadHistoryAsync(
        string documentId,
        int limit,
        int offset,
        CancellationToken token);

    Task<RegisterDocumentResult> RegisterDocumentAsync(
        RegisterDocumentParams request,
        CancellationToken token);

    Task UnlinkAsync(string linkId, CancellationToken token);
}
