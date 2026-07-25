using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Backend-owned support operations used by the product workspace. Business
/// records stay in the local data service; only per-user grid state and brokered atomic paste
/// operations cross this JSON-RPC adapter.
/// </summary>
public interface IWorkspaceSupportRpcGateway
{
    Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token);

    Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token);

    Task<PastePlan> PreviewPasteAsync(
        string collection, string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token);

    Task<ApplyPasteResult> ApplyPasteAsync(
        string collection, string token, string idempotencyKey,
        CancellationToken cancellationToken);
}
