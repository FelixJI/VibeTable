using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class JsonRpcDashboardGatewayTests
{
    [TestMethod]
    public async Task Gateway_UsesFrozenRpcNamesAndCamelCasePayloads()
    {
        var transport = new DashboardTransport();
        await using var client = new JsonRpcClient(transport);
        var gateway = new JsonRpcDashboardGateway(client);

        await gateway.ListDashboardsAsync(CancellationToken.None);
        AssertMethod(transport, "directus.listDashboards");

        await gateway.ReadDashboardWorkspaceAsync("dash-1", CancellationToken.None);
        AssertMethod(transport, "directus.readDashboardWorkspace");
        Assert.AreEqual("dash-1", transport.LastRequest.GetProperty("params").GetProperty("dashboardId").GetString());

        var draft = Draft();
        await gateway.SaveDashboardDraftAsync(draft, CancellationToken.None);
        AssertMethod(transport, "directus.saveDashboardDraft");
        Assert.AreEqual(draft.IdempotencyKey,
            transport.LastRequest.GetProperty("params").GetProperty("idempotencyKey").GetString());

        await gateway.DeleteDashboardAsync("dash-1", CancellationToken.None);
        AssertMethod(transport, "directus.deleteDashboardWorkspace");
        Assert.AreEqual("dash-1", transport.LastRequest.GetProperty("params").GetProperty("dashboardId").GetString());

        await gateway.ExecuteDashboardQueryAsync(
            new ExecuteDashboardQueryParams(
                PanelTypes.Metric,
                new DashboardRecordQuery("orders", ["id"]),
                "web-request-7"),
            CancellationToken.None);
        AssertMethod(transport, "directus.executeDashboardQuery");
        var query = transport.LastRequest.GetProperty("params").GetProperty("query");
        Assert.AreEqual("records", query.GetProperty("kind").GetString());
        Assert.AreEqual("web-request-7",
            transport.LastRequest.GetProperty("params").GetProperty("requestId").GetString());

        await gateway.GetDashboardQueryLimitsAsync(CancellationToken.None);
        AssertMethod(transport, "directus.dashboardQueryLimits");
        await gateway.GetPanelManifestAsync(CancellationToken.None);
        AssertMethod(transport, "directus.panelManifest");
    }

    private static void AssertMethod(DashboardTransport transport, string expected)
        => Assert.AreEqual(expected, transport.LastRequest.GetProperty("method").GetString());

    private static SaveDashboardDraftParams Draft()
        => new(
            "Sales", "", [], [], new DashboardManagedConfig(),
            "123e4567-e89b-42d3-a456-426614174000");

    private sealed class DashboardTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming = Channel.CreateUnbounded<JsonElement?>();
        public JsonElement LastRequest { get; private set; }

        public Task<JsonElement?> ReadAsync(CancellationToken token)
            => _incoming.Reader.ReadAsync(token).AsTask();

        public Task WriteAsync(string line, CancellationToken token)
        {
            using var request = JsonDocument.Parse(line);
            LastRequest = request.RootElement.Clone();
            string id = LastRequest.GetProperty("id").GetString()!;
            string method = LastRequest.GetProperty("method").GetString()!;
            string result = method switch
            {
                "directus.listDashboards" => """{"dashboards":[]}""",
                "directus.readDashboardWorkspace" => WorkspaceJson,
                "directus.saveDashboardDraft" => $$"""{"workspace":{{WorkspaceJson}},"clientPanelIds":{},"atomic":true}""",
                "directus.deleteDashboardWorkspace" => """{"deleted":"dash-1"}""",
                "directus.executeDashboardQuery" => """{"rows":[],"truncated":false,"maxPoints":100000}""",
                "directus.dashboardQueryLimits" => LimitsJson,
                "directus.panelManifest" => """{"manifestVersion":"v2","directusCompatibility":">=12 <13","panels":[]}""",
                _ => "{}",
            };
            using var response = JsonDocument.Parse(
                $$"""{"jsonrpc":"2.0","id":"{{id}}","result":{{result}}}""");
            _incoming.Writer.TryWrite(response.RootElement.Clone());
            return Task.CompletedTask;
        }

        private const string LimitsJson =
            """{"maxConcurrentRequests":6,"maxSeriesPoints":50000,"maxPanelPoints":100000,"maxCategoryPoints":5000,"defaultTopN":100,"maxPieSlices":50,"maxListRows":100}""";
        private const string WorkspaceJson =
            """{"dashboard":{"id":"dash-1","name":"Sales","note":"","panels":[]},"config":{"configVersion":1,"globalFilters":[],"interactions":[],"refreshInterval":0},"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","atomicSaveEndpoint":"vibetable-dashboard-atomic.v1","queryLimits":{"maxConcurrentRequests":6,"maxSeriesPoints":50000,"maxPanelPoints":100000,"maxCategoryPoints":5000,"defaultTopN":100,"maxPieSlices":50,"maxListRows":100}}""";

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
