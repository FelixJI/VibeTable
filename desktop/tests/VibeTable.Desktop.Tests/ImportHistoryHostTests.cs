using System.Net;
using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Import management seams: durable Go history start precedes worker
/// execution, Go receipts beat late cancellation and worker crashes, history
/// reads never start Python, and unknown outcomes never carry fake counts.
/// </summary>
[TestClass]
public sealed class ImportHistoryHostTests
{
    [TestMethod]
    public async Task StartPersistFailureBlocksWorkerExecution()
    {
        await using var fixture = new ImportHistoryFixture();
        fixture.Http.StartStatus = HttpStatusCode.InternalServerError;
        var failed = new TaskCompletionSource<JsonElement>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Tasks.TaskChanged += change => failed.TrySetResult(change);

        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            fixture.Gateway().CreateTaskAsync(fixture.ImportTaskParams(), CancellationToken.None));

        Assert.AreEqual(1, fixture.Http.StartCalls.Count, "exactly one start attempt");
        Assert.AreEqual(0, fixture.Python.Requests.Count, "worker startExecution must never run");
        JsonElement change = await failed.Task.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual("failed", change.GetProperty("state").GetString(),
            "the renderer event projects aborts as failures");
        Assert.AreEqual("aborted", fixture.Tasks.Status(change.GetProperty("taskId").GetString()!)
            .GetProperty("state").GetString());
        StringAssert.Contains(
            fixture.Tasks.Status(change.GetProperty("taskId").GetString()!)
                .GetProperty("error").GetString()!, "待核实");
    }

    [TestMethod]
    public async Task StartPersistsSafeFactsOnlyBeforeWorkerRuns()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;

        Assert.AreEqual("queued", status.GetProperty("state").GetString());
        Assert.AreEqual(1, fixture.Http.StartCalls.Count);
        JsonElement start = fixture.Http.StartCalls[0];
        Assert.AreEqual(taskId, start.GetProperty("taskId").GetString());
        Assert.AreEqual("orders", start.GetProperty("collection").GetString());
        Assert.AreEqual("xlsx", start.GetProperty("sourceType").GetString(),
            "xlsm maps to xlsx like the worker reader");
        Assert.AreEqual("订单导入.xlsm", start.GetProperty("sourceName").GetString());
        Assert.AreEqual("imp-17-0", start.GetProperty("idempotencyKey").GetString());
        Assert.AreEqual(7UL, start.GetProperty("sessionEpoch").GetUInt64());
        CollectionAssert.AreEquivalent(new[]
        {
            "taskId", "collection", "sourceType", "sourceName",
            "idempotencyKey", "sessionEpoch",
        }, start.EnumerateObject().Select(property => (string?)property.Name).ToArray());
        Assert.AreEqual(1, fixture.Python.Requests.Count);
        Assert.AreEqual("task.startExecution", fixture.Python.Requests[0].Method);
        Assert.AreEqual(taskId,
            fixture.Python.Requests[0].Params.GetProperty("taskId").GetString());
        Assert.IsTrue(fixture.Http.SessionHeaderSeen,
            "history calls use the fixed loopback session header");
    }

    [TestMethod]
    public async Task ExportTasksNeverPersistImportHistory()
    {
        await using var fixture = new ImportHistoryFixture();
        await fixture.Gateway().CreateTaskAsync(JsonSerializer.SerializeToElement(new
        {
            kind = "data.export",
            @params = new
            {
                grantId = "unused",
                collection = "orders",
                query = new { },
                format = "csv",
                includeRelations = false,
            },
        }), CancellationToken.None);

        Assert.AreEqual(0, fixture.Http.StartCalls.Count);
        Assert.AreEqual(1, fixture.Python.Requests.Count);
    }

    [TestMethod]
    public async Task HistoryReadNeverStartsPythonAndOverlaysOnlyLiveTasks()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string liveTaskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(liveTaskId, TaskStates.Running);
        fixture.Http.Page = Page(
            Entry(liveTaskId),
            Entry("task-oldepoch", sessionEpoch: 7),
            Entry("task-committed", state: "succeeded",
                commitState: "committed", created: 12, updated: 3,
                finishedAt: "2026-10-01T00:01:00Z"));

        JsonElement history = await fixture.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);

        Assert.AreEqual(0, fixture.Http.FinishCalls.Count);
        Assert.AreEqual(1, fixture.Python.Requests.Count,
            "history reads must never start or touch Python");
        JsonElement items = history.GetProperty("items");
        Assert.AreEqual(3, items.GetArrayLength());
        Assert.AreEqual("running", items[0].GetProperty("state").GetString(),
            "the current session's running task overlays interrupted");
        Assert.IsTrue(items[0].GetProperty("createdCount").ValueKind == JsonValueKind.Null);
        Assert.IsTrue(items[0].GetProperty("updatedCount").ValueKind == JsonValueKind.Null);
        Assert.IsTrue(items[0].GetProperty("finishedAt").ValueKind == JsonValueKind.Null);
        Assert.AreEqual("interrupted", items[1].GetProperty("state").GetString(),
            "entries without a live registry record never revive");
        Assert.AreEqual("succeeded", items[2].GetProperty("state").GetString());
        Assert.AreEqual(12L, items[2].GetProperty("createdCount").GetInt64());
        Assert.AreEqual(3L, items[2].GetProperty("updatedCount").GetInt64());

        // Worker churn ends the live overlay; the durable entry stays honest.
        fixture.Tasks.RetireClient(fixture.Client);
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));
        JsonElement settled = await fixture.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);
        Assert.AreEqual("interrupted",
            settled.GetProperty("items")[0].GetProperty("state").GetString());
    }

    [TestMethod]
    public async Task CommittedGoReceiptBeatsLateCancelAndWorkerCrash()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);
        // The worker crashes after Go committed; the crash settlement and the
        // late cancellation must not rewrite the committed receipt.
        fixture.Tasks.RetireClient(fixture.Client);
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(1, fixture.Http.FinishCalls.Count);
        Assert.AreEqual("aborted", fixture.Http.FinishCalls[0].GetProperty("state").GetString());
        fixture.Report(taskId, TaskStates.Cancelled);
        Assert.AreEqual(1, fixture.Http.FinishCalls.Count,
            "a retired binding rejects late worker reports");

        fixture.Http.Page = Page(Entry(taskId, state: "succeeded",
            commitState: "committed", created: 5, updated: 2,
            finishedAt: "2026-10-01T00:02:00Z"));
        JsonElement history = await fixture.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);
        JsonElement entry = history.GetProperty("items")[0];
        Assert.AreEqual("succeeded", entry.GetProperty("state").GetString());
        Assert.AreEqual("committed", entry.GetProperty("commitState").GetString());
        Assert.AreEqual(5L, entry.GetProperty("createdCount").GetInt64());
        Assert.AreEqual("aborted", fixture.Tasks.Status(taskId)
            .GetProperty("state").GetString(),
            "in-flight state stays Host-authoritative while Go proves results");
    }

    [TestMethod]
    public async Task UnknownOutcomesKeepNullCountsAndFakeSuccessIsRejected()
    {
        await using var fixture = new ImportHistoryFixture();
        fixture.Http.Page = Page(Entry("task-unknown"));
        JsonElement history = await fixture.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);
        JsonElement entry = history.GetProperty("items")[0];
        Assert.AreEqual("unknown", entry.GetProperty("commitState").GetString());
        Assert.IsTrue(entry.GetProperty("createdCount").ValueKind == JsonValueKind.Null);
        Assert.IsTrue(entry.GetProperty("updatedCount").ValueKind == JsonValueKind.Null);
        Assert.IsTrue(entry.GetProperty("finishedAt").ValueKind == JsonValueKind.Null);

        fixture.Http.Page = Page(Entry("task-fake", state: "succeeded"));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            fixture.Gateway().GetImportHistoryAsync(Json("{}"), CancellationToken.None));

        fixture.Http.Page = Page(Entry("task-mixed", state: "failed",
            commitState: "committed", created: 1, updated: 1,
            finishedAt: "2026-10-01T00:03:00Z"));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            fixture.Gateway().GetImportHistoryAsync(Json("{}"), CancellationToken.None));
    }

    [TestMethod]
    public async Task TerminalReportPersistsFinishAndCloseKeepsInterrupted()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);

        fixture.Report(taskId, TaskStates.Failed);
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(1, fixture.Http.FinishCalls.Count);
        Assert.AreEqual(taskId, fixture.Http.FinishCalls[0].GetProperty("taskId").GetString());
        Assert.AreEqual("failed", fixture.Http.FinishCalls[0].GetProperty("state").GetString());
        Assert.AreEqual("failed", fixture.Tasks.Status(taskId)
            .GetProperty("state").GetString());

        // Workspace close settles nothing new: still-live imports stay
        // interrupted (pending verification) for the next open.
        JsonElement second = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string closingTaskId = second.GetProperty("taskId").GetString()!;
        fixture.Report(closingTaskId, TaskStates.Running);
        fixture.Tasks.Dispose();
        Assert.AreEqual(1, fixture.Http.FinishCalls.Count,
            "dispose must not persist new finishes for in-flight imports");
        Assert.AreEqual("aborted", fixture.Tasks.Status(closingTaskId)
            .GetProperty("state").GetString());
    }

    [TestMethod]
    public async Task FinishSettlementIsAwaitableThroughDrain()
    {
        await using var fixture = new ImportHistoryFixture();
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.BeforeFinish = () => release.Task;
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);
        fixture.Report(taskId, TaskStates.Cancelled);
        await fixture.Http.FinishEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(0, fixture.Http.FinishCalls.Count,
            "the finish is in flight until released");
        release.SetResult();
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(1, fixture.Http.FinishCalls.Count);
    }

    [TestMethod]
    public async Task RetiredGenerationBindsNoJournalAndDropsFinishes()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);
        fixture.CurrentSnapshot = null;
        fixture.Tasks.RetireClient(fixture.Client);
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));

        Assert.AreEqual(0, fixture.Http.FinishCalls.Count,
            "a retired Sidecar generation must not write history");
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            fixture.Gateway().GetImportHistoryAsync(Json("{}"), CancellationToken.None));
    }

    [TestMethod]
    public async Task OldSessionTaskIdDoesNotOverlayAcrossRegistries()
    {
        // Reopen simulation: a fresh registry has no live records, so a
        // leftover interrupted entry from the previous session stays
        // interrupted even though the old task was running when it died.
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);
        fixture.Http.Page = Page(Entry(taskId));

        await using var reopened = new ImportHistoryFixture();
        reopened.Http.Page = Page(Entry(taskId, sessionEpoch: reopened.SessionEpoch));
        JsonElement history = await reopened.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);
        Assert.AreEqual("interrupted",
            history.GetProperty("items")[0].GetProperty("state").GetString());
    }

    private static JsonElement Json(string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return document.RootElement.Clone();
    }

    [TestMethod]
    public async Task InterruptedEntriesCarryFrozenDiagnosticAndOverlayStripsIt()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string liveTaskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(liveTaskId, TaskStates.Running);
        // The real Go Start shape: interrupted with the frozen pending-
        // verification diagnostic and no finishedAt.
        fixture.Http.Page = Page(
            Entry(liveTaskId, errorCode: "import.interrupted"),
            Entry("task-reopened", errorCode: "import.interrupted"));

        JsonElement history = await fixture.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);

        JsonElement live = history.GetProperty("items")[0];
        Assert.AreEqual("running", live.GetProperty("state").GetString());
        Assert.IsTrue(live.GetProperty("errorCode").ValueKind == JsonValueKind.Null,
            "a running projection must not show the interrupted diagnostic");
        Assert.IsTrue(live.GetProperty("finishedAt").ValueKind == JsonValueKind.Null);
        JsonElement reopened = history.GetProperty("items")[1];
        Assert.AreEqual("interrupted", reopened.GetProperty("state").GetString());
        Assert.AreEqual("import.interrupted",
            reopened.GetProperty("errorCode").GetString(),
            "reopen keeps the pending-verification diagnostic");

        fixture.Http.Page = Page(Entry("task-badsuccess", state: "succeeded",
            commitState: "committed", created: 1, updated: 0,
            finishedAt: "2026-10-01T00:04:00Z", errorCode: "import.interrupted"));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            fixture.Gateway().GetImportHistoryAsync(Json("{}"), CancellationToken.None));
    }

    [TestMethod]
    public async Task DrainSurvivesFinishHttpFailureWithoutThrowing()
    {
        await using var fixture = new ImportHistoryFixture();
        fixture.Http.FinishStatus = HttpStatusCode.InternalServerError;
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);

        fixture.Report(taskId, TaskStates.Failed);
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));

        Assert.AreEqual("failed", fixture.Tasks.Status(taskId)
            .GetProperty("state").GetString(),
            "a failed settlement must not rewrite the task record");
    }

    [TestMethod]
    public async Task WorkerExitDuringDurableReadDoesNotProjectRunning()
    {
        await using var fixture = new ImportHistoryFixture();
        JsonElement status = await fixture.Gateway().CreateTaskAsync(
            fixture.ImportTaskParams(), CancellationToken.None);
        string taskId = status.GetProperty("taskId").GetString()!;
        fixture.Report(taskId, TaskStates.Running);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Http.BeforeList = () => release.Task;
        fixture.Http.Page = Page(Entry(taskId, errorCode: "import.interrupted"));

        Task<JsonElement> pending = fixture.Gateway().GetImportHistoryAsync(
            Json("{}"), CancellationToken.None);
        await fixture.Http.ListEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        fixture.Tasks.RetireClient(fixture.Client);
        await fixture.Tasks.DrainImportHistoryAsync(TimeSpan.FromSeconds(3));
        release.SetResult();

        JsonElement history = await pending.WaitAsync(TimeSpan.FromSeconds(3));
        JsonElement entry = history.GetProperty("items")[0];
        Assert.AreEqual("interrupted", entry.GetProperty("state").GetString(),
            "liveness is re-checked after the durable read, not before it");
        Assert.AreEqual("import.interrupted", entry.GetProperty("errorCode").GetString());
    }

    [TestMethod]
    public async Task StaleSnapshotCannotOverwriteNewerJournal()
    {
        await using var fixture = new ImportHistoryFixture();
        // Generation 1 binds its journal through a first read.
        await fixture.Gateway().GetImportHistoryAsync(Json("{}"), CancellationToken.None);
        Assert.AreEqual(1, fixture.Http.ListCalls);

        // A newer Sidecar generation becomes current and binds its own journal.
        using var newerPeer = new HistoryHttpPeer(fixture.Snapshot);
        newerPeer.Page = Page(Entry("task-newer"));
        ProductSidecarGenerationSnapshot newer = fixture.NewSnapshot(2, newerPeer);
        fixture.CurrentSnapshot = newer;
        fixture.Tasks.BindImportHistory(newer, newerPeer);

        // The stale generation-1 invoker races a rebind after the currency
        // check window; it must not overwrite or retire the newer journal.
        fixture.Tasks.BindImportHistory(fixture.Snapshot, fixture.Http);

        JsonElement history = await fixture.Tasks.ReadImportHistoryAsync(CancellationToken.None);
        Assert.AreEqual("task-newer",
            history.GetProperty("items")[0].GetProperty("taskId").GetString(),
            "the newer generation's journal must keep serving reads");
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() =>
            fixture.Gateway().GetImportHistoryAsync(Json("{}"), CancellationToken.None));
        Assert.AreEqual(1, fixture.Http.ListCalls,
            "the stale journal must not receive further calls");
    }

    private static JsonElement Page(params JsonElement[] entries)
        => JsonSerializer.SerializeToElement(new { items = entries });

    private static JsonElement Entry(
        string taskId,
        string state = "interrupted",
        string commitState = "unknown",
        long? created = null,
        long? updated = null,
        string? finishedAt = null,
        ulong sessionEpoch = 7,
        string collection = "orders",
        string sourceType = "csv",
        string? errorCode = null)
        => JsonSerializer.SerializeToElement(new
        {
            taskId,
            collection,
            sourceType,
            sourceName = "月度数据.csv",
            state,
            commitState,
            createdCount = created,
            updatedCount = updated,
            startedAt = "2026-10-01T00:00:00Z",
            finishedAt,
            sessionEpoch,
            errorCode,
        });

    private sealed class ImportHistoryFixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(),
            "vibetable-import-history-" + Guid.NewGuid().ToString("N"));
        private readonly HostSessionFileBroker _files;
        private readonly AlwaysCurrentLeases _leases = new();
        private JsonRpcProductDataGateway? _gateway;

        internal ImportHistoryFixture()
        {
            Directory.CreateDirectory(_root);
            string sourcePath = Path.Combine(_root, "订单导入.xlsm");
            File.WriteAllText(sourcePath, "workbook");
            SessionEpoch = 7;
            Snapshot = new ProductSidecarGenerationSnapshot(
                RuntimeAuthority,
                1,
                new PocketBaseAdminContext(
                    new Uri("http://127.0.0.1:12345/_/"),
                    new Uri("http://127.0.0.1:12345/"),
                    "X-VibeTable-Session",
                    "history-session-secret"),
                new ProductSidecarIdentity(
                    Guid.NewGuid().ToString("D"),
                    SessionEpoch,
                    3,
                    Guid.NewGuid().ToString("D")),
                Array.Empty<ProductSidecarRegistration>(),
                action => ReferenceEquals(CurrentSnapshot, Snapshot) && action());
            CurrentSnapshot = Snapshot;
            Python = new PythonWorkerTransport();
            Client = new JsonRpcClient(Python);
            Http = new HistoryHttpPeer(Snapshot);
            Tasks = new HostDataIoTaskRegistry();
            _files = new HostSessionFileBroker(
                () => Lease(),
                action => action());
            Grant = _files.IssueAsync(
                sourcePath, write: false, runId: null, CancellationToken.None)
                .GetAwaiter().GetResult();
            Invoker = new HostProductRpcInvoker(
                Client,
                Snapshot,
                _leases,
                action => action(),
                handler: Http,
                taskOwner: Tasks,
                hostFiles: () => _files);
        }

        internal PythonWorkerTransport Python { get; }
        internal HistoryHttpPeer Http { get; }
        internal HostDataIoTaskRegistry Tasks { get; }
        internal JsonRpcClient Client { get; }
        internal HostProductRpcInvoker Invoker { get; }
        internal ProductSidecarGenerationSnapshot Snapshot { get; }
        internal JsonElement Grant { get; }
        internal ulong SessionEpoch { get; }
        internal object RuntimeAuthority { get; } = new();
        /// <summary>The generation authority view: only this snapshot is current.</summary>
        internal ProductSidecarGenerationSnapshot? CurrentSnapshot { get; set; }

        internal ProductSidecarGenerationSnapshot NewSnapshot(
            long generationId,
            HistoryHttpPeer peer)
        {
            ProductSidecarGenerationSnapshot? created = null;
            ProductSidecarGenerationSnapshot snapshot = new(
                RuntimeAuthority,
                generationId,
                Snapshot.Context,
                Snapshot.Identity,
                Array.Empty<ProductSidecarRegistration>(),
                action => ReferenceEquals(CurrentSnapshot, created) && action());
            created = snapshot;
            return snapshot;
        }

        internal JsonRpcProductDataGateway Gateway()
            => _gateway ??= new JsonRpcProductDataGateway(Invoker);

        internal JsonElement ImportTaskParams()
            => JsonSerializer.SerializeToElement(new
            {
                kind = "data.import",
                @params = new
                {
                    grantId = Grant.GetProperty("grantId").GetString(),
                    collection = "orders",
                    token = "plan-token",
                    mode = "upsert",
                    idempotencyPrefix = "imp-17",
                },
            });

        internal void Report(string taskId, string state)
            => Tasks.ApplyReport(Client, JsonSerializer.SerializeToElement(new
            {
                taskId,
                kind = "data.import",
                state,
                progress = new { done = 1, total = 2, message = "" },
                result = (object?)null,
                error = (string?)null,
            }));

        private WorkspaceRequestEpochLease Lease()
        {
            if (!_leases.TryCaptureHost(
                    Guid.NewGuid(), SessionEpoch, Guid.NewGuid(),
                    out WorkspaceRequestEpochLease? lease)
                || lease is null)
                throw new InvalidOperationException("test lease capture failed");
            return lease;
        }

        public async ValueTask DisposeAsync()
        {
            _gateway?.Dispose();
            Tasks.Dispose();
            await Client.DisposeAsync();
            Http.Dispose();
            try { Directory.Delete(_root, recursive: true); }
            catch (IOException) { }
        }
    }

    private sealed class AlwaysCurrentLeases : IWorkspaceHostEpochLeaseSource
    {
        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => true;

        public bool TryCaptureHost(
            Guid workspaceId,
            ulong sessionEpoch,
            Guid operationId,
            out WorkspaceRequestEpochLease? lease)
        {
            lease = new WorkspaceRequestEpochLease(
                new WorkspaceWireScope
                {
                    Scope = "workspace",
                    WorkspaceId = workspaceId,
                    SessionEpoch = sessionEpoch,
                    OperationId = operationId,
                    Sequence = 1,
                },
                CancellationToken.None,
                () => { });
            return true;
        }
    }

    private sealed class PythonWorkerTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        internal List<(string Method, JsonElement Params)> Requests { get; } = [];

        public Task<JsonElement?> ReadAsync(CancellationToken token)
            => _incoming.Reader.ReadAsync(token).AsTask();

        public Task WriteAsync(string line, CancellationToken token)
        {
            using JsonDocument document = JsonDocument.Parse(line);
            JsonElement root = document.RootElement;
            string method = root.GetProperty("method").GetString()!;
            Requests.Add((method, root.GetProperty("params").Clone()));
            string id = root.GetProperty("id").GetString()!;
            object result = method == "task.startExecution"
                ? new { accepted = true }
                : new { };
            _incoming.Writer.TryWrite(JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0",
                id,
                result,
            }));
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }

    private sealed class HistoryHttpPeer(ProductSidecarGenerationSnapshot snapshot)
        : HttpMessageHandler
    {
        internal HttpStatusCode StartStatus { get; set; } = HttpStatusCode.OK;
        internal HttpStatusCode FinishStatus { get; set; } = HttpStatusCode.OK;
        internal JsonElement Page { get; set; } =
            JsonSerializer.SerializeToElement(new { items = Array.Empty<object>() });
        internal List<JsonElement> StartCalls { get; } = [];
        internal List<JsonElement> FinishCalls { get; } = [];
        internal bool SessionHeaderSeen { get; private set; }
        internal int ListCalls { get; private set; }
        internal TaskCompletionSource FinishEntered { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal TaskCompletionSource ListEntered { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        internal Func<Task>? BeforeFinish { get; set; }
        internal Func<Task>? BeforeList { get; set; }

        protected override async Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
        {
            SessionHeaderSeen |= request.Headers.TryGetValues(
                "X-VibeTable-Session",
                out IEnumerable<string>? values)
                && values.Contains("history-session-secret");
            string path = request.RequestUri!.AbsolutePath;
            if (request.Method == HttpMethod.Get
                && path == "/api/vibetable/v2/source-import/history")
                return Reply(Json("{\"contract\":\"vibetable.source-import.v1\",\"entries\":[]}"));
            if (request.Method == HttpMethod.Get
                && path == "/api/vibetable/v2/import-history")
            {
                ListCalls++;
                ListEntered.TrySetResult();
                if (BeforeList is { } delay)
                    await delay().WaitAsync(cancellationToken);
                return Reply(Page);
            }
            if (request.Method == HttpMethod.Post)
            {
                JsonElement body = Json(
                    await request.Content!.ReadAsStringAsync(cancellationToken));
                if (path == "/api/vibetable/v2/import-history/start")
                {
                    StartCalls.Add(body);
                    return StartStatus == HttpStatusCode.OK
                        ? Reply(EntryFromBody(body, "interrupted"))
                        : new HttpResponseMessage(StartStatus);
                }
                if (path == "/api/vibetable/v2/import-history/finish")
                {
                    FinishEntered.TrySetResult();
                    if (BeforeFinish is { } delay)
                        await delay().WaitAsync(cancellationToken);
                    FinishCalls.Add(body);
                    return FinishStatus == HttpStatusCode.OK
                        ? Reply(EntryFromBody(body,
                            body.GetProperty("state").GetString()!))
                        : new HttpResponseMessage(FinishStatus);
                }
            }
            if (request.Method == HttpMethod.Get
                && path == "/api/vibetable/v2/product/capabilities")
            {
                return Reply(JsonSerializer.SerializeToElement(new
                {
                    contractVersion = "2.0",
                    workspaceId = snapshot.Identity.WorkspaceId,
                    sessionEpoch = snapshot.Identity.SessionEpoch,
                    fenceEpoch = snapshot.Identity.FenceEpoch,
                    claimId = snapshot.Identity.ClaimId,
                    rpcMethods = snapshot.Registrations.Select(item => item.Method),
                    registrations = snapshot.Registrations.Select(
                        item => new { method = item.Method, scope = item.Scope }),
                }));
            }
            throw new InvalidOperationException(
                $"Unexpected history HTTP call: {request.Method} {path}");
        }

        private JsonElement EntryFromBody(JsonElement body, string state)
            => JsonSerializer.SerializeToElement(new
            {
                taskId = body.GetProperty("taskId"),
                collection = "orders",
                sourceType = "csv",
                sourceName = "月度数据.csv",
                state,
                commitState = "unknown",
                createdCount = (long?)null,
                updatedCount = (long?)null,
                startedAt = "2026-10-01T00:00:00Z",
                finishedAt = state == "interrupted"
                    ? (string?)null
                    : "2026-10-01T00:05:00Z",
                sessionEpoch = snapshot.Identity.SessionEpoch,
                errorCode = state == "interrupted" ? "import.interrupted" : null,
            });

        private static HttpResponseMessage Reply(JsonElement body) => new(HttpStatusCode.OK)
        {
            Content = new StringContent(body.GetRawText()),
        };

        private static JsonElement Json(string json)
        {
            using JsonDocument document = JsonDocument.Parse(json);
            return document.RootElement.Clone();
        }
    }
}
