using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Typed WPF boundary for Directus. Credentials cross only the local RPC pipe;
/// tokens remain owned by the Python broker and never enter this interface.
/// </summary>
public interface IDirectusRpcGateway : IDisposable
{
    event Action<DirectusChange>? Changed;

    Task<DirectusSessionStatus> LoginAsync(
        string email, string password, string? otp, CancellationToken token);
    Task<DirectusSessionStatus> RefreshAsync(CancellationToken token);
    Task<DirectusSessionStatus> LogoutAsync(CancellationToken token);
    Task<DirectusSessionStatus> GetStatusAsync(CancellationToken token);
    Task<DirectusServerInfo> GetServerInfoAsync(CancellationToken token);
    Task<DirectusCurrentUser> GetCurrentUserAsync(CancellationToken token);
    Task<DirectusCollectionList> ListCollectionsAsync(CancellationToken token);
    Task<CreateTableResult> CreateTableAsync(
        string name, IReadOnlyList<FieldDefinition> fields, CancellationToken token);
    Task<DeleteTableResult> DeleteTableAsync(string name, CancellationToken token);
    Task<IdentifierMappingsResult> ListIdentifierMappingsAsync(
        string? search, CancellationToken token);
    Task<IdentifierMappingsResult> UpdateIdentifierAliasesAsync(
        string mappingId, IReadOnlyList<string> aliases, CancellationToken token);
    Task<IdentifierMappingsResult> ImportIdentifierMappingsAsync(
        IReadOnlyList<IdentifierMappingImportItem> mappings, CancellationToken token);
    Task<IdentifierMappingsResult> ReconcileIdentifierMappingsAsync(CancellationToken token);
    Task<IdentifierMappingsResult> DeleteIdentifierMappingAsync(
        string mappingId, CancellationToken token);
    Task<IdentifierMappingsResult> PurgeIdentifierMappingsAsync(CancellationToken token);
    Task<DirectusSchema> GetSchemaAsync(string collection, CancellationToken token);
    Task<DirectusPage> ReadAsync(
        string collection, TableQuery query, bool includeArchived, CancellationToken token);
    Task<DirectusItem> CreateAsync(
        string collection, IReadOnlyDictionary<string, object?> values,
        string? requestId, CancellationToken token);
    Task<DirectusItem> UpdateAsync(
        string collection, string itemId, IReadOnlyDictionary<string, object?> values,
        string? expectedDateUpdated, string? requestId, CancellationToken token);
    Task<DirectusItem> ArchiveAsync(string collection, string itemId, CancellationToken token);
    Task<DirectusItem> RestoreAsync(string collection, string itemId, CancellationToken token);
    Task<DirectusItem> DeleteAsync(string collection, string itemId, CancellationToken token);
    Task<HistoryPage> ReadChangeSetsAsync(
        ReadChangeSetsParams parameters, CancellationToken token);
    Task<RestorePreview> PreviewRestoreAsync(
        PreviewRestoreParams parameters, CancellationToken token);
    Task<RestoreResult> ApplyRestoreAsync(
        ApplyRestoreParams parameters, CancellationToken token);
    Task<DirectusSubscription> SubscribeAsync(
        string uid, string collection, IReadOnlyList<string> fields, CancellationToken token);
    Task<DirectusSubscription> UnsubscribeAsync(string uid, CancellationToken token);

    // Closed relation/Lookup RPC surface. JsonElement is deliberate here: the
    // Python broker owns the canonical Pydantic contracts and final validation,
    // while this interface prevents an arbitrary renderer-supplied method name.
    Task<JsonElement> DescribeSchemaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> SearchRelationTargetsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> UpdateSingleRelationAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewRelationDeltaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyRelationDeltaAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ListLookupsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ValidateLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> CreateLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> UpdateLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> DeleteLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewLookupAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> QueryLookupsAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> PreviewRelationChangeAsync(JsonElement parameters, CancellationToken token);
    Task<JsonElement> ApplyRelationChangeAsync(JsonElement parameters, CancellationToken token);
}

/// <summary>A single field definition for a new table (column key + data type).</summary>
public sealed record FieldDefinition(string Key, string Type);

/// <summary>Result of <c>table_admin.createTable</c>: the created collection, its primary key, and field list.</summary>
public sealed record CreateTableResult(
    string Collection,
    string PrimaryKey,
    IReadOnlyList<string> Fields,
    string? DisplayName = null,
    IReadOnlyDictionary<string, string>? FieldDisplayNames = null);

/// <summary>Result of <c>table_admin.deleteTable</c>: the collection name and whether it was deleted.</summary>
public sealed record DeleteTableResult(string Collection, bool Deleted);

public sealed record IdentifierMappingEntry(
    string Id,
    string EntityKind,
    string? ParentPhysicalName,
    string PhysicalName,
    string DisplayName,
    string Locale,
    IReadOnlyList<string> Aliases,
    string Origin,
    string Status);

public sealed record IdentifierMappingImportItem(
    string EntityKind,
    string? ParentPhysicalName,
    string PhysicalName,
    string DisplayName,
    IReadOnlyList<string> Aliases);

public sealed record IdentifierMappingsResult(IReadOnlyList<IdentifierMappingEntry> Mappings);
