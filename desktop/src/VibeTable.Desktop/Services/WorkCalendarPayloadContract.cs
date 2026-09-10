using System;
using System.Linq;
using System.Text;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal static class WorkCalendarPayloadContract
{
    internal static bool IsValidRead(JsonElement value) =>
        value.ValueKind == JsonValueKind.Object && !value.EnumerateObject().Any();

    internal static bool IsValidCommit(JsonElement value)
    {
        if (!Exact(value, "overrides", "expectedRevision", "idempotencyKey")
            || Encoding.UTF8.GetByteCount(value.GetRawText()) > 1024 * 1024
            || !Text(value, "expectedRevision", 0, 256)
            || !Text(value, "idempotencyKey", 1, 192)
            || value.GetProperty("overrides").ValueKind != JsonValueKind.Array)
            return false;
        JsonElement overrides = value.GetProperty("overrides");
        if (overrides.GetArrayLength() > 3660) return false;
        return overrides.EnumerateArray().All(item =>
            Exact(item, "date", "kind", "name")
            && Text(item, "date", 10, 10)
            && Text(item, "kind", 1, 16)
            && Text(item, "name", 0, 40));
    }

    private static bool Text(JsonElement value, string name, int min, int max) =>
        value.TryGetProperty(name, out JsonElement item)
        && item.ValueKind == JsonValueKind.String
        && item.GetString() is string text && text.Length >= min && text.Length <= max;

    private static bool Exact(JsonElement value, params string[] names) =>
        value.ValueKind == JsonValueKind.Object
        && value.EnumerateObject().Count() == names.Length
        && names.All(name => value.TryGetProperty(name, out _));
}
