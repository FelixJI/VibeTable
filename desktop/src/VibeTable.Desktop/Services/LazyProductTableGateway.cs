using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Binds the stable workspace orchestration service to the current supervised
/// Python RPC generation. Retired generations stay alive until host shutdown
/// so a restart cannot dispose an in-flight request.
/// </summary>
public sealed class LazyProductTableGateway : ITableRpcGateway, IDisposable
{
    private PythonBackendSupervisor? _supervisor;
    private readonly object _gate = new();
    private readonly List<PocketBaseTableGateway> _retired = [];
    private JsonRpcClient? _resolvedClient;
    private PocketBaseTableGateway? _resolved;
    private bool _disposed;

    public LazyProductTableGateway()
    {
    }

    public LazyProductTableGateway(PythonBackendSupervisor supervisor)
        => Bind(supervisor);

    public void Bind(PythonBackendSupervisor supervisor)
    {
        ArgumentNullException.ThrowIfNull(supervisor);
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (ReferenceEquals(_supervisor, supervisor))
                return;
            RetireResolved();
            _supervisor = supervisor;
        }
    }

    public void Unbind(PythonBackendSupervisor? supervisor = null)
    {
        lock (_gate)
        {
            if (_disposed
                || (supervisor is not null
                    && !ReferenceEquals(_supervisor, supervisor)))
            {
                return;
            }
            RetireResolved();
            _supervisor = null;
        }
    }

    private PocketBaseTableGateway Gateway
    {
        get
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            lock (_gate)
            {
                PythonBackendSupervisor supervisor = _supervisor
                    ?? throw new BackendUnavailableException(
                        "No workspace runtime is bound.");
                JsonRpcClient client = supervisor.Client
                    ?? throw new BackendUnavailableException(
                        "Product data service is not ready.");
                if (_resolved is not null
                    && ReferenceEquals(_resolvedClient, client))
                {
                    return _resolved;
                }
                if (_resolved is not null)
                {
                    _retired.Add(_resolved);
                }
                _resolvedClient = client;
                _resolved = new PocketBaseTableGateway(
                    new JsonRpcProductDataGateway(client),
                    new JsonRpcWorkspaceSupportGateway(client));
                return _resolved;
            }
        }
    }

    public Task<DatabaseOpenResult> OpenDatabaseAsync(string path, CancellationToken token)
        => Gateway.OpenDatabaseAsync(path, token);
    public Task<TableSummary> ListTablesAsync(CancellationToken token)
        => Gateway.ListTablesAsync(token);
    public Task<EditSchemaResult> GetEditSchemaAsync(string table, CancellationToken token)
        => Gateway.GetEditSchemaAsync(table, token);
    public Task<UpdateCellResult> UpdateCellAsync(
        string table, object rowKey, string column, object? oldValue,
        object? newValue, string schemaRevision, CancellationToken token,
        string? expectedDigest = null)
        => Gateway.UpdateCellAsync(
            table, rowKey, column, oldValue, newValue, schemaRevision, token,
            expectedDigest);
    public Task<InsertRowResult> InsertRowAsync(
        string table, IReadOnlyDictionary<string, object?> values,
        string schemaRevision, CancellationToken token)
        => Gateway.InsertRowAsync(table, values, schemaRevision, token);
    public Task<DeleteRowsResult> DeleteRowsAsync(
        string table, IReadOnlyList<(object RowKey, string ExpectedDigest)> rows,
        string schemaRevision, CancellationToken token)
        => Gateway.DeleteRowsAsync(table, rows, schemaRevision, token);
    public Task<ReadRowsResult> ReadRowsAsync(
        string table, IReadOnlyList<object> rowKeys, CancellationToken token)
        => Gateway.ReadRowsAsync(table, rowKeys, token);
    public Task<HistoryPage> ReadChangeSetsAsync(
        ReadChangeSetsParams parameters, CancellationToken token)
        => Gateway.ReadChangeSetsAsync(parameters, token);
    public Task<RestorePreview> PreviewRestoreAsync(
        PreviewRestoreParams parameters, CancellationToken token)
        => Gateway.PreviewRestoreAsync(parameters, token);
    public Task<RestoreResult> ApplyRestoreAsync(
        ApplyRestoreParams parameters, CancellationToken token)
        => Gateway.ApplyRestoreAsync(parameters, token);

    public Task<TablePage> QueryTableViewRawAsync(
        string table, JsonElement query, CancellationToken token)
        => Gateway.QueryTableViewRawAsync(table, query, token);

    public Task<TablePage> OpenTableCursorRawAsync(
        string table, JsonElement query, CancellationToken token)
        => Gateway.OpenTableCursorRawAsync(table, query, token);

    public Task<TableSelectionProjection> OpenTableSelectionAsync(
        string table, JsonElement query, CancellationToken token)
        => Gateway.OpenTableSelectionAsync(table, query, token);

    public Task<TablePage> FetchTableCursorAsync(string cursor, CancellationToken token)
        => Gateway.FetchTableCursorAsync(cursor, token);
    public Task<SnapshotValidation> ValidateSnapshotAsync(
        QuerySnapshot snapshot, int? currentRevision, CancellationToken token)
        => Gateway.ValidateSnapshotAsync(snapshot, currentRevision, token);
    public Task<GridStateResult> GetGridStateAsync(
        string databaseId, string table, CancellationToken token)
        => Gateway.GetGridStateAsync(databaseId, table, token);
    public Task<GridStateResult> SaveGridStateAsync(
        string databaseId, string table, GridState state,
        string? revision, CancellationToken token)
        => Gateway.SaveGridStateAsync(databaseId, table, state, revision, token);
    public Task<PastePlan> PreviewPasteAsync(
        string collection, string schemaRevision,
        IReadOnlyDictionary<string, object?> selection,
        PasteStartCell startCell,
        IReadOnlyList<IReadOnlyList<PasteCell>> cells,
        CancellationToken token)
        => Gateway.PreviewPasteAsync(
            collection, schemaRevision, selection, startCell, cells, token);
    public Task<ApplyPasteResult> ApplyPasteAsync(
        string collection, string token, string idempotencyKey,
        CancellationToken cancellationToken)
        => Gateway.ApplyPasteAsync(
            collection, token, idempotencyKey, cancellationToken);

    public void Dispose()
    {
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
            _resolved?.Dispose();
            foreach (PocketBaseTableGateway gateway in _retired)
            {
                gateway.Dispose();
            }
            _retired.Clear();
            _resolved = null;
            _resolvedClient = null;
            _supervisor = null;
        }
    }

    private void RetireResolved()
    {
        if (_resolved is not null)
            _retired.Add(_resolved);
        _resolved = null;
        _resolvedClient = null;
    }
}
