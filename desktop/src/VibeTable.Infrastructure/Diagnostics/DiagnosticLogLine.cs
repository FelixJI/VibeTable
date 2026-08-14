using System.Text.Json;

namespace VibeTable.Infrastructure.Diagnostics;

public static class DiagnosticLogLine
{
    private static readonly HashSet<string> Fields = new(StringComparer.Ordinal)
    {
        "timestamp", "level", "module", "event", "errorCode", "requestId",
        "operationId", "workspaceId", "sessionEpoch", "jobId", "durationMs",
    };

    /// <summary>
    /// Accepts only the closed, content-free cross-process schema for disk
    /// persistence. Unknown subprocess output stays available to tests and
    /// lifecycle diagnostics in memory but never reaches production log files.
    /// </summary>
    public static bool IsSafe(string line)
    {
        try
        {
            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement root = document.RootElement;
            if (root.ValueKind != JsonValueKind.Object ||
                !root.EnumerateObject().Select(property => property.Name)
                    .ToHashSet(StringComparer.Ordinal).SetEquals(Fields))
                return false;
            foreach (string name in new[] { "timestamp", "level", "module", "event" })
            {
                if (!root.TryGetProperty(name, out JsonElement value) ||
                    value.ValueKind != JsonValueKind.String ||
                    value.GetString() is not { Length: > 0 and <= 160 })
                    return false;
            }
            return true;
        }
        catch (JsonException)
        {
            return false;
        }
    }
}
