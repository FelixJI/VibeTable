using System.Collections.Generic;
using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class JsonRpcDirectusGatewayTests
{
    [TestMethod]
    public async Task Login_UsesLocalRpcAndDeserializesOnlySafeSessionDto()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcDirectusGateway(client);

        var result = await gateway.LoginAsync(
            "user@example.test", "local-password", null, CancellationToken.None);

        Assert.AreEqual("authenticated", result.State);
        Assert.AreEqual("Test User", result.User?.DisplayName);
        Assert.IsFalse(JsonSerializer.Serialize(result).Contains("token", StringComparison.OrdinalIgnoreCase));
        Assert.AreEqual("directus.login", transport.LastRequest.GetProperty("method").GetString());
        Assert.AreEqual(
            "local-password",
            transport.LastRequest.GetProperty("params").GetProperty("password").GetString());
    }

    [TestMethod]
    public async Task DirectusChangedNotification_IsRoutedAsTypedEvent()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcDirectusGateway(client);
        var received = new TaskCompletionSource<DirectusChange>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.Changed += change => received.TrySetResult(change);

        transport.EnqueueNotification(
            "directus.changed",
            """
            {"uid":"projects-main","collection":"vibetable_demo","event":"update","data":[{"id":"1"}],"invalidateQuery":true}
            """);
        var change = await received.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("projects-main", change.Uid);
        Assert.AreEqual("vibetable_demo", change.Collection);
        Assert.IsTrue(change.InvalidateQuery);
    }

    [TestMethod]
    public async Task CreateTable_SendsTableAdminCreateTableWithCamelCaseParams()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcDirectusGateway(client);

        var result = await gateway.CreateTableAsync(
            "customers",
            new[] { new FieldDefinition("name", "string"), new FieldDefinition("age", "integer") },
            CancellationToken.None);

        Assert.AreEqual("customers", result.Collection);
        Assert.AreEqual("id", result.PrimaryKey);
        CollectionAssert.AreEqual(new List<string> { "id", "name", "age" }, new List<string>(result.Fields));
        Assert.AreEqual("table_admin.createTable", transport.LastRequest.GetProperty("method").GetString());

        var parameters = transport.LastRequest.GetProperty("params");
        Assert.AreEqual("customers", parameters.GetProperty("name").GetString());
        var fields = parameters.GetProperty("fields");
        Assert.AreEqual(2, fields.GetArrayLength());
        Assert.AreEqual("name", fields[0].GetProperty("key").GetString());
        Assert.AreEqual("string", fields[0].GetProperty("type").GetString());
        Assert.AreEqual("age", fields[1].GetProperty("key").GetString());
        Assert.AreEqual("integer", fields[1].GetProperty("type").GetString());
    }

    [TestMethod]
    public async Task DeleteTable_SendsTableAdminDeleteTableWithNameParam()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcDirectusGateway(client);

        var result = await gateway.DeleteTableAsync("customers", CancellationToken.None);

        Assert.AreEqual("customers", result.Collection);
        Assert.IsTrue(result.Deleted);
        Assert.AreEqual("table_admin.deleteTable", transport.LastRequest.GetProperty("method").GetString());
        Assert.AreEqual(
            "customers",
            transport.LastRequest.GetProperty("params").GetProperty("name").GetString());
    }

    [TestMethod]
    public async Task HistoryFlow_UsesTypedRpcMethodsAndCamelCasePayloads()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcDirectusGateway(client);

        var page = await gateway.ReadChangeSetsAsync(
            new ReadChangeSetsParams(
                "projects", null, 50, 100, "table", "status", "alpha",
                "2026-01-01T00:00:00Z", "2026-07-22T00:00:00Z", "u-1",
                new[] { "update", "archive" }, "p-1"),
            CancellationToken.None);

        Assert.AreEqual("history.readChangeSets", transport.LastRequest.GetProperty("method").GetString());
        var readParams = transport.LastRequest.GetProperty("params");
        Assert.AreEqual("projects", readParams.GetProperty("collection").GetString());
        Assert.AreEqual("table", readParams.GetProperty("scope").GetString());
        Assert.AreEqual("alpha", readParams.GetProperty("search").GetString());
        Assert.AreEqual(2, readParams.GetProperty("actions").GetArrayLength());
        Assert.AreEqual("table", page.Scope);
        Assert.IsTrue(page.HasMore);
        Assert.AreEqual(2, page.ChangeSets[0].AffectedRecords);
        Assert.AreEqual(2, page.ChangeSets[0].RecordChanges!.Count);

        var preview = await gateway.PreviewRestoreAsync(
            new PreviewRestoreParams("projects", "p-1", "rev-7", "cell", "status"),
            CancellationToken.None);

        Assert.AreEqual("history.previewRestore", transport.LastRequest.GetProperty("method").GetString());
        var previewParams = transport.LastRequest.GetProperty("params");
        Assert.AreEqual("cell", previewParams.GetProperty("scope").GetString());
        Assert.AreEqual("status", previewParams.GetProperty("field").GetString());
        Assert.IsTrue(preview.CanApply);
        CollectionAssert.AreEqual(new[] { "status" }, preview.RestorableFields!);

        var applied = await gateway.ApplyRestoreAsync(
            new ApplyRestoreParams("projects", "p-1", preview.Token),
            CancellationToken.None);

        Assert.AreEqual("history.applyRestore", transport.LastRequest.GetProperty("method").GetString());
        Assert.AreEqual("restore-token", transport.LastRequest.GetProperty("params").GetProperty("token").GetString());
        Assert.AreEqual("rev-8", applied.NewRevisionId);
        Assert.AreEqual("draft", Convert.ToString(applied.Item["status"]));
    }

    private sealed class AutoRespondTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming = Channel.CreateUnbounded<JsonElement?>();

        public JsonElement LastRequest { get; private set; }

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            LastRequest = request.RootElement.Clone();
            string id = LastRequest.GetProperty("id").GetString()!;
            string method = LastRequest.GetProperty("method").GetString()!;
            string result = method switch
            {
                "directus.login" => """{"state":"authenticated","expiresAt":1234,"user":{"id":"u1","displayName":"Test User","capabilities":[]}}""",
                "table_admin.createTable" => """{"collection":"customers","primaryKey":"id","fields":["id","name","age"]}""",
                "table_admin.deleteTable" => """{"collection":"customers","deleted":true}""",
                "history.readChangeSets" => """
                    {"collection":"projects","itemId":null,"scope":"table","field":"status","changeSets":[{"rootRevisionId":"rev-7","activityId":"act-1","action":"update","timestamp":"2026-07-22T00:00:00Z","actor":{"userId":"u-1","displayName":"User"},"scalarChanges":[],"relationChanges":[],"itemId":null,"recordLabel":null,"revisionIds":["rev-7","rev-6"],"affectedRecords":2,"recordChanges":[{"revisionId":"rev-7","itemId":"p-1","recordLabel":"Alpha","action":"update","scalarChanges":[],"relationChanges":[]},{"revisionId":"rev-6","itemId":"p-2","recordLabel":"Beta","action":"update","scalarChanges":[],"relationChanges":[]}]}],"total":2,"capabilityHash":"cap","schemaRevision":"schema-1","hasMore":true}
                    """,
                "history.previewRestore" => """
                    {"collection":"projects","itemId":"p-1","targetRevision":"rev-7","currentHash":"current","schemaRevision":"schema-1","scalarChanges":[{"field":"status","before":"published","after":"draft"}],"relationChanges":[],"diagnostics":[],"token":"restore-token","expiresAt":"2099-01-01T00:00:00Z","scope":"cell","field":"status","canApply":true,"restorableFields":["status"]}
                    """,
                "history.applyRestore" => """
                    {"collection":"projects","itemId":"p-1","restoredToRevision":"rev-7","newRevisionId":"rev-8","item":{"id":"p-1","status":"draft"}}
                    """,
                _ => "{}",
            };
            using var response = JsonDocument.Parse(
                $$"""{"jsonrpc":"2.0","id":"{{id}}","result":{{result}}}""");
            _incoming.Writer.TryWrite(response.RootElement.Clone());
            return Task.CompletedTask;
        }

        public void EnqueueNotification(string method, string parameters)
        {
            using var notification = JsonDocument.Parse(
                $$"""{"jsonrpc":"2.0","method":"{{method}}","params":{{parameters}}}""");
            _incoming.Writer.TryWrite(notification.RootElement.Clone());
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
