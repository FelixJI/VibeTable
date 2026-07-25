using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceRequestDispatcherQueryTests
{
    [TestMethod]
    public async Task TableQuery_ForwardsTypedFiltersAndSortsToGateway()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryTablePageResults["records"] = new TablePage(
            "records",
            Array.Empty<ColumnSchema>(),
            Array.Empty<Dictionary<string, object?>>(),
            0,
            500,
            1,
            "server");
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "keyword": "needle",
                "filters": [
                  {
                    "field": "payload",
                    "operator": "contains",
                    "value": "8",
                    "logic": "AND",
                    "ignored": "not-forwarded"
                  },
                  {
                    "field": "metadata",
                    "operator": "in",
                    "value": [{"rank": 2}, 3, true]
                  }
                ],
                "sorts": [
                  {"field": "payload", "direction": "desc", "nullsLast": false}
                ],
                "offset": 25,
                "limit": 500,
                "ignored": "not-forwarded"
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-1",
            document.RootElement.Clone(),
            ""));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        var query = gateway.QueryTablePageQueries.Single();
        Assert.AreEqual("needle", query.Keyword);
        Assert.AreEqual(25, query.Offset);
        Assert.AreEqual(500, query.Limit);
        Assert.AreEqual(2, query.Filters?.Count);
        Assert.AreEqual("payload", query.Filters![0].Field);
        Assert.AreEqual(FilterOperators.Contains, query.Filters[0].Operator);
        Assert.AreEqual("8", query.Filters[0].Value);
        Assert.AreEqual("AND", query.Filters[0].Logic);
        var compositeValue = (object?[])query.Filters[1].Value!;
        var objectValue = (Dictionary<string, object?>)compositeValue[0]!;
        Assert.AreEqual(2L, objectValue["rank"]);
        Assert.AreEqual(3L, compositeValue[1]);
        Assert.AreEqual(true, compositeValue[2]);
        Assert.AreEqual(1, query.Sorts?.Count);
        Assert.AreEqual("payload", query.Sorts![0].Field);
        Assert.AreEqual("desc", query.Sorts[0].Direction);
        Assert.IsFalse(query.Sorts[0].NullsLast);
    }

    [TestMethod]
    public async Task TableQuery_DropsUnknownOperatorsAndUnknownObjectFields()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("local://configured"),
            new FakeWebReplySink(),
            coordinator);
        using var document = JsonDocument.Parse("""
            {
              "table": "records",
              "query": {
                "filters": [
                  {"field": "payload", "operator": "raw_sql", "value": "x"}
                ]
              }
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "table.queryRequested",
            "query-2",
            document.RootElement.Clone(),
            ""));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        var query = gateway.QueryTablePageQueries.Single();
        Assert.AreEqual(0, query.Filters?.Count);
    }
}
