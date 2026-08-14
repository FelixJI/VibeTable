using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GridRequestControllerTests
{
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
    public async Task DispatchAsync_PreservesCompleteGridStateWireShape()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryWindowResults["records"] = new TablePage(
            "records",
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            100,
            0,
            "remote");
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        coordinator.SetDatabase("database-1");
        var controller = new GridRequestController(coordinator, new FakeWebReplySink());
        using var query = JsonDocument.Parse(
            """{"table":"records","query":{"filters":[]}}""");
        await controller.DispatchAsync(new RoutedWebRequest(
            "table.queryRequested",
            "query-1",
            query.RootElement.Clone(),
            string.Empty));
        using var save = JsonDocument.Parse("""
            {
              "state": {
                "columns": [
                  {"name":"title","width":240,"visible":true,"frozen":true,"order":1}
                ],
                "sorts": [{"field":"title","direction":"desc","nullsLast":false}],
                "filters": [{"field":"status","operator":"eq","value":"open","logic":"AND"}],
                "keyword": "urgent",
                "density": "compact",
                "forcedRemote": true,
                "revision": "grid-rev-4"
              }
            }
            """);

        await controller.DispatchAsync(new RoutedWebRequest(
            "gridState.saveRequested",
            "save-1",
            save.RootElement.Clone(),
            string.Empty));
        await Task.Delay(GridStateCoordinator.SaveDebounceMs + 100);

        var saved = gateway.SavedGridStates.Single();
        IReadOnlyList<ColumnState> columns = saved.State.Columns!;
        IReadOnlyList<SortCondition> sorts = saved.State.Sorts!;
        IReadOnlyList<FilterCondition> filters = saved.State.Filters!;
        Assert.AreEqual("database-1", saved.DatabaseId);
        Assert.AreEqual("records", saved.Table);
        Assert.AreEqual("title", columns.Single().Name);
        Assert.AreEqual(240, columns.Single().Width);
        Assert.AreEqual("title", sorts.Single().Field);
        Assert.AreEqual("status", filters.Single().Field);
        Assert.AreEqual("urgent", saved.State.Keyword);
        Assert.AreEqual("compact", saved.State.Density);
        Assert.IsTrue(saved.State.ForcedRemote);
        Assert.AreEqual("grid-rev-4", saved.Revision);
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
