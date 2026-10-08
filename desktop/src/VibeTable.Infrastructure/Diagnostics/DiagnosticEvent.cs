using System.Text.Json;

namespace VibeTable.Infrastructure.Diagnostics;

public static class DiagnosticEvent
{
    // Persisted diagnostics never carry renderer-controlled IDs: requestId
    // stays null in the closed schema and callers have no way to set it.
    public static string Failure(
        string module,
        string eventName,
        string errorCode,
        double? durationMs = null) =>
        JsonSerializer.Serialize(new
        {
            timestamp = DateTimeOffset.UtcNow,
            level = "error",
            module,
            @event = eventName,
            errorCode,
            requestId = (string?)null,
            operationId = (string?)null,
            workspaceId = (string?)null,
            sessionEpoch = (long?)null,
            jobId = (string?)null,
            durationMs,
        });
}
