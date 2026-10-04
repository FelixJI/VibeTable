using System.IO;
using System.Net.Http;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using TaskStatus = VibeTable.Contracts.TaskStatus;

namespace VibeTable.Desktop.Services;

internal sealed partial class HostDataIoTaskRegistry
{
    private readonly Dictionary<string, SourceSession> _sourceSessions = new(StringComparer.Ordinal);
    private readonly Dictionary<string, SourceTask> _sourceTasks = new(StringComparer.Ordinal);

    internal JsonElement[] OverlaySourceImportHistory(ProductSidecarGenerationSnapshot snapshot,
        IReadOnlyList<HostSourceImportResult> durable)
    {
        var entries = durable.Select(result => result.Wire).ToList();
        lock (_gate)
        {
            if (_disposed) return entries.ToArray();
            foreach (SourceTask task in _sourceTasks.Values)
            {
                // Recheck liveness after the Go read. Retired generations and
                // terminal Host tasks must never revive an interrupted job.
                if (task.Status.State is not (TaskStates.Queued or TaskStates.Running)
                    || task.Session is not { Retired: false } session
                    || session.Lease.CancellationToken.IsCancellationRequested
                    || !session.Snapshot.Matches(snapshot)) continue;
                int index = -1;
                for (int candidate = 0; candidate < durable.Count; candidate++)
                {
                    if (durable[candidate].JobId == task.Status.TaskId
                        && durable[candidate].SessionEpoch == snapshot.Identity.SessionEpoch)
                    { index = candidate; break; }
                }
                if (index >= 0)
                {
                    HostSourceImportResult receipt = durable[index];
                    bool finished = receipt.Wire.TryGetProperty("finishedAt", out JsonElement finishedAt)
                        && finishedAt.ValueKind == JsonValueKind.String && !string.IsNullOrWhiteSpace(finishedAt.GetString());
                    if (receipt.State is "succeeded" or "failed" or "cancelled" or "unknown" || finished) continue;
                    // Go owns counts, targets and receipts, including a pending
                    // batch's unknown subset. Only Host liveness is projected.
                    entries[index] = SourceHistoryState(receipt.Wire, task.Status.State,
                        task.Submitted ? receipt.Wire.GetProperty("stage").GetString()! : task.Phase);
                }
                else
                {
                    if (task.Submitted)
                        throw new BackendUnavailableException("Source import submission receipt is not available yet.");
                    entries.Add(SourceHistoryState(task.Preparation, task.Status.State, task.Phase));
                }
            }
        }
        return entries.ToArray();
    }

    private static JsonElement SourceHistoryState(JsonElement source, string state, string stage)
    {
        var values = source.EnumerateObject().ToDictionary(property => property.Name, property => property.Value);
        values["state"] = JsonSerializer.SerializeToElement(state);
        values["stage"] = JsonSerializer.SerializeToElement(stage);
        return JsonSerializer.SerializeToElement(values);
    }

    private static JsonElement SourcePreparationHistory(HostSourceImportPreview preview, string taskId, ulong epoch)
    {
        int total = 0;
        foreach (JsonElement table in preview.Plan.GetProperty("tables").EnumerateArray())
        {
            int count = table.GetProperty("recordCount").GetInt32();
            if (count < 0) throw new JsonException("Source plan record count is invalid.");
            total = checked(total + count);
        }
        JsonElement PlanArray(string name)
        {
            JsonElement value = preview.Plan.GetProperty(name);
            if (value.ValueKind == JsonValueKind.Array) return value;
            if (value.ValueKind == JsonValueKind.Null) return JsonSerializer.SerializeToElement(Array.Empty<object>());
            throw new JsonException("Source plan summary is invalid.");
        }
        return JsonSerializer.SerializeToElement(new
        {
            contract = HostSourceImportResult.ContractName,
            jobId = taskId,
            provider = preview.Plan.GetProperty("provider"),
            containerId = preview.Plan.GetProperty("containerId"),
            sourceName = preview.Plan.GetProperty("displayName"),
            state = TaskStates.Queued,
            stage = "preparing",
            created = 0,
            total,
            notSubmitted = total,
            unknownRecords = 0,
            targets = Array.Empty<object>(),
            batches = Array.Empty<object>(),
            diagnostics = PlanArray("diagnostics"),
            fields = PlanArray("fields").EnumerateArray().Select(field => new
            {
                source = field.GetProperty("source"),
                kind = field.GetProperty("kind"),
                policy = field.GetProperty("policy"),
                definition = "",
            }).ToArray(),
            startedAt = DateTimeOffset.UtcNow.ToString("O"),
            sessionEpoch = epoch,
            readWindow = preview.Plan.GetProperty("readWindow"),
        });
    }

    internal string RegisterSourceImportProvider(ProductSidecarGenerationSnapshot snapshot,
        IWorkspaceHostEpochLeaseSource leases, IHostSourceImportProvider provider,
        HttpMessageHandler? handler = null)
    {
        ArgumentNullException.ThrowIfNull(provider);
        if (!snapshot.TryUseCurrent(() => true)
            || !leases.TryCaptureHost(Guid.Parse(snapshot.Identity.WorkspaceId),
                snapshot.Identity.SessionEpoch, Guid.NewGuid(), out WorkspaceRequestEpochLease? lease)
            || lease is null)
            throw new BackendUnavailableException("Source provider session is no longer current.");
        var session = new SourceSession(snapshot, leases, lease, provider, handler);
        string id = "source-" + Guid.NewGuid().ToString("N");
        lock (_gate)
        {
            if (_disposed || _sourceSessions.Count >= 8)
            {
                session.Dispose();
                throw new InvalidOperationException("Source provider session capacity is unavailable.");
            }
            _sourceSessions.Add(id, session);
        }
        session.OnRetired = () => RetireSourceSession(session);
        session.Subscribe();
        session.SetExpiry(DateTimeOffset.UtcNow.AddMinutes(15));
        if (!session.IsCurrent()) RetireSourceSession(session);
        return id;
    }

    internal async Task<HostSourceImportPreview> PrepareSourceImportAsync(string sessionId,
        HostSourceImportOptions options, CancellationToken token)
    {
        SourceSession session;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            session = _sourceSessions.GetValueOrDefault(sessionId)
                ?? throw new InvalidOperationException("Source provider is not registered.");
            if (session.Consumed || session.Preparing || session.Retired)
                throw new InvalidOperationException("Source provider session cannot prepare a plan.");
            session.Preparing = true;
        }
        using var call = CancellationTokenSource.CreateLinkedTokenSource(token, session.Lifetime.Token);
        try
        {
            HostSourceImportSnapshot source = await session.Provider.ReadAsync(call.Token).ConfigureAwait(false);
            EnsureSourceCurrent(session, call.Token);
            JsonElement reply = await StartSourceCurrent(session, () => session.Gateway.PreviewSourceImportAsync(new
            {
                contract = HostSourceImportResult.ContractName,
                sessionEpoch = session.Snapshot.Identity.SessionEpoch,
                snapshot = source,
                options,
            }, call.Token)).ConfigureAwait(false);
            EnsureSourceCurrent(session, call.Token);
            if (reply.GetProperty("contract").GetString() != HostSourceImportResult.ContractName)
                throw new JsonException("Invalid source plan contract.");
            string planToken = reply.GetProperty("token").GetString()
                ?? throw new JsonException("Source plan token missing.");
            DateTimeOffset expires = DateTimeOffset.FromUnixTimeMilliseconds(
                checked((long)(reply.GetProperty("expiresAt").GetDouble() * 1000)));
            JsonElement plan = reply.GetProperty("plan").Clone();
            if (string.IsNullOrWhiteSpace(planToken) || expires <= DateTimeOffset.UtcNow
                || plan.GetProperty("provider").GetString() != source.Provider
                || plan.GetProperty("containerId").GetString() != source.ContainerId)
                throw new JsonException("Invalid source plan binding.");
            var preview = new HostSourceImportPreview(sessionId, planToken, expires, plan);
            lock (_gate)
            {
                if (session.Retired || _disposed) throw new OperationCanceledException(call.Token);
                session.Preview = preview;
                session.SetExpiry(expires);
            }
            return preview;
        }
        finally
        {
            bool retired;
            lock (_gate) { session.Preparing = false; retired = session.Retired; }
            if (retired) session.Dispose();
        }
    }

    internal JsonElement StartSourceImport(ProductSidecarGenerationSnapshot snapshot, JsonElement parameters)
    {
        if (parameters.ValueKind != JsonValueKind.Object
            || !parameters.EnumerateObject().Select(p => p.Name).Order(StringComparer.Ordinal)
                .SequenceEqual(new[] { "confirmed", "providerSessionId", "token" }, StringComparer.Ordinal)
            || parameters.GetProperty("confirmed").ValueKind != JsonValueKind.True)
            throw new JsonException("Source import requires only providerSessionId, token and explicit confirmation.");
        string id = parameters.GetProperty("providerSessionId").GetString() ?? "";
        string planToken = parameters.GetProperty("token").GetString() ?? "";
        SourceTask task;
        JsonElement initial;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            SourceSession session = _sourceSessions.GetValueOrDefault(id)
                ?? throw new InvalidOperationException("Source provider is not registered.");
            if (!session.Snapshot.Matches(snapshot) || session.Retired || session.Consumed || session.Preparing
                || session.Preview is not { } preview || preview.Token != planToken
                || preview.ExpiresAt <= DateTimeOffset.UtcNow
                || preview.Plan.GetProperty("canApply").ValueKind != JsonValueKind.True)
                throw new InvalidOperationException("Source import plan is not available for confirmation.");
            string taskId = "task-" + Guid.NewGuid().ToString("N");
            JsonElement preparation = SourcePreparationHistory(preview, taskId, snapshot.Identity.SessionEpoch);
            session.Consumed = true;
            task = new SourceTask(session, new TaskStatus(taskId, "data.sourceImport", TaskStates.Queued,
                new TaskProgress(0, preparation.GetProperty("total").GetInt32(), "等待迁移"), null, null), preparation);
            _sourceTasks.Add(taskId, task);
            foreach (string old in _sourceTasks.Where(p => Terminal(p.Value.Status.State))
                .Take(Math.Max(0, _sourceTasks.Count - MaxTasks)).Select(p => p.Key).ToArray())
                _sourceTasks.Remove(old);
            initial = JsonSerializer.SerializeToElement(task.Status, Wire);
        }
        // The workspace registry, not the creating invoker or Python client,
        // owns this run and its terminal receipt.
        task.Run = Task.Run(() => RunSourceImportAsync(task));
        return initial;
    }

    private async Task RunSourceImportAsync(SourceTask task)
    {
        SourceSession session = task.Session!;
        bool submitted = false;
        bool receiptConfirmed = false;
        try
        {
            CancellationToken token = session.Lifetime.Token;
            EnsureSourceCurrent(session, CancellationToken.None);
            UpdateSourceTask(task, TaskStates.Running, "重新核对来源版本");
            HostSourceImportPreview preview = session.Preview!;
            // Admission records the confirmed task before any file work. A
            // queued cancellation still needs a durable cancelled receipt.
            using (var admission = CancellationTokenSource.CreateLinkedTokenSource(session.Lease.CancellationToken))
            {
                admission.CancelAfter(TimeSpan.FromSeconds(10));
                JsonElement started = await StartSourceCurrent(session, () => session.Gateway.StartSourceImportAsync(
                    preview.Token, task.Status.TaskId, session.Snapshot.Identity.SessionEpoch, admission.Token)).ConfigureAwait(false);
                HostSourceImportResult result = ValidateSourceResult(task, started);
                if (result.State != "interrupted" || started.GetProperty("stage").GetString() != "preparing"
                    || result.Created != 0 || started.GetProperty("unknownRecords").GetInt32() != 0
                    || started.GetProperty("notSubmitted").GetInt32() != result.Total)
                    throw new JsonException("Invalid source import admission receipt.");
            }
            EnsureSourceCurrent(session, token);
            HostSourceImportAttachment[] attachments = preview.Plan.GetProperty("attachments")
                .Deserialize<HostSourceImportAttachment[]>(Wire) ?? [];
            if (attachments.Length > 500 || attachments.Any(a => a.Size < 0 || a.Size > 32 * 1024 * 1024)
                || attachments.Sum(a => a.Size) > 128L * 1024 * 1024)
                throw new InvalidOperationException("Source attachments exceed the migration capacity.");
            lock (_gate) task.Phase = "attachments";
            foreach (HostSourceImportAttachment attachment in attachments)
            {
                EnsureSourceCurrent(session, token);
                await using Stream stream = await session.Provider.OpenAttachmentAsync(attachment, token).ConfigureAwait(false);
                byte[] bytes = await ReadSourceAttachmentAsync(stream, attachment.Size, token).ConfigureAwait(false);
                await StartSourceCurrent(session, () => session.Gateway.UploadSourceImportAsync(new
                {
                    token = preview.Token,
                    sessionEpoch = session.Snapshot.Identity.SessionEpoch,
                    key = new
                    {
                        provider = preview.Plan.GetProperty("provider").GetString(),
                        containerId = preview.Plan.GetProperty("containerId").GetString(),
                        tableId = attachment.TableId,
                        fieldId = attachment.FieldId,
                        recordId = attachment.RecordId,
                        objectId = attachment.Id,
                    },
                }, bytes, token)).ConfigureAwait(false);
            }
            // Re-observe after downloads, immediately before consuming the Go
            // plan. The renderer has no observation input on this path.
            lock (_gate) task.Phase = "preparing";
            HostSourceImportObservation observation = await session.Provider.ObserveAsync(token).ConfigureAwait(false);
            EnsureSourceCurrent(session, token);
            if (observation.Version != preview.Plan.GetProperty("version").GetString()
                || preview.Plan.GetProperty("tables").EnumerateArray().Any(table =>
                    !observation.TableVersions.TryGetValue(table.GetProperty("sourceId").GetString()!, out string? version)
                    || version != table.GetProperty("version").GetString()))
                throw new InvalidOperationException("来源版本或结构已变化，请重新预检。");
            UpdateSourceTask(task, TaskStates.Running, "正在迁移；已提交批次不会自动回滚");
            JsonElement wire = await StartSourceCurrent(session, () =>
            {
                token.ThrowIfCancellationRequested();
                lock (_gate) task.Submitted = true;
                submitted = true;
                return session.Gateway.ExecuteSourceImportAsync(new
                {
                    contract = HostSourceImportResult.ContractName,
                    token = preview.Token,
                    jobId = task.Status.TaskId,
                    sessionEpoch = session.Snapshot.Identity.SessionEpoch,
                    confirmed = true,
                    observation,
                }, token);
            }).ConfigureAwait(false);
            CompleteSourceResult(task, wire);
            receiptConfirmed = true;
        }
        catch (Exception)
        {
            if (submitted && session.IsCurrent())
            {
                // A lost ACK is reconciled using the same durable job ID. No
                // second execute or replacement mutation key is permitted.
                try
                {
                    using var reconcile = new CancellationTokenSource(TimeSpan.FromSeconds(10));
                    JsonElement wire = await StartSourceCurrent(session, () => session.Gateway
                        .ReadSourceImportResultAsync(task.Status.TaskId, reconcile.Token)).ConfigureAwait(false);
                    HostSourceImportResult receipt = ValidateSourceResult(task, wire);
                    // A rejected execute can leave its durable admission in
                    // preparing. Only Go's unclaimed-token finish may settle it.
                    if (receipt.State != "interrupted" || wire.GetProperty("stage").GetString() != "preparing")
                    {
                        CompleteSourceResult(task, wire);
                        receiptConfirmed = true;
                        return;
                    }
                }
                catch (Exception) { /* Keep the outcome explicitly unconfirmed. */ }
            }
        }
        finally
        {
            if (!receiptConfirmed && session.IsCurrent() && session.Preview is { } unused)
            {
                try
                {
                    using var settlement = CancellationTokenSource.CreateLinkedTokenSource(session.Lease.CancellationToken);
                    settlement.CancelAfter(TimeSpan.FromSeconds(5));
                    string state = session.Lifetime.IsCancellationRequested ? "cancelled" : "failed";
                    JsonElement finished = await StartSourceCurrent(session, () => session.Gateway.FinishSourceImportAsync(
                        unused.Token, task.Status.TaskId, session.Snapshot.Identity.SessionEpoch, state, settlement.Token))
                        .ConfigureAwait(false);
                    HostSourceImportResult result = ValidateSourceResult(task, finished);
                    if (result.State != state || result.Created != 0 || finished.GetProperty("unknownRecords").GetInt32() != 0
                        || finished.GetProperty("notSubmitted").GetInt32() != result.Total
                        || !finished.TryGetProperty("finishedAt", out JsonElement finishedAt)
                        || finishedAt.ValueKind != JsonValueKind.String || string.IsNullOrWhiteSpace(finishedAt.GetString()))
                        throw new JsonException("Invalid source import preparation settlement.");
                    CompleteSourceResult(task, finished);
                    receiptConfirmed = true;
                }
                catch (Exception error)
                {
                    // Claimed or unreachable plans remain under Go authority;
                    // never fabricate a zero-count terminal result.
                    System.Diagnostics.Trace.TraceWarning(
                        $"Source import preparation finish was not acknowledged: {error.GetType().Name}");
                }
                try
                {
                    // Cancellation of this task must not cancel its cleanup.
                    // The original epoch still fences the request. Go atomically
                    // rejects claimed tokens, including an ambiguous execute
                    // whose ACK and result read were both lost.
                    using var cleanup = CancellationTokenSource.CreateLinkedTokenSource(session.Lease.CancellationToken);
                    cleanup.CancelAfter(TimeSpan.FromSeconds(5));
                    await StartSourceCurrent(session, async () =>
                    {
                        await session.Gateway.DiscardSourceImportAsync(unused.Token,
                            session.Snapshot.Identity.SessionEpoch, cleanup.Token).ConfigureAwait(false);
                        return true;
                    }).ConfigureAwait(false);
                }
                catch (Exception error)
                {
                    // Keep the original business outcome. A claimed token or
                    // retired/unreachable Go must not lead to another execute.
                    System.Diagnostics.Trace.TraceWarning(
                        $"Source import staging discard was not acknowledged: {error.GetType().Name}");
                }
            }
            if (!receiptConfirmed)
                UpdateSourceTask(task, TaskStates.Aborted,
                    "迁移结果待核实；请核对 Go 持久结果，不能自动重试。", error: true);
            lock (_gate)
            {
                string? id = _sourceSessions.FirstOrDefault(p => ReferenceEquals(p.Value, session)).Key;
                if (id is not null) _sourceSessions.Remove(id);
                // Terminal task history retains only its receipt, never the
                // provider credentials or the potentially large source plan.
                task.Session = null;
            }
            session.Dispose();
        }
    }

    private void CompleteSourceResult(SourceTask task, JsonElement wire)
    {
        HostSourceImportResult result = ValidateSourceResult(task, wire);
        string state = result.State switch
        {
            "succeeded" => TaskStates.Succeeded,
            "cancelled" => TaskStates.Cancelled,
            "failed" => TaskStates.Failed,
            _ => TaskStates.Aborted,
        };
        UpdateSourceTask(task, state, result.State == "succeeded" ? "迁移完成" : "迁移未完整完成，请核对已创建目标。",
            result, state != TaskStates.Succeeded);
    }

    private static HostSourceImportResult ValidateSourceResult(SourceTask task, JsonElement wire)
    {
        SourceSession session = task.Session!;
        EnsureSourceCurrent(session, CancellationToken.None);
        HostSourceImportResult result = HostSourceImportResult.Parse(wire);
        if (result.JobId != task.Status.TaskId || result.SessionEpoch != session.Snapshot.Identity.SessionEpoch)
            throw new JsonException("Source import receipt belongs to another job or epoch.");
        return result;
    }

    private void UpdateSourceTask(SourceTask task, string state, string message,
        HostSourceImportResult? result = null, bool error = false)
    {
        JsonElement changed;
        lock (_gate)
        {
            if (Terminal(task.Status.State)) return;
            task.Status = task.Status with
            {
                State = state,
                Progress = new TaskProgress(result?.Created ?? task.Status.Progress.Done,
                    result?.Total ?? task.Status.Progress.Total, message),
                Result = result?.Wire,
                Error = error ? message : null,
            };
            changed = EventLocked(task.Status);
        }
        Publish([changed]);
    }

    private bool TryCancelSourceImport(string taskId, out JsonElement snapshot)
    {
        SourceTask? task;
        lock (_gate) _sourceTasks.TryGetValue(taskId, out task);
        if (task is null) { snapshot = default; return false; }
        if (!Terminal(task.Status.State)) task.Session?.Cancel();
        snapshot = Status(taskId);
        return true;
    }

    private void RetireSourceSession(SourceSession session)
    {
        SourceTask? task;
        lock (_gate)
        {
            if (session.Retired) return;
            session.Retired = true;
            task = _sourceTasks.Values.FirstOrDefault(t => ReferenceEquals(t.Session, session));
            foreach (string id in _sourceSessions.Where(p => ReferenceEquals(p.Value, session)).Select(p => p.Key).ToArray())
                _sourceSessions.Remove(id);
        }
        if (task is not null)
            UpdateSourceTask(task, TaskStates.Aborted, "Go 会话已退休；迁移结果待核实，请重新预检。", error: true);
        session.Cancel();
        if (task is null && !session.Preparing) session.Dispose();
    }

    private void DisposeSourceImports()
    {
        SourceSession[] sessions;
        lock (_gate) sessions = _sourceSessions.Values.ToArray();
        foreach (SourceSession session in sessions) RetireSourceSession(session);
    }

    private static void EnsureSourceCurrent(SourceSession session, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        if (!session.IsCurrent()) throw new BackendUnavailableException("Source import Go session retired.");
    }

    private static Task<T> StartSourceCurrent<T>(SourceSession session, Func<Task<T>> action)
    {
        Task<T>? pending = null;
        if (!session.Leases.IsCurrent(session.Lease)
            || !session.Snapshot.TryUseCurrent(() => { pending = action(); return true; }))
            throw new BackendUnavailableException("Source import Go session retired.");
        return pending!;
    }

    private static async Task<byte[]> ReadSourceAttachmentAsync(Stream stream, long expected, CancellationToken token)
    {
        using var content = new MemoryStream();
        byte[] buffer = new byte[16 * 1024];
        int count;
        while ((count = await stream.ReadAsync(buffer, token).ConfigureAwait(false)) != 0)
        {
            if (content.Length + count > expected || content.Length + count > 32 * 1024 * 1024)
                throw new IOException("Source attachment size changed.");
            content.Write(buffer, 0, count);
        }
        if (content.Length != expected) throw new IOException("Source attachment is incomplete.");
        return content.ToArray();
    }

    private sealed class SourceTask(SourceSession session, TaskStatus status, JsonElement preparation)
    {
        internal SourceSession? Session { get; set; } = session;
        internal TaskStatus Status { get; set; } = status;
        internal JsonElement Preparation { get; } = preparation;
        internal bool Submitted { get; set; }
        internal string Phase { get; set; } = "preparing";
        internal Task? Run { get; set; }
    }

    private sealed class SourceSession(ProductSidecarGenerationSnapshot snapshot,
        IWorkspaceHostEpochLeaseSource leases, WorkspaceRequestEpochLease lease,
        IHostSourceImportProvider provider, HttpMessageHandler? handler) : IDisposable
    {
        internal ProductSidecarGenerationSnapshot Snapshot { get; } = snapshot;
        internal IWorkspaceHostEpochLeaseSource Leases { get; } = leases;
        internal WorkspaceRequestEpochLease Lease { get; } = lease;
        internal IHostSourceImportProvider Provider { get; } = provider;
        internal ProductSidecarHttpGateway Gateway { get; } = new(snapshot.Context, snapshot.Identity, snapshot.Registrations, handler);
        internal CancellationTokenSource Lifetime { get; } = new();
        internal HostSourceImportPreview? Preview { get; set; }
        internal bool Preparing { get; set; }
        internal bool Consumed { get; set; }
        internal bool Retired { get; set; }
        internal Action? OnRetired { get; set; }
        private CancellationTokenRegistration _epoch;
        private Timer? _expiry;
        private int _disposed;

        internal bool IsCurrent() => !Retired && Leases.IsCurrent(Lease) && Snapshot.TryUseCurrent(() => true);
        internal void Subscribe()
        {
            if (Snapshot.RuntimeAuthority is ProductionWorkspaceRuntime runtime)
                runtime.Sidecar.StatusChanged += SidecarChanged;
            if (Snapshot.RuntimeAuthority is IProductSidecarGenerationAuthority authority)
                authority.CurrentChanged += CheckCurrent;
            _epoch = Lease.CancellationToken.Register(() => OnRetired?.Invoke());
        }
        private void SidecarChanged(object? sender, PocketBaseStatus status) => CheckCurrent();
        private void CheckCurrent() { if (!IsCurrent()) OnRetired?.Invoke(); }
        internal void SetExpiry(DateTimeOffset expires)
        {
            if (Volatile.Read(ref _disposed) != 0) return;
            _expiry?.Dispose();
            _expiry = new Timer(_ => { if (!Consumed) OnRetired?.Invoke(); }, null,
                expires > DateTimeOffset.UtcNow ? expires - DateTimeOffset.UtcNow : TimeSpan.Zero,
                Timeout.InfiniteTimeSpan);
        }
        internal void Cancel()
        {
            try { Lifetime.Cancel(); }
            catch (ObjectDisposedException) { }
        }
        public void Dispose()
        {
            if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
            if (Snapshot.RuntimeAuthority is ProductionWorkspaceRuntime runtime)
                runtime.Sidecar.StatusChanged -= SidecarChanged;
            if (Snapshot.RuntimeAuthority is IProductSidecarGenerationAuthority authority)
                authority.CurrentChanged -= CheckCurrent;
            _epoch.Unregister();
            _expiry?.Dispose();
            Gateway.Dispose();
            Provider.Dispose();
            Lease.Dispose();
            Lifetime.Dispose();
        }
    }
}
