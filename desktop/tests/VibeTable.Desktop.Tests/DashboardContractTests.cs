using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DashboardContractTests
{
    private static readonly JsonSerializerOptions Web = new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void Draft_RoundTripsDiscriminatedAggregateQueryAsCamelCase()
    {
        var draft = new SaveDashboardDraftParams(
            Name: "Sales",
            Note: "",
            Panels:
            [
                new DashboardPanelDraft(
                    ClientId: "client-1",
                    Name: "Revenue",
                    Type: PanelTypes.Line,
                    Position: new PanelPosition(0, 0, 6, 4),
                    Options: new Dictionary<string, object?>(),
                    Query: new DashboardAggregateQuery(
                        Collection: "orders",
                        Measures: [new DashboardMeasure("total", DashboardAggregates.Sum, "amount")],
                        Dimensions: ["region"],
                        TimeBucket: new DashboardTimeBucket("date_created", "day")))
            ],
            DeletedPanelIds: [],
            Config: new DashboardManagedConfig(RefreshInterval: 60),
            IdempotencyKey: "123e4567-e89b-42d3-a456-426614174000");

        string json = JsonSerializer.Serialize(draft, Web);
        using var document = JsonDocument.Parse(json);
        var query = document.RootElement.GetProperty("panels")[0].GetProperty("query");
        Assert.AreEqual("aggregate", query.GetProperty("kind").GetString());
        Assert.AreEqual("date_created", query.GetProperty("timeBucket").GetProperty("field").GetString());
        Assert.IsFalse(document.RootElement.GetProperty("config").TryGetProperty("globalFilters", out _));
        Assert.IsFalse(document.RootElement.GetProperty("config").TryGetProperty("interactions", out _));

        var restored = JsonSerializer.Deserialize<SaveDashboardDraftParams>(json, Web);
        Assert.IsInstanceOfType<DashboardAggregateQuery>(restored!.Panels[0].Query);
        Assert.AreEqual(60, restored.Config.RefreshInterval);
    }

    [TestMethod]
    public void Workspace_RoundTripsDashboardAndPanelPresentationFields()
    {
        const string json =
            """
            {
              "dashboard": {
                "id": "123e4567-e89b-42d3-a456-426614174001",
                "name": "Sales",
                "note": "Executive view",
                "icon": "insights",
                "color": "#2563eb",
                "panels": [{
                  "id": "123e4567-e89b-42d3-a456-426614174002",
                  "dashboardId": "123e4567-e89b-42d3-a456-426614174001",
                  "name": "Revenue",
                  "type": "line",
                  "position": { "x": 0, "y": 0, "width": 6, "height": 4 },
                  "options": {},
                  "query": {},
                  "note": "Net revenue",
                  "icon": "paid",
                  "color": "#16a34a",
                  "showHeader": false
                }]
              },
              "config": { "configVersion": 1, "globalFilters": [], "interactions": [], "refreshInterval": 0 },
              "revision": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
              "atomicSaveEndpoint": "vibetable-dashboard-atomic.v1",
              "queryLimits": { "maxConcurrentRequests": 6, "maxSeriesPoints": 50000, "maxPanelPoints": 100000, "maxCategoryPoints": 5000, "defaultTopN": 100, "maxPieSlices": 50, "maxListRows": 100 }
            }
            """;

        var workspace = JsonSerializer.Deserialize<DashboardWorkspaceResult>(json, Web);
        Assert.IsNotNull(workspace);
        Assert.AreEqual("insights", workspace.Dashboard.Icon);
        Assert.AreEqual("#2563eb", workspace.Dashboard.Color);
        Assert.AreEqual("Net revenue", workspace.Dashboard.Panels[0].Note);
        Assert.AreEqual("paid", workspace.Dashboard.Panels[0].Icon);
        Assert.AreEqual("#16a34a", workspace.Dashboard.Panels[0].Color);
        Assert.IsFalse(workspace.Dashboard.Panels[0].ShowHeader);

        string restored = JsonSerializer.Serialize(workspace, Web);
        using var document = JsonDocument.Parse(restored);
        var panel = document.RootElement.GetProperty("dashboard").GetProperty("panels")[0];
        Assert.AreEqual("Net revenue", panel.GetProperty("note").GetString());
        Assert.IsFalse(panel.GetProperty("showHeader").GetBoolean());
    }

    [TestMethod]
    public void PanelTypes_ContainAllNineProductTypes()
    {
        string[] values =
        [
            PanelTypes.Label, PanelTypes.Metric, PanelTypes.MetricList,
            PanelTypes.List, PanelTypes.TimeSeries, PanelTypes.Bar,
            PanelTypes.Line, PanelTypes.Donut, PanelTypes.Pie,
        ];
        Assert.AreEqual(9, values.Distinct().Count());
    }
}
