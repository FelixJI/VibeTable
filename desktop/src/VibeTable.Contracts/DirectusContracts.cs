using System.Collections.Generic;
using System.Text.Json;

namespace VibeTable.Contracts;

/// <summary>Safe local-session state returned by the Python Directus broker.</summary>
public sealed record DirectusSessionStatus(
    string State,
    double? ExpiresAt = null,
    DirectusCurrentUser? User = null);

/// <summary>Current user metadata. Authentication tokens are deliberately absent.</summary>
public sealed record DirectusCurrentUser(
    string Id,
    string DisplayName,
    string? AvatarFileId = null,
    string? RoleId = null,
    IReadOnlyList<string>? Capabilities = null);

public sealed record DirectusCollectionList(
    IReadOnlyList<string> Collections,
    IReadOnlyDictionary<string, string> CapabilityHashes,
    IReadOnlyDictionary<string, string>? DisplayNames = null);

public sealed record DirectusServerInfo(
    string? ProjectName,
    string? DirectusVersion,
    string Compatibility);

public sealed record DirectusSchema(
    string Collection,
    string PrimaryKey,
    IReadOnlyList<ColumnSchema> Columns,
    IReadOnlyList<JsonElement> Relations,
    string SchemaRevision,
    string CapabilityHash);

public sealed record DirectusPage(
    string Collection,
    IReadOnlyList<IReadOnlyDictionary<string, JsonElement>> Rows,
    int Offset,
    int Limit,
    int? FilteredRows,
    int? TotalRows,
    IReadOnlyList<string> SemanticGaps,
    string CapabilityHash);

public sealed record DirectusItem(
    string Collection,
    IReadOnlyDictionary<string, JsonElement> Item);

public sealed record DirectusSubscription(
    string Uid,
    string? Collection,
    bool Active);

/// <summary>Permission-filtered Directus change notification.</summary>
public sealed record DirectusChange(
    string Uid,
    string Collection,
    string Event,
    IReadOnlyList<JsonElement> Data,
    bool InvalidateQuery);
