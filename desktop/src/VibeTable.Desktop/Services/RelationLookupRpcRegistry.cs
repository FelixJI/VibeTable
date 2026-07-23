using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// One explicitly registered relation/Lookup RPC use case. The invocation
/// delegate binds a fixed renderer request type to one typed gateway method;
/// it never accepts a renderer-provided backend method name.
/// </summary>
internal sealed record RelationLookupRpcEndpoint(
    string Type,
    Func<JsonElement, bool> IsValidPayload,
    Func<IDirectusRpcGateway, JsonElement, CancellationToken, Task<JsonElement>> InvokeAsync);

/// <summary>
/// Closed registry shared by the WebView whitelist and workspace dispatcher.
/// Adding an endpoint is intentionally a single edit that must supply both a
/// host-side payload guard and an explicit <see cref="IDirectusRpcGateway"/>
/// method binding.
/// </summary>
internal static class RelationLookupRpcRegistry
{
    private static readonly RelationLookupRpcEndpoint[] RegisteredEndpoints =
    [
        new(
            "schema.describe",
            payload => HasString(payload, "collection")
                && HasNumber(payload, "requestGeneration")
                && HasArray(payload, "accepts"),
            (gateway, payload, token) => gateway.DescribeSchemaAsync(payload, token)),
        new(
            "relation.searchTargets",
            payload => HasString(payload, "relationId"),
            (gateway, payload, token) => gateway.SearchRelationTargetsAsync(payload, token)),
        new(
            "relation.updateSingle",
            payload => HasStrings(
                    payload,
                    "relationId",
                    "sourceItemId",
                    "expectedSchemaRevision",
                    "idempotencyKey")
                && HasNullOrObject(payload, "target"),
            (gateway, payload, token) => gateway.UpdateSingleRelationAsync(payload, token)),
        new(
            "relation.previewDelta",
            IsValidRelationDelta,
            (gateway, payload, token) => gateway.PreviewRelationDeltaAsync(payload, token)),
        new(
            "relation.applyDelta",
            IsValidRelationDelta,
            (gateway, payload, token) => gateway.ApplyRelationDeltaAsync(payload, token)),
        new(
            "lookup.list",
            payload => HasString(payload, "collection"),
            (gateway, payload, token) => gateway.ListLookupsAsync(payload, token)),
        new(
            "lookup.validate",
            payload => HasObject(payload, "definition") && HasArray(payload, "existing"),
            (gateway, payload, token) => gateway.ValidateLookupAsync(payload, token)),
        new(
            "lookup.create",
            payload => HasObject(payload, "definition") && HasString(payload, "requestId"),
            (gateway, payload, token) => gateway.CreateLookupAsync(payload, token)),
        new(
            "lookup.update",
            payload => HasObject(payload, "definition")
                && HasString(payload, "requestId")
                && HasNumber(payload, "expectedRevision"),
            (gateway, payload, token) => gateway.UpdateLookupAsync(payload, token)),
        new(
            "lookup.delete",
            payload => HasStrings(payload, "collection", "lookupId", "requestId")
                && HasNumber(payload, "expectedRevision"),
            (gateway, payload, token) => gateway.DeleteLookupAsync(payload, token)),
        new(
            "lookup.preview",
            payload => IsValidLookupQuery(payload) && HasArray(payload, "definitions"),
            (gateway, payload, token) => gateway.PreviewLookupAsync(payload, token)),
        new(
            "lookup.query",
            IsValidLookupQuery,
            (gateway, payload, token) => gateway.QueryLookupsAsync(payload, token)),
        new(
            "table_admin.previewRelationChange",
            payload => HasStrings(payload, "collection", "action", "expectedSchemaRevision")
                && IsRelationChangeAction(payload),
            (gateway, payload, token) => gateway.PreviewRelationChangeAsync(payload, token)),
        new(
            "table_admin.applyRelationChange",
            payload => HasStrings(
                    payload,
                    "planId",
                    "operationId",
                    "expectedSchemaRevision")
                && HasArray(payload, "cascadeLookupIds"),
            (gateway, payload, token) => gateway.ApplyRelationChangeAsync(payload, token)),
    ];

    private static readonly IReadOnlyDictionary<string, RelationLookupRpcEndpoint> ByType =
        RegisteredEndpoints.ToDictionary(endpoint => endpoint.Type, StringComparer.Ordinal);

    internal static IReadOnlyList<string> RequestTypes { get; } =
        Array.AsReadOnly(RegisteredEndpoints.Select(endpoint => endpoint.Type).ToArray());

    internal static bool Contains(string type)
        => type is not null && ByType.ContainsKey(type);

    internal static bool TryGet(string type, out RelationLookupRpcEndpoint endpoint)
        => ByType.TryGetValue(type, out endpoint!);

    private static bool IsValidRelationDelta(JsonElement payload)
        => HasStrings(
                payload,
                "relationId",
                "sourceItemId",
                "expectedSchemaRevision",
                "idempotencyKey")
            && HasArray(payload, "adds")
            && HasArray(payload, "updates")
            && HasArray(payload, "removes");

    private static bool IsRelationChangeAction(JsonElement payload)
    {
        string? action = payload.GetProperty("action").GetString();
        return action is "create" or "update" or "delete";
    }

    private static bool IsValidLookupQuery(JsonElement payload)
        => HasStrings(
                payload,
                "contract",
                "collection",
                "schemaRevision",
                "permissionRevision",
                "lookupRevision")
            && HasArray(payload, "fieldRefs")
            && HasObject(payload, "query")
            && HasNumber(payload, "requestGeneration");

    private static bool HasStrings(JsonElement payload, params string[] names)
        => names.All(name => HasString(payload, name));

    private static bool HasString(JsonElement payload, string name)
        => IsObject(payload)
            && payload.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(value.GetString());

    private static bool HasNumber(JsonElement payload, string name)
        => IsObject(payload)
            && payload.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.Number;

    private static bool HasArray(JsonElement payload, string name)
        => IsObject(payload)
            && payload.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.Array;

    private static bool HasObject(JsonElement payload, string name)
        => IsObject(payload)
            && payload.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.Object;

    private static bool HasNullOrObject(JsonElement payload, string name)
        => IsObject(payload)
            && payload.TryGetProperty(name, out var value)
            && value.ValueKind is JsonValueKind.Null or JsonValueKind.Object;

    private static bool IsObject(JsonElement payload)
        => payload.ValueKind == JsonValueKind.Object;
}
