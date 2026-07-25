using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal static class ProductRpcErrorMapper
{
    internal static bool TryMap(JsonElement source, out JsonElement response)
    {
        response = default;
        if (source.ValueKind != JsonValueKind.Object
            || !TryString(source, "code", out string code)
            || !TryString(source, "message", out string message)
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
