using System.Text.Json;

namespace VibeTable.Infrastructure.Diagnostics;

public static class DiagnosticEvent
{
    // Renderer-controlled IDs reach disk only when they match a known producer shape.
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
            requestId = IsOpaqueRequestId(requestId) ? requestId : null,
            operationId = (string?)null,
            workspaceId = (string?)null,
            sessionEpoch = (long?)null,
            jobId = (string?)null,
            durationMs,
        });

    private static bool IsOpaqueRequestId(string? requestId)
        => requestId is { Length: > 0 and <= 80 }
            && (Guid.TryParseExact(requestId, "D", out _)
                || Guid.TryParseExact(requestId, "N", out _)
                || IsE2eRequestId(requestId)
                || IsHostBridgeRequestId(requestId));

    private static bool IsE2eRequestId(string requestId)
        => requestId.Length == 40
            && requestId.StartsWith("e2e-", StringComparison.Ordinal)
            && Guid.TryParseExact(requestId[4..], "D", out _);

    private static bool IsHostBridgeRequestId(string requestId)
    {
        // Mirrors hostBridge.defaultGenerateRequestId, including its Math.random() fallback.
        if (requestId[0] != 'r') return false;
        string[] parts = requestId[1..].Split('-', 3);
        if (parts.Length != 3) return false;
        string timestamp = parts[0];
        string counter = parts[1];
        string random = parts[2];
        return timestamp.Length is >= 6 and <= 12 && IsLowercaseBase36(timestamp)
            && counter.Length is >= 1 and <= 10 && counter.All(char.IsAsciiDigit)
            && (Guid.TryParseExact(random, "D", out _)
                || (random.Length <= 8 && IsLowercaseBase36(random)));
    }

    private static bool IsLowercaseBase36(string value)
        => value.All(c => c is (>= '0' and <= '9') or (>= 'a' and <= 'z'));
}
