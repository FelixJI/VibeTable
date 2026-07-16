using System.Collections.Generic;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>
/// G1 full-field history contracts. Mirrors
/// <c>backend.contracts.history</c>. The C# records deserialize the exact
/// camelCase bytes the Python Pydantic service emits.
/// </summary>

// --- Actor ---

public sealed record HistoryActor(
    [property: JsonPropertyName("userId")] string? UserId,
    [property: JsonPropertyName("displayName")] string? DisplayName
);

// --- Field-level changes ---

public sealed record ScalarFieldChange(
    [property: JsonPropertyName("field")] string Field,
    [property: JsonPropertyName("before")] object? Before,
    [property: JsonPropertyName("after")] object? After
);

public sealed record RelationFieldChange(
    [property: JsonPropertyName("field")] string Field,
    [property: JsonPropertyName("kind")] string Kind,
    [property: JsonPropertyName("relatedCollection")] string? RelatedCollection,
    [property: JsonPropertyName("relatedItemId")] string? RelatedItemId,
    [property: JsonPropertyName("displayValue")] string? DisplayValue,
    [property: JsonPropertyName("beforeItemId")] string? BeforeItemId,
    [property: JsonPropertyName("afterItemId")] string? AfterItemId
);

// --- ChangeSet ---

public sealed record HistoryChangeSet(
    [property: JsonPropertyName("rootRevisionId")] string RootRevisionId,
    [property: JsonPropertyName("activityId")] string? ActivityId,
    [property: JsonPropertyName("action")] string Action,
    [property: JsonPropertyName("timestamp")] string Timestamp,
    [property: JsonPropertyName("actor")] HistoryActor? Actor,
    [property: JsonPropertyName("scalarChanges")] List<ScalarFieldChange> ScalarChanges,
    [property: JsonPropertyName("relationChanges")] List<RelationFieldChange> RelationChanges
);

// --- Paging ---

public sealed record ReadChangeSetsParams(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("limit")] int Limit,
    [property: JsonPropertyName("offset")] int Offset
);

public sealed record HistoryPage(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("changeSets")] List<HistoryChangeSet> ChangeSets,
    [property: JsonPropertyName("total")] int Total,
    [property: JsonPropertyName("capabilityHash")] string CapabilityHash,
    [property: JsonPropertyName("schemaRevision")] string SchemaRevision
);

// --- Safe restore (two-phase) ---

public sealed record RestoreDiagnostic(
    [property: JsonPropertyName("field")] string Field,
    [property: JsonPropertyName("classification")] string Classification,
    [property: JsonPropertyName("severity")] string Severity,
    [property: JsonPropertyName("code")] string Code,
    [property: JsonPropertyName("message")] string Message
);

public sealed record PreviewRestoreParams(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("targetRevision")] string TargetRevision
);

public sealed record RestorePreview(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("targetRevision")] string TargetRevision,
    [property: JsonPropertyName("currentHash")] string CurrentHash,
    [property: JsonPropertyName("schemaRevision")] string SchemaRevision,
    [property: JsonPropertyName("scalarChanges")] List<ScalarFieldChange> ScalarChanges,
    [property: JsonPropertyName("relationChanges")] List<RelationFieldChange> RelationChanges,
    [property: JsonPropertyName("diagnostics")] List<RestoreDiagnostic> Diagnostics,
    [property: JsonPropertyName("token")] string Token,
    [property: JsonPropertyName("expiresAt")] string ExpiresAt
);

public sealed record ApplyRestoreParams(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("token")] string Token
);

public sealed record RestoreResult(
    [property: JsonPropertyName("collection")] string Collection,
    [property: JsonPropertyName("itemId")] string ItemId,
    [property: JsonPropertyName("restoredToRevision")] string RestoredToRevision,
    [property: JsonPropertyName("newRevisionId")] string? NewRevisionId,
    [property: JsonPropertyName("item")] Dictionary<string, object?> Item
);
