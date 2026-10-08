using System.Text.Json;

namespace VibeTable.Infrastructure.Diagnostics;

public static class DiagnosticEvent
{
    public static string Failure(
        string module,
        string eventName,
        string errorCode,
        string? requestId = null,
        double? durationMs = null) =>
        JsonSerializer.Serialize(new
        {
            timestamp = DateTimeOffset.UtcNow,
            level = "error",
            module,
            @event = eventName,
            errorCode,
            requestId,
            operationId = (string?)null,
            workspaceId = (string?)null,
            sessionEpoch = (long?)null,
            jobId = (string?)null,
            durationMs,
        });
}
