using System;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>JSON-RPC adapter for the closed provider-neutral product surface.</summary>
public sealed class JsonRpcProductDataGateway : IProductDataRpcGateway
{
    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web)
        {
            UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
            RespectNullableAnnotations = true,
            RespectRequiredConstructorParameters = true,
        };
    private readonly JsonRpcClient _client;
    private bool _disposed;

    public JsonRpcProductDataGateway(JsonRpcClient client)
    {
        _client = client ?? throw new ArgumentNullException(nameof(client));
        _client.NotificationReceived += OnNotification;
    }

    public event Action<DataChangedEvent>? DataChanged;
    public event Action<JsonElement>? TaskChanged;

    public Task<JsonElement> ValidateSchemaAsync(JsonElement p, CancellationToken t) => Invoke("schema.validate", p, t);
    public Task<JsonElement> ApplySchemaAsync(JsonElement p, CancellationToken t) => Invoke("schema.apply", p, t);
    public Task<JsonElement> DeleteSchemaAsync(JsonElement p, CancellationToken t) => Invoke("schema.delete", p, t);
    public Task<JsonElement> ListTablesAsync(JsonElement p, CancellationToken t) => Invoke("schema.list", p, t);
    public Task<JsonElement> GetTableSchemaAsync(JsonElement p, CancellationToken t) => Invoke("schema.getTable", p, t);
    public Task<JsonElement> QueryPageAsync(JsonElement p, CancellationToken t) => Invoke("query.page", p, t);
    public Task<JsonElement> ReadRowsAsync(JsonElement p, CancellationToken t) => Invoke("query.readRows", p, t);
    public Task<JsonElement> ValidateSnapshotAsync(JsonElement p, CancellationToken t) => Invoke("query.validateSnapshot", p, t);
    public Task<JsonElement> PreviewMutationAsync(JsonElement p, CancellationToken t) => Invoke("mutation.preview", p, t);
    public Task<JsonElement> ApplyMutationAsync(JsonElement p, CancellationToken t) => Invoke("mutation.apply", p, t);
    public Task<JsonElement> ValidateFormulaAsync(JsonElement p, CancellationToken t) => Invoke("formula.validate", p, t);
    public Task<JsonElement> PreviewFormulaAsync(JsonElement p, CancellationToken t) => Invoke("formula.preview", p, t);
    public Task<JsonElement> ListAttachmentRefsAsync(JsonElement p, CancellationToken t) => Invoke("file.list", p, t);
    public Task<JsonElement> CreateFileTokenAsync(JsonElement p, CancellationToken t) => Invoke("file.token", p, t);
    public Task<JsonElement> ApplyHostAttachmentChangeAsync(JsonElement p, CancellationToken t)
        => Invoke("file.applyHostChange", p, t);
    public Task<JsonElement> SaveAttachmentToHostAsync(JsonElement p, CancellationToken t)
        => Invoke("file.saveHostFile", p, t);
    public Task<HistoryPage> ReadHistoryAsync(ReadChangeSetsParams p, CancellationToken t)
        => InvokeStrict<ReadChangeSetsParams, HistoryPage>("history.read", p, t);
    public Task<RestorePreview> PreviewHistoryRestoreAsync(PreviewRestoreParams p, CancellationToken t)
        => InvokeStrict<PreviewRestoreParams, RestorePreview>("history.previewRestore", p, t);
    public Task<RestoreResult> ApplyHistoryRestoreAsync(ApplyRestoreParams p, CancellationToken t)
        => InvokeStrict<ApplyRestoreParams, RestoreResult>("history.applyRestore", p, t);
    public Task<JsonElement> ReconcileAsync(JsonElement p, CancellationToken t) => Invoke("events.reconcile", p, t);
    public Task<JsonElement> ListIdentifierMappingsAsync(JsonElement p, CancellationToken t) => Invoke("identifier.list", p, t);
    public Task<JsonElement> UpdateIdentifierAliasesAsync(JsonElement p, CancellationToken t) => Invoke("identifier.updateAliases", p, t);
    public Task<JsonElement> ReconcileIdentifierMappingsAsync(JsonElement p, CancellationToken t) => Invoke("identifier.reconcile", p, t);
    public Task<JsonElement> ListPresetsAsync(JsonElement p, CancellationToken t) => Invoke("preset.list", p, t);
    public Task<JsonElement> SavePresetAsync(JsonElement p, CancellationToken t) => Invoke("preset.save", p, t);
    public Task<JsonElement> DeletePresetAsync(JsonElement p, CancellationToken t) => Invoke("preset.delete", p, t);
    public Task<JsonElement> ListVersionsAsync(JsonElement p, CancellationToken t) => Invoke("version.list", p, t);
    public Task<JsonElement> CreateVersionAsync(JsonElement p, CancellationToken t) => Invoke("version.create", p, t);
    public Task<JsonElement> SaveVersionAsync(JsonElement p, CancellationToken t) => Invoke("version.save", p, t);
    public Task<JsonElement> CompareVersionAsync(JsonElement p, CancellationToken t) => Invoke("version.compare", p, t);
    public Task<JsonElement> PromoteVersionAsync(JsonElement p, CancellationToken t) => Invoke("version.promote", p, t);
    public Task<JsonElement> DeleteVersionAsync(JsonElement p, CancellationToken t) => Invoke("version.delete", p, t);
    public Task<JsonElement> ListBackupsAsync(JsonElement p, CancellationToken t) => Invoke("backup.list", p, t);
    public Task<JsonElement> CreateBackupAsync(JsonElement p, CancellationToken t) => Invoke("backup.create", p, t);
    public Task<JsonElement> RestoreBackupAsync(JsonElement p, CancellationToken t) => Invoke("backup.restore", p, t);
    public Task<JsonElement> RegisterImportSourceAsync(JsonElement p, CancellationToken t) => Invoke("path.registerImportSource", p, t);
    public Task<JsonElement> RegisterExportTargetAsync(JsonElement p, CancellationToken t) => Invoke("path.registerExportTarget", p, t);
    public Task<JsonElement> PreviewImportAsync(JsonElement p, CancellationToken t) => Invoke("data.previewImport", p, t);
    public Task<JsonElement> ApplyImportAsync(JsonElement p, CancellationToken t) => Invoke("data.applyImport", p, t);
    public Task<JsonElement> ExportAsync(JsonElement p, CancellationToken t) => Invoke("data.export", p, t);
    public Task<JsonElement> CreateTaskAsync(JsonElement p, CancellationToken t) => Invoke("task.create", p, t);
    public Task<JsonElement> CancelTaskAsync(JsonElement p, CancellationToken t) => Invoke("task.cancel", p, t);
    public Task<JsonElement> GetTaskStatusAsync(JsonElement p, CancellationToken t) => Invoke("task.status", p, t);
    public Task<JsonElement> DescribeSchemaAsync(JsonElement p, CancellationToken t) => Invoke("schema.describe", p, t);
    public Task<JsonElement> SearchRelationTargetsAsync(JsonElement p, CancellationToken t) => Invoke("relation.searchTargets", p, t);
    public Task<JsonElement> UpdateSingleRelationAsync(JsonElement p, CancellationToken t) => Invoke("relation.updateSingle", p, t);
    public Task<JsonElement> PreviewRelationDeltaAsync(JsonElement p, CancellationToken t) => Invoke("relation.previewDelta", p, t);
    public Task<JsonElement> ApplyRelationDeltaAsync(JsonElement p, CancellationToken t) => Invoke("relation.applyDelta", p, t);
    public Task<JsonElement> ListLookupsAsync(JsonElement p, CancellationToken t) => Invoke("lookup.list", p, t);
    public Task<JsonElement> ValidateLookupAsync(JsonElement p, CancellationToken t) => Invoke("lookup.validate", p, t);
    public Task<JsonElement> CreateLookupAsync(JsonElement p, CancellationToken t) => Invoke("lookup.create", p, t);
    public Task<JsonElement> UpdateLookupAsync(JsonElement p, CancellationToken t) => Invoke("lookup.update", p, t);
    public Task<JsonElement> DeleteLookupAsync(JsonElement p, CancellationToken t) => Invoke("lookup.delete", p, t);
    public Task<JsonElement> PreviewLookupAsync(JsonElement p, CancellationToken t) => Invoke("lookup.preview", p, t);
    public Task<JsonElement> QueryLookupsAsync(JsonElement p, CancellationToken t) => Invoke("lookup.query", p, t);
    public Task<JsonElement> PreviewRelationChangeAsync(JsonElement p, CancellationToken t) => Invoke("table_admin.previewRelationChange", p, t);
    public Task<JsonElement> ApplyRelationChangeAsync(JsonElement p, CancellationToken t) => Invoke("table_admin.applyRelationChange", p, t);

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _client.NotificationReceived -= OnNotification;
    }

    private Task<JsonElement> Invoke(string method, JsonElement parameters, CancellationToken token)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        token.ThrowIfCancellationRequested();
        return _client.InvokeAsync<JsonElement, JsonElement>(method, parameters, token);
    }

    private Task<TResult> Invoke<TParams, TResult>(
        string method,
        TParams parameters,
        CancellationToken token)
        where TParams : notnull
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        token.ThrowIfCancellationRequested();
        return _client.InvokeAsync<TParams, TResult>(method, parameters, token);
    }

    private async Task<TResult> InvokeStrict<TParams, TResult>(
        string method,
        TParams parameters,
        CancellationToken token)
        where TParams : notnull
        where TResult : notnull
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        token.ThrowIfCancellationRequested();
        JsonElement response = await _client
            .InvokeAsync<TParams, JsonElement>(method, parameters, token)
            .ConfigureAwait(false);
        try
        {
            return response.Deserialize<TResult>(JsonOptions)
                ?? throw new JsonException(
                    $"Product RPC '{method}' returned null.");
        }
        catch (JsonException exception)
        {
            throw new InvalidOperationException(
                $"Product RPC '{method}' returned an invalid response.",
                exception);
        }
    }

    private void OnNotification(string method, JsonElement parameters)
    {
        if (_disposed) return;
        if (string.Equals(method, "data.changed", StringComparison.Ordinal))
        {
            var value = parameters.Deserialize<DataChangedEvent>(JsonOptions);
            if (value is not null
                && value.ContractVersion == "1.0"
                && value.Topic == "data.changed")
            {
                DataChanged?.Invoke(value);
            }
            return;
        }
        if (string.Equals(method, "task.changed", StringComparison.Ordinal)
            && parameters.ValueKind == JsonValueKind.Object
            && parameters.TryGetProperty("contractVersion", out var contractVersion)
            && contractVersion.GetString() == "1.0"
            && parameters.TryGetProperty("topic", out var topic)
            && topic.GetString() == "task.changed")
        {
            TaskChanged?.Invoke(parameters.Clone());
        }
    }
}
