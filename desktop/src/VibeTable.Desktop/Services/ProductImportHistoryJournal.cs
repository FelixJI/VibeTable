using System.Diagnostics;
using System.Net.Http;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Internal start request persisted in the Go authority before the worker
/// starts executing an import. It carries safe display facts only: no grant,
/// token, absolute path, or credential ever crosses this seam.
/// </summary>
internal sealed record ImportHistoryStart(
    string TaskId,
    string Collection,
    string SourceType,
    string SourceName,
    string IdempotencyKey,
    ulong SessionEpoch);

/// <summary>
/// One validated durable import history entry. Success is proven only by the
/// Go commit receipt; unknown outcomes keep null counts instead of fake zeros.
/// </summary>
internal sealed record ImportHistoryEntry(
    string TaskId,
    string Collection,
    string SourceType,
    string SourceName,
    string State,
    string CommitState,
    long? CreatedCount,
    long? UpdatedCount,
    string StartedAt,
    string? FinishedAt,
    ulong SessionEpoch,
    string? ErrorCode);

/// <summary>
/// Workspace-runtime-owned import history sink for one Go Sidecar generation.
/// The registry outlives invoker replacements, so terminal finish calls are
/// never bound to a short-lived gateway. Every HTTP call is fenced by the
/// captured generation snapshot; retired generations drop their pending
/// writes and the durable entries stay <c>interrupted</c> (pending
/// verification) instead of being faked.
/// </summary>
internal sealed class ProductImportHistoryJournal : IDisposable
{
    private const int MaxEntries = 200;
    private static readonly JsonSerializerOptions Wire = new(JsonSerializerDefaults.Web);
    private readonly ProductSidecarGenerationSnapshot _snapshot;
    private readonly ProductSidecarHttpGateway _gateway;
    private readonly object _gate = new();
    private readonly List<Task> _pendingFinishes = [];
    private bool _disposed;

    internal ProductImportHistoryJournal(
        ProductSidecarGenerationSnapshot snapshot,
        HttpMessageHandler? handler = null)
    {
        _snapshot = snapshot ?? throw new ArgumentNullException(nameof(snapshot));
        _gateway = new ProductSidecarHttpGateway(
            snapshot.Context,
            snapshot.Identity,
            snapshot.Registrations,
            handler);
    }

    internal ProductSidecarGenerationSnapshot Snapshot => _snapshot;

    internal ProductSidecarIdentity Identity => _snapshot.Identity;

    /// <summary>
    /// Persists one start. Callers treat any failure as a business execution
    /// block: without a durable start receipt the worker must not run.
    /// </summary>
    internal async Task StartAsync(ImportHistoryStart start, CancellationToken token)
    {
        ArgumentNullException.ThrowIfNull(start);
        ValidateStart(start);
        JsonElement reply = await PostFencedAsync(
            gateway => gateway.StartImportHistoryAsync(
                JsonSerializer.SerializeToElement(start, Wire),
                token),
            token).ConfigureAwait(false);
        try
        {
            ValidateReply(reply, start.TaskId);
        }
        catch (JsonException error)
        {
            throw new InvalidOperationException(
                "Import history start reply is invalid.", error);
        }
    }

    /// <summary>
    /// Fire-and-forget terminal settlement for failed/cancelled/aborted
    /// executions. Success is deliberately absent: only the Go commit
    /// transaction may record it. Failures are observable (trace) but never
    /// fabricate a business outcome.
    /// </summary>
    internal void Finish(string taskId, string state)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(taskId);
        if (state is not (ImportHistoryStates.Failed
            or ImportHistoryStates.Cancelled
            or ImportHistoryStates.Aborted))
        {
            throw new ArgumentOutOfRangeException(nameof(state));
        }
        Task? pending = null;
        lock (_gate)
        {
            if (_disposed) return;
        }
        // The action only starts the HTTP call; the generation authority
        // fences it exactly like every other Go-bound Host call.
        if (!_snapshot.TryUseCurrent(() =>
            {
                pending = PostFencedAsync(
                    gateway => gateway.FinishImportHistoryAsync(
                        JsonSerializer.SerializeToElement(
                            new { taskId, state },
                            Wire),
                        CancellationToken.None),
                    CancellationToken.None);
                return true;
            }))
        {
            Trace.TraceWarning(
                $"import_history.finish_dropped: taskId={taskId}; state={state}");
            return;
        }
        Track(pending!);
    }

    /// <summary>
    /// Reads the durable entries and overlays the live queued/running state
    /// of the current session's in-flight tasks. The lookup runs after the
    /// durable read so a worker exit during the wait cannot project a dead
    /// task as running. Go receipts are never rewritten by the overlay.
    /// </summary>
    internal async Task<JsonElement> ListAsync(
        Func<string, (string State, ulong SessionEpoch)?> liveLookup,
        CancellationToken token)
    {
        ArgumentNullException.ThrowIfNull(liveLookup);
        JsonElement reply = await PostFencedAsync(
            gateway => gateway.GetImportHistoryAsync(token),
            token).ConfigureAwait(false);
        List<ImportHistoryEntry> entries;
        try
        {
            entries = ParsePage(reply);
        }
        catch (JsonException error)
        {
            // A malformed Go page is a server-side contract failure, never a
            // renderer payload problem; it must surface as a product failure.
            throw new InvalidOperationException(
                "Import history page is invalid.", error);
        }
        for (int index = 0; index < entries.Count; index++)
        {
            ImportHistoryEntry entry = entries[index];
            if (entry.State != ImportHistoryStates.Interrupted
                || liveLookup(entry.TaskId) is not { } current
                || current.SessionEpoch != entry.SessionEpoch)
            {
                continue;
            }
            // A live projection must not show the pending-verification
            // diagnostic while the task is actually queued or running.
            entries[index] = entry with
            {
                State = current.State,
                ErrorCode = null,
            };
        }
        return Render(entries);
    }

    /// <summary>Awaits already-started finish settlements within a budget.</summary>
    internal async Task DrainAsync(TimeSpan budget)
    {
        Task[] pending;
        lock (_gate)
            pending = _pendingFinishes.ToArray();
        if (pending.Length == 0) return;
        try
        {
            // Tracked tasks never fault (failures are observed on arrival),
            // so only the budget can stop this await.
            await Task.WhenAll(pending).WaitAsync(budget).ConfigureAwait(false);
        }
        catch (TimeoutException)
        {
            Trace.TraceWarning(
                $"import_history.drain_incomplete: pending={pending.Length}");
        }
    }

    public void Dispose()
    {
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
        }
        // Disposing the gateway cancels in-flight calls through its lifetime
        // token; dropped finishes leave the durable entry interrupted.
        _gateway.Dispose();
        lock (_gate)
            _pendingFinishes.Clear();
    }

    private async Task<JsonElement> PostFencedAsync(
        Func<ProductSidecarHttpGateway, Task<JsonElement>> call,
        CancellationToken token)
    {
        Task<JsonElement>? pending = null;
        if (!_snapshot.TryUseCurrent(() =>
            {
                pending = call(_gateway);
                return true;
            }))
        {
            throw new BackendUnavailableException(
                "The import history Sidecar generation is retired.");
        }
        return await pending!.ConfigureAwait(false);
    }

    private void Track(Task pending)
    {
        Task observed = ObserveAsync(pending);
        lock (_gate)
        {
            _pendingFinishes.RemoveAll(task => task.IsCompleted);
            _pendingFinishes.Add(observed);
        }
    }

    private async Task ObserveAsync(Task pending)
    {
        try
        {
            await pending.ConfigureAwait(false);
        }
        catch (Exception error)
        {
            // A failed settlement is observable but must not fake an outcome
            // or disturb the report path that triggered it. Only the fixed
            // type is traced; message bodies may echo server-side paths.
            Trace.TraceWarning(
                $"import_history.finish_failed: {error.GetType().Name}");
        }
    }

    private static void ValidateStart(ImportHistoryStart start)
    {
        if (string.IsNullOrWhiteSpace(start.TaskId) || start.TaskId.Length > 128
            || string.IsNullOrWhiteSpace(start.Collection) || start.Collection.Length > 128
            || start.SourceType is not ("csv" or "xlsx")
            || string.IsNullOrWhiteSpace(start.SourceName) || start.SourceName.Length > 256
            || string.IsNullOrWhiteSpace(start.IdempotencyKey) || start.IdempotencyKey.Length > 256
            || start.SessionEpoch == 0)
        {
            throw new JsonException("Import history start is invalid.");
        }
    }

    private static void ValidateReply(JsonElement reply, string taskId)
    {
        if (reply.TryGetProperty("taskId", out JsonElement echoed)
            && (echoed.ValueKind != JsonValueKind.String
                || echoed.GetString() != taskId))
        {
            throw new JsonException("Import history reply does not match the task.");
        }
    }

    internal static JsonElement Render(IReadOnlyList<ImportHistoryEntry> entries)
        => JsonSerializer.SerializeToElement(new
        {
            items = entries.Select(ToWire).ToArray(),
        }, Wire);

    private static object ToWire(ImportHistoryEntry entry) => new
    {
        taskId = entry.TaskId,
        collection = entry.Collection,
        sourceType = entry.SourceType,
        sourceName = entry.SourceName,
        state = entry.State,
        commitState = entry.CommitState,
        createdCount = entry.CreatedCount,
        updatedCount = entry.UpdatedCount,
        startedAt = entry.StartedAt,
        finishedAt = entry.FinishedAt,
        sessionEpoch = entry.SessionEpoch,
        errorCode = entry.ErrorCode,
    };

    internal static List<ImportHistoryEntry> ParsePage(JsonElement root)
    {
        if (root.ValueKind != JsonValueKind.Object
            || !root.TryGetProperty("items", out JsonElement items)
            || items.ValueKind != JsonValueKind.Array
            || items.GetArrayLength() > MaxEntries)
        {
            throw new JsonException("Import history page is invalid.");
        }
        var entries = new List<ImportHistoryEntry>();
        foreach (JsonElement item in items.EnumerateArray())
            entries.Add(ParseEntry(item));
        return entries;
    }

    private static ImportHistoryEntry ParseEntry(JsonElement element)
    {
        if (element.ValueKind != JsonValueKind.Object
            || !HasExactProperties(
                element,
                "taskId",
                "collection",
                "sourceType",
                "sourceName",
                "state",
                "commitState",
                "createdCount",
                "updatedCount",
                "startedAt",
                "finishedAt",
                "sessionEpoch",
                "errorCode"))
        {
            throw new JsonException("Import history entry is invalid.");
        }
        string taskId = RequiredText(element, "taskId", 128);
        string collection = RequiredText(element, "collection", 128);
        string sourceType = RequiredText(element, "sourceType", 16);
        string sourceName = RequiredText(element, "sourceName", 256);
        string state = RequiredText(element, "state", 16);
        string commitState = RequiredText(element, "commitState", 16);
        string startedAt = RequiredText(element, "startedAt", 64);
        long? createdCount = OptionalCount(element, "createdCount");
        long? updatedCount = OptionalCount(element, "updatedCount");
        string? finishedAt = OptionalText(element, "finishedAt", 64);
        string? errorCode = OptionalText(element, "errorCode", 64);
        ulong sessionEpoch = RequiredEpoch(element, "sessionEpoch");
        if (sourceType is not ("csv" or "xlsx")
            || state is not (ImportHistoryStates.Interrupted
                or ImportHistoryStates.Succeeded
                or ImportHistoryStates.Failed
                or ImportHistoryStates.Cancelled
                or ImportHistoryStates.Aborted)
            || commitState is not (ImportHistoryStates.CommitUnknown
                or ImportHistoryStates.CommitCommitted))
        {
            throw new JsonException("Import history entry enum is invalid.");
        }
        // Success exists only with a Go commit receipt and real counts; an
        // unknown outcome never carries fabricated numbers or timestamps.
        bool committed = commitState == ImportHistoryStates.CommitCommitted;
        if ((committed
                && (state != ImportHistoryStates.Succeeded
                    || createdCount is null || updatedCount is null
                    || finishedAt is null))
            || (state == ImportHistoryStates.Succeeded
                && (!committed
                    || createdCount is null || updatedCount is null
                    || finishedAt is null))
            || (!committed && (createdCount is not null || updatedCount is not null)))
        {
            throw new JsonException("Import history entry outcome is inconsistent.");
        }
        if (state == ImportHistoryStates.Interrupted
            && (finishedAt is not null
                || errorCode is not null
                    and not ImportHistoryStates.InterruptedDiagnostic))
        {
            // The frozen Go Start diagnostic marks the pending-verification
            // outcome; interrupted entries carry nothing else.
            throw new JsonException("Import history entry is not terminal yet.");
        }
        if (state == ImportHistoryStates.Succeeded && errorCode is not null)
        {
            // Committed receipts never carry a diagnostic.
            throw new JsonException("Import history success is inconsistent.");
        }
        return new ImportHistoryEntry(
            taskId,
            collection,
            sourceType,
            sourceName,
            state,
            commitState,
            createdCount,
            updatedCount,
            startedAt,
            finishedAt,
            sessionEpoch,
            errorCode);
    }

    private static bool HasExactProperties(JsonElement element, params string[] expected)
    {
        int count = 0;
        foreach (JsonProperty property in element.EnumerateObject())
        {
            count++;
            if (Array.IndexOf(expected, property.Name) < 0)
                return false;
        }
        return count == expected.Length;
    }

    private static string RequiredText(JsonElement element, string name, int maximum)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.String
            && value.GetString() is { Length: > 0 } text
            && text.Length <= maximum
                ? text
                : throw new JsonException($"Import history '{name}' is invalid.");

    private static string? OptionalText(JsonElement element, string name, int maximum)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.Null
                ? null
                : RequiredText(element, name, maximum);

    private static long? OptionalCount(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.Null
                ? null
                : value.ValueKind == JsonValueKind.Number
                    && value.TryGetInt64(out long count) && count >= 0
                        ? count
                        : throw new JsonException(
                            $"Import history '{name}' is invalid.");

    private static ulong RequiredEpoch(JsonElement element, string name)
        => element.TryGetProperty(name, out JsonElement value)
            && value.ValueKind == JsonValueKind.Number
            && value.TryGetUInt64(out ulong epoch)
            && epoch >= 1
                ? epoch
                : throw new JsonException($"Import history '{name}' is invalid.");

    internal static class ImportHistoryStates
    {
        internal const string Interrupted = "interrupted";
        internal const string Succeeded = "succeeded";
        internal const string Failed = "failed";
        internal const string Cancelled = "cancelled";
        internal const string Aborted = "aborted";
        internal const string CommitUnknown = "unknown";
        internal const string CommitCommitted = "committed";
        internal const string InterruptedDiagnostic = "import.interrupted";
    }
}
