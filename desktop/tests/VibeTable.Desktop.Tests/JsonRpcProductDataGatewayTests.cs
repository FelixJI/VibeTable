using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class JsonRpcProductDataGatewayTests
{
    [TestMethod]
    public async Task ClosedProductMethodsNeverEmitRetiredProviderOrSecrets()
    {
        var transport = new AutoRespondTransport();
        transport.Results["schema.getTable"] =
            """
            {
              "contract":"vibetable.schema.v2",
              "tableId":"tbl_orders",
              "displayName":"Orders",
              "kind":"base",
              "schemaRevision":"schema_1",
              "dataRevision":0,
              "archivePolicy":{"mode":"none","fieldId":null,"archivedValue":null},
              "fields":[],
              "capabilities":[]
            }
            """;
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        JsonElement payload = JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement.Clone();

        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.DescribeFieldSettingsAsync(payload, CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.PlanFieldChangeAsync(payload, CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.ApplyFieldChangeAsync(payload, CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.GetFieldChangeStatusAsync(payload, CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.CancelFieldChangeAsync(payload, CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.ListRecycledFieldsAsync(payload, CancellationToken.None));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            gateway.CreateTableAsync(payload, CancellationToken.None));
        await gateway.DeleteSchemaAsync(payload, CancellationToken.None);
        await gateway.ListTablesAsync(payload, CancellationToken.None);
        await gateway.GetTableSchemaAsync(payload, CancellationToken.None);
        await gateway.QueryPageAsync(payload, CancellationToken.None);
        await gateway.OpenQueryCursorAsync(payload, CancellationToken.None);
        await gateway.OpenSelectionProjectionAsync(payload, CancellationToken.None);
        await gateway.FetchQueryCursorAsync(payload, CancellationToken.None);
        await gateway.QueryViewAsync(payload, CancellationToken.None);
        await gateway.ReadRowsAsync(payload, CancellationToken.None);
        await gateway.ValidateSnapshotAsync(payload, CancellationToken.None);
        await gateway.PreviewMutationAsync(payload, CancellationToken.None);
        await gateway.ApplyMutationAsync(payload, CancellationToken.None);
        await gateway.ValidateFormulaAsync(payload, CancellationToken.None);
        await gateway.ValidateFormulaDraftAsync(payload, CancellationToken.None);
        await gateway.PreviewFormulaAsync(payload, CancellationToken.None);
        await gateway.ListAttachmentRefsAsync(payload, CancellationToken.None);
        await gateway.CreateFileTokenAsync(payload, CancellationToken.None);
        await gateway.ApplyHostAttachmentChangeAsync(payload, CancellationToken.None);
        await gateway.SaveAttachmentToHostAsync(payload, CancellationToken.None);
        await gateway.ReconcileAsync(payload, CancellationToken.None);
        await gateway.ListPresetsAsync(payload, CancellationToken.None);
        await gateway.SavePresetAsync(payload, CancellationToken.None);
        await gateway.DeletePresetAsync(payload, CancellationToken.None);
        await gateway.ListVersionsAsync(payload, CancellationToken.None);
        await gateway.CreateVersionAsync(payload, CancellationToken.None);
        await gateway.SaveVersionAsync(payload, CancellationToken.None);
        await gateway.CompareVersionAsync(payload, CancellationToken.None);
        await gateway.PromoteVersionAsync(payload, CancellationToken.None);
        await gateway.DeleteVersionAsync(payload, CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "field.settings.describe", "field.change.plan", "field.change.apply",
                "field.change.status", "field.change.cancel", "field.recycleBin.list",
                "schema.table.create", "schema.delete", "schema.list",
                "schema.getTable",
                "query.page", "query.cursorOpen", "query.selectionOpen", "query.cursorFetch", "query.view",
                "query.readRows", "query.validateSnapshot",
                "mutation.preview", "mutation.apply",
                "formula.validate", "formula.draft.validate", "formula.preview",
                "file.list", "file.token",
                "file.applyHostChange", "file.saveHostFile",
                "events.reconcile",
                "preset.list", "preset.save", "preset.delete",
                "version.list", "version.create", "version.save", "version.compare",
                "version.promote", "version.delete",
            },
            transport.Methods);
        Assert.IsFalse(transport.Serialized.Contains(
            "dire" + "ctus.", StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(transport.Serialized.Contains("sessionSecret", StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(transport.Serialized.Contains("pocketbase", StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task FieldSettingsDescribeUsesTheStrictV2ResultContract()
    {
        var transport = new AutoRespondTransport();
        transport.Results["field.settings.describe"] =
            """
            {"contract":"vibetable.schema.v2","tableId":"tbl_orders","fieldId":"",
             "schemaRevision":"schema_7","dataRevision":12,"definition":null,
             "capabilities":[],"recommendedDefaultsVersion":1}
            """;
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);

        JsonElement result = await gateway.DescribeFieldSettingsAsync(
            JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement.Clone(),
            CancellationToken.None);

        Assert.AreEqual("vibetable.schema.v2", result.GetProperty("contract").GetString());
        Assert.AreEqual(12, result.GetProperty("dataRevision").GetInt64());
        Assert.AreEqual(0, result.GetProperty("capabilities").GetArrayLength());
        Assert.AreEqual(JsonValueKind.Null, result.GetProperty("definition").ValueKind);
    }

    [TestMethod]
    public async Task FieldChangePlanAcceptsOmittedInapplicableSpecializedSettings()
    {
        var transport = new AutoRespondTransport();
        transport.Results["field.change.plan"] =
            """
            {
              "contract":"vibetable.schema.v2",
              "planId":"plan_text","planHash":"sha256:abc","expiresAt":"2026-07-28T11:00:00Z",
              "intent":{
                "action":"create","tableId":"tbl_orders","fieldId":"",
                "expectedSchemaRevision":"schema_7","expectedDataRevision":0,
                "draft":{
                  "displayName":"Title","help":"","logicalType":"text",
                  "value":{
                    "required":false,
                    "default":{"enabled":false,"value":null,"source":"recommended","defaultsVersion":1},
                    "presence":{"mode":"companion"}
                  },
                  "constraints":{
                    "unique":{"enabled":false,"blankPolicy":"ignoreMissing"},
                    "range":{"min":null,"max":null},
                    "length":{"min":null,"max":null},
                    "pattern":{"enabled":false,"value":""},
                    "domains":{"only":[],"except":[]},
                    "selection":{"min":0,"max":null}
                  },
                  "storage":{"kind":"pocketbase-text","options":{"onlyInt":false,"maxSize":0,"convertURLs":false,"presentable":false}},
                  "display":{
                    "kind":"text","preset":"plain","displayScale":0,"scaleMode":"fixed",
                    "trimTrailingZeros":false,"useGrouping":false,"currency":"",
                    "percentStorage":"ratio","unit":null,"precision":"exact","timezone":"",
                    "mode":"","trueLabel":"","falseLabel":""
                  }
                },
                "actor":{"id":"local-user","kind":"user"},
                "conversionRule":"","confirmation":"","backupReceipt":""
              },
              "before":null,
              "after":{
                "contract":"vibetable.schema.v2",
                "identity":{"fieldId":"fld_text","physicalName":"f_text","providerFieldId":"pb_text"},
                "displayName":"Title","help":"","logicalType":"text",
                "lifecycle":{"state":"active","retiredAt":null},
                "value":{
                  "required":false,
                  "default":{"enabled":false,"value":null,"source":"recommended","defaultsVersion":1},
                  "presence":{"mode":"companion","providerFieldId":"pb_presence","physicalName":"__vt_has_f_text"}
                },
                "constraints":{
                  "unique":{"enabled":false,"blankPolicy":"ignoreMissing"},
                  "range":{"min":null,"max":null},
                  "length":{"min":null,"max":null},
                  "pattern":{"enabled":false,"value":""},
                  "domains":{"only":[],"except":[]},
                  "selection":{"min":0,"max":null}
                },
                "storage":{"kind":"pocketbase-text","options":{"onlyInt":false,"maxSize":0,"convertURLs":false,"presentable":false}},
                "display":{
                  "kind":"text","preset":"plain","displayScale":0,"scaleMode":"fixed",
                  "trimTrailingZeros":false,"useGrouping":false,"currency":"",
                  "percentStorage":"ratio","unit":null,"precision":"exact","timezone":"",
                  "mode":"","trueLabel":"","falseLabel":""
                }
              },
              "classes":["schema"],"expectedSchemaRevision":"schema_7","expectedDataRevision":0,
              "impact":{"records":0,"missing":0,"ambiguous":0,"failures":[],"dependencies":[]},
              "steps":[],"warnings":[],"errors":[],"confirmations":[],
              "createsMigration":false,"canApply":true
            }
            """;
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);

        JsonElement result = await gateway.PlanFieldChangeAsync(
            JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement.Clone(),
            CancellationToken.None);

        Assert.AreEqual(
            "fld_text",
            result.GetProperty("after").GetProperty("identity").GetProperty("fieldId").GetString());
        Assert.IsFalse(
            result.GetProperty("after").TryGetProperty("select", out _),
            "nullable specialized fields must be omitted at the renderer boundary");
    }

    [TestMethod]
    public async Task FieldChangePlanAcceptsFormulaDraftWithoutCompiledResultType()
    {
        var transport = new AutoRespondTransport();
        transport.Results["field.change.plan"] =
            """
            {
              "contract":"vibetable.schema.v2",
              "planId":"plan_formula","planHash":"sha256:formula","expiresAt":"2026-08-05T16:00:00Z",
              "intent":{
                "action":"create","tableId":"tbl_orders","fieldId":"",
                "expectedSchemaRevision":"schema_7","expectedDataRevision":0,
                "draft":{
                  "displayName":"Upper title","help":"","logicalType":"formula",
                  "value":{
                    "required":false,
                    "default":{"enabled":false,"value":null,"source":"recommended","defaultsVersion":1},
                    "presence":{"mode":"none"}
                  },
                  "constraints":{
                    "unique":{"enabled":false,"blankPolicy":"ignoreMissing"},
                    "range":{"min":null,"max":null},
                    "length":{"min":null,"max":null},
                    "pattern":{"enabled":false,"value":""},
                    "domains":{"only":[],"except":[]},
                    "selection":{"min":0,"max":null}
                  },
                  "storage":{"kind":"computed","options":{"onlyInt":false,"maxSize":0,"convertURLs":false,"presentable":false}},
                  "display":{
                    "kind":"text","preset":"plain","displayScale":0,"scaleMode":"fixed",
                    "trimTrailingZeros":false,"useGrouping":false,"currency":"",
                    "percentStorage":"ratio","unit":null,"precision":"exact","timezone":"",
                    "mode":"","trueLabel":"","falseLabel":""
                  },
                  "formula":{"language":"cel-v1","source":"upper(f_title)"}
                },
                "actor":{"id":"local-user","kind":"user"},
                "conversionRule":"","confirmation":"","backupReceipt":""
              },
              "before":null,
              "after":{
                "contract":"vibetable.schema.v2",
                "identity":{"fieldId":"fld_formula","physicalName":"f_formula","providerFieldId":""},
                "displayName":"Upper title","help":"","logicalType":"formula",
                "lifecycle":{"state":"active","retiredAt":null},
                "value":{
                  "required":false,
                  "default":{"enabled":false,"value":null,"source":"recommended","defaultsVersion":1},
                  "presence":{"mode":"none"}
                },
                "constraints":{
                  "unique":{"enabled":false,"blankPolicy":"ignoreMissing"},
                  "range":{"min":null,"max":null},
                  "length":{"min":null,"max":null},
                  "pattern":{"enabled":false,"value":""},
                  "domains":{"only":[],"except":[]},
                  "selection":{"min":0,"max":null}
                },
                "storage":{"kind":"computed","options":{"onlyInt":false,"maxSize":0,"convertURLs":false,"presentable":false}},
                "display":{
                  "kind":"text","preset":"plain","displayScale":0,"scaleMode":"fixed",
                  "trimTrailingZeros":false,"useGrouping":false,"currency":"",
                  "percentStorage":"ratio","unit":null,"precision":"exact","timezone":"",
                  "mode":"","trueLabel":"","falseLabel":""
                },
                "formula":{"language":"cel-v1","source":"upper(f_title)","resultType":"text"}
              },
              "classes":["schema"],"expectedSchemaRevision":"schema_7","expectedDataRevision":0,
              "impact":{"records":0,"missing":0,"ambiguous":0,"failures":[],"dependencies":[]},
              "steps":[],"warnings":[],"errors":[],"confirmations":[],
              "createsMigration":false,"canApply":true
            }
            """;
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);

        JsonElement result = await gateway.PlanFieldChangeAsync(
            JsonDocument.Parse("""{"tableId":"tbl_orders"}""").RootElement.Clone(),
            CancellationToken.None);

        JsonElement draftFormula = result
            .GetProperty("intent")
            .GetProperty("draft")
            .GetProperty("formula");
        Assert.AreEqual("upper(f_title)", draftFormula.GetProperty("source").GetString());
        Assert.IsFalse(draftFormula.TryGetProperty("resultType", out _));
        Assert.AreEqual(
            "text",
            result.GetProperty("after").GetProperty("formula").GetProperty("resultType").GetString());
    }

    [TestMethod]
    public async Task SchemaListCancellationIsPropagatedBeforeTransportWrite()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        using var cancelled = new CancellationTokenSource();
        cancelled.Cancel();

        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() =>
            gateway.ListTablesAsync(
                JsonDocument.Parse("{}").RootElement.Clone(),
                cancelled.Token));
        Assert.HasCount(0, transport.Methods);
    }

    [TestMethod]
    public async Task DataChangedNotificationUsesFrozenProductEnvelope()
    {
        await using var fixture = new NotificationBindingFixture();
        JsonRpcProductDataGateway gateway = fixture.Gateway;
        var received = new TaskCompletionSource<DataChangedEvent>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.DataChanged += value => received.TrySetResult(value);

        fixture.Transport.EnqueueNotification(
            "data.changed",
            """
            {"contractVersion":"2.0","topic":"data.changed","eventId":"evt_1","sequence":12,
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
        await using var fixture = new NotificationBindingFixture();
        JsonRpcProductDataGateway gateway = fixture.Gateway;
        var received = new TaskCompletionSource<JsonElement>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.TaskChanged += value => received.TrySetResult(value);

        fixture.Transport.EnqueueNotification(
            "task.changed",
            """
            {"contractVersion":"2.0","topic":"task.changed","eventId":"evt_2","sequence":13,
             "occurredAt":"2026-07-24T08:31:00Z","taskId":"job_1",
             "taskType":"formulaBackfill","state":"running","progress":0.5,
             "cursor":"row:5000","error":null}
            """);

        JsonElement change = await received.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("job_1", change.GetProperty("taskId").GetString());
        Assert.AreEqual(0.5, change.GetProperty("progress").GetDouble());
    }

    private sealed class NotificationBindingFixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(), "vibetable-notifications-" + Guid.NewGuid().ToString("N"));
        private readonly ProductionWorkspaceRuntimeFactory _runtime = new(
            new PocketBaseLaunchOptions { ExecutablePath = "unused", DataDirectory = "unused" }, new BackendLaunchOptions());
        private readonly WorkspaceSessionManager _sessions;
        private readonly WorkspaceSessionEnvelopeFilter _leases;
        private readonly JsonRpcClient _client;
        internal AutoRespondTransport Transport { get; } = new();
        internal JsonRpcProductDataGateway Gateway { get; }

        internal NotificationBindingFixture()
        {
            _sessions = new(new WorkspaceRegistry(_root), _runtime);
            _leases = new(_sessions);
            _client = new(Transport);
            var snapshot = new ProductSidecarGenerationSnapshot(_runtime, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:12345/bootstrap"),
                    new Uri("http://127.0.0.1:12345/"), "X-VibeTable-Session", "test-session"),
                new ProductSidecarIdentity(Guid.NewGuid().ToString("D"), 1, 1, Guid.NewGuid().ToString("D")), []);
            // No RPC admission: this fixture tests only the paired client's notification path.
            var binding = new HostProductRpcBinding(_runtime, _client, snapshot, ProductRpcRouteSelector.Default, _ => false);
            Gateway = binding.CreateGateway(_leases);
        }

        public async ValueTask DisposeAsync()
        {
            Gateway.Dispose();
            await _client.DisposeAsync();
            _leases.Dispose();
            await _sessions.DisposeAsync();
            await _runtime.DisposeAsync();
            if (Directory.Exists(_root)) Directory.Delete(_root, recursive: true);
        }
    }

    private sealed class AutoRespondTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming = Channel.CreateUnbounded<JsonElement?>();
        public List<string> Methods { get; } = [];
        public Dictionary<string, string> Results { get; } = [];
        public string Serialized { get; private set; } = "";

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            Serialized += line;
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            Methods.Add(method);
            string result = Results.TryGetValue(method, out string? configured)
                ? configured
                : "{}";
            using var response = JsonDocument.Parse(
                $"{{\"jsonrpc\":\"2.0\",\"id\":\"{id}\",\"result\":{result}}}");
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
