using System.Text.Json;
using System.Text.Json.Serialization;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Complete protection policy for one product endpoint. Admission, receipt
/// sanitization/injection, and authoritative response observation stay behind
/// this seam so a newly protected endpoint cannot silently discard a receipt.
/// </summary>
internal sealed class ProtectionSnapshotPolicy
{
    private readonly Func<
        JsonElement,
        FieldChangeProtectionPlanLedger,
        FieldChangeProtectionLedgerContext,
        bool> _requiresSnapshot;
    private readonly Func<
        JsonElement,
        ProtectionSnapshotReceipt?,
        JsonElement> _rewritePayload;
    private readonly Action<
        JsonElement,
        FieldChangeProtectionPlanLedger,
        FieldChangeProtectionLedgerContext> _observeSuccessfulResponse;

    private ProtectionSnapshotPolicy(
        Func<
            JsonElement,
            FieldChangeProtectionPlanLedger,
            FieldChangeProtectionLedgerContext,
            bool> requiresSnapshot,
        Func<JsonElement, ProtectionSnapshotReceipt?, JsonElement> rewritePayload,
        Action<
            JsonElement,
            FieldChangeProtectionPlanLedger,
            FieldChangeProtectionLedgerContext>? observeSuccessfulResponse = null)
    {
        _requiresSnapshot = requiresSnapshot
            ?? throw new ArgumentNullException(nameof(requiresSnapshot));
        _rewritePayload = rewritePayload
            ?? throw new ArgumentNullException(nameof(rewritePayload));
        _observeSuccessfulResponse = observeSuccessfulResponse
            ?? (static (_, _, _) => { });
    }

    internal static ProtectionSnapshotPolicy AlwaysSideEffectOnly { get; } =
        new(
            static (_, _, _) => true,
            static (payload, _) => payload);

    internal static ProtectionSnapshotPolicy FieldChangePlan { get; } =
        new(
            static (payload, _, _) =>
                FieldChangePayloadContract.IsPurgePlanRequest(payload),
            static (payload, receipt) => WithStringProperty(
                payload,
                "backupReceipt",
                receipt?.SnapshotId.ToString("D") ?? string.Empty),
            static (result, ledger, context) => ledger.RecordPlan(context, result));

    internal static ProtectionSnapshotPolicy FieldChangeApply { get; } =
        new(
            static (payload, ledger, context) =>
                ledger.RequiresProtectionForApply(context, payload),
            static (payload, receipt) => receipt is null
                ? WithoutProperty(payload, "protectionSnapshotId")
                : WithStringProperty(
                    payload,
                    "protectionSnapshotId",
                    receipt.SnapshotId.ToString("D")));

    internal bool RequiresSnapshot(
        JsonElement payload,
        FieldChangeProtectionPlanLedger ledger,
        FieldChangeProtectionLedgerContext context)
        => _requiresSnapshot(payload, ledger, context);

    internal JsonElement RewritePayload(
        JsonElement payload,
        ProtectionSnapshotReceipt? receipt)
        => _rewritePayload(payload, receipt);

    internal void ObserveSuccessfulResponse(
        JsonElement result,
        FieldChangeProtectionPlanLedger ledger,
        FieldChangeProtectionLedgerContext context)
        => _observeSuccessfulResponse(result, ledger, context);

    private static JsonElement WithStringProperty(
        JsonElement payload,
        string propertyName,
        string value)
    {
        Dictionary<string, JsonElement> properties = payload.EnumerateObject()
            .ToDictionary(
                property => property.Name,
                property => property.Value.Clone(),
                StringComparer.Ordinal);
        properties[propertyName] = JsonSerializer.SerializeToElement(value);
        return JsonSerializer.SerializeToElement(properties);
    }

    private static JsonElement WithoutProperty(
        JsonElement payload,
        string propertyName)
    {
        Dictionary<string, JsonElement> properties = payload.EnumerateObject()
            .Where(property => !string.Equals(
                property.Name,
                propertyName,
                StringComparison.Ordinal))
            .ToDictionary(
                property => property.Name,
                property => property.Value.Clone(),
                StringComparer.Ordinal);
        return JsonSerializer.SerializeToElement(properties);
    }
}

internal readonly record struct FieldChangeProtectionLedgerContext(
    long Generation,
    Guid? WorkspaceId,
    ulong SessionEpoch);

/// <summary>
/// Bounded authority cache populated only from strict gateway plan results.
/// Unknown, evicted, stale-gateway, and stale-workspace applies fail closed.
/// </summary>
internal sealed class FieldChangeProtectionPlanLedger
{
    private const int DefaultCapacity = 256;
    private readonly object _gate = new();
    private readonly int _capacity;
    private readonly Dictionary<PlanKey, LedgerEntry> _entries = [];
    private readonly LinkedList<PlanKey> _order = [];
    private long _generation;
    private Guid? _workspaceId;
    private ulong _sessionEpoch;

    internal FieldChangeProtectionPlanLedger(int capacity = DefaultCapacity)
    {
        if (capacity <= 0)
            throw new ArgumentOutOfRangeException(nameof(capacity));
        _capacity = capacity;
    }

    internal void ResetGateway()
    {
        lock (_gate)
        {
            ClearLocked();
            _generation = checked(_generation + 1);
        }
    }

    internal FieldChangeProtectionLedgerContext BeginRequest(
        WorkspaceWireScope? scope)
    {
        Guid? workspaceId = scope?.WorkspaceId;
        ulong sessionEpoch = scope?.SessionEpoch ?? 0;
        lock (_gate)
        {
            if (_workspaceId != workspaceId || _sessionEpoch != sessionEpoch)
            {
                ClearLocked();
                _workspaceId = workspaceId;
                _sessionEpoch = sessionEpoch;
                _generation = checked(_generation + 1);
            }
            return CurrentContextLocked();
        }
    }

    internal void RecordPlan(
        FieldChangeProtectionLedgerContext context,
        JsonElement result)
    {
        if (!FieldChangePayloadContract.TryReadPlanAuthority(
                result,
                out string planId,
                out string planHash,
                out bool requiresProtection))
        {
            InvalidateAuthority(context);
            return;
        }
        var key = new PlanKey(planId, planHash);
        lock (_gate)
        {
            if (context != CurrentContextLocked())
                return;
            if (_entries.Remove(key, out LedgerEntry? previous))
                _order.Remove(previous.Node);
            LinkedListNode<PlanKey> node = _order.AddLast(key);
            _entries[key] = new LedgerEntry(requiresProtection, node);
            while (_entries.Count > _capacity)
            {
                LinkedListNode<PlanKey> oldest = _order.First!;
                _order.RemoveFirst();
                _entries.Remove(oldest.Value);
            }
        }
    }

    internal bool RequiresProtectionForApply(
        FieldChangeProtectionLedgerContext context,
        JsonElement payload)
    {
        if (!FieldChangePayloadContract.TryReadApplyPlanKey(
                payload,
                out string planId,
                out string planHash))
            return true;
        lock (_gate)
        {
            if (context != CurrentContextLocked())
                return true;
            return !_entries.TryGetValue(
                    new PlanKey(planId, planHash),
                    out LedgerEntry? entry)
                || entry.RequiresProtection;
        }
    }

    private FieldChangeProtectionLedgerContext CurrentContextLocked()
        => new(_generation, _workspaceId, _sessionEpoch);

    private void InvalidateAuthority(
        FieldChangeProtectionLedgerContext context)
    {
        lock (_gate)
        {
            if (context != CurrentContextLocked())
                return;
            ClearLocked();
            _generation = checked(_generation + 1);
        }
    }

    private void ClearLocked()
    {
        _entries.Clear();
        _order.Clear();
    }

    private readonly record struct PlanKey(string PlanId, string PlanHash);

    private sealed record LedgerEntry(
        bool RequiresProtection,
        LinkedListNode<PlanKey> Node);
}

internal static class FieldChangePayloadContract
{
    private static readonly JsonSerializerOptions StrictJson =
        new(JsonSerializerDefaults.Web)
        {
            AllowDuplicateProperties = false,
            PropertyNameCaseInsensitive = false,
            UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
            RespectNullableAnnotations = true,
            RespectRequiredConstructorParameters = true,
        };

    internal static bool IsValidPlanRequest(JsonElement payload)
    {
        if (!TryDeserialize(payload, out FieldChangeIntentV2? parsedIntent)
            || parsedIntent is null)
            return false;
        FieldChangeIntentV2 intent = parsedIntent;
        return IsValidPlanIntent(intent);
    }

    internal static bool IsValidApplyRequest(JsonElement payload)
    {
        if (!TryDeserialize(payload, out FieldApplyRequestV2? parsedRequest)
            || parsedRequest is null)
            return false;
        FieldApplyRequestV2 request = parsedRequest;
        if (string.IsNullOrWhiteSpace(request.PlanId)
            || string.IsNullOrWhiteSpace(request.PlanHash)
            || string.IsNullOrWhiteSpace(request.OperationId)
            || string.IsNullOrWhiteSpace(request.Actor.Id)
            || string.IsNullOrWhiteSpace(request.Actor.Kind)
            || request.Confirmations.Any(string.IsNullOrWhiteSpace)
            || request.Confirmations.Distinct(StringComparer.Ordinal).Count()
                != request.Confirmations.Count)
            return false;
        return !payload.TryGetProperty(
                "protectionSnapshotId",
                out JsonElement protectionSnapshotId)
            || (protectionSnapshotId.ValueKind == JsonValueKind.String
                && !string.IsNullOrWhiteSpace(protectionSnapshotId.GetString()));
    }

    internal static bool IsPurgePlanRequest(JsonElement payload)
        => payload.TryGetProperty("action", out JsonElement action)
            && string.Equals(action.GetString(), "purge", StringComparison.Ordinal);

    internal static bool TryReadApplyPlanKey(
        JsonElement payload,
        out string planId,
        out string planHash)
    {
        planId = string.Empty;
        planHash = string.Empty;
        if (!payload.TryGetProperty("planId", out JsonElement planIdValue)
            || !payload.TryGetProperty("planHash", out JsonElement planHashValue)
            || planIdValue.ValueKind != JsonValueKind.String
            || planHashValue.ValueKind != JsonValueKind.String)
            return false;
        planId = planIdValue.GetString() ?? string.Empty;
        planHash = planHashValue.GetString() ?? string.Empty;
        return planId.Length > 0 && planHash.Length > 0;
    }

    internal static bool TryReadPlanAuthority(
        JsonElement result,
        out string planId,
        out string planHash,
        out bool requiresProtection)
    {
        planId = string.Empty;
        planHash = string.Empty;
        requiresProtection = true;
        if (!TryDeserialize(result, out FieldChangePlanV2? parsedPlan)
            || parsedPlan is null)
            return false;
        FieldChangePlanV2 plan = parsedPlan;
        if (!SchemaV2Contract.ValidateResult(plan, out _)
            || string.IsNullOrWhiteSpace(plan.PlanId)
            || string.IsNullOrWhiteSpace(plan.PlanHash)
            || !IsValidPlanIntent(plan.Intent)
            || plan.Confirmations.Any(string.IsNullOrWhiteSpace)
            || plan.Confirmations.Distinct(StringComparer.Ordinal).Count()
                != plan.Confirmations.Count)
            return false;
        planId = plan.PlanId;
        planHash = plan.PlanHash;
        requiresProtection = string.Equals(
                plan.Intent.Action,
                "purge",
                StringComparison.Ordinal)
            || plan.Confirmations.Contains(
                "backupReceipt",
                StringComparer.Ordinal);
        return true;
    }

    private static bool IsKnownAction(string action)
        => action is "create" or "update" or "retire" or "restore"
            or "purge" or "convert" or "backfill";

    private static bool IsValidPlanIntent(FieldChangeIntentV2 intent)
        => IsKnownAction(intent.Action)
            && !string.IsNullOrWhiteSpace(intent.TableId)
            && !string.IsNullOrWhiteSpace(intent.ExpectedSchemaRevision)
            && intent.ExpectedDataRevision is null or >= 0
            && !string.IsNullOrWhiteSpace(intent.Actor.Id)
            && !string.IsNullOrWhiteSpace(intent.Actor.Kind)
            && (intent.Action == "create"
                || !string.IsNullOrWhiteSpace(intent.FieldId))
            && (intent.RelationPair is null
                || (!string.IsNullOrWhiteSpace(
                        intent.RelationPair.ReciprocalDisplayName)
                    && intent.RelationPair.ReciprocalCardinality is "one" or "many"
                    && !string.IsNullOrWhiteSpace(
                        intent.RelationPair.SourceDisplayFieldId)));

    private static bool TryDeserialize<T>(
        JsonElement payload,
        out T? value)
        where T : class
    {
        value = null;
        try
        {
            value = payload.Deserialize<T>(StrictJson);
            return value is not null;
        }
        catch (JsonException)
        {
            return false;
        }
        catch (NotSupportedException)
        {
            return false;
        }
    }
}
