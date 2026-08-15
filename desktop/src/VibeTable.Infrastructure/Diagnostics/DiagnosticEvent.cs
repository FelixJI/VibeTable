using System.Text.Json;

namespace VibeTable.Infrastructure.Diagnostics;

public static class DiagnosticEvent
{
    public static string Failure(string module, string eventName, string errorCode) =>
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
            durationMs = (double?)null,
        });
}
