using System.Collections.Generic;
using System.Text.Json;

namespace VibeTable.Contracts;

public static class SchemaV2Contract
{
    public const string Name = "vibetable.schema.v2";

    public static bool ValidateResult(object value, out string reason)
    {
        reason = "";
        switch (value)
        {
            case FieldSettingsDescribeResultV2 described:
                if (described.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                if (described.Definition is not null
                    && !ValidateField(described.Definition, out reason))
                {
                    return false;
                }
                return true;
            case FieldChangePlanV2 plan:
                if (plan.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                if (!((plan.Before is null || ValidateField(plan.Before, out reason))
                    && (plan.After is null || ValidateField(plan.After, out reason))))
                {
                    return false;
                }
                foreach (RelatedFieldChangeV2 related in plan.RelatedChanges ?? [])
                {
                    if ((related.Before is not null && !ValidateField(related.Before, out reason))
                        || (related.After is not null && !ValidateField(related.After, out reason)))
                    {
                        return false;
                    }
                }
                return true;
            case FieldApplyReceiptV2 receipt:
                if (receipt.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                if (receipt.Definition is not null
                    && !ValidateField(receipt.Definition, out reason))
                {
                    return false;
                }
                foreach (RelatedFieldApplyReceiptV2 related in receipt.Related ?? [])
                {
                    if (related.Definition is not null
                        && !ValidateField(related.Definition, out reason))
                    {
                        return false;
                    }
                }
                return true;
            case FieldMigrationStatusV2 status:
                if (status.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                return true;
            case FieldRecycleBinResultV2 recycle:
                if (recycle.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                foreach (FieldDefinitionV2 field in recycle.Fields)
                {
                    if (!ValidateField(field, out reason))
                    {
                        return false;
                    }
                }
                return true;
            default:
                return true;
        }
    }

    private static bool ValidateField(FieldDefinitionV2 field, out string reason)
    {
        reason = "";
        if (field.Contract != Name)
        {
            reason = "unsupported field contract";
            return false;
        }
        if (field.Lifecycle.State is not ("active" or "retired"))
        {
            reason = $"unsupported lifecycle state {field.Lifecycle.State}";
            return false;
        }
        if (field.Storage.Kind is not (
            "pocketbase-text" or "pocketbase-editor" or "pocketbase-number"
            or "pocketbase-bool" or "pocketbase-date" or "pocketbase-autodate"
            or "pocketbase-email" or "pocketbase-url" or "pocketbase-select"
            or "pocketbase-relation" or "pocketbase-file" or "pocketbase-geo-point"
            or "pocketbase-json" or "computed"))
        {
            reason = $"unsupported storage kind {field.Storage.Kind}";
            return false;
        }
        if (field.Display.ScaleMode is not ("max" or "fixed")
            || field.Display.PercentStorage is not ("ratio" or "percent")
            || field.Display.Precision is not (
                "exact" or "day" or "minute" or "second" or "millisecond")
            || field.Display.Indent is not (0 or 2 or 4))
        {
            reason = "unsupported display settings";
            return false;
        }
        string? expected = field.LogicalType switch
        {
            "select" or "multiSelect" => nameof(field.Select),
            "relation" => nameof(field.Relation),
            "file" => nameof(field.File),
            "json" => nameof(field.Json),
            "autoDate" => nameof(field.AutoDate),
            "formula" => nameof(field.Formula),
            "lookup" => nameof(field.Lookup),
            _ => null,
        };
        var configured = new Dictionary<string, bool>
        {
            [nameof(field.Select)] = field.Select is not null,
            [nameof(field.Relation)] = field.Relation is not null,
            [nameof(field.File)] = field.File is not null,
            [nameof(field.Json)] = field.Json is not null,
            [nameof(field.AutoDate)] = field.AutoDate is not null,
            [nameof(field.Formula)] = field.Formula is not null,
            [nameof(field.Lookup)] = field.Lookup is not null,
        };
        if (expected is not null && !configured[expected])
        {
            reason = $"{expected} settings are required for {field.LogicalType}";
            return false;
        }
        foreach (var item in configured)
        {
            if (item.Value && item.Key != expected)
            {
                reason = $"{item.Key} settings are not allowed for {field.LogicalType}";
                return false;
            }
        }
        if (field.Lookup is not null
            && (field.Lookup.Path.Count is < 1 or > 8
                || field.Lookup.Path.Any(step => string.IsNullOrWhiteSpace(step.RelationFieldId))
                || string.IsNullOrWhiteSpace(field.Lookup.TargetFieldId)))
        {
            reason = "Lookup requires one to eight relation path steps and a target field";
            return false;
        }
        return true;
    }
}

public sealed record FieldIdentityV2(
    string FieldId,
    string PhysicalName,
    string ProviderFieldId);

public sealed record FieldLifecycleV2(
    string State,
    string? RetiredAt);

public sealed record FieldDefaultV2(
    bool Enabled,
    JsonElement Value,
    string Source,
    int DefaultsVersion);

public sealed record FieldPresenceV2(
    string Mode,
    string? ProviderFieldId = null,
    string? PhysicalName = null);

public sealed record FieldValueV2(
    bool Required,
    FieldDefaultV2 Default,
    FieldPresenceV2 Presence);

public sealed record FieldUniqueV2(bool Enabled, string BlankPolicy);
public sealed record FieldRangeV2(JsonElement Min, JsonElement Max);
public sealed record FieldLengthV2(int? Min, int? Max);
public sealed record FieldPatternV2(bool Enabled, string Value);
public sealed record FieldDomainsV2(
    IReadOnlyList<string> Only,
    IReadOnlyList<string> Except);
public sealed record FieldSelectionV2(int Min, int? Max);

public sealed record FieldConstraintsV2(
    FieldUniqueV2 Unique,
    FieldRangeV2 Range,
    FieldLengthV2 Length,
    FieldPatternV2 Pattern,
    FieldDomainsV2 Domains,
    FieldSelectionV2 Selection);

public sealed record FieldStorageOptionsV2(
    bool OnlyInt,
    int MaxSize,
    bool ConvertURLs,
    bool Presentable);

public sealed record FieldStorageV2(
    string Kind,
    FieldStorageOptionsV2 Options);

public sealed record FieldDisplayV2(
    string Kind,
    string Preset,
    int DisplayScale,
    string ScaleMode,
    bool TrimTrailingZeros,
    bool UseGrouping,
    string Currency,
    string PercentStorage,
    string? Unit,
    string Precision,
    string Timezone,
    string Mode,
    string TrueLabel,
    string FalseLabel,
    int Indent = 0);

public sealed record FieldSelectOptionV2(
    string OptionId,
    string Label,
    string Color,
    int Order,
    string State);

public sealed record FieldSelectV2(IReadOnlyList<FieldSelectOptionV2> Options);

public sealed record FieldRelationV2(
    string TargetTableId,
    string Cardinality,
    string DeletePolicy,
    string DisplayFieldId,
    string PairId = "",
    string ReciprocalFieldId = "");

public sealed record FieldFileV2(
    int MaxFiles,
    long MaxBytesPerFile,
    IReadOnlyList<string> AllowedMimeTypes,
    IReadOnlyList<string> Thumbs,
    bool Protected);

public sealed record FieldJsonV2(
    string RootType,
    int MaxSize,
    JsonElement Schema);

public sealed record FieldAutoDateV2(string Role);

public sealed record FieldFormulaV2(
    string Language,
    string Source,
    string ResultType);

public sealed record FieldLookupV2(
    IReadOnlyList<FieldLookupPathStepV2> Path,
    string TargetFieldId);

public sealed record FieldLookupPathStepV2(string RelationFieldId);

public sealed record FieldDefinitionV2(
    string Contract,
    FieldIdentityV2 Identity,
    string DisplayName,
    string Help,
    string LogicalType,
    FieldLifecycleV2 Lifecycle,
    FieldValueV2 Value,
    FieldConstraintsV2 Constraints,
    FieldStorageV2 Storage,
    FieldDisplayV2 Display,
    FieldSelectV2? Select = null,
    FieldRelationV2? Relation = null,
    FieldFileV2? File = null,
    FieldJsonV2? Json = null,
    FieldAutoDateV2? AutoDate = null,
    FieldFormulaV2? Formula = null,
    FieldLookupV2? Lookup = null);

public sealed record FieldDraftV2(
    string DisplayName,
    string Help,
    string LogicalType,
    FieldValueV2 Value,
    FieldConstraintsV2 Constraints,
    FieldStorageV2 Storage,
    FieldDisplayV2 Display,
    FieldSelectV2? Select = null,
    FieldRelationV2? Relation = null,
    FieldFileV2? File = null,
    FieldJsonV2? Json = null,
    FieldAutoDateV2? AutoDate = null,
    FieldFormulaV2? Formula = null,
    FieldLookupV2? Lookup = null);

public sealed record FieldRecommendedValuesV2(
    int DefaultsVersion,
    FieldValueV2 Value,
    FieldConstraintsV2 Constraints,
    FieldStorageV2 Storage,
    FieldDisplayV2 Display,
    FieldFileV2? File = null,
    FieldJsonV2? Json = null);

public sealed record FieldCapabilityV2(
    string LogicalType,
    IReadOnlyList<string> GeneralSettings,
    IReadOnlyList<string> AdvancedSettings,
    IReadOnlyList<string> DangerSettings,
    FieldRecommendedValuesV2 Recommended,
    bool SupportsRequired,
    bool SupportsDefault,
    bool SupportsUnique,
    bool NeedsPresence,
    IReadOnlyList<string> DisplayPresets,
    IReadOnlyList<string> ConversionTargets,
    IReadOnlyList<string> ConversionRules,
    string CompileStrategy,
    bool UserCreatable);

public sealed record FieldActorV2(
    string Id,
    string Kind);

public sealed record FieldRelationPairDraftV2(
    string ReciprocalDisplayName,
    string ReciprocalCardinality,
    string SourceDisplayFieldId);

public sealed record FieldChangeIntentV2(
    string Action,
    string TableId,
    string FieldId,
    string ExpectedSchemaRevision,
    long? ExpectedDataRevision,
    FieldDraftV2? Draft,
    FieldActorV2 Actor,
    string ConversionRule,
    string Confirmation,
    string BackupReceipt,
    FieldRelationPairDraftV2? RelationPair = null);

public sealed record FieldDiagnosticV2(
    string Code,
    string Path,
    string Message,
    IReadOnlyDictionary<string, JsonElement> Details);

public sealed record FieldFailureSampleV2(string RecordId, string Reason);
public sealed record FieldDependencyRefV2(string Kind, string Id, string Name);
public sealed record FieldImpactV2(
    long Records,
    long Missing,
    long Ambiguous,
    IReadOnlyList<FieldFailureSampleV2> Failures,
    IReadOnlyList<FieldDependencyRefV2> Dependencies);
public sealed record FieldPlanStepV2(string Kind, JsonElement Details);
public sealed record RelatedFieldChangeV2(
    string TableId,
    string FieldId,
    FieldDefinitionV2? Before,
    FieldDefinitionV2? After,
    string ExpectedSchemaRevision);

public sealed record FieldChangePlanV2(
    string Contract,
    string PlanId,
    string PlanHash,
    string ExpiresAt,
    FieldChangeIntentV2 Intent,
    FieldDefinitionV2? Before,
    FieldDefinitionV2? After,
    IReadOnlyList<string> Classes,
    string ExpectedSchemaRevision,
    long? ExpectedDataRevision,
    FieldImpactV2 Impact,
    IReadOnlyList<FieldPlanStepV2> Steps,
    IReadOnlyList<FieldDiagnosticV2> Warnings,
    IReadOnlyList<FieldDiagnosticV2> Errors,
    IReadOnlyList<string> Confirmations,
    bool CreatesMigration,
    bool CanApply,
    IReadOnlyList<RelatedFieldChangeV2>? RelatedChanges = null);

public sealed record RelatedFieldApplyReceiptV2(
    string TableId,
    string FieldId,
    string SchemaRevision,
    FieldDefinitionV2? Definition);

public sealed record FieldApplyReceiptV2(
    string Contract,
    string OperationId,
    string PlanId,
    string Action,
    string TableId,
    string FieldId,
    string SchemaRevision,
    FieldDefinitionV2? Definition,
    string MigrationJobId,
    IReadOnlyList<RelatedFieldApplyReceiptV2>? Related = null);

public sealed record FieldApplyRequestV2(
    string PlanId,
    string PlanHash,
    string OperationId,
    FieldActorV2 Actor,
    IReadOnlyList<string> Confirmations,
    string ProtectionSnapshotId = "");

public sealed record FieldMigrationStatusV2(
    string Contract,
    string JobId,
    string PlanId,
    string Phase,
    long Processed,
    long Total,
    bool CanCancel,
    FieldDiagnosticV2? Error,
    string UpdatedAt);

public sealed record FieldSettingsDescribeResultV2(
    string Contract,
    string TableId,
    string FieldId,
    string SchemaRevision,
    long DataRevision,
    FieldDefinitionV2? Definition,
    IReadOnlyList<FieldCapabilityV2> Capabilities,
    int RecommendedDefaultsVersion);

public sealed record FieldRecycleBinResultV2(
    string Contract,
    IReadOnlyList<FieldDefinitionV2> Fields);
