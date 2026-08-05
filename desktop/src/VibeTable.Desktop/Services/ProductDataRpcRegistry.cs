using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record ProductDataRpcEndpoint(
    string Type,
    Func<JsonElement, bool> IsValidPayload,
    Func<IProductDataRpcGateway, JsonElement, CancellationToken, Task<JsonElement>> InvokeAsync,
    bool MutatesWorkspace = false,
    bool RequiresProtectionSnapshot = false);

/// <summary>
/// Closed renderer-to-product dispatch table. The renderer chooses a complete
/// product use case, never a backend method name or provider credential.
/// </summary>
internal static class ProductDataRpcRegistry
{
    private const int MaxPayloadDepth = 32;
    private const int MaxPayloadNodes = 10_000;
    private static readonly ProductDataRpcEndpoint[] RegisteredEndpoints =
    [
        new("field.settings.describe", p => Safe(p)
            && HasOnlyProperties(p, "tableId", "fieldId")
            && HasString(p, "tableId")
            && HasOptionalString(p, "fieldId"),
            (g, p, t) => g.DescribeFieldSettingsAsync(p, t)),
        new("field.change.plan", p => Safe(p)
            && HasOnlyProperties(
                p, "action", "tableId", "fieldId", "expectedSchemaRevision",
                "expectedDataRevision", "draft", "actor", "conversionRule",
                "confirmation", "backupReceipt", "relationPair")
            && HasStrings(p, "action", "tableId", "expectedSchemaRevision")
            && HasObject(p, "actor"),
            (g, p, t) => g.PlanFieldChangeAsync(p, t),
            MutatesWorkspace: true,
            RequiresProtectionSnapshot: true),
        new("field.change.apply", p => Safe(p)
            && HasExactProperties(
                p, "planId", "planHash", "operationId", "actor", "confirmations")
            && HasStrings(p, "planId", "planHash", "operationId")
            && HasObject(p, "actor")
            && HasArray(p, "confirmations"),
            (g, p, t) => g.ApplyFieldChangeAsync(p, t),
            MutatesWorkspace: true,
            RequiresProtectionSnapshot: true),
        new("field.change.status", p => Safe(p)
            && HasExactProperties(p, "jobId")
            && HasString(p, "jobId"),
            (g, p, t) => g.GetFieldChangeStatusAsync(p, t)),
        new("field.change.cancel", p => Safe(p)
            && HasExactProperties(p, "jobId")
            && HasString(p, "jobId"),
            (g, p, t) => g.CancelFieldChangeAsync(p, t),
            MutatesWorkspace: true),
        new("field.recycleBin.list", p => Safe(p)
            && HasExactProperties(p, "tableId")
            && HasString(p, "tableId"),
            (g, p, t) => g.ListRecycledFieldsAsync(p, t)),
        new("schema.getTable", p => Safe(p) && HasExactProperties(p, "tableId") && HasString(p, "tableId"),
            (g, p, t) => g.GetTableSchemaAsync(p, t)),
        new("query.page", p => Safe(p) && HasString(p, "tableId") && HasObject(p, "query"),
            (g, p, t) => g.QueryPageAsync(p, t)),
        new("query.view", p => Safe(p) && HasExactProperties(p, "tableId", "view")
            && HasString(p, "tableId") && HasObject(p, "view"),
            (g, p, t) => g.QueryViewAsync(p, t)),
        new("mutation.preview", p => Safe(p) && HasString(p, "tableId") && HasArray(p, "operations"),
            (g, p, t) => g.PreviewMutationAsync(p, t)),
        new("mutation.apply", p => Safe(p) && HasString(p, "tableId") && HasArray(p, "operations"),
            (g, p, t) => g.ApplyMutationAsync(p, t),
            MutatesWorkspace: true),
        new("data.previewImport", p => Safe(p) && HasStrings(p, "grantId", "collection", "schemaRevision"),
            (g, p, t) => g.PreviewImportAsync(p, t)),
        new("data.applyImport", p => Safe(p) && HasStrings(p, "grantId", "collection", "token"),
            (g, p, t) => g.ApplyImportAsync(p, t),
            MutatesWorkspace: true,
            RequiresProtectionSnapshot: true),
        new("data.export", p => Safe(p) && HasStrings(p, "grantId", "collection") && HasObject(p, "query"),
            (g, p, t) => g.ExportAsync(p, t)),
        new("task.create", p => Safe(p) && HasString(p, "kind") && HasObject(p, "params"),
            (g, p, t) => g.CreateTaskAsync(p, t),
            MutatesWorkspace: true,
            RequiresProtectionSnapshot: true),
        new("task.cancel", p => Safe(p) && HasString(p, "taskId"),
            (g, p, t) => g.CancelTaskAsync(p, t),
            MutatesWorkspace: true),
        new("task.status", p => Safe(p) && HasString(p, "taskId"),
            (g, p, t) => g.GetTaskStatusAsync(p, t)),
        new("formula.validate", p => Safe(p) && HasObject(p, "definition"),
            (g, p, t) => g.ValidateFormulaAsync(p, t)),
        new("formula.preview", p => Safe(p) && HasObject(p, "definition") && HasObject(p, "row") && HasArray(p, "changedFieldIds"),
            (g, p, t) => g.PreviewFormulaAsync(p, t)),
        new("file.list", p => Safe(p) && HasStrings(p, "tableId", "recordId", "fieldId"),
            (g, p, t) => g.ListAttachmentRefsAsync(p, t)),
        new("file.token", p => Safe(p) && HasStrings(p, "tableId", "recordId", "fieldId", "storedName"),
            (g, p, t) => g.CreateFileTokenAsync(p, t)),
        new("events.reconcile", p => Safe(p) && HasStrings(p, "tableId", "schemaRevision", "dataRevision"),
            (g, p, t) => g.ReconcileAsync(p, t)),
        new("preset.list", p => Safe(p) && HasString(p, "collection"),
            (g, p, t) => g.ListPresetsAsync(p, t)),
        new("preset.save", p => Safe(p) && HasStrings(p, "collection", "name", "operationId") && HasObject(p, "view"),
            (g, p, t) => g.SavePresetAsync(p, t),
            MutatesWorkspace: true),
        new("preset.delete", p => Safe(p) && HasStrings(p, "presetId", "expectedRevision", "operationId"),
            (g, p, t) => g.DeletePresetAsync(p, t),
            MutatesWorkspace: true),
        new("version.list", p => Safe(p) && HasStrings(p, "collection", "itemId"),
            (g, p, t) => g.ListVersionsAsync(p, t)),
        new("version.create", p => Safe(p) && HasStrings(p, "collection", "itemId", "operationId"),
            (g, p, t) => g.CreateVersionAsync(p, t),
            MutatesWorkspace: true),
        new("version.save", p => Safe(p) && HasStrings(p, "collection", "itemId", "versionId", "operationId") && HasObject(p, "values"),
            (g, p, t) => g.SaveVersionAsync(p, t),
            MutatesWorkspace: true),
        new("version.compare", p => Safe(p) && HasStrings(p, "collection", "itemId", "versionId"),
            (g, p, t) => g.CompareVersionAsync(p, t)),
        new("version.promote", p => Safe(p) && HasStrings(p, "collection", "itemId", "versionId", "mainHash", "operationId"),
            (g, p, t) => g.PromoteVersionAsync(p, t),
            MutatesWorkspace: true,
            RequiresProtectionSnapshot: true),
        new("version.delete", p => Safe(p) && HasStrings(p, "collection", "itemId", "versionId", "expectedRevision", "operationId"),
            (g, p, t) => g.DeleteVersionAsync(p, t),
            MutatesWorkspace: true),
    ];

    private static readonly IReadOnlyDictionary<string, ProductDataRpcEndpoint> ByType =
        RegisteredEndpoints.ToDictionary(endpoint => endpoint.Type, StringComparer.Ordinal);

    internal static IReadOnlyList<string> RequestTypes { get; } =
        Array.AsReadOnly(RegisteredEndpoints.Select(endpoint => endpoint.Type).ToArray());

    internal static bool Contains(string type) => type is not null && ByType.ContainsKey(type);
    internal static bool TryGet(string type, out ProductDataRpcEndpoint endpoint)
        => ByType.TryGetValue(type, out endpoint!);

    private static bool Safe(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object) return false;
        int nodes = 0;
        return SafeValue(payload, depth: 0, ref nodes);
    }

    private static bool SafeValue(JsonElement value, int depth, ref int nodes)
    {
        if (depth > MaxPayloadDepth || ++nodes > MaxPayloadNodes)
        {
            return false;
        }
        if (value.ValueKind == JsonValueKind.Object)
        {
            foreach (var property in value.EnumerateObject())
            {
                if (property.Name is "sessionSecret" or "accessToken" or "refreshToken"
                    or "password" or "pocketBaseToken"
                    || !SafeValue(property.Value, depth + 1, ref nodes))
                {
                    return false;
                }
            }
            return true;
        }
        if (value.ValueKind == JsonValueKind.Array)
        {
            foreach (var item in value.EnumerateArray())
            {
                if (!SafeValue(item, depth + 1, ref nodes))
                {
                    return false;
                }
            }
            return true;
        }
        return value.ValueKind is JsonValueKind.String
            or JsonValueKind.Number
            or JsonValueKind.True
            or JsonValueKind.False
            or JsonValueKind.Null;
    }

    private static bool HasStrings(JsonElement payload, params string[] names)
        => names.All(name => HasString(payload, name));
    private static bool HasString(JsonElement payload, string name)
        => payload.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString());
    private static bool HasNumber(JsonElement payload, string name)
        => payload.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.Number;
    private static bool HasArray(JsonElement payload, string name)
        => payload.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.Array;
    private static bool HasObject(JsonElement payload, string name)
        => payload.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.Object;
    private static bool HasTrue(JsonElement payload, string name)
        => payload.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.True;
    private static bool HasOptionalString(JsonElement payload, string name)
        => !payload.TryGetProperty(name, out var value)
            || value.ValueKind == JsonValueKind.String;
    private static bool HasOnlyProperties(JsonElement payload, params string[] names)
    {
        var allowed = new HashSet<string>(names, StringComparer.Ordinal);
        return payload.EnumerateObject().All(property => allowed.Contains(property.Name));
    }
    private static bool HasExactProperties(JsonElement payload, params string[] names)
    {
        var expected = new HashSet<string>(names, StringComparer.Ordinal);
        int count = 0;
        foreach (var property in payload.EnumerateObject())
        {
            count++;
            if (!expected.Contains(property.Name))
            {
                return false;
            }
        }
        return count == expected.Count;
    }
}
