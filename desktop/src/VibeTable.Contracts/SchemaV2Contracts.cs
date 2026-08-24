using System.Collections.Generic;

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
            case SchemaTableCreateReceiptV2 receipt:
                if (receipt.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                return true;
            case SchemaSnapshotV2 snapshot:
                if (snapshot.Contract != Name)
                {
                    reason = "unsupported contract";
                    return false;
                }
                foreach (FieldDefinitionV2 field in snapshot.Fields)
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
            || (field.Display.Indent is not null
                && field.Display.Indent is not (0 or 2 or 4)))
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
