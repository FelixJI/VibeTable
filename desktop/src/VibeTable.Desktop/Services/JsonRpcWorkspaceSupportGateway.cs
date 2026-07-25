using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

public sealed record GridStateGetParams(string DatabaseId, string Table);

public sealed record GridStateSaveParams(
    string DatabaseId, string Table, GridState State, string? Revision);

public sealed record PreviewPasteRpcParams(
    string Collection,
    string SchemaRevision,
    IReadOnlyDictionary<string, object?> Selection,
    PasteStartCell StartCell,
    IReadOnlyList<IReadOnlyList<PasteCell>> Cells);

public sealed record ApplyPasteRpcParams(
    string Collection, string Token, string IdempotencyKey);

/// <summary>
/// JSON-RPC adapter for backend support operations that remain local after
/// the provider migration. It intentionally exposes no local business-table
/// read/write methods.
/// </summary>
public sealed class JsonRpcWorkspaceSupportGateway : IWorkspaceSupportRpcGateway
{
    private readonly JsonRpcClient _client;

    public JsonRpcWorkspaceSupportGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
    }

    public Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token)
        => _client.InvokeAsync<GridStateGetParams, GridStateResult>(
            "gridState.get",
            new GridStateGetParams(databaseId, table),
            token);

    public Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token)
        => _client.InvokeAsync<GridStateSaveParams, GridStateResult>(
            "gridState.save",
            new GridStateSaveParams(databaseId, table, state, revision),
            token);

    public Task<PastePlan> PreviewPasteAsync(
        string collection, string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token)
        => _client.InvokeAsync<PreviewPasteRpcParams, PastePlan>(
            "table.previewPaste",
            new PreviewPasteRpcParams(
                collection, schemaRevision, selection, startCell, cells),
            token);

    public Task<ApplyPasteResult> ApplyPasteAsync(
        string collection, string token, string idempotencyKey,
        CancellationToken cancellationToken)
        => _client.InvokeAsync<ApplyPasteRpcParams, ApplyPasteResult>(
            "table.applyPaste",
            new ApplyPasteRpcParams(collection, token, idempotencyKey),
            cancellationToken);
}
