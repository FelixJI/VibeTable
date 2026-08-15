using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Desktop.Services;

public sealed record RuntimeDiagnosticsInput(
    string OperatingSystem,
    string ProgramVersion,
    string DotnetVersion,
    string PocketBaseVersion,
    long MemoryBytes,
    string DesktopState,
    string SidecarState,
    string BackendState,
    string DesktopLog,
    string SidecarLog,
    string BackendLog,
    JsonElement? WorkspaceSummary);

public sealed record RuntimeComponentHealth(
    [property: JsonPropertyName("component")] string Component,
    [property: JsonPropertyName("state")] string State);

public sealed record RuntimeJobStatus(
    [property: JsonPropertyName("queued")] long Queued,
    [property: JsonPropertyName("running")] long Running,
    [property: JsonPropertyName("succeeded")] long Succeeded,
    [property: JsonPropertyName("failed")] long Failed,
    [property: JsonPropertyName("cancelled")] long Cancelled);

public sealed record RuntimeIndexStatus(
    [property: JsonPropertyName("state")] string State,
    [property: JsonPropertyName("generation")] long Generation,
    [property: JsonPropertyName("processed")] long Processed,
    [property: JsonPropertyName("total")] long? Total,
    [property: JsonPropertyName("errorCode")] string? ErrorCode);

public sealed record RuntimeErrorCount(
    [property: JsonPropertyName("errorCode")] string ErrorCode,
    [property: JsonPropertyName("count")] int Count);

public sealed record RuntimeDiagnosticLogEntry(
    [property: JsonPropertyName("timestamp")] string Timestamp,
    [property: JsonPropertyName("level")] string Level,
    [property: JsonPropertyName("module")] string Module,
    [property: JsonPropertyName("event")] string Event,
    [property: JsonPropertyName("errorCode")] string? ErrorCode,
    [property: JsonPropertyName("requestId")] string? RequestId,
    [property: JsonPropertyName("operationId")] string? OperationId,
    [property: JsonPropertyName("workspaceId")] string? WorkspaceId,
    [property: JsonPropertyName("sessionEpoch")] long? SessionEpoch,
    [property: JsonPropertyName("jobId")] string? JobId,
    [property: JsonPropertyName("durationMs")] double? DurationMs);

public sealed record RuntimeDiagnosticsBundle(
    [property: JsonPropertyName("bundleVersion")] string BundleVersion,
    [property: JsonPropertyName("generatedAt")] string GeneratedAt,
    [property: JsonPropertyName("operatingSystem")] string OperatingSystem,
    [property: JsonPropertyName("programVersion")] string ProgramVersion,
    [property: JsonPropertyName("dotnetVersion")] string DotnetVersion,
    [property: JsonPropertyName("pocketBaseVersion")] string PocketBaseVersion,
    [property: JsonPropertyName("memoryBytes")] long MemoryBytes,
    [property: JsonPropertyName("components")] IReadOnlyList<RuntimeComponentHealth> Components,
    [property: JsonPropertyName("jobs")] RuntimeJobStatus Jobs,
    [property: JsonPropertyName("index")] RuntimeIndexStatus Index,
    [property: JsonPropertyName("pendingMutationRevision")] long PendingMutationRevision,
    [property: JsonPropertyName("recentErrorCounts")] IReadOnlyList<RuntimeErrorCount> RecentErrorCounts,
    [property: JsonPropertyName("logs")] IReadOnlyList<RuntimeDiagnosticLogEntry> Logs);

/// <summary>
/// Builds one content-free support bundle from process health and already
/// redacted fixed-schema log lines. Non-JSON or open-schema lines are dropped.
/// </summary>
public static class RuntimeDiagnosticsBundleService
{
    private static readonly HashSet<string> LogFields = new(StringComparer.Ordinal)
    {
        "timestamp", "level", "module", "event", "errorCode", "requestId",
        "operationId", "workspaceId", "sessionEpoch", "jobId", "durationMs",
    };

    public static RuntimeDiagnosticsBundle Build(RuntimeDiagnosticsInput input)
    {
        ArgumentNullException.ThrowIfNull(input);
        RuntimeDiagnosticLogEntry[] logs = ParseLogs(input.DesktopLog)
            .Concat(ParseLogs(input.SidecarLog))
            .Concat(ParseLogs(input.BackendLog))
            .OrderByDescending(item => item.Timestamp, StringComparer.Ordinal)
            .Take(100)
            .ToArray();
        RuntimeErrorCount[] counts = logs
            .Where(item => item.ErrorCode is not null)
            .GroupBy(item => item.ErrorCode!, StringComparer.Ordinal)
            .Select(group => new RuntimeErrorCount(group.Key, group.Count()))
            .OrderByDescending(item => item.Count)
            .ThenBy(item => item.ErrorCode, StringComparer.Ordinal)
            .ToArray();
        ParseWorkspaceSummary(
            input.WorkspaceSummary,
            out RuntimeJobStatus jobs,
            out RuntimeIndexStatus index,
            out long pendingMutationRevision);
        return new RuntimeDiagnosticsBundle(
            "1.0",
            DateTimeOffset.UtcNow.ToString("O"),
            input.OperatingSystem,
            input.ProgramVersion,
            input.DotnetVersion,
            input.PocketBaseVersion,
            input.MemoryBytes,
            [
                new RuntimeComponentHealth("desktop", input.DesktopState),
                new RuntimeComponentHealth("sidecar", input.SidecarState),
                new RuntimeComponentHealth("backend", input.BackendState),
            ],
            jobs,
            index,
            pendingMutationRevision,
            counts,
            logs);
    }

    private static IEnumerable<RuntimeDiagnosticLogEntry> ParseLogs(string raw)
    {
        foreach (string line in (raw ?? string.Empty).Split('\n'))
        {
            JsonDocument document;
            try { document = JsonDocument.Parse(line); }
            catch (JsonException) { continue; }
            using (document)
            {
                JsonElement root = document.RootElement;
                if (root.ValueKind != JsonValueKind.Object ||
                    root.EnumerateObject().Select(property => property.Name)
                        .ToHashSet(StringComparer.Ordinal)
                        .SetEquals(LogFields) == false ||
                    !TryBoundedString(root, "timestamp", out string? timestamp) ||
                    !TryBoundedString(root, "level", out string? level) ||
                    !TryBoundedString(root, "module", out string? module) ||
                    !TryBoundedString(root, "event", out string? eventName))
                    continue;
                yield return new RuntimeDiagnosticLogEntry(
                    timestamp!, level!, module!, eventName!,
                    NullableString(root, "errorCode"),
                    NullableString(root, "requestId"),
                    NullableString(root, "operationId"),
                    NullableString(root, "workspaceId"),
                    NullableInt64(root, "sessionEpoch"),
                    NullableString(root, "jobId"),
                    NullableDouble(root, "durationMs"));
            }
        }
    }

    private static void ParseWorkspaceSummary(
        JsonElement? summary,
        out RuntimeJobStatus jobs,
        out RuntimeIndexStatus index,
        out long pendingMutationRevision)
    {
        jobs = new RuntimeJobStatus(0, 0, 0, 0, 0);
        index = new RuntimeIndexStatus("unavailable", 0, 0, null, null);
        pendingMutationRevision = 0;
        if (summary is not JsonElement root || root.ValueKind != JsonValueKind.Object)
            return;
        if (root.TryGetProperty("jobs", out JsonElement jobRoot) &&
            TryInt64(jobRoot, "queued", out long queued) &&
            TryInt64(jobRoot, "running", out long running) &&
            TryInt64(jobRoot, "succeeded", out long succeeded) &&
            TryInt64(jobRoot, "failed", out long failed) &&
            TryInt64(jobRoot, "cancelled", out long cancelled))
            jobs = new RuntimeJobStatus(queued, running, succeeded, failed, cancelled);
        if (root.TryGetProperty("index", out JsonElement indexRoot) &&
            TryBoundedString(indexRoot, "state", out string? state) &&
            TryInt64(indexRoot, "generation", out long generation) &&
            TryInt64(indexRoot, "processed", out long processed))
            index = new RuntimeIndexStatus(
                state!, generation, processed,
                NullableInt64(indexRoot, "total"),
                NullableString(indexRoot, "errorCode"));
        if (root.TryGetProperty("recovery", out JsonElement recovery) &&
            TryInt64(recovery, "pendingMutationRevision", out long pending))
            pendingMutationRevision = pending;
    }

    private static bool TryBoundedString(
        JsonElement root,
        string name,
        out string? value)
    {
        value = null;
        if (!root.TryGetProperty(name, out JsonElement element) ||
            element.ValueKind != JsonValueKind.String)
            return false;
        value = element.GetString();
        return value is { Length: > 0 and <= 160 };
    }

    private static string? NullableString(JsonElement root, string name)
    {
        if (!root.TryGetProperty(name, out JsonElement element) ||
            element.ValueKind == JsonValueKind.Null)
            return null;
        return element.ValueKind == JsonValueKind.String &&
            element.GetString() is { Length: > 0 and <= 160 } value
            ? value : null;
    }

    private static long? NullableInt64(JsonElement root, string name) =>
        root.TryGetProperty(name, out JsonElement element) &&
        element.ValueKind == JsonValueKind.Number &&
        element.TryGetInt64(out long value) ? value : null;

    private static double? NullableDouble(JsonElement root, string name) =>
        root.TryGetProperty(name, out JsonElement element) &&
        element.ValueKind == JsonValueKind.Number &&
        element.TryGetDouble(out double value) && double.IsFinite(value)
            ? value : null;

    private static bool TryInt64(JsonElement root, string name, out long value)
    {
        value = 0;
        return root.ValueKind == JsonValueKind.Object &&
            root.TryGetProperty(name, out JsonElement element) &&
            element.ValueKind == JsonValueKind.Number &&
            element.TryGetInt64(out value) && value >= 0;
    }
}
