using System.Text.Json;

namespace VibeTable.Infrastructure.Diagnostics;

public static class DiagnosticLogLine
{
    private const string AuditDrainField = "auditDrain";

    private static readonly HashSet<string> Fields = new(StringComparer.Ordinal)
    {
        "timestamp", "level", "module", "event", "errorCode", "requestId",
        "operationId", "workspaceId", "sessionEpoch", "jobId", "durationMs",
    };

    // Mirrors sidecar/internal/diagnostics/logging.go auditDrainValue: closed
    // enums plus the operational measurements it can emit, and nothing else.
    private static readonly HashSet<string> AuditDrainSources = new(
        new[] { "business", "file" }, StringComparer.Ordinal);

    private static readonly HashSet<string> AuditDrainPhases = new(
        new[] { "pending", "append", "ack", "between_records", "completed" },
        StringComparer.Ordinal);

    private static readonly HashSet<string> AuditDrainNonNegativeInt64Fields =
        new(StringComparer.Ordinal)
        {
            "appended", "duplicates", "acknowledged", "pendingCalls",
            "appendCalls", "ackCalls", "pbReadWaitCount", "pbWriteWaitCount",
        };

    private static readonly HashSet<string> AuditDrainSignedInt64Fields = new(
        new[] { "pbReadInUseDelta", "pbWriteInUseDelta" }, StringComparer.Ordinal);

    private static readonly HashSet<string> AuditDrainNonNegativeDoubleFields =
        new(StringComparer.Ordinal)
        {
            "pendingMs", "appendMs", "appendMaxMs", "ackMs", "pbReadWaitMs",
            "pbWriteWaitMs",
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
            return IsSafe(document.RootElement);
        }
        catch (JsonException)
        {
            return false;
        }
    }

    /// <summary>
    /// Validates an already parsed line against the same closed schema. The
    /// optional <c>auditDrain</c> detail must match the sidecar diagnostics
    /// contract: closed source/phase enums, closed numeric measurement keys
    /// with their declared number kinds. Unknown or duplicate keys, illegal
    /// enums, and illegal numbers fail closed.
    /// </summary>
    public static bool IsSafe(JsonElement root)
    {
        if (root.ValueKind != JsonValueKind.Object ||
            !HasClosedFieldSet(root, out bool hasAuditDrain))
        {
            return false;
        }
        foreach (string name in new[] { "timestamp", "level", "module", "event" })
        {
            if (!root.TryGetProperty(name, out JsonElement value) ||
                value.ValueKind != JsonValueKind.String ||
                value.GetString() is not { Length: > 0 and <= 160 })
                return false;
        }
        return !hasAuditDrain || IsSafeAuditDrain(root.GetProperty(AuditDrainField));
    }

    private static bool HasClosedFieldSet(JsonElement root, out bool hasAuditDrain)
    {
        hasAuditDrain = false;
        HashSet<string> present = new(StringComparer.Ordinal);
        foreach (JsonProperty property in root.EnumerateObject())
        {
            if (!present.Add(property.Name))
                return false;
            if (string.Equals(property.Name, AuditDrainField, StringComparison.Ordinal))
                hasAuditDrain = true;
            else if (!Fields.Contains(property.Name))
                return false;
        }
        return present.IsSupersetOf(Fields);
    }

    private static bool IsSafeAuditDrain(JsonElement detail)
    {
        if (detail.ValueKind != JsonValueKind.Object)
            return false;
        bool hasSource = false;
        bool hasPhase = false;
        HashSet<string> present = new(StringComparer.Ordinal);
        foreach (JsonProperty property in detail.EnumerateObject())
        {
            if (!present.Add(property.Name))
                return false;
            JsonElement value = property.Value;
            switch (property.Name)
            {
                case "source":
                    hasSource = value.ValueKind == JsonValueKind.String &&
                        AuditDrainSources.Contains(value.GetString()!);
                    if (!hasSource)
                        return false;
                    break;
                case "phase":
                    hasPhase = value.ValueKind == JsonValueKind.String &&
                        AuditDrainPhases.Contains(value.GetString()!);
                    if (!hasPhase)
                        return false;
                    break;
                case string name when AuditDrainNonNegativeInt64Fields.Contains(name):
                    if (!IsInt64(value, nonNegative: true))
                        return false;
                    break;
                case string name when AuditDrainSignedInt64Fields.Contains(name):
                    if (!IsInt64(value, nonNegative: false))
                        return false;
                    break;
                case string name when AuditDrainNonNegativeDoubleFields.Contains(name):
                    if (value.ValueKind != JsonValueKind.Number ||
                        !value.TryGetDouble(out double number) ||
                        !double.IsFinite(number) ||
                        number < 0)
                        return false;
                    break;
                default:
                    return false;
            }
        }
        return hasSource && hasPhase;
    }

    private static bool IsInt64(JsonElement value, bool nonNegative) =>
        value.ValueKind == JsonValueKind.Number &&
        value.TryGetInt64(out long number) &&
        (!nonNegative || number >= 0);
}
