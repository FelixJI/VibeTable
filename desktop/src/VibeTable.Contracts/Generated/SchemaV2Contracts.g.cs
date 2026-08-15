// Generated from contracts/schema-v2/schema.schema.json; do not edit.
#nullable enable

using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldIdentityV2
{
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string FieldId { get; init; }
    [JsonPropertyName("physicalName")]
    [JsonRequired] public required string PhysicalName { get; init; }
    [JsonPropertyName("providerFieldId")]
    [JsonRequired] public required string ProviderFieldId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldLifecycleV2
{
    [JsonPropertyName("state")]
    [JsonRequired] public required string State { get; init; }
    [JsonPropertyName("retiredAt")]
    [JsonRequired] public required string? RetiredAt { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDefaultV2
{
    [JsonPropertyName("enabled")]
    [JsonRequired] public required bool Enabled { get; init; }
    [JsonPropertyName("value")]
    [JsonRequired] public required JsonElement Value { get; init; }
    [JsonPropertyName("source")]
    [JsonRequired] public required string Source { get; init; }
    [JsonPropertyName("defaultsVersion")]
    [JsonRequired] public required long DefaultsVersion { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldPresenceV2
{
    [JsonPropertyName("mode")]
    [JsonRequired] public required string Mode { get; init; }
    [JsonPropertyName("providerFieldId")]
    public string? ProviderFieldId { get; init; }
    [JsonPropertyName("physicalName")]
    public string? PhysicalName { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldValueV2
{
    [JsonPropertyName("required")]
    [JsonRequired] public required bool Required { get; init; }
    [JsonPropertyName("default")]
    [JsonRequired] public required FieldDefaultV2 Default { get; init; }
    [JsonPropertyName("presence")]
    [JsonRequired] public required FieldPresenceV2 Presence { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldUniqueV2
{
    [JsonPropertyName("enabled")]
    [JsonRequired] public required bool Enabled { get; init; }
    [JsonPropertyName("blankPolicy")]
    [JsonRequired] public required string BlankPolicy { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldRangeV2
{
    [JsonPropertyName("min")]
    [JsonRequired] public required JsonElement? Min { get; init; }
    [JsonPropertyName("max")]
    [JsonRequired] public required JsonElement? Max { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldLengthV2
{
    [JsonPropertyName("min")]
    [JsonRequired] public required long? Min { get; init; }
    [JsonPropertyName("max")]
    [JsonRequired] public required long? Max { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldPatternV2
{
    [JsonPropertyName("enabled")]
    [JsonRequired] public required bool Enabled { get; init; }
    [JsonPropertyName("value")]
    [JsonRequired] public required string Value { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDomainsV2
{
    [JsonPropertyName("only")]
    [JsonRequired] public required IReadOnlyList<string> Only { get; init; }
    [JsonPropertyName("except")]
    [JsonRequired] public required IReadOnlyList<string> Except { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldSelectionV2
{
    [JsonPropertyName("min")]
    [JsonRequired] public required long Min { get; init; }
    [JsonPropertyName("max")]
    [JsonRequired] public required long? Max { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldConstraintsV2
{
    [JsonPropertyName("unique")]
    [JsonRequired] public required FieldUniqueV2 Unique { get; init; }
    [JsonPropertyName("range")]
    [JsonRequired] public required FieldRangeV2 Range { get; init; }
    [JsonPropertyName("length")]
    [JsonRequired] public required FieldLengthV2 Length { get; init; }
    [JsonPropertyName("pattern")]
    [JsonRequired] public required FieldPatternV2 Pattern { get; init; }
    [JsonPropertyName("domains")]
    [JsonRequired] public required FieldDomainsV2 Domains { get; init; }
    [JsonPropertyName("selection")]
    [JsonRequired] public required FieldSelectionV2 Selection { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldStorageOptionsV2
{
    [JsonPropertyName("onlyInt")]
    [JsonRequired] public required bool OnlyInt { get; init; }
    [JsonPropertyName("maxSize")]
    [JsonRequired] public required long MaxSize { get; init; }
    [JsonPropertyName("convertURLs")]
    [JsonRequired] public required bool ConvertURLs { get; init; }
    [JsonPropertyName("presentable")]
    [JsonRequired] public required bool Presentable { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldStorageV2
{
    [JsonPropertyName("kind")]
    [JsonRequired] public required string Kind { get; init; }
    [JsonPropertyName("options")]
    [JsonRequired] public required FieldStorageOptionsV2 Options { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDisplayV2
{
    [JsonPropertyName("kind")]
    [JsonRequired] public required string Kind { get; init; }
    [JsonPropertyName("preset")]
    [JsonRequired] public required string Preset { get; init; }
    [JsonPropertyName("displayScale")]
    [JsonRequired] public required long DisplayScale { get; init; }
    [JsonPropertyName("scaleMode")]
    [JsonRequired] public required string ScaleMode { get; init; }
    [JsonPropertyName("trimTrailingZeros")]
    [JsonRequired] public required bool TrimTrailingZeros { get; init; }
    [JsonPropertyName("useGrouping")]
    [JsonRequired] public required bool UseGrouping { get; init; }
    [JsonPropertyName("currency")]
    [JsonRequired] public required string Currency { get; init; }
    [JsonPropertyName("percentStorage")]
    [JsonRequired] public required string PercentStorage { get; init; }
    [JsonPropertyName("unit")]
    [JsonRequired] public required string? Unit { get; init; }
    [JsonPropertyName("precision")]
    [JsonRequired] public required string Precision { get; init; }
    [JsonPropertyName("timezone")]
    [JsonRequired] public required string Timezone { get; init; }
    [JsonPropertyName("mode")]
    [JsonRequired] public required string Mode { get; init; }
    [JsonPropertyName("indent")]
    public long? Indent { get; init; }
    [JsonPropertyName("trueLabel")]
    [JsonRequired] public required string TrueLabel { get; init; }
    [JsonPropertyName("falseLabel")]
    [JsonRequired] public required string FalseLabel { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldSelectOptionV2
{
    [JsonPropertyName("optionId")]
    [JsonRequired] public required string OptionId { get; init; }
    [JsonPropertyName("label")]
    [JsonRequired] public required string Label { get; init; }
    [JsonPropertyName("color")]
    [JsonRequired] public required string Color { get; init; }
    [JsonPropertyName("order")]
    [JsonRequired] public required long Order { get; init; }
    [JsonPropertyName("state")]
    [JsonRequired] public required string State { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldSelectV2
{
    [JsonPropertyName("options")]
    [JsonRequired] public required IReadOnlyList<FieldSelectOptionV2> Options { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldSelectOptionDraftV2
{
    [JsonPropertyName("optionId")]
    [JsonRequired] public required string OptionId { get; init; }
    [JsonPropertyName("label")]
    [JsonRequired] public required string Label { get; init; }
    [JsonPropertyName("color")]
    [JsonRequired] public required string Color { get; init; }
    [JsonPropertyName("order")]
    [JsonRequired] public required long Order { get; init; }
    [JsonPropertyName("state")]
    [JsonRequired] public required string State { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldSelectDraftV2
{
    [JsonPropertyName("options")]
    [JsonRequired] public required IReadOnlyList<FieldSelectOptionDraftV2> Options { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldRelationV2
{
    [JsonPropertyName("targetTableId")]
    [JsonRequired] public required string TargetTableId { get; init; }
    [JsonPropertyName("cardinality")]
    [JsonRequired] public required string Cardinality { get; init; }
    [JsonPropertyName("deletePolicy")]
    [JsonRequired] public required string DeletePolicy { get; init; }
    [JsonPropertyName("displayFieldId")]
    [JsonRequired] public required string DisplayFieldId { get; init; }
    [JsonPropertyName("pairId")]
    public string? PairId { get; init; }
    [JsonPropertyName("reciprocalFieldId")]
    public string? ReciprocalFieldId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldFileV2
{
    [JsonPropertyName("maxFiles")]
    [JsonRequired] public required long MaxFiles { get; init; }
    [JsonPropertyName("maxBytesPerFile")]
    [JsonRequired] public required long MaxBytesPerFile { get; init; }
    [JsonPropertyName("allowedMimeTypes")]
    [JsonRequired] public required IReadOnlyList<string> AllowedMimeTypes { get; init; }
    [JsonPropertyName("thumbs")]
    [JsonRequired] public required IReadOnlyList<string> Thumbs { get; init; }
    [JsonPropertyName("protected")]
    [JsonRequired] public required bool Protected { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldJsonV2
{
    [JsonPropertyName("rootType")]
    [JsonRequired] public required string RootType { get; init; }
    [JsonPropertyName("maxSize")]
    [JsonRequired] public required long MaxSize { get; init; }
    [JsonPropertyName("schema")]
    [JsonRequired] public required IReadOnlyDictionary<string, JsonElement> Schema { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldAutoDateV2
{
    [JsonPropertyName("role")]
    [JsonRequired] public required string Role { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldFormulaV2
{
    [JsonPropertyName("language")]
    [JsonRequired] public required string Language { get; init; }
    [JsonPropertyName("source")]
    [JsonRequired] public required string Source { get; init; }
    [JsonPropertyName("resultType")]
    [JsonRequired] public required string ResultType { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldFormulaDraftV2
{
    [JsonPropertyName("language")]
    [JsonRequired] public required string Language { get; init; }
    [JsonPropertyName("source")]
    [JsonRequired] public required string Source { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldLookupV2
{
    [JsonPropertyName("path")]
    [JsonRequired] public required IReadOnlyList<FieldLookupPathStepV2> Path { get; init; }
    [JsonPropertyName("targetFieldId")]
    [JsonRequired] public required string TargetFieldId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldLookupPathStepV2
{
    [JsonPropertyName("relationFieldId")]
    [JsonRequired] public required string RelationFieldId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDefinitionV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("identity")]
    [JsonRequired] public required FieldIdentityV2 Identity { get; init; }
    [JsonPropertyName("displayName")]
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonPropertyName("help")]
    [JsonRequired] public required string Help { get; init; }
    [JsonPropertyName("logicalType")]
    [JsonRequired] public required string LogicalType { get; init; }
    [JsonPropertyName("lifecycle")]
    [JsonRequired] public required FieldLifecycleV2 Lifecycle { get; init; }
    [JsonPropertyName("value")]
    [JsonRequired] public required FieldValueV2 Value { get; init; }
    [JsonPropertyName("constraints")]
    [JsonRequired] public required FieldConstraintsV2 Constraints { get; init; }
    [JsonPropertyName("storage")]
    [JsonRequired] public required FieldStorageV2 Storage { get; init; }
    [JsonPropertyName("display")]
    [JsonRequired] public required FieldDisplayV2 Display { get; init; }
    [JsonPropertyName("select")]
    public FieldSelectV2? Select { get; init; }
    [JsonPropertyName("relation")]
    public FieldRelationV2? Relation { get; init; }
    [JsonPropertyName("file")]
    public FieldFileV2? File { get; init; }
    [JsonPropertyName("json")]
    public FieldJsonV2? Json { get; init; }
    [JsonPropertyName("autoDate")]
    public FieldAutoDateV2? AutoDate { get; init; }
    [JsonPropertyName("formula")]
    public FieldFormulaV2? Formula { get; init; }
    [JsonPropertyName("lookup")]
    public FieldLookupV2? Lookup { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDraftV2
{
    [JsonPropertyName("displayName")]
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonPropertyName("help")]
    [JsonRequired] public required string Help { get; init; }
    [JsonPropertyName("logicalType")]
    [JsonRequired] public required string LogicalType { get; init; }
    [JsonPropertyName("value")]
    [JsonRequired] public required FieldValueV2 Value { get; init; }
    [JsonPropertyName("constraints")]
    [JsonRequired] public required FieldConstraintsV2 Constraints { get; init; }
    [JsonPropertyName("storage")]
    [JsonRequired] public required FieldStorageV2 Storage { get; init; }
    [JsonPropertyName("display")]
    [JsonRequired] public required FieldDisplayV2 Display { get; init; }
    [JsonPropertyName("select")]
    public FieldSelectDraftV2? Select { get; init; }
    [JsonPropertyName("relation")]
    public FieldRelationV2? Relation { get; init; }
    [JsonPropertyName("file")]
    public FieldFileV2? File { get; init; }
    [JsonPropertyName("json")]
    public FieldJsonV2? Json { get; init; }
    [JsonPropertyName("autoDate")]
    public FieldAutoDateV2? AutoDate { get; init; }
    [JsonPropertyName("formula")]
    public FieldFormulaDraftV2? Formula { get; init; }
    [JsonPropertyName("lookup")]
    public FieldLookupV2? Lookup { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldRecommendedValuesV2
{
    [JsonPropertyName("defaultsVersion")]
    [JsonRequired] public required long DefaultsVersion { get; init; }
    [JsonPropertyName("value")]
    [JsonRequired] public required FieldValueV2 Value { get; init; }
    [JsonPropertyName("constraints")]
    [JsonRequired] public required FieldConstraintsV2 Constraints { get; init; }
    [JsonPropertyName("storage")]
    [JsonRequired] public required FieldStorageV2 Storage { get; init; }
    [JsonPropertyName("display")]
    [JsonRequired] public required FieldDisplayV2 Display { get; init; }
    [JsonPropertyName("file")]
    public FieldFileV2? File { get; init; }
    [JsonPropertyName("json")]
    public FieldJsonV2? Json { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldCapabilityV2
{
    [JsonPropertyName("logicalType")]
    [JsonRequired] public required string LogicalType { get; init; }
    [JsonPropertyName("generalSettings")]
    [JsonRequired] public required IReadOnlyList<string> GeneralSettings { get; init; }
    [JsonPropertyName("advancedSettings")]
    [JsonRequired] public required IReadOnlyList<string> AdvancedSettings { get; init; }
    [JsonPropertyName("dangerSettings")]
    [JsonRequired] public required IReadOnlyList<string> DangerSettings { get; init; }
    [JsonPropertyName("recommended")]
    [JsonRequired] public required FieldRecommendedValuesV2 Recommended { get; init; }
    [JsonPropertyName("supportsRequired")]
    [JsonRequired] public required bool SupportsRequired { get; init; }
    [JsonPropertyName("supportsDefault")]
    [JsonRequired] public required bool SupportsDefault { get; init; }
    [JsonPropertyName("supportsUnique")]
    [JsonRequired] public required bool SupportsUnique { get; init; }
    [JsonPropertyName("needsPresence")]
    [JsonRequired] public required bool NeedsPresence { get; init; }
    [JsonPropertyName("displayPresets")]
    [JsonRequired] public required IReadOnlyList<string> DisplayPresets { get; init; }
    [JsonPropertyName("conversionTargets")]
    [JsonRequired] public required IReadOnlyList<string> ConversionTargets { get; init; }
    [JsonPropertyName("conversionRules")]
    [JsonRequired] public required IReadOnlyList<string> ConversionRules { get; init; }
    [JsonPropertyName("compileStrategy")]
    [JsonRequired] public required string CompileStrategy { get; init; }
    [JsonPropertyName("userCreatable")]
    [JsonRequired] public required bool UserCreatable { get; init; }
    [JsonPropertyName("filterOperators")]
    [JsonRequired] public required IReadOnlyList<string> FilterOperators { get; init; }
    [JsonPropertyName("groupable")]
    [JsonRequired] public required bool Groupable { get; init; }
    [JsonPropertyName("summaryOperations")]
    [JsonRequired] public required IReadOnlyList<string> SummaryOperations { get; init; }
    [JsonPropertyName("relationCardinalities")]
    [JsonRequired] public required IReadOnlyList<string> RelationCardinalities { get; init; }
    [JsonPropertyName("relationDeletePolicies")]
    [JsonRequired] public required IReadOnlyList<string> RelationDeletePolicies { get; init; }
    [JsonPropertyName("lookupMaxDepth")]
    [JsonRequired] public required long LookupMaxDepth { get; init; }
    [JsonPropertyName("formulaResultTypeInferred")]
    [JsonRequired] public required bool FormulaResultTypeInferred { get; init; }
    [JsonPropertyName("formulaRelationAggregates")]
    [JsonRequired] public required IReadOnlyList<string> FormulaRelationAggregates { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaSnapshotV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("displayName")]
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonPropertyName("kind")]
    [JsonRequired] public required string Kind { get; init; }
    [JsonPropertyName("schemaRevision")]
    [JsonRequired] public required string SchemaRevision { get; init; }
    [JsonPropertyName("dataRevision")]
    [JsonRequired] public required long DataRevision { get; init; }
    [JsonPropertyName("archivePolicy")]
    [JsonRequired] public required SchemaArchivePolicyV2 ArchivePolicy { get; init; }
    [JsonPropertyName("fields")]
    [JsonRequired] public required IReadOnlyList<FieldDefinitionV2> Fields { get; init; }
    [JsonPropertyName("capabilities")]
    [JsonRequired] public required IReadOnlyList<FieldCapabilityV2> Capabilities { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FormulaValidateRequestV2
{
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("field")]
    [JsonRequired] public required FieldDefinitionV2 Field { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FormulaPreviewRequestV2
{
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("field")]
    [JsonRequired] public required FieldDefinitionV2 Field { get; init; }
    [JsonPropertyName("row")]
    [JsonRequired] public required IReadOnlyDictionary<string, JsonElement> Row { get; init; }
    [JsonPropertyName("changedFieldIds")]
    [JsonRequired] public required IReadOnlyList<string> ChangedFieldIds { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaTableCreateIntentV2
{
    [JsonPropertyName("displayName")]
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonPropertyName("operationId")]
    [JsonRequired] public required string OperationId { get; init; }
    [JsonPropertyName("actor")]
    [JsonRequired] public required FieldActorV2 Actor { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaTableCreateReceiptV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("operationId")]
    [JsonRequired] public required string OperationId { get; init; }
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("displayName")]
    [JsonRequired] public required string DisplayName { get; init; }
    [JsonPropertyName("schemaRevision")]
    [JsonRequired] public required string SchemaRevision { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaArchivePolicyV2
{
    [JsonPropertyName("mode")]
    [JsonRequired] public required string Mode { get; init; }
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string? FieldId { get; init; }
    [JsonPropertyName("archivedValue")]
    [JsonRequired] public required JsonElement ArchivedValue { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaTableSettingsIntentV2
{
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("expectedSchemaRevision")]
    [JsonRequired] public required string ExpectedSchemaRevision { get; init; }
    [JsonPropertyName("archivePolicy")]
    [JsonRequired] public required SchemaArchivePolicyV2 ArchivePolicy { get; init; }
    [JsonPropertyName("operationId")]
    [JsonRequired] public required string OperationId { get; init; }
    [JsonPropertyName("actor")]
    [JsonRequired] public required FieldActorV2 Actor { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record SchemaTableSettingsReceiptV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("operationId")]
    [JsonRequired] public required string OperationId { get; init; }
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("schemaRevision")]
    [JsonRequired] public required string SchemaRevision { get; init; }
    [JsonPropertyName("archivePolicy")]
    [JsonRequired] public required SchemaArchivePolicyV2 ArchivePolicy { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldActorV2
{
    [JsonPropertyName("id")]
    [JsonRequired] public required string Id { get; init; }
    [JsonPropertyName("kind")]
    [JsonRequired] public required string Kind { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldRelationPairDraftV2
{
    [JsonPropertyName("reciprocalDisplayName")]
    [JsonRequired] public required string ReciprocalDisplayName { get; init; }
    [JsonPropertyName("reciprocalCardinality")]
    [JsonRequired] public required string ReciprocalCardinality { get; init; }
    [JsonPropertyName("sourceDisplayFieldId")]
    [JsonRequired] public required string SourceDisplayFieldId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldChangeIntentV2
{
    [JsonPropertyName("action")]
    [JsonRequired] public required string Action { get; init; }
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string FieldId { get; init; }
    [JsonPropertyName("expectedSchemaRevision")]
    [JsonRequired] public required string ExpectedSchemaRevision { get; init; }
    [JsonPropertyName("expectedDataRevision")]
    [JsonRequired] public required long? ExpectedDataRevision { get; init; }
    [JsonPropertyName("draft")]
    [JsonRequired] public required FieldDraftV2? Draft { get; init; }
    [JsonPropertyName("actor")]
    [JsonRequired] public required FieldActorV2 Actor { get; init; }
    [JsonPropertyName("conversionRule")]
    [JsonRequired] public required string ConversionRule { get; init; }
    [JsonPropertyName("confirmation")]
    [JsonRequired] public required string Confirmation { get; init; }
    [JsonPropertyName("backupReceipt")]
    [JsonRequired] public required string BackupReceipt { get; init; }
    [JsonPropertyName("relationPair")]
    public FieldRelationPairDraftV2? RelationPair { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDiagnosticV2
{
    [JsonPropertyName("code")]
    [JsonRequired] public required string Code { get; init; }
    [JsonPropertyName("path")]
    [JsonRequired] public required string Path { get; init; }
    [JsonPropertyName("message")]
    [JsonRequired] public required string Message { get; init; }
    [JsonPropertyName("details")]
    [JsonRequired] public required IReadOnlyDictionary<string, JsonElement> Details { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldFailureSampleV2
{
    [JsonPropertyName("recordId")]
    [JsonRequired] public required string RecordId { get; init; }
    [JsonPropertyName("reason")]
    [JsonRequired] public required string Reason { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldDependencyRefV2
{
    [JsonPropertyName("kind")]
    [JsonRequired] public required string Kind { get; init; }
    [JsonPropertyName("id")]
    [JsonRequired] public required string Id { get; init; }
    [JsonPropertyName("name")]
    [JsonRequired] public required string Name { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldImpactV2
{
    [JsonPropertyName("records")]
    [JsonRequired] public required long Records { get; init; }
    [JsonPropertyName("missing")]
    [JsonRequired] public required long Missing { get; init; }
    [JsonPropertyName("ambiguous")]
    [JsonRequired] public required long Ambiguous { get; init; }
    [JsonPropertyName("failures")]
    [JsonRequired] public required IReadOnlyList<FieldFailureSampleV2> Failures { get; init; }
    [JsonPropertyName("dependencies")]
    [JsonRequired] public required IReadOnlyList<FieldDependencyRefV2> Dependencies { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldPlanStepV2
{
    [JsonPropertyName("kind")]
    [JsonRequired] public required string Kind { get; init; }
    [JsonPropertyName("details")]
    [JsonRequired] public required IReadOnlyDictionary<string, JsonElement> Details { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RelatedFieldChangeV2
{
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string FieldId { get; init; }
    [JsonPropertyName("before")]
    [JsonRequired] public required FieldDefinitionV2? Before { get; init; }
    [JsonPropertyName("after")]
    [JsonRequired] public required FieldDefinitionV2? After { get; init; }
    [JsonPropertyName("expectedSchemaRevision")]
    [JsonRequired] public required string ExpectedSchemaRevision { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldChangePlanV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("planId")]
    [JsonRequired] public required string PlanId { get; init; }
    [JsonPropertyName("planHash")]
    [JsonRequired] public required string PlanHash { get; init; }
    [JsonPropertyName("expiresAt")]
    [JsonRequired] public required string ExpiresAt { get; init; }
    [JsonPropertyName("intent")]
    [JsonRequired] public required FieldChangeIntentV2 Intent { get; init; }
    [JsonPropertyName("before")]
    [JsonRequired] public required FieldDefinitionV2? Before { get; init; }
    [JsonPropertyName("after")]
    [JsonRequired] public required FieldDefinitionV2? After { get; init; }
    [JsonPropertyName("classes")]
    [JsonRequired] public required IReadOnlyList<string> Classes { get; init; }
    [JsonPropertyName("expectedSchemaRevision")]
    [JsonRequired] public required string ExpectedSchemaRevision { get; init; }
    [JsonPropertyName("expectedDataRevision")]
    [JsonRequired] public required long? ExpectedDataRevision { get; init; }
    [JsonPropertyName("impact")]
    [JsonRequired] public required FieldImpactV2 Impact { get; init; }
    [JsonPropertyName("steps")]
    [JsonRequired] public required IReadOnlyList<FieldPlanStepV2> Steps { get; init; }
    [JsonPropertyName("warnings")]
    [JsonRequired] public required IReadOnlyList<FieldDiagnosticV2> Warnings { get; init; }
    [JsonPropertyName("errors")]
    [JsonRequired] public required IReadOnlyList<FieldDiagnosticV2> Errors { get; init; }
    [JsonPropertyName("confirmations")]
    [JsonRequired] public required IReadOnlyList<string> Confirmations { get; init; }
    [JsonPropertyName("createsMigration")]
    [JsonRequired] public required bool CreatesMigration { get; init; }
    [JsonPropertyName("canApply")]
    [JsonRequired] public required bool CanApply { get; init; }
    [JsonPropertyName("relatedChanges")]
    public IReadOnlyList<RelatedFieldChangeV2>? RelatedChanges { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record RelatedFieldApplyReceiptV2
{
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string FieldId { get; init; }
    [JsonPropertyName("schemaRevision")]
    [JsonRequired] public required string SchemaRevision { get; init; }
    [JsonPropertyName("definition")]
    [JsonRequired] public required FieldDefinitionV2? Definition { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldApplyReceiptV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("operationId")]
    [JsonRequired] public required string OperationId { get; init; }
    [JsonPropertyName("planId")]
    [JsonRequired] public required string PlanId { get; init; }
    [JsonPropertyName("action")]
    [JsonRequired] public required string Action { get; init; }
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string FieldId { get; init; }
    [JsonPropertyName("schemaRevision")]
    [JsonRequired] public required string SchemaRevision { get; init; }
    [JsonPropertyName("definition")]
    [JsonRequired] public required FieldDefinitionV2? Definition { get; init; }
    [JsonPropertyName("migrationJobId")]
    [JsonRequired] public required string MigrationJobId { get; init; }
    [JsonPropertyName("related")]
    public IReadOnlyList<RelatedFieldApplyReceiptV2>? Related { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldApplyRequestV2
{
    [JsonPropertyName("planId")]
    [JsonRequired] public required string PlanId { get; init; }
    [JsonPropertyName("planHash")]
    [JsonRequired] public required string PlanHash { get; init; }
    [JsonPropertyName("operationId")]
    [JsonRequired] public required string OperationId { get; init; }
    [JsonPropertyName("actor")]
    [JsonRequired] public required FieldActorV2 Actor { get; init; }
    [JsonPropertyName("confirmations")]
    [JsonRequired] public required IReadOnlyList<string> Confirmations { get; init; }
    [JsonPropertyName("protectionSnapshotId")]
    public string? ProtectionSnapshotId { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldMigrationStatusV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("jobId")]
    [JsonRequired] public required string JobId { get; init; }
    [JsonPropertyName("planId")]
    [JsonRequired] public required string PlanId { get; init; }
    [JsonPropertyName("phase")]
    [JsonRequired] public required string Phase { get; init; }
    [JsonPropertyName("processed")]
    [JsonRequired] public required long Processed { get; init; }
    [JsonPropertyName("total")]
    [JsonRequired] public required long Total { get; init; }
    [JsonPropertyName("canCancel")]
    [JsonRequired] public required bool CanCancel { get; init; }
    [JsonPropertyName("error")]
    [JsonRequired] public required FieldDiagnosticV2? Error { get; init; }
    [JsonPropertyName("updatedAt")]
    [JsonRequired] public required string UpdatedAt { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldSettingsDescribeResultV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("tableId")]
    [JsonRequired] public required string TableId { get; init; }
    [JsonPropertyName("fieldId")]
    [JsonRequired] public required string FieldId { get; init; }
    [JsonPropertyName("schemaRevision")]
    [JsonRequired] public required string SchemaRevision { get; init; }
    [JsonPropertyName("dataRevision")]
    [JsonRequired] public required long DataRevision { get; init; }
    [JsonPropertyName("definition")]
    [JsonRequired] public required FieldDefinitionV2? Definition { get; init; }
    [JsonPropertyName("capabilities")]
    [JsonRequired] public required IReadOnlyList<FieldCapabilityV2> Capabilities { get; init; }
    [JsonPropertyName("recommendedDefaultsVersion")]
    [JsonRequired] public required long RecommendedDefaultsVersion { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldRecycleBinResultV2
{
    [JsonPropertyName("contract")]
    [JsonRequired] public required string Contract { get; init; }
    [JsonPropertyName("fields")]
    [JsonRequired] public required IReadOnlyList<FieldDefinitionV2> Fields { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldValueCorpusOptionV2
{
    [JsonPropertyName("optionId")]
    [JsonRequired] public required string OptionId { get; init; }
    [JsonPropertyName("label")]
    [JsonRequired] public required string Label { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldValueCorpusCaseV2
{
    [JsonPropertyName("id")]
    [JsonRequired] public required string Id { get; init; }
    [JsonPropertyName("field")]
    [JsonRequired] public required string Field { get; init; }
    [JsonPropertyName("logicalType")]
    [JsonRequired] public required string LogicalType { get; init; }
    [JsonPropertyName("rawValue")]
    [JsonRequired] public required string RawValue { get; init; }
    [JsonPropertyName("productValue")]
    [JsonRequired] public required JsonElement ProductValue { get; init; }
    [JsonPropertyName("selectOptions")]
    public IReadOnlyList<FieldValueCorpusOptionV2>? SelectOptions { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record FieldValueEntryCorpusV2
{
    [JsonPropertyName("$schema")]
    [JsonRequired] public required string Schema { get; init; }
    [JsonPropertyName("description")]
    [JsonRequired] public required string Description { get; init; }
    [JsonPropertyName("cases")]
    [JsonRequired] public required IReadOnlyList<FieldValueCorpusCaseV2> Cases { get; init; }
}
