using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HistoryBridgeTests
{
    [TestMethod]
    public void Router_AllowsOnlyTheNamedHistoryRequestsAndNotifications()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(dispatched.Add) { IsReady = true };
        string[] requestTypes =
        [
            "history.queryRequested",
            "history.previewRestoreRequested",
            "history.applyRestoreRequested",
        ];

        foreach (string type in requestTypes)
        {
            var failure = router.Route(JsonSerializer.Serialize(new
            {
                type,
                requestId = type,
                payload = new { },
            }));
            Assert.IsNull(failure, type);
        }

        CollectionAssert.AreEqual(requestTypes, dispatched.Select(item => item.Type).ToArray());
        Assert.IsTrue(router.IsHostNotificationAllowed("history.pageLoaded"));
        Assert.IsTrue(router.IsHostNotificationAllowed("history.restorePreviewReady"));
        Assert.IsTrue(router.IsHostNotificationAllowed("history.restoreApplied"));
        Assert.IsFalse(router.IsHostNotificationAllowed("history.rawRevisionLoaded"));
    }

    [TestMethod]
    public async Task Query_ForwardsTableSearchFiltersAndPaging()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var payload = Parse("""
            {
              "collection":"projects","scope":"table","field":"status",
              "search":"alpha","dateFrom":"2026-01-01T00:00:00Z",
              "dateTo":"2026-07-22T00:00:00Z","actorId":"u-1",
              "actions":["update","archive"],"recordId":"p-7",
              "limit":75,"offset":150
            }
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "history.queryRequested", "query-1", payload, ""));

        var reply = await sink.WaitForAsync("history.pageLoaded");
        Assert.IsNotNull(reply);
        Assert.AreEqual("query-1", reply!.RequestId);
        var parameters = gateway.ReadChangeSetsCalls.Single();
        Assert.AreEqual("projects", parameters.Collection);
        Assert.AreEqual("table", parameters.Scope);
        Assert.IsNull(parameters.ItemId);
        Assert.AreEqual("status", parameters.Field);
        Assert.AreEqual("alpha", parameters.Search);
        Assert.AreEqual("2026-01-01T00:00:00Z", parameters.DateFrom);
        Assert.AreEqual("2026-07-22T00:00:00Z", parameters.DateTo);
        Assert.AreEqual("u-1", parameters.ActorId);
        CollectionAssert.AreEqual(new[] { "update", "archive" }, parameters.Actions!.ToArray());
        Assert.AreEqual("p-7", parameters.RecordId);
        Assert.AreEqual(75, parameters.Limit);
        Assert.AreEqual(150, parameters.Offset);
    }

    [TestMethod]
    [DataRow("row", "p-1", null)]
    [DataRow("cell", "p-1", "status")]
    [DataRow("archived", null, null)]
    public async Task Query_AcceptsEachSelectableScope(
        string scope,
        string? itemId,
        string? field)
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var payload = Parse(JsonSerializer.Serialize(new
        {
            table = "projects",
            scope,
            itemId,
            field,
            limit = 50,
            offset = 0,
        }));

        dispatcher.Dispatch(new RoutedWebRequest(
            "history.queryRequested", $"query-{scope}", payload, ""));

        Assert.IsNotNull(await sink.WaitForAsync("history.pageLoaded"));
        Assert.AreEqual(scope, gateway.ReadChangeSetsCalls.Single().Scope);
    }

    [TestMethod]
    [DataRow("row", null)]
    [DataRow("cell", "status")]
    [DataRow("archived", null)]
    public async Task PreviewRestore_ForwardsRowCellAndArchivedScopes(
        string scope,
        string? field)
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var payload = Parse(JsonSerializer.Serialize(new
        {
            collection = "projects",
            itemId = "p-1",
            targetRevision = "rev-7",
            scope,
            field,
        }));

        dispatcher.Dispatch(new RoutedWebRequest(
            "history.previewRestoreRequested", $"preview-{scope}", payload, ""));

        var reply = await sink.WaitForAsync("history.restorePreviewReady");
        Assert.IsNotNull(reply);
        Assert.AreEqual($"preview-{scope}", reply!.RequestId);
        var parameters = gateway.PreviewRestoreCalls.Single();
        Assert.AreEqual(scope, parameters.Scope);
        Assert.AreEqual(field, parameters.Field);
        Assert.AreEqual("rev-7", parameters.TargetRevision);
    }

    [TestMethod]
    public async Task ApplyRestore_ForwardsPreviewTokenAndReturnsApplyResult()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        gateway.NextRestoreResult = new RestoreResult(
            "projects", "p-1", "rev-7", "rev-8",
            new Dictionary<string, object?> { ["status"] = "draft" });
        var payload = Parse("""
            {"table":"projects","itemId":"p-1","token":"preview-token"}
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "history.applyRestoreRequested", "apply-1", payload, ""));

        var reply = await sink.WaitForAsync("history.restoreApplied");
        Assert.IsNotNull(reply);
        Assert.AreEqual("apply-1", reply!.RequestId);
        var parameters = gateway.ApplyRestoreCalls.Single();
        Assert.AreEqual("preview-token", parameters.Token);
        var result = (RestoreResult)reply.Payload!;
        Assert.AreEqual("rev-8", result.NewRevisionId);
        Assert.AreEqual("draft", result.Item["status"]);
    }

    [TestMethod]
    public async Task CellQueryWithoutField_IsRejectedBeforeGateway()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var payload = Parse("""
            {"collection":"projects","scope":"cell","itemId":"p-1"}
            """);

        dispatcher.Dispatch(new RoutedWebRequest(
            "history.queryRequested", "bad-cell", payload, ""));

        var reply = await sink.WaitForFailedAsync();
        Assert.IsNotNull(reply);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)reply!.Payload!).code);
        Assert.AreEqual(0, gateway.ReadChangeSetsCalls.Count);
    }

    [TestMethod]
    [DataRow("history_not_allowed", "history_not_allowed")]
    [DataRow("history_field_unreadable", "history_field_unreadable")]
    [DataRow("archive_not_supported", "archive_not_supported")]
    [DataRow("restore_token_expired", "restore_token_expired")]
    [DataRow("restore_scope_mismatch", "restore_scope_mismatch")]
    [DataRow("restore_conflict", "restore_conflict")]
    [DataRow("schema_drift", "schema_drift")]
    [DataRow("restore_no_fields", "restore_no_fields")]
    [DataRow("target_revision_invalid", "target_revision_invalid")]
    [DataRow("relation_target_unavailable", "relation_target_unavailable")]
    [DataRow("revision_not_created", "revision_not_created")]
    public void ErrorMapper_UsesStableRendererCodes(string backendCode, string expectedCode)
    {
        var failure = HistoryErrorMapper.MapBackendCode(
            backendCode, "HISTORY_APPLY_FAILED");

        Assert.AreEqual(expectedCode, failure.Code);
        Assert.IsFalse(string.IsNullOrWhiteSpace(failure.Message));
    }

    private static (WorkspaceRequestDispatcher Dispatcher, FakeTableRpcGateway Gateway, FakeWebReplySink Sink)
        CreateDispatcher()
    {
        var gateway = new FakeTableRpcGateway();
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(gateway),
            new FakeDatabasePicker("directus://configured"),
            sink);
        return (dispatcher, gateway, sink);
    }

    private static JsonElement Parse(string json)
    {
        using var document = JsonDocument.Parse(json);
        return document.RootElement.Clone();
    }
}
