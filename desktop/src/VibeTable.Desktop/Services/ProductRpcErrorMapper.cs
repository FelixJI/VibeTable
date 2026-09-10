using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal static class ProductRpcErrorMapper
{
    internal static bool TryMapContent(string method, JsonElement data, out JsonElement response)
    {
        response = default;
        if (method is not ("contentProfile.load" or "contentProfile.commit" or "contentProfile.delete"
            or "recordDocumentLink.list" or "recordDocumentLink.commit"
            or "recordDocumentLink.repair" or "recordDocumentLink.delete"))
            return false;
        bool hasPath = data.ValueKind == JsonValueKind.Object && data.TryGetProperty("path", out _);
        if (!HasContentProperties(data, hasPath)
            || data.GetProperty("kind").ValueKind != JsonValueKind.String
            || data.GetProperty("kind").GetString() != "content_model_error"
            || !TryString(data, "message", out _)
            || !TryString(data, "code", out _)
            || (hasPath && data.GetProperty("path").ValueKind != JsonValueKind.String))
            return false;
        string code = data.GetProperty("code").GetString()!;
        if (code is not ("content_model.edit_conflict"
            or "content_model.idempotency_conflict"
            or "content_model.not_found"
            or "content_model.persistence_failed"
            or "content_model.storage_invalid"
            or "content_profile.body_type_invalid"
            or "content_profile.edit_conflict"
            or "content_profile.field_missing"
            or "content_profile.not_found"
            or "content_profile.search_field_duplicate"
            or "content_profile.search_field_invalid"
            or "content_profile.summary_type_invalid"
            or "content_profile.table_missing"
            or "content_profile.title_type_invalid"
            or "record_document_link.edit_conflict"
            or "record_document_link.not_found"
            or "record_document_link.record_lookup_failed"
            or "record_document_link.record_missing"))
            return false;
        return TryMap(data, out response);
    }

    private static bool HasContentProperties(JsonElement data, bool hasPath)
    {
        if (data.ValueKind != JsonValueKind.Object) return false;
        string[] expected = hasPath ? ["kind", "message", "code", "path"] : ["kind", "message", "code"];
        return data.EnumerateObject().Select(property => property.Name)
            .Order(StringComparer.Ordinal).SequenceEqual(expected.Order(StringComparer.Ordinal));
    }

    internal static bool TryMap(JsonElement source, out JsonElement response)
    {
        response = default;
        if (source.ValueKind != JsonValueKind.Object
            || !TryString(source, "code", out string code)
            || !TryString(source, "message", out string message)
            || !WorkCalendarErrorPolicy.Accepts(code)
            || code.StartsWith("pocketbase.", StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }

        string path = "";
        if (source.TryGetProperty("path", out var pathElement)
            && pathElement.ValueKind == JsonValueKind.String)
        {
            path = pathElement.GetString() ?? "";
        }
        else if (pathElement.ValueKind is not (
            JsonValueKind.Undefined or JsonValueKind.Null))
        {
            return false;
        }
        if (string.IsNullOrEmpty(path)
            && source.TryGetProperty("field", out var fieldElement))
        {
            if (fieldElement.ValueKind == JsonValueKind.String)
            {
                path = fieldElement.GetString() ?? "";
            }
            else if (fieldElement.ValueKind is not JsonValueKind.Null)
            {
                return false;
            }
        }
        bool retryable = source.TryGetProperty("retryable", out var retryableElement)
            && retryableElement.ValueKind == JsonValueKind.True;
        object? details = source.TryGetProperty("details", out var detailsElement)
            && detailsElement.ValueKind == JsonValueKind.Object
                ? Sanitize(detailsElement)
                : null;
        response = JsonSerializer.SerializeToElement(new
        {
            error = new { code, path, message, details, retryable },
        });
        return true;
    }

    private static bool TryString(JsonElement source, string name, out string value)
    {
        value = "";
        if (!source.TryGetProperty(name, out var element)
            || element.ValueKind != JsonValueKind.String)
        {
            return false;
        }
        value = element.GetString() ?? "";
        return !string.IsNullOrWhiteSpace(value) || name == "path";
    }

    private static object? Sanitize(JsonElement value) => value.ValueKind switch
    {
        JsonValueKind.Object => value.EnumerateObject()
            .Where(property => property.Name is not (
                "sessionSecret" or "accessToken" or "refreshToken"
                or "password" or "pocketBaseToken"))
            .ToDictionary(
                property => property.Name,
                property => Sanitize(property.Value),
                StringComparer.Ordinal),
        JsonValueKind.Array => value.EnumerateArray().Select(Sanitize).ToArray(),
        JsonValueKind.String => value.GetString(),
        JsonValueKind.Number when value.TryGetInt64(out long integer) => integer,
        JsonValueKind.Number => value.GetDouble(),
        JsonValueKind.True => true,
        JsonValueKind.False => false,
        JsonValueKind.Null or JsonValueKind.Undefined => null,
        _ => null,
    };
}
