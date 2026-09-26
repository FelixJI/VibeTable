using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GridRequestControllerTests
{
    [TestMethod]
    public async Task CorrelatedQueryReturnsOnePageWithoutDatasetBroadcast()
    {
        var gateway = new FakeTableRpcGateway();
        var page = new TablePage("records", [], [], 0, 100, 0, "remote");
        gateway.CursorOpenResults["records"] = page;
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(coordinator, sink);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{"filters":[]}}""");

        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", "query-recovery", payload.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await request.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, sink.Replies.Count, "The correlated query must settle its requester.");
        var reply = sink.Replies.Single();
        Assert.AreEqual("table.pageLoaded", reply.Type);
        Assert.AreEqual("query-recovery", reply.RequestId);
        Assert.AreSame(page, reply.Payload);
        Assert.AreEqual(0, notifications.Count, "The same result must not also be broadcast.");
    }

    [TestMethod]
    public async Task UncorrelatedQueryRetainsTransientRecoveryUnderItsLease()
    {
        var gateway = new FakeTableRpcGateway();
        var page = new TablePage("records", [], [], 0, 100, 0, "remote");
        int calls = 0;
        gateway.CursorOpenOverride = (_, _, _) => ++calls == 1
            ? Task.FromException<TablePage>(new BackendUnavailableException(
                "The host Product RPC binding is no longer current."))
            : Task.FromResult(page);
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(coordinator, sink);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", null, payload.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        time.Advance(TimeSpan.FromMilliseconds(250));
        await request.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(2, calls,
            "Uncorrelated renderer queries must retain the notify transient recovery.");
        var reply = sink.Replies.Single();
        Assert.AreEqual("table.datasetReady", reply.Type);
        Assert.AreEqual(0, notifications.Count,
            "The controller owns delivery under its epoch lease.");
    }

    [TestMethod]
    public async Task CursorNotificationRetainsTransientRecoveryThroughController()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenResults["records"] = new TablePage(
            "records", [], [], 0, 100, 0, "remote", NextCursor: "opaque-2", HasMore: true);
        var window = new TablePage("records", [], [], 100, 100, 0, "remote");
        int fetches = 0;
        gateway.CursorFetchOverride = (_, _) => ++fetches == 1
            ? Task.FromException<TablePage>(new BackendUnavailableException(
                "The host Product RPC binding is no longer current."))
            : Task.FromResult(window);
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(coordinator, sink);
        using var query = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task queryRequest = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", "cursor-setup", query.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await queryRequest.WaitAsync(TimeSpan.FromSeconds(2));

        using var cursor = JsonDocument.Parse("""{"cursor":"opaque-2"}""");
        Task cursorRequest = controller.DispatchAsync(new RoutedWebRequest(
            "table.cursorRequested", null, cursor.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(250));
        await cursorRequest.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(2, fetches,
            "Cursor notifications must retain the notify transient recovery.");
        var reply = sink.Replies.Single(r => r.Type == "table.windowLoaded");
        Assert.IsNull(reply.RequestId);
        Assert.AreEqual(0, notifications.Count,
            "The controller owns delivery under its epoch lease.");
    }

    [TestMethod]
    public async Task UncorrelatedQuerySurfacesSingleFailureAfterRecoveryWindow()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenOverride = (_, _, _) =>
            Task.FromException<TablePage>(new BackendUnavailableException(
                "The host Product RPC binding is no longer current."));
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(coordinator, sink);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", null, payload.RootElement, string.Empty));
        time.Advance(TimeSpan.FromSeconds(10));
        await request.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsTrue(gateway.QueryWindowCalls.Count >= 2,
            "The recovering read must retry within the bounded window.");
        var failure = sink.Replies.Single();
        Assert.AreEqual("operation.failed", failure.Type);
        StringAssert.Contains(
            JsonSerializer.Serialize(failure.Payload), "\"operation\":\"query\"");
        Assert.AreEqual(0, notifications.Count,
            "The coordinator must not double-broadcast the controller's failure.");
    }

    [TestMethod]
    public async Task UncorrelatedQuerySurfacesNonTransientFailureOnce()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenOverride = (_, _, _) =>
            Task.FromException<TablePage>(
                new InvalidOperationException("The table read is corrupt."));
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(coordinator, sink);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", null, payload.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await request.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, gateway.QueryWindowCalls.Count,
            "Non-transient failures must not be retried.");
        var failure = sink.Replies.Single();
        Assert.AreEqual("operation.failed", failure.Type);
        Assert.AreEqual(0, notifications.Count);
    }

    [TestMethod]
    public async Task ClosedSessionRetiresRecoveringUncorrelatedQueryWithoutLateDelivery()
    {
        var gateway = new FakeTableRpcGateway();
        var page = new TablePage("records", [], [], 0, 100, 0, "remote");
        int calls = 0;
        gateway.CursorOpenOverride = (_, _, _) => ++calls == 1
            ? Task.FromException<TablePage>(new BackendUnavailableException(
                "The host Product RPC binding is no longer current."))
            : Task.FromResult(page);
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        using var session = new CancellationTokenSource();
        var controller = new GridRequestController(coordinator, sink, () => session.Token);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", null, payload.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));

        session.Cancel();
        time.Advance(TimeSpan.FromMilliseconds(250));
        await request.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, calls,
            "A closed session must not retry the recovering read.");
        Assert.AreEqual(0, sink.Replies.Count, "No late success may be delivered.");
        Assert.AreEqual(0, notifications.Count, "No late failure may be delivered.");
    }

    [TestMethod]
    public async Task CorrelatedQueryFailsOnceWithoutNotifyRecovery()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenOverride = (_, _, _) =>
            Task.FromException<TablePage>(new BackendUnavailableException(
                "The host Product RPC binding is no longer current."));
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(coordinator, sink);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", "q-1", payload.RootElement, string.Empty));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await request.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, gateway.QueryWindowCalls.Count,
            "Correlated queries keep their single-attempt rejection contract.");
        var reply = sink.Replies.Single();
        Assert.AreEqual("operation.failed", reply.Type);
        Assert.AreEqual("q-1", reply.RequestId);
        Assert.AreEqual(0, notifications.Count);
    }

    [TestMethod]
    public async Task DispatchAsync_ForwardsOpaqueQueryAndCursorAcrossControllerInterface()
    {
        var gateway = new FakeTableRpcGateway();
        var snapshot = new QuerySnapshot(
            "snapshot-1",
            "digest-1",
            "database-1",
            "records",
            "schema-1",
            7,
            new Dictionary<string, object?>());
        gateway.QueryWindowResults["records"] = new TablePage(
            "records",
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            100,
            0,
            "remote",
            0,
            snapshot,
            NextCursor: "cursor-2",
            HasMore: true);
        gateway.CursorPageResults["cursor-2"] = new TablePage(
            "records",
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            100,
            0,
            "remote",
            0,
            snapshot,
            NextCursor: null,
            HasMore: false);
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var controller = new GridRequestController(coordinator, new FakeWebReplySink());
        using var query = JsonDocument.Parse(
            """{"table":"records","query":{"filters":[],"extension":"opaque"}}""");

        await controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested",
            "query-1",
            query.RootElement.Clone(),
            string.Empty));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);
        using var cursor = JsonDocument.Parse("""{"cursor":"cursor-2"}""");
        await controller.DispatchAsync(new RoutedWebRequest(
            "table.cursorRequested",
            "cursor-1",
            cursor.RootElement.Clone(),
            string.Empty));
        await Task.Delay(100);

        Assert.AreEqual(
            "opaque",
            gateway.RawViewQueries.Single().GetProperty("extension").GetString());
        CollectionAssert.AreEqual(new[] { "cursor-2" }, gateway.CursorFetchCalls);
    }

    [TestMethod]
    public async Task CancelledSessionRetiresDebouncedCorrelatedRequestWithoutReadingOrReplying()
    {
        var gateway = new FakeTableRpcGateway();
        var time = new ManualTimeProvider();
        var notifications = new List<TableNotification>();
        var coordinator = new GridStateCoordinator(gateway, notifications.Add, time);
        var sink = new FakeWebReplySink();
        using var session = new CancellationTokenSource();
        var controller = new GridRequestController(coordinator, sink, () => session.Token);
        using var payload = JsonDocument.Parse("""{"table":"records","query":{}}""");
        Task request = controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested", "cancel-before-read", payload.RootElement, string.Empty));

        session.Cancel();
        await request.WaitAsync(TimeSpan.FromSeconds(2));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));

        Assert.AreEqual(0, gateway.QueryWindowCalls.Count);
        Assert.AreEqual(0, sink.Replies.Count);
        Assert.AreEqual(0, notifications.Count);
    }

    [TestMethod]
    public async Task DispatchAsync_OwnsStableConfigurationPayloadAndTypeFailures()
    {
        var sink = new FakeWebReplySink();
        var controller = new GridRequestController(null, sink);
        using var empty = JsonDocument.Parse("{}");

        await controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested",
            "query-unconfigured",
            empty.RootElement.Clone(),
            string.Empty));
        await controller.DispatchAsync(new RoutedWebRequest(
            "grid.unknownRequested",
            "unknown",
            empty.RootElement.Clone(),
            string.Empty));

        string first = JsonSerializer.Serialize(sink.Replies[0].Payload);
        string second = JsonSerializer.Serialize(sink.Replies[1].Payload);
        StringAssert.Contains(first, "NOT_CONFIGURED");
        StringAssert.Contains(second, "UNKNOWN_TYPE");
    }
}
