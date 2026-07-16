using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// The RPC surface the <see cref="TableWorkspaceService"/> depends on. The
/// production adapter wraps the supervisor's
/// <see cref="VibeTable.Infrastructure.Rpc.JsonRpcClient"/>; tests inject a fake
/// that returns canned results without a backend process.
/// </summary>
/// <remarks>
/// The production implementation maps the established workspace contract to
/// Directus collections. The workspace stays a thin orchestrator for paging,
/// cancellation, mutation, query state, and stale-result suppression.
/// </remarks>
public interface ITableRpcGateway
{
    /// <summary>
    /// Opens the configured logical source and returns its collections.
    /// </summary>
    Task<DatabaseOpenResult> OpenDatabaseAsync(string path, CancellationToken token);

    /// <summary>
    /// Lists collections from the currently configured source.
    /// </summary>
    Task<TableSummary> ListTablesAsync(CancellationToken token);

    /// <summary>
    /// Reads one collection page. <paramref name="table"/> must come from a
    /// prior source-open result (the workspace service enforces this).
    /// </summary>
    Task<TablePage> ReadTablePageAsync(
        string table,
        int offset,
        int limit,
        CancellationToken token);

    /// <summary>
    /// Reads the editable schema for a Directus collection.
    /// </summary>
    Task<EditSchemaResult> GetEditSchemaAsync(string table, CancellationToken token);

    /// <summary>
    /// Updates one Directus item field with the expected-value conflict guard.
    /// </summary>
    Task<UpdateCellResult> UpdateCellAsync(
        string table,
        object rowKey,
        string column,
        object? oldValue,
        object? newValue,
        string schemaRevision,
        CancellationToken token);

    /// <summary>
    /// Creates one item in the Directus collection.
    /// </summary>
    Task<InsertRowResult> InsertRowAsync(
        string table,
        IReadOnlyDictionary<string, object?> values,
        string schemaRevision,
        CancellationToken token);

    /// <summary>
    /// Deletes Directus items after validating their expected digests.
    /// </summary>
    Task<DeleteRowsResult> DeleteRowsAsync(
        string table,
        IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
        string schemaRevision,
        CancellationToken token);

    /// <summary>
    /// Re-reads explicit Directus items for conflict refresh.
    /// </summary>
    Task<ReadRowsResult> ReadRowsAsync(
        string table,
        IReadOnlyList<object> rowKeys,
        CancellationToken token);

    /// <summary>
    /// Queries Directus with the typed table AST. Returns a page
    /// carrying <c>querySnapshot</c>/<c>revision</c>/<c>filteredRows</c> so the
    /// host can bind selection state to a stable snapshot.
    /// </summary>
    Task<TablePage> QueryTablePageAsync(
        string table,
        int offset,
        int limit,
        TableQuery query,
        CancellationToken token);

    /// <summary>
    /// Validates a carried query snapshot against the current Directus view
    /// and returns the invalidation reason when it is stale.
    /// </summary>
    Task<SnapshotValidation> ValidateSnapshotAsync(
        QuerySnapshot snapshot,
        int? currentRevision,
        CancellationToken token);

    /// <summary>
    /// B3 Task 3: calls <c>gridState.get</c>. Returns the saved grid state for
    /// ``(databaseId, table)`` or a default with a fresh revision.
    /// </summary>
    Task<GridStateResult> GetGridStateAsync(
        string databaseId,
        string table,
        CancellationToken token);

    /// <summary>
    /// B3 Task 3: calls <c>gridState.save</c>. Returns the new revision, or
    /// <c>Conflict=true</c> when the carried revision is stale.
    /// </summary>
    Task<GridStateResult> SaveGridStateAsync(
        string databaseId,
        string table,
        GridState state,
        string? revision,
        CancellationToken token);

    /// <summary>
    /// B2 Task 2: calls <c>table.previewPaste</c>. Produces a zero-write plan
    /// describing exactly what will change plus a single-use token bound to the
    /// user/collection/schema/rows/payload. The host shows the plan for
    /// confirmation before calling <see cref="ApplyPasteAsync"/>.
    /// </summary>
    Task<PastePlan> PreviewPasteAsync(
        string collection,
        string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token);

    /// <summary>
    /// B2 Task 3: calls <c>table.applyPaste</c>. Validates the preview token and
    /// delegates the atomic batch to the Directus bulk-mutation endpoint. Returns
    /// a server-confirmed result (committed/conflict/pending).
    /// </summary>
    Task<ApplyPasteResult> ApplyPasteAsync(
        string collection,
        string token,
        string idempotencyKey,
        CancellationToken cancellationToken);
}
