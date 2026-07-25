using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class JsonRpcProductDataGatewayTests
{
    [TestMethod]
    public async Task ClosedProductMethodsNeverEmitRetiredProviderOrSecrets()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        JsonElement payload = JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement.Clone();

        await gateway.ValidateSchemaAsync(payload, CancellationToken.None);
        await gateway.ApplySchemaAsync(payload, CancellationToken.None);
        await gateway.DeleteSchemaAsync(payload, CancellationToken.None);
        await gateway.ListTablesAsync(payload, CancellationToken.None);
        await gateway.GetTableSchemaAsync(payload, CancellationToken.None);
        await gateway.QueryPageAsync(payload, CancellationToken.None);
        await gateway.ReadRowsAsync(payload, CancellationToken.None);
        await gateway.ValidateSnapshotAsync(payload, CancellationToken.None);
        await gateway.PreviewMutationAsync(payload, CancellationToken.None);
        await gateway.ApplyMutationAsync(payload, CancellationToken.None);
        await gateway.ValidateFormulaAsync(payload, CancellationToken.None);
        await gateway.PreviewFormulaAsync(payload, CancellationToken.None);
        await gateway.ListAttachmentRefsAsync(payload, CancellationToken.None);
        await gateway.CreateFileTokenAsync(payload, CancellationToken.None);
        await gateway.ApplyHostAttachmentChangeAsync(payload, CancellationToken.None);
        await gateway.SaveAttachmentToHostAsync(payload, CancellationToken.None);
        await gateway.ReconcileAsync(payload, CancellationToken.None);
        await gateway.ListIdentifierMappingsAsync(payload, CancellationToken.None);
        await gateway.UpdateIdentifierAliasesAsync(payload, CancellationToken.None);
        await gateway.ReconcileIdentifierMappingsAsync(payload, CancellationToken.None);
        await gateway.ListPresetsAsync(payload, CancellationToken.None);
        await gateway.SavePresetAsync(payload, CancellationToken.None);
        await gateway.DeletePresetAsync(payload, CancellationToken.None);
        await gateway.ListVersionsAsync(payload, CancellationToken.None);
        await gateway.CreateVersionAsync(payload, CancellationToken.None);
        await gateway.SaveVersionAsync(payload, CancellationToken.None);
        await gateway.CompareVersionAsync(payload, CancellationToken.None);
        await gateway.PromoteVersionAsync(payload, CancellationToken.None);
        await gateway.DeleteVersionAsync(payload, CancellationToken.None);
        await gateway.ListBackupsAsync(payload, CancellationToken.None);
        await gateway.CreateBackupAsync(payload, CancellationToken.None);
        await gateway.RestoreBackupAsync(payload, CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "schema.validate", "schema.apply", "schema.delete", "schema.list",
                "schema.getTable",
                "query.page", "query.readRows", "query.validateSnapshot",
                "mutation.preview", "mutation.apply",
                "formula.validate", "formula.preview",
                "file.list", "file.token",
                "file.applyHostChange", "file.saveHostFile",
                "events.reconcile",
                "identifier.list", "identifier.updateAliases", "identifier.reconcile",
                "preset.list", "preset.save", "preset.delete",
                "version.list", "version.create", "version.save", "version.compare",
                "version.promote", "version.delete",
                "backup.list", "backup.create", "backup.restore",
            },
            transport.Methods);
        Assert.IsFalse(transport.Serialized.Contains(
            "dire" + "ctus.", StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(transport.Serialized.Contains("sessionSecret", StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(transport.Serialized.Contains("pocketbase", StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task CancellationIsPropagatedBeforeTransportWrite()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        using var cancelled = new CancellationTokenSource();
        cancelled.Cancel();

        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() =>
            gateway.QueryPageAsync(
                JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement.Clone(),
                cancelled.Token));
        Assert.HasCount(0, transport.Methods);
    }

    [TestMethod]
    public async Task DataChangedNotificationUsesFrozenProductEnvelope()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var received = new TaskCompletionSource<DataChangedEvent>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.DataChanged += value => received.TrySetResult(value);

        transport.EnqueueNotification(
            "data.changed",
            """
            {"contractVersion":"1.0","topic":"data.changed","eventId":"evt_1","sequence":12,
             "occurredAt":"2026-07-24T08:30:00Z","schemaRevision":"schema_0007",
             "dataRevision":"data_0012","changeSetId":"chg_1","tableId":"tbl_orders",
             "recordIds":["rec_1"],"operation":"update"}
            """);

        var change = await received.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("tbl_orders", change.TableId);
        CollectionAssert.AreEqual(new[] { "rec_1" }, change.RecordIds.ToArray());
    }

    [TestMethod]
    public async Task TaskChangedNotificationCrossesTheProductBoundary()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var received = new TaskCompletionSource<JsonElement>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.TaskChanged += value => received.TrySetResult(value);

        transport.EnqueueNotification(
            "task.changed",
            """
            {"contractVersion":"1.0","topic":"task.changed","eventId":"evt_2","sequence":13,
             "occurredAt":"2026-07-24T08:31:00Z","taskId":"job_1",
             "taskType":"formulaBackfill","state":"running","progress":0.5,
             "cursor":"row:5000","error":null}
            """);

        JsonElement change = await received.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("job_1", change.GetProperty("taskId").GetString());
        Assert.AreEqual(0.5, change.GetProperty("progress").GetDouble());
    }

    private sealed class AutoRespondTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming = Channel.CreateUnbounded<JsonElement?>();
        public List<string> Methods { get; } = [];
        public string Serialized { get; private set; } = "";

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            Serialized += line;
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            Methods.Add(request.RootElement.GetProperty("method").GetString()!);
            using var response = JsonDocument.Parse(
                $"{{\"jsonrpc\":\"2.0\",\"id\":\"{id}\",\"result\":{{}}}}");
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
