// Generated from contracts/workbench/workbench.schema.json; do not edit.
#nullable enable

using System.Text.Json.Serialization;

namespace VibeTable.Contracts.Generated;

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ViewFilter
{
    [JsonRequired, JsonPropertyName("fieldId")] public required string FieldId { get; init; }
    [JsonRequired, JsonPropertyName("operator")] public required string Operator { get; init; }
    [JsonRequired, JsonPropertyName("value")] public required object? Value { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ViewSort
{
    [JsonRequired, JsonPropertyName("fieldId")] public required string FieldId { get; init; }
    [JsonRequired, JsonPropertyName("direction")] public required string Direction { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ViewQuery
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
    [JsonRequired, JsonPropertyName("fields")] public required IReadOnlyList<string> Fields { get; init; }
    [JsonRequired, JsonPropertyName("filters")] public required IReadOnlyList<ViewFilter> Filters { get; init; }
    [JsonRequired, JsonPropertyName("sorts")] public required IReadOnlyList<ViewSort> Sorts { get; init; }
    [JsonRequired, JsonPropertyName("cursor")] public required string? Cursor { get; init; }
    [JsonRequired, JsonPropertyName("pageSize")] public required long PageSize { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record BindingVariable
{
    [JsonRequired, JsonPropertyName("variableId")] public required string VariableId { get; init; }
    [JsonRequired, JsonPropertyName("targetFieldId")] public required string TargetFieldId { get; init; }
    [JsonRequired, JsonPropertyName("operator")] public required string Operator { get; init; }
    [JsonRequired, JsonPropertyName("source")] public required string Source { get; init; }
    [JsonRequired, JsonPropertyName("sourceBindingId")] public required string? SourceBindingId { get; init; }
    [JsonRequired, JsonPropertyName("sourceFieldId")] public required string? SourceFieldId { get; init; }
    [JsonRequired, JsonPropertyName("value")] public required object? Value { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record DataBinding
{
    [JsonRequired, JsonPropertyName("bindingId")] public required string BindingId { get; init; }
    [JsonRequired, JsonPropertyName("query")] public required ViewQuery Query { get; init; }
    [JsonRequired, JsonPropertyName("variables")] public required IReadOnlyList<BindingVariable> Variables { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceAction
{
    [JsonRequired, JsonPropertyName("actionId")] public required string ActionId { get; init; }
    [JsonRequired, JsonPropertyName("kind")] public required string Kind { get; init; }
    [JsonRequired, JsonPropertyName("bindingId")] public required string? BindingId { get; init; }
    [JsonRequired, JsonPropertyName("targetPageId")] public required string? TargetPageId { get; init; }
    [JsonRequired, JsonPropertyName("pluginId")] public required string? PluginId { get; init; }
    [JsonRequired, JsonPropertyName("pluginActionId")] public required string? PluginActionId { get; init; }
    [JsonRequired, JsonPropertyName("requiresConfirmation")] public required bool RequiresConfirmation { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceElement
{
    [JsonRequired, JsonPropertyName("elementId")] public required string ElementId { get; init; }
    [JsonRequired, JsonPropertyName("kind")] public required string Kind { get; init; }
    [JsonRequired, JsonPropertyName("bindingId")] public required string? BindingId { get; init; }
    [JsonRequired, JsonPropertyName("actionId")] public required string? ActionId { get; init; }
    [JsonRequired, JsonPropertyName("text")] public required string? Text { get; init; }
    [JsonRequired, JsonPropertyName("width")] public required string Width { get; init; }
    [JsonRequired, JsonPropertyName("children")] public required IReadOnlyList<InterfaceElement> Children { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfacePage
{
    [JsonRequired, JsonPropertyName("pageId")] public required string PageId { get; init; }
    [JsonRequired, JsonPropertyName("title")] public required string Title { get; init; }
    [JsonRequired, JsonPropertyName("elements")] public required IReadOnlyList<InterfaceElement> Elements { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceDefinition
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("interfaceId")] public required string InterfaceId { get; init; }
    [JsonRequired, JsonPropertyName("name")] public required string Name { get; init; }
    [JsonRequired, JsonPropertyName("bindings")] public required IReadOnlyList<DataBinding> Bindings { get; init; }
    [JsonRequired, JsonPropertyName("actions")] public required IReadOnlyList<InterfaceAction> Actions { get; init; }
    [JsonRequired, JsonPropertyName("pages")] public required IReadOnlyList<InterfacePage> Pages { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceSnapshot
{
    [JsonRequired, JsonPropertyName("definition")] public required InterfaceDefinition Definition { get; init; }
    [JsonRequired, JsonPropertyName("revision")] public required string Revision { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceCommitRequest
{
    [JsonRequired, JsonPropertyName("definition")] public required InterfaceDefinition Definition { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string? ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceListRequest
{
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceListEntry
{
    [JsonRequired, JsonPropertyName("interfaceId")] public required string InterfaceId { get; init; }
    [JsonRequired, JsonPropertyName("name")] public required string Name { get; init; }
    [JsonRequired, JsonPropertyName("revision")] public required string Revision { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceListResult
{
    [JsonRequired, JsonPropertyName("items")] public required IReadOnlyList<InterfaceListEntry> Items { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceLoadRequest
{
    [JsonRequired, JsonPropertyName("interfaceId")] public required string InterfaceId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceCancelRequest
{
    [JsonRequired, JsonPropertyName("targetRequestId")] public required string TargetRequestId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceDeleteRequest
{
    [JsonRequired, JsonPropertyName("interfaceId")] public required string InterfaceId { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record InterfaceDeleteResult
{
    [JsonRequired, JsonPropertyName("interfaceId")] public required string InterfaceId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ContentProfile
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
    [JsonRequired, JsonPropertyName("titleFieldId")] public required string TitleFieldId { get; init; }
    [JsonRequired, JsonPropertyName("bodyFieldId")] public required string BodyFieldId { get; init; }
    [JsonRequired, JsonPropertyName("summaryFieldId")] public required string? SummaryFieldId { get; init; }
    [JsonRequired, JsonPropertyName("searchableFieldIds")] public required IReadOnlyList<string> SearchableFieldIds { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ContentProfileSnapshot
{
    [JsonRequired, JsonPropertyName("profile")] public required ContentProfile Profile { get; init; }
    [JsonRequired, JsonPropertyName("revision")] public required string Revision { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ContentProfileLoadRequest
{
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ContentProfileCommitRequest
{
    [JsonRequired, JsonPropertyName("profile")] public required ContentProfile Profile { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string? ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ContentProfileDeleteRequest
{
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ContentProfileDeleteResult
{
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLink
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("linkId")] public required string LinkId { get; init; }
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
    [JsonRequired, JsonPropertyName("recordId")] public required string RecordId { get; init; }
    [JsonRequired, JsonPropertyName("documentId")] public required string DocumentId { get; init; }
    [JsonRequired, JsonPropertyName("role")] public required string Role { get; init; }
    [JsonRequired, JsonPropertyName("order")] public required long Order { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkSnapshot
{
    [JsonRequired, JsonPropertyName("link")] public required RecordDocumentLink Link { get; init; }
    [JsonRequired, JsonPropertyName("revision")] public required string Revision { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkListRequest
{
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
    [JsonRequired, JsonPropertyName("recordId")] public required string RecordId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkListResult
{
    [JsonRequired, JsonPropertyName("items")] public required IReadOnlyList<RecordDocumentLinkSnapshot> Items { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkCommitRequest
{
    [JsonRequired, JsonPropertyName("link")] public required RecordDocumentLink Link { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string? ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkRepairRequest
{
    [JsonRequired, JsonPropertyName("linkId")] public required string LinkId { get; init; }
    [JsonRequired, JsonPropertyName("documentId")] public required string DocumentId { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkDeleteRequest
{
    [JsonRequired, JsonPropertyName("linkId")] public required string LinkId { get; init; }
    [JsonRequired, JsonPropertyName("expectedRevision")] public required string ExpectedRevision { get; init; }
    [JsonRequired, JsonPropertyName("idempotencyKey")] public required string IdempotencyKey { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RecordDocumentLinkDeleteResult
{
    [JsonRequired, JsonPropertyName("linkId")] public required string LinkId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchFilter
{
    [JsonRequired, JsonPropertyName("field")] public required string Field { get; init; }
    [JsonRequired, JsonPropertyName("operator")] public required string Operator { get; init; }
    [JsonRequired, JsonPropertyName("value")] public required object? Value { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchSort
{
    [JsonRequired, JsonPropertyName("field")] public required string Field { get; init; }
    [JsonRequired, JsonPropertyName("direction")] public required string Direction { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchOpenTarget
{
    [JsonRequired, JsonPropertyName("kind")] public required string Kind { get; init; }
    [JsonRequired, JsonPropertyName("tableId")] public required string? TableId { get; init; }
    [JsonRequired, JsonPropertyName("recordId")] public required string? RecordId { get; init; }
    [JsonRequired, JsonPropertyName("fieldId")] public required string? FieldId { get; init; }
    [JsonRequired, JsonPropertyName("documentId")] public required string? DocumentId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchMetadataItem
{
    [JsonRequired, JsonPropertyName("key")] public required string Key { get; init; }
    [JsonRequired, JsonPropertyName("value")] public required object? Value { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchRequest
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("query")] public required string Query { get; init; }
    [JsonRequired, JsonPropertyName("logic")] public required string Logic { get; init; }
    [JsonRequired, JsonPropertyName("filters")] public required IReadOnlyList<SearchFilter> Filters { get; init; }
    [JsonRequired, JsonPropertyName("sorts")] public required IReadOnlyList<SearchSort> Sorts { get; init; }
    [JsonRequired, JsonPropertyName("scope")] public required string Scope { get; init; }
    [JsonRequired, JsonPropertyName("cursor")] public required string? Cursor { get; init; }
    [JsonRequired, JsonPropertyName("limit")] public required long Limit { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchHit
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("hitId")] public required string HitId { get; init; }
    [JsonRequired, JsonPropertyName("kind")] public required string Kind { get; init; }
    [JsonRequired, JsonPropertyName("canonicalId")] public required string CanonicalId { get; init; }
    [JsonRequired, JsonPropertyName("title")] public required string Title { get; init; }
    [JsonRequired, JsonPropertyName("snippet")] public required string? Snippet { get; init; }
    [JsonRequired, JsonPropertyName("highlights")] public required IReadOnlyList<string> Highlights { get; init; }
    [JsonRequired, JsonPropertyName("sourceRevision")] public required string SourceRevision { get; init; }
    [JsonRequired, JsonPropertyName("score")] public required double Score { get; init; }
    [JsonRequired, JsonPropertyName("revisionTime")] public required string RevisionTime { get; init; }
    [JsonRequired, JsonPropertyName("metadata")] public required IReadOnlyList<SearchMetadataItem> Metadata { get; init; }
    [JsonRequired, JsonPropertyName("openTarget")] public required SearchOpenTarget OpenTarget { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchResolveRequest
{
    [JsonRequired, JsonPropertyName("contractVersion")] public required string ContractVersion { get; init; }
    [JsonRequired, JsonPropertyName("scope")] public required string Scope { get; init; }
    [JsonRequired, JsonPropertyName("hit")] public required SearchHit Hit { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchResolveResult
{
    [JsonRequired, JsonPropertyName("status")] public required string Status { get; init; }
    [JsonRequired, JsonPropertyName("hit")] public required SearchHit Hit { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SearchStatus
{
    [JsonRequired, JsonPropertyName("state")] public required string State { get; init; }
    [JsonRequired, JsonPropertyName("generation")] public required long Generation { get; init; }
    [JsonRequired, JsonPropertyName("checkpoint")] public required string? Checkpoint { get; init; }
    [JsonRequired, JsonPropertyName("processed")] public required long Processed { get; init; }
    [JsonRequired, JsonPropertyName("total")] public required long? Total { get; init; }
    [JsonRequired, JsonPropertyName("errorCode")] public required string? ErrorCode { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record ComputedCellEnvelope
{
    [JsonRequired, JsonPropertyName("state")] public required string State { get; init; }
    [JsonRequired, JsonPropertyName("value")] public required object? Value { get; init; }
    [JsonRequired, JsonPropertyName("definitionVersion")] public required long DefinitionVersion { get; init; }
    [JsonRequired, JsonPropertyName("sourceDataRevision")] public required long SourceDataRevision { get; init; }
    [JsonRequired, JsonPropertyName("dependencyWatermark")] public required long DependencyWatermark { get; init; }
    [JsonRequired, JsonPropertyName("diagnostic")] public required string? Diagnostic { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaAuditEvent
{
    [JsonRequired, JsonPropertyName("eventId")] public required string EventId { get; init; }
    [JsonRequired, JsonPropertyName("workspaceId")] public required string WorkspaceId { get; init; }
    [JsonRequired, JsonPropertyName("tableId")] public required string TableId { get; init; }
    [JsonRequired, JsonPropertyName("fieldId")] public required string? FieldId { get; init; }
    [JsonRequired, JsonPropertyName("operation")] public required string Operation { get; init; }
    [JsonRequired, JsonPropertyName("schemaRevision")] public required long SchemaRevision { get; init; }
    [JsonRequired, JsonPropertyName("occurredAt")] public required string OccurredAt { get; init; }
    [JsonRequired, JsonPropertyName("actorId")] public required string ActorId { get; init; }
}
