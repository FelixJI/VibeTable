using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// In-memory IDirectusRpcGateway for dispatcher tests. Records calls and
/// returns programmable results. Throws by default for methods the test
/// does not configure (so accidental calls are loud).
/// </summary>
internal sealed class FakeDirectusRpcGateway : IDirectusRpcGateway
{
    public List<string> ListCollectionsCalls { get; } = new();
    public DirectusCollectionList? ListCollectionsResult { get; set; }
    public Exception? ListCollectionsException { get; set; }

    public List<(string Name, IReadOnlyList<FieldDefinition> Fields)> CreateTableCalls { get; } = new();
    public CreateTableResult? CreateTableResult { get; set; }
    public Exception? CreateTableException { get; set; }

    public List<string> DeleteTableCalls { get; } = new();
    public DeleteTableResult? DeleteTableResult { get; set; }
    public Exception? DeleteTableException { get; set; }
    public IdentifierMappingsResult IdentifierMappingsResult { get; set; }
        = new IdentifierMappingsResult(Array.Empty<IdentifierMappingEntry>());

    public Task<DirectusCollectionList> ListCollectionsAsync(CancellationToken token)
    {
        ListCollectionsCalls.Add("list");
        if (ListCollectionsException is not null) throw ListCollectionsException;
        return Task.FromResult(ListCollectionsResult
            ?? throw new InvalidOperationException("ListCollectionsResult not configured."));
    }

    public Task<CreateTableResult> CreateTableAsync(
        string name, IReadOnlyList<FieldDefinition> fields, CancellationToken token)
    {
        CreateTableCalls.Add((name, fields));
        if (CreateTableException is not null) throw CreateTableException;
        return Task.FromResult(CreateTableResult
            ?? new CreateTableResult(name, "id", new[] { "id" }));
    }

    public Task<DeleteTableResult> DeleteTableAsync(string name, CancellationToken token)
    {
        DeleteTableCalls.Add(name);
        if (DeleteTableException is not null) throw DeleteTableException;
        return Task.FromResult(DeleteTableResult
            ?? new DeleteTableResult(name, Deleted: true));
    }

    public Task<IdentifierMappingsResult> ListIdentifierMappingsAsync(
        string? search, CancellationToken token) => Task.FromResult(IdentifierMappingsResult);
    public Task<IdentifierMappingsResult> UpdateIdentifierAliasesAsync(
        string mappingId, IReadOnlyList<string> aliases, CancellationToken token)
        => Task.FromResult(IdentifierMappingsResult);
    public Task<IdentifierMappingsResult> ImportIdentifierMappingsAsync(
        IReadOnlyList<IdentifierMappingImportItem> mappings, CancellationToken token)
        => Task.FromResult(IdentifierMappingsResult);
    public Task<IdentifierMappingsResult> ReconcileIdentifierMappingsAsync(CancellationToken token)
        => Task.FromResult(IdentifierMappingsResult);
    public List<string> DeleteIdentifierMappingCalls { get; } = new();
    public Task<IdentifierMappingsResult> DeleteIdentifierMappingAsync(
        string mappingId, CancellationToken token)
    {
        DeleteIdentifierMappingCalls.Add(mappingId);
        return Task.FromResult(IdentifierMappingsResult);
    }
    public int PurgeIdentifierMappingsCalls { get; private set; }
    public Task<IdentifierMappingsResult> PurgeIdentifierMappingsAsync(CancellationToken token)
    {
        PurgeIdentifierMappingsCalls++;
        return Task.FromResult(IdentifierMappingsResult);
    }

    public List<(string Method, JsonElement Parameters)> RelationLookupCalls { get; } = new();
    public Exception? RelationLookupException { get; set; }
    public JsonElement RelationLookupResult { get; set; }
        = JsonDocument.Parse("""{"ok":true}""").RootElement.Clone();

    public Task<JsonElement> DescribeSchemaAsync(JsonElement p, CancellationToken t) => RelationLookup("schema.describe", p);
    public Task<JsonElement> SearchRelationTargetsAsync(JsonElement p, CancellationToken t) => RelationLookup("relation.searchTargets", p);
    public Task<JsonElement> UpdateSingleRelationAsync(JsonElement p, CancellationToken t) => RelationLookup("relation.updateSingle", p);
    public Task<JsonElement> PreviewRelationDeltaAsync(JsonElement p, CancellationToken t) => RelationLookup("relation.previewDelta", p);
    public Task<JsonElement> ApplyRelationDeltaAsync(JsonElement p, CancellationToken t) => RelationLookup("relation.applyDelta", p);
    public Task<JsonElement> ListLookupsAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.list", p);
    public Task<JsonElement> ValidateLookupAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.validate", p);
    public Task<JsonElement> CreateLookupAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.create", p);
    public Task<JsonElement> UpdateLookupAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.update", p);
    public Task<JsonElement> DeleteLookupAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.delete", p);
    public Task<JsonElement> PreviewLookupAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.preview", p);
    public Task<JsonElement> QueryLookupsAsync(JsonElement p, CancellationToken t) => RelationLookup("lookup.query", p);
    public Task<JsonElement> PreviewRelationChangeAsync(JsonElement p, CancellationToken t) => RelationLookup("table_admin.previewRelationChange", p);
    public Task<JsonElement> ApplyRelationChangeAsync(JsonElement p, CancellationToken t) => RelationLookup("table_admin.applyRelationChange", p);

    private Task<JsonElement> RelationLookup(string method, JsonElement parameters)
    {
        RelationLookupCalls.Add((method, parameters.Clone()));
        if (RelationLookupException is not null) throw RelationLookupException;
        return Task.FromResult(RelationLookupResult.Clone());
    }

    // The rest of the interface is unused by the dispatcher; throw to keep tests honest.
    public event Action<DirectusChange>? Changed { add { } remove { } }
    public Task<DirectusSessionStatus> LoginAsync(string e, string p, string? o, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSessionStatus> RefreshAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSessionStatus> LogoutAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSessionStatus> GetStatusAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusServerInfo> GetServerInfoAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusCurrentUser> GetCurrentUserAsync(CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSchema> GetSchemaAsync(string c, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusPage> ReadAsync(string c, TableQuery q, bool a, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> CreateAsync(string c, IReadOnlyDictionary<string, object?> v, string? r, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> UpdateAsync(string c, string i, IReadOnlyDictionary<string, object?> v, string? d, string? r, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> ArchiveAsync(string c, string i, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> RestoreAsync(string c, string i, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusItem> DeleteAsync(string c, string i, CancellationToken t) => throw new NotImplementedException();
    public Task<HistoryPage> ReadChangeSetsAsync(ReadChangeSetsParams p, CancellationToken t) => throw new NotImplementedException();
    public Task<RestorePreview> PreviewRestoreAsync(PreviewRestoreParams p, CancellationToken t) => throw new NotImplementedException();
    public Task<RestoreResult> ApplyRestoreAsync(ApplyRestoreParams p, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSubscription> SubscribeAsync(string u, string c, IReadOnlyList<string> f, CancellationToken t) => throw new NotImplementedException();
    public Task<DirectusSubscription> UnsubscribeAsync(string u, CancellationToken t) => throw new NotImplementedException();
    public void Dispose() { }
}
