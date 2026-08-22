using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DashboardBridgeTests
{
    private static readonly string[] RequestTypes =
    [
        "dashboard.listRequested", "dashboard.readRequested",
        "dashboard.manifestRequested", "dashboard.queryRequested",
        "dashboard.saveRequested", "dashboard.deleteRequested", "dashboard.cancelRequested",
    ];

    [TestMethod]
    public void Router_AllowsOnlyNamedDashboardMessages()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(dispatched.Add) { IsReady = true };
        foreach (string type in RequestTypes)
        {
            Assert.IsNull(router.Route(JsonSerializer.Serialize(new
            {
                type,
                requestId = type,
                payload = new { },
            })));
        }
        CollectionAssert.AreEqual(RequestTypes, dispatched.Select(item => item.Type).ToArray());
        foreach (string type in new[]
        {
            "dashboard.listLoaded", "dashboard.loaded", "dashboard.manifestLoaded",
            "dashboard.queryLoaded", "dashboard.saved", "dashboard.deleted",
        }) Assert.IsTrue(router.IsHostNotificationAllowed(type), type);
        Assert.IsFalse(router.IsHostNotificationAllowed("dashboard.rpcResult"));
    }

    [TestMethod]
    public async Task Controller_InterfaceCorrelatesValidRequests()
    {
        var gateway = new FakeDashboardGateway();
        var sink = new FakeWebReplySink();
        var controller = new DashboardRequestController(sink, TimeSpan.FromSeconds(1));
        controller.SetGateway(gateway);

        await controller.DispatchAsync(Request("dashboard.listRequested", "direct-list", "{}"));

        FakeWebReplySink.Reply? reply = await sink.WaitForAsync("dashboard.listLoaded");
        Assert.AreEqual("direct-list", reply!.RequestId);
        Assert.AreEqual(1, gateway.ListCalls);
    }

    [TestMethod]
    public async Task Controller_InterfaceRejectsUnknownTypeAndInvalidPayload()
    {
        var sink = new FakeWebReplySink();
        var controller = new DashboardRequestController(sink, TimeSpan.FromSeconds(1));

        await controller.DispatchAsync(Request("dashboard.unknownRequested", "unknown", "{}"));
        await controller.DispatchAsync(Request("dashboard.queryRequested", "invalid", "[]"));

        FakeWebReplySink.Reply unknown = sink.Replies.Single(reply => reply.RequestId == "unknown");
        FakeWebReplySink.Reply invalid = sink.Replies.Single(reply => reply.RequestId == "invalid");
        Assert.AreEqual("UNKNOWN_TYPE", ((dynamic)unknown.Payload!).code);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)invalid.Payload!).code);
    }

    [TestMethod]
    public async Task Dispatcher_CorrelatesListReadManifestQueryAndSave()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();

        dispatcher.Dispatch(Request("dashboard.listRequested", "list-1", "{}"));
        Assert.AreEqual("list-1", (await sink.WaitForAsync("dashboard.listLoaded"))!.RequestId);

        dispatcher.Dispatch(Request("dashboard.readRequested", "read-1", """{"dashboardId":"dash-1"}"""));
        Assert.AreEqual("read-1", (await sink.WaitForAsync("dashboard.loaded"))!.RequestId);

        dispatcher.Dispatch(Request("dashboard.manifestRequested", "manifest-1", "{}"));
        var manifest = await sink.WaitForAsync("dashboard.manifestLoaded");
        Assert.AreEqual("manifest-1", manifest!.RequestId);
        Assert.AreEqual(1, gateway.ManifestCalls);
        Assert.AreEqual(1, gateway.LimitsCalls);

        dispatcher.Dispatch(Request(
            "dashboard.queryRequested", "query-1",
            """{"panelType":"metric","query":{"kind":"records","collection":"orders","fields":["id"],"limit":20}}"""));
        Assert.AreEqual("query-1", (await sink.WaitForAsync("dashboard.queryLoaded"))!.RequestId);
        Assert.AreEqual("query-1", gateway.Queries.Single().RequestId);

        dispatcher.Dispatch(Request(
            "dashboard.saveRequested", "save-1",
            """{"dashboardId":null,"expectedRevision":null,"idempotencyKey":"123e4567-e89b-42d3-a456-426614174000","name":"Sales","note":"","panels":[],"deletedPanelIds":[],"config":{"configVersion":1,"globalFilters":[],"interactions":[],"refreshInterval":0}}"""));
        Assert.AreEqual("save-1", (await sink.WaitForAsync("dashboard.saved"))!.RequestId);
        Assert.AreEqual("Sales", gateway.Saves.Single().Name);

        dispatcher.Dispatch(Request(
            "dashboard.deleteRequested", "delete-1", """{"dashboardId":"dash-1"}"""));
        Assert.AreEqual("delete-1", (await sink.WaitForAsync("dashboard.deleted"))!.RequestId);
        CollectionAssert.AreEqual(new[] { "dash-1" }, gateway.Deletes);
    }

    [TestMethod]
    public async Task Dispatcher_AcceptsDashboardQueryDiscriminatorAfterRoundTripSorting()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();

        dispatcher.Dispatch(Request(
            "dashboard.saveRequested", "save-roundtrip",
            """{"dashboardId":"123e4567-e89b-42d3-a456-426614174010","expectedRevision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotencyKey":"123e4567-e89b-42d3-a456-426614174011","name":"Sales","note":"","panels":[{"clientId":"123e4567-e89b-42d3-a456-426614174012","panelId":"123e4567-e89b-42d3-a456-426614174012","name":"By region","type":"bar","position":{"x":0,"y":0,"width":4,"height":3},"options":{},"query":{"collection":"orders","dimensions":["region"],"filters":[],"kind":"aggregate","limit":100,"measures":[{"field":null,"key":"value","op":"count"}],"timeBucket":null,"topN":null}}],"deletedPanelIds":[],"config":{"configVersion":1,"globalFilters":[],"interactions":[],"refreshInterval":0}}"""));

        Assert.AreEqual("save-roundtrip", (await sink.WaitForAsync("dashboard.saved"))!.RequestId);
        Assert.IsInstanceOfType<DashboardAggregateQuery>(gateway.Saves.Single().Panels.Single().Query);
    }

    [TestMethod]
    public async Task Dispatcher_CancelPostsOneCorrelatedFailureAndSuppressesLateResult()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var completion = new TaskCompletionSource<DashboardQueryResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.QueryHandler = (_, _) => completion.Task; // Deliberately ignores cancellation.

        dispatcher.Dispatch(Request(
            "dashboard.queryRequested", "slow-query",
            """{"panelType":"metric","query":{"kind":"records","collection":"orders","fields":["id"]}}"""));
        await gateway.QueryStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.Dispatch(Request(
            "dashboard.cancelRequested", "cancel-1",
            """{"targetRequestId":"slow-query"}"""));

        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("slow-query", failure!.RequestId);
        Assert.AreEqual("DASHBOARD_CANCELLED", ((dynamic)failure.Payload!).code);
        completion.SetResult(new DashboardQueryResult([], false, 100));
        await Task.Delay(100);
        Assert.IsFalse(sink.Replies.Any(reply =>
            reply.Type == "dashboard.queryLoaded" && reply.RequestId == "slow-query"));
        Assert.AreEqual(1, sink.Replies.Count(reply =>
            reply.Type == "operation.failed" && reply.RequestId == "slow-query"));
    }

    [TestMethod]
    public async Task Dispatcher_CancelSuppressesLateBackendException()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var completion = new TaskCompletionSource<DashboardQueryResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.QueryHandler = (_, _) => completion.Task;

        dispatcher.Dispatch(Request(
            "dashboard.queryRequested", "late-error",
            """{"panelType":"metric","query":{"kind":"records","collection":"orders","fields":["id"]}}"""));
        await gateway.QueryStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.Dispatch(Request(
            "dashboard.cancelRequested", "cancel-late-error",
            """{"targetRequestId":"late-error"}"""));
        await sink.WaitForFailedAsync();
        completion.SetException(new InvalidOperationException("late private failure"));
        await Task.Delay(100);

        Assert.AreEqual(1, sink.Replies.Count(reply =>
            reply.Type == "operation.failed" && reply.RequestId == "late-error"));
    }

    [TestMethod]
    public async Task Dispatcher_QueryTimeoutUsesStableSafeError()
    {
        var gateway = new FakeDashboardGateway
        {
            QueryHandler = async (_, token) =>
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, token);
                return new DashboardQueryResult([], false, 100);
            },
        };
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"), sink,
            NoDatabaseOpenRoute.Instance,
            dashboardRequestTimeout: TimeSpan.FromMilliseconds(30));
        dispatcher.SetDashboardGateway(gateway);

        dispatcher.Dispatch(Request(
            "dashboard.queryRequested", "timeout-1",
            """{"panelType":"metric","query":{"kind":"records","collection":"orders","fields":["id"]}}"""));
        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("DASHBOARD_TIMEOUT", ((dynamic)failure!.Payload!).code);
    }

    [TestMethod]
    public async Task Dispatcher_NeverRunsMoreThanSixDashboardQueriesConcurrently()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var release = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.QueryHandler = async (_, _) =>
        {
            await release.Task;
            return new DashboardQueryResult([], false, 100);
        };

        for (int index = 0; index < 8; index++)
        {
            dispatcher.Dispatch(Request(
                "dashboard.queryRequested", $"parallel-{index}",
                """{"panelType":"metric","query":{"kind":"records","collection":"orders","fields":["id"]}}"""));
        }

        Assert.IsTrue(SpinWait.SpinUntil(() => gateway.QueryCount == 6, 2_000));
        Assert.AreEqual(6, gateway.MaxActiveQueries);
        release.SetResult();
        Assert.IsTrue(SpinWait.SpinUntil(
            () => sink.Replies.Count(reply => reply.Type == "dashboard.queryLoaded") == 8,
            2_000));
        Assert.IsTrue(gateway.MaxActiveQueries <= 6);
    }

    [TestMethod]
    public async Task Dispatcher_UnexpectedFailureDoesNotLeakExceptionMessage()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        gateway.QueryHandler = (_, _) => throw new InvalidOperationException("secret C:\\private\\path");
        dispatcher.Dispatch(Request(
            "dashboard.queryRequested", "safe-error",
            """{"panelType":"metric","query":{"kind":"records","collection":"orders","fields":["id"]}}"""));

        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("DASHBOARD_OPERATION_FAILED", ((dynamic)failure!.Payload!).code);
        Assert.IsFalse(((string)((dynamic)failure.Payload!).message).Contains("private", StringComparison.Ordinal));
    }

    private static (WorkspaceRequestDispatcher Dispatcher, FakeDashboardGateway Gateway, FakeWebReplySink Sink)
        CreateDispatcher()
    {
        var gateway = new FakeDashboardGateway();
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"), sink,
            NoDatabaseOpenRoute.Instance);
        dispatcher.SetDashboardGateway(gateway);
        return (dispatcher, gateway, sink);
    }

    private static RoutedWebRequest Request(string type, string requestId, string json)
    {
        using var document = JsonDocument.Parse(json);
        return new RoutedWebRequest(type, requestId, document.RootElement.Clone(), string.Empty);
    }

    private sealed class FakeDashboardGateway : IDashboardRpcGateway
    {
        public int ListCalls { get; private set; }
        public int ManifestCalls { get; private set; }
        public int LimitsCalls { get; private set; }
        public List<ExecuteDashboardQueryParams> Queries { get; } = [];
        public List<SaveDashboardDraftParams> Saves { get; } = [];
        public List<string> Deletes { get; } = [];
        public TaskCompletionSource QueryStarted { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        public Func<ExecuteDashboardQueryParams, CancellationToken, Task<DashboardQueryResult>>? QueryHandler { get; set; }
        private int _activeQueries;
        private int _maxActiveQueries;
        public int QueryCount { get { lock (Queries) return Queries.Count; } }
        public int MaxActiveQueries => Volatile.Read(ref _maxActiveQueries);

        public Task<DashboardsResult> ListDashboardsAsync(CancellationToken token)
        {
            ListCalls++;
            return Task.FromResult(new DashboardsResult([]));
        }

        public Task<DashboardWorkspaceResult> ReadDashboardWorkspaceAsync(string dashboardId, CancellationToken token)
            => Task.FromResult(Workspace(dashboardId));

        public Task<SaveDashboardDraftResult> SaveDashboardDraftAsync(SaveDashboardDraftParams parameters, CancellationToken token)
        {
            Saves.Add(parameters);
            return Task.FromResult(new SaveDashboardDraftResult(Workspace("dash-1"), new Dictionary<string, string>(), true));
        }

        public Task<DeleteDashboardResult> DeleteDashboardAsync(string dashboardId, CancellationToken token)
        {
            Deletes.Add(dashboardId);
            return Task.FromResult(new DeleteDashboardResult(dashboardId));
        }

        public async Task<DashboardQueryResult> ExecuteDashboardQueryAsync(ExecuteDashboardQueryParams parameters, CancellationToken token)
        {
            lock (Queries) Queries.Add(parameters);
            QueryStarted.TrySetResult();
            int active = Interlocked.Increment(ref _activeQueries);
            int currentMax;
            do
            {
                currentMax = Volatile.Read(ref _maxActiveQueries);
                if (active <= currentMax) break;
            }
            while (Interlocked.CompareExchange(ref _maxActiveQueries, active, currentMax) != currentMax);
            try
            {
                return QueryHandler is null
                    ? new DashboardQueryResult([], false, 100_000)
                    : await QueryHandler(parameters, token);
            }
            finally
            {
                Interlocked.Decrement(ref _activeQueries);
            }
        }

        public Task<DashboardQueryLimits> GetDashboardQueryLimitsAsync(CancellationToken token)
        {
            LimitsCalls++;
            return Task.FromResult(new DashboardQueryLimits());
        }

        public Task<PanelManifestResult> GetPanelManifestAsync(CancellationToken token)
        {
            ManifestCalls++;
            return Task.FromResult(new PanelManifestResult("v2", "product-query-port.v1", []));
        }

        private static DashboardWorkspaceResult Workspace(string id)
            => new(
                new DashboardEntry(id, "Sales", "", []),
                new DashboardManagedConfig(),
                new string('a', 64),
                "vibetable-dashboard-atomic.v1",
                new DashboardQueryLimits());
    }
}
