using System.Collections.Concurrent;
using System.Net;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceApiTests
{
    internal const string DatasetBody = """{"contract":"vibetable.plugin-dataset.v1","scope":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"orders","fields":["id","amount"]},"groupBy":[],"sums":[]}],"versions":{"orders":{"schemaRevision":"s1","dataRevision":1,"clockPeriod":"","computed":{}}},"sources":[{"collection":"orders","fields":["id","amount"],"computedFields":[],"items":[{"id":"a","amount":"9007199254740993.01"},{"id":"b","amount":"0.01"}],"filteredRows":2,"totalRows":2,"complete":true,"summary":{"count":2,"complete":true,"sums":[{"field":"amount","value":"9007199254740993.02","complete":true}],"groups":[]}}]}""";
    private const string DatasetRequest = """{"contract":"vibetable.plugin-dataset.v1","sources":[]}""";
    internal const string SnapshotBody = """{"contract":"vibetable.plugin-snapshot.v1","versions":{"orders":{"schemaRevision":"s1","dataRevision":1,"clockPeriod":"","computed":{}},"notes":{"schemaRevision":"s1","dataRevision":0,"clockPeriod":"","computed":{}}},"sources":[{"collection":"orders","fields":["id","amount"],"computedFields":[],"items":[{"id":"a","amount":"9007199254740993.01"},{"id":"b","amount":"0.01"}],"rowGuards":[["a","ga"],["b","gb"]],"filteredRows":2,"totalRows":2,"complete":true},{"collection":"notes","fields":["body"],"computedFields":[],"items":[{"body":"hello"}],"rowGuards":[["n1","gn"]],"filteredRows":1,"totalRows":1,"complete":true}],"binding":7}""";
    private const string SnapshotRequest = """{"contract":"vibetable.plugin-snapshot.v1","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"orders","fields":["id","amount"]}},{"query":{"contract":"vibetable.plugin-query.v2","collection":"notes","fields":["body"]}}]}""";

    [TestMethod]
    public async Task DatasetUnknownAmountKeepsBothSummaryCompletenessSignalsFalse()
    {
        using var f = new Fixture();
        f.Http.DatasetResultBody = DatasetBody.Replace("\"summary\":{\"count\":2,\"complete\":true", "\"summary\":{\"count\":2,\"complete\":false", StringComparison.Ordinal)
            .Replace("\"value\":\"9007199254740993.02\",\"complete\":true", "\"value\":null,\"complete\":false,\"unknownCount\":1", StringComparison.Ordinal);
        var opened = (await f.Call("dataset.open", DatasetRequest)).GetProperty("result");
        Assert.IsTrue(opened.GetProperty("sources")[0].GetProperty("complete").GetBoolean(), "the source rows were fully read");
        string id = opened.GetProperty("datasetId").GetString()!;
        var summary = (await f.Call("dataset.summary", JsonSerializer.Serialize(new { datasetId = id, collection = "orders" }))).GetProperty("result");
        Assert.IsFalse(summary.GetProperty("complete").GetBoolean());
        Assert.IsFalse(summary.GetProperty("summary").GetProperty("complete").GetBoolean());
        Assert.AreEqual(JsonValueKind.Null, summary.GetProperty("summary").GetProperty("sums")[0].GetProperty("value").ValueKind);
        Assert.AreEqual(1, summary.GetProperty("summary").GetProperty("sums")[0].GetProperty("unknownCount").GetInt32());
    }

    [TestMethod]
    public async Task DatasetIsImmutableBoundedAndReleaseRemainsScoped()
    {
        using var f = new Fixture();
        string first = (await f.Call("dataset.open", DatasetRequest)).GetProperty("result").GetProperty("datasetId").GetString()!;
        string second = (await f.Call("dataset.open", DatasetRequest)).GetProperty("result").GetProperty("datasetId").GetString()!;
        Assert.AreEqual("plugin_dataset_limit", Code(await f.Call("dataset.open", DatasetRequest)));
        var page = (await f.Call("dataset.page", JsonSerializer.Serialize(new { datasetId = first, collection = "orders", offset = 0, pageSize = 1 }))).GetProperty("result");
        Assert.AreEqual("9007199254740993.01", page.GetProperty("items")[0].GetProperty("amount").GetString());
        Assert.AreEqual(1, page.GetProperty("nextOffset").GetInt32());
        var summary = (await f.Call("dataset.summary", JsonSerializer.Serialize(new { datasetId = first, collection = "orders" }))).GetProperty("result");
        Assert.AreEqual("9007199254740993.02", summary.GetProperty("summary").GetProperty("sums")[0].GetProperty("value").GetString());
        Assert.AreEqual(page.GetProperty("versions").GetRawText(), summary.GetProperty("versions").GetRawText());
        var validations = f.Http.Calls.Where(call => call.GetProperty("method").GetString() == "dataset.validate").ToArray();
        Assert.AreEqual(2, validations.Length);
        Assert.IsTrue(validations.All(call => call.GetProperty("request").GetProperty("sources")[0].GetProperty("query").GetProperty("collection").GetString() == "orders"));
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "data.query"));
        int calls = f.Http.Calls.Count;
        foreach (string handle in new[] { first, first, "foreign" }) Assert.IsTrue((await f.Call("dataset.release", JsonSerializer.Serialize(new { datasetId = handle }))).GetProperty("result").GetProperty("released").GetBoolean());
        Assert.AreEqual(calls, f.Http.Calls.Count);
        Assert.AreEqual("plugin_dataset_stale", Code(await f.Call("dataset.summary", JsonSerializer.Serialize(new { datasetId = first, collection = "orders" }))));
        Assert.IsTrue((await f.Call("dataset.open", DatasetRequest)).TryGetProperty("result", out _));
        f.Http.DatasetValidationCode = "plugin_read_denied";
        Assert.AreEqual("plugin_read_denied", Code(await f.Call("dataset.summary", JsonSerializer.Serialize(new { datasetId = second, collection = "orders" }))));
        f.Http.DatasetValidationCode = null;
        Assert.AreEqual("plugin_dataset_stale", Code(await f.Call("dataset.summary", JsonSerializer.Serialize(new { datasetId = second, collection = "orders" }))));
    }

    [TestMethod]
    public async Task DatasetOversizeEnvelopeAndRetirementNeverPublishLateHandles()
    {
        using (var f = new Fixture())
        {
            f.Http.DatasetResultBody = DatasetBody.Replace("s1", new string('x', 512 * 1024), StringComparison.Ordinal);
            Assert.AreEqual("plugin_dataset_limit", Code(await f.Call("dataset.open", DatasetRequest)));
            f.Http.DatasetResultBody = DatasetBody.Replace("\"filteredRows\":2,", "", StringComparison.Ordinal);
            for (int i = 0; i < 3; i++) Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("dataset.open", DatasetRequest)));
            f.Http.DatasetResultBody = DatasetBody;
            Assert.IsTrue((await f.Call("dataset.open", DatasetRequest)).TryGetProperty("result", out _));
            Assert.IsTrue((await f.Call("dataset.open", DatasetRequest)).TryGetProperty("result", out _));
        }
        using (var f = new Fixture())
        {
            f.Http.AfterReadMethod = "dataset.open";
            f.Http.AfterCommitRead = f.RetireAfterReceipt;
            Assert.AreEqual("plugin_source_denied", Code(await f.Call("dataset.open", DatasetRequest)));
        }
    }

    [TestMethod]
    public async Task CancelledDatasetOpenCannotConsumeCapacityAfterItsLateResponse()
    {
        using var f = new Fixture();
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var resume = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        f.Http.OnDatasetOpen = _ => { entered.TrySetResult(); return resume.Task; };
        string wire = f.Wire("dataset.open", DatasetRequest);
        string id = JsonDocument.Parse(wire).RootElement.GetProperty("requestId").GetString()!;
        Task<string> opening = f.Api.HandleAsync(f.Surface.SurfaceToken, f.Source, wire);
        await entered.Task;
        await f.Call("request.cancel", JsonSerializer.Serialize(new { requestId = id }));
        resume.SetResult();
        Assert.AreEqual("plugin_cancel_requested", Code(JsonDocument.Parse(await opening).RootElement));
        f.Http.OnDatasetOpen = null;
        for (int i = 0; i < 2; i++) Assert.IsTrue((await f.Call("dataset.open", DatasetRequest)).TryGetProperty("result", out _));
    }

    [TestMethod]
    public async Task SnapshotPagesServeOneReadPointValidateBeforeEveryPageAndReleaseStaysScoped()
    {
        using var f = new Fixture();
        var opened = (await f.Call("snapshot.open", SnapshotRequest)).GetProperty("result");
        Assert.AreEqual("vibetable.plugin-snapshot.v1", opened.GetProperty("contract").GetString());
        string id = opened.GetProperty("snapshotId").GetString()!;
        // The handle reply carries metadata only; rows live in the Host-side corpus, and the
        // Host-only binding record never reaches the plugin.
        Assert.AreEqual(2, opened.GetProperty("sources").GetArrayLength());
        Assert.IsFalse(opened.GetProperty("sources")[0].TryGetProperty("items", out _));
        Assert.IsFalse(opened.TryGetProperty("binding", out _));
        Assert.AreEqual("orders", opened.GetProperty("sources")[0].GetProperty("collection").GetString());
        Assert.IsTrue(DateTimeOffset.Parse(opened.GetProperty("expiresAt").GetString()!) > DateTimeOffset.UtcNow.AddMinutes(4));
        var first = (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 1 }))).GetProperty("result");
        Assert.AreEqual("9007199254740993.01", first.GetProperty("items")[0].GetProperty("amount").GetString());
        Assert.AreEqual(1, first.GetProperty("nextOffset").GetInt32());
        // Row guards are filtered to the page's rows when id is projected.
        Assert.AreEqual("ga", first.GetProperty("rowGuards").GetProperty("a").GetString());
        Assert.IsFalse(first.GetProperty("rowGuards").TryGetProperty("b", out _));
        var second = (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 1, pageSize = 1 }))).GetProperty("result");
        Assert.AreEqual("0.01", second.GetProperty("items")[0].GetProperty("amount").GetString());
        Assert.AreEqual(JsonValueKind.Null, second.GetProperty("nextOffset").ValueKind);
        Assert.AreEqual(first.GetProperty("versions").GetRawText(), second.GetProperty("versions").GetRawText());
        Assert.AreEqual(first.GetProperty("versions").GetRawText(), opened.GetProperty("versions").GetRawText());
        // data.query parity: guards are keyed by row id for every row whenever id is currently
        // readable, projected (orders) or not (notes projects body only and gains no id field).
        var notes = (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "notes", offset = 0, pageSize = 200 }))).GetProperty("result");
        Assert.AreEqual("hello", notes.GetProperty("items")[0].GetProperty("body").GetString());
        Assert.IsFalse(notes.GetProperty("items")[0].TryGetProperty("id", out _));
        Assert.AreEqual("gn", notes.GetProperty("rowGuards").GetProperty("n1").GetString());
        Assert.AreEqual(1, notes.GetProperty("rowGuards").EnumerateObject().Count());
        // Revoking id without touching the projection keeps the page readable but serves no row identity.
        f.Http.SnapshotIdentityDenied.Add("notes");
        notes = (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "notes", offset = 0, pageSize = 200 }))).GetProperty("result");
        Assert.AreEqual(0, notes.GetProperty("rowGuards").EnumerateObject().Count());
        Assert.AreEqual("hello", notes.GetProperty("items")[0].GetProperty("body").GetString());
        Assert.AreEqual("ga", (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 1 }))).GetProperty("result").GetProperty("rowGuards").GetProperty("a").GetString());
        f.Http.SnapshotIdentityDenied.Clear();
        var validations = f.Http.Calls.Where(call => call.GetProperty("method").GetString() == "snapshot.validate").ToArray();
        Assert.AreEqual(5, validations.Length);
        foreach (var validation in validations)
        {
            var request = validation.GetProperty("request");
            Assert.AreEqual("vibetable.plugin-snapshot.v1", request.GetProperty("contract").GetString());
            Assert.AreEqual("orders", request.GetProperty("sources")[0].GetProperty("query").GetProperty("collection").GetString());
            Assert.AreEqual("notes", request.GetProperty("sources")[1].GetProperty("query").GetProperty("collection").GetString());
            Assert.AreEqual("s1", request.GetProperty("versions").GetProperty("orders").GetProperty("schemaRevision").GetString());
            // The validate request replays the binding record the open fixed, exactly.
            Assert.AreEqual(7, request.GetProperty("binding").GetInt64());
        }
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() is "data.query" or "dataset.open"));
        // Page bounds and unknown members are rejected locally.
        Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 201 }))));
        Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 3, pageSize = 1 }))));
        Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 1, extra = 1 }))));
        Assert.AreEqual("plugin_read_denied", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "missing", offset = 0, pageSize = 1 }))));
        int calls = f.Http.Calls.Count;
        foreach (string handle in new[] { id, id, "foreign" }) Assert.IsTrue((await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = handle }))).GetProperty("result").GetProperty("released").GetBoolean());
        Assert.AreEqual(calls, f.Http.Calls.Count);
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 1 }))));
        Assert.AreEqual(calls, f.Http.Calls.Count);
        // Authority rejections keep their code, drop the handle and never fall back to data.query.
        string again = (await f.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        f.Http.SnapshotValidationCode = "plugin_read_denied";
        Assert.AreEqual("plugin_read_denied", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = again, collection = "orders", offset = 0, pageSize = 1 }))));
        f.Http.SnapshotValidationCode = null;
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = again, collection = "orders", offset = 0, pageSize = 1 }))));
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = again, collection = "orders", offset = 0, pageSize = 1 }))));
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() is "data.query" or "dataset.open"));
    }

    [TestMethod]
    public async Task SnapshotHandlesAreBoundedOversizeIsRejectedAndCancelledOpensCannotConsumeCapacity()
    {
        using (var f = new Fixture())
        {
            Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
            Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
            // The third open is refused locally: it never reaches the authority.
            Assert.AreEqual(2, f.Http.Calls.Count);
            // Release is a local handle drop that never reaches the authority.
            Assert.IsTrue((await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = "whatever" }))).GetProperty("result").GetProperty("released").GetBoolean());
            Assert.AreEqual(2, f.Http.Calls.Count);
        }
        using (var f = new Fixture())
        {
            f.Http.SnapshotResultBody = SnapshotBody.Replace("\"notes\":{\"schemaRevision\":\"s1\"", "\"notes\":{\"schemaRevision\":\"" + new string('x', 520 * 1024) + "\"", StringComparison.Ordinal);
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
            f.Http.SnapshotResultBody = SnapshotBody;
            // Shape violations fail before the authority is contacted (query
            // member checks, empty sources and duplicate JSON members are Go's, as
            // for dataset.open).
            foreach (string malformed in new[] { "[]", "{}", """{"contract":"vibetable.plugin-snapshot.v1","sources":[],"versions":{}}""" })
                Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.open", malformed)));
            Assert.AreEqual(1, f.Http.Calls.Count);
            // Authority rejections keep their own code and consume no capacity; no fallback request exists.
            f.Http.SnapshotOpenCode = "plugin_snapshot_stale";
            Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.open", SnapshotRequest)));
            f.Http.SnapshotOpenCode = null;
            Assert.AreEqual(2, f.Http.Calls.Count);
            // An oversize or rejected open consumed no capacity.
            Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
            Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
        }
        using (var f = new Fixture())
        {
            var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            var resume = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            f.Http.OnSnapshotOpen = _ => { entered.TrySetResult(); return resume.Task; };
            string wire = f.Wire("snapshot.open", SnapshotRequest);
            string id = JsonDocument.Parse(wire).RootElement.GetProperty("requestId").GetString()!;
            Task<string> opening = f.Api.HandleAsync(f.Surface.SurfaceToken, f.Source, wire);
            await entered.Task;
            await f.Call("request.cancel", JsonSerializer.Serialize(new { requestId = id }));
            resume.SetResult();
            Assert.AreEqual("plugin_cancel_requested", Code(JsonDocument.Parse(await opening).RootElement));
            f.Http.OnSnapshotOpen = null;
            for (int i = 0; i < 2; i++) Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
        }
        using (var f = new Fixture())
        {
            f.Http.AfterReadMethod = "snapshot.open";
            f.Http.AfterCommitRead = f.RetireAfterReceipt;
            Assert.AreEqual("plugin_source_denied", Code(await f.Call("snapshot.open", SnapshotRequest)));
        }
    }

    [TestMethod]
    public async Task SnapshotOpensReserveCapacityWhileMaterializingSoConcurrentOpensStayWithinBothLimits()
    {
        using var f = new Fixture();
        // Every open blocks inside the authority until its surface's gate is opened or its request
        // is cancelled; nothing publishes meanwhile.
        var gates = new ConcurrentDictionary<string, ConcurrentQueue<TaskCompletionSource>>();
        int entered = 0, exited = 0;
        f.Http.OnCall = async (call, token) =>
        {
            if (call.GetProperty("method").GetString() != "snapshot.open") return;
            Interlocked.Increment(ref entered);
            try
            {
                var gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
                gates.GetOrAdd(call.GetProperty("pluginId").GetString()!, _ => new()).Enqueue(gate);
                await gate.Task.WaitAsync(token);
            }
            finally { Interlocked.Increment(ref exited); }
        };
        (Task<string> Reply, string RequestId) Open(PluginSurfaceSession surface)
        {
            string wire = f.Wire("snapshot.open", SnapshotRequest, surface.SurfaceToken);
            return (f.Api.HandleAsync(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri, wire), JsonDocument.Parse(wire).RootElement.GetProperty("requestId").GetString()!);
        }
        PluginSurfaceSession Foreign(int index)
        {
            var surface = f.Surfaces.Open(f.Context.ProjectKey, $"com.example.foreign{index}", PluginPackageRevision.Create("C:/plugins/foreign", new string('b', 64)), "ui/index.html");
            f.Surfaces.BindIdentity(surface.SurfaceToken, f.Context, 1, "3.x", installationId: $"foreign-install-{index}");
            return surface;
        }
        async Task<JsonElement> Release(PluginSurfaceSession surface, string id) => JsonDocument.Parse(await f.Api.HandleAsync(surface.SurfaceToken, surface.DocumentUri.AbsoluteUri,
            f.Wire("snapshot.release", JsonSerializer.Serialize(new { snapshotId = id }), surface.SurfaceToken))).RootElement;
        void OpenGates(PluginSurfaceSession surface) { foreach (var gate in gates.GetOrAdd(surface.PluginId, _ => new())) gate.TrySetResult(); }
        int InFlight() => Volatile.Read(ref entered) - Volatile.Read(ref exited);
        var a = Foreign(1);
        var b = Foreign(2);
        var c = Foreign(3);
        var d = Foreign(4);
        var e = Foreign(5);
        // Eight opens are in flight across four surfaces: the total is reached before anything publishes.
        var pending = new List<Task<string>> { Open(f.Surface).Reply, Open(f.Surface).Reply, Open(a).Reply, Open(a).Reply, Open(b).Reply, Open(b).Reply, Open(c).Reply, Open(c).Reply };
        await AwaitCondition(() => InFlight() == 8);
        Assert.AreEqual(8, f.Http.Calls.Count);
        // In-flight opens count: a fresh surface with both slots free is refused locally for the total
        // and a third open on a busy surface for the per-surface limit; neither reaches the authority.
        Assert.AreEqual("plugin_snapshot_limit", Code(await Open(e).Reply));
        Assert.AreEqual("plugin_snapshot_limit", Code(await Open(f.Surface).Reply));
        Assert.AreEqual(8, f.Http.Calls.Count);
        // Retiring a surface fails its in-flight opens and hands their reservations back.
        f.Surfaces.Close(c.SurfaceToken);
        foreach (var reply in pending.Skip(6)) Assert.IsTrue(JsonDocument.Parse(await reply).RootElement.TryGetProperty("error", out _));
        Assert.AreEqual(6, InFlight());
        var late = new List<Task<string>> { Open(d).Reply, Open(d).Reply };
        await AwaitCondition(() => InFlight() == 8);
        Assert.AreEqual(10, f.Http.Calls.Count);
        Assert.AreEqual("plugin_snapshot_limit", Code(await Open(e).Reply));
        Assert.AreEqual(10, f.Http.Calls.Count);
        // Everything publishes under the limit.
        foreach (var surface in new[] { f.Surface, a, b, d }) OpenGates(surface);
        foreach (var reply in pending.Take(6).Concat(late)) Assert.IsTrue(JsonDocument.Parse(await reply).RootElement.GetProperty("result").TryGetProperty("snapshotId", out _));
        Assert.AreEqual(0, InFlight());
        Assert.AreEqual("plugin_snapshot_limit", Code(await Open(e).Reply));
        // A released handle frees its slot; a cancelled open hands its reservation back.
        string released = JsonDocument.Parse(await late[0]).RootElement.GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.IsTrue((await Release(d, released)).GetProperty("result").GetProperty("released").GetBoolean());
        var cancelled = Open(d);
        await AwaitCondition(() => InFlight() == 1);
        Assert.AreEqual(11, f.Http.Calls.Count);
        Assert.AreEqual("plugin_snapshot_limit", Code(await Open(d).Reply));
        Assert.AreEqual(11, f.Http.Calls.Count);
        Assert.IsTrue(JsonDocument.Parse(await f.Api.HandleAsync(d.SurfaceToken, d.DocumentUri.AbsoluteUri, f.Wire("request.cancel", JsonSerializer.Serialize(new { requestId = cancelled.RequestId }), d.SurfaceToken))).RootElement.GetProperty("result").GetProperty("cancelRequested").GetBoolean());
        Assert.AreEqual("plugin_cancel_requested", Code(await cancelled.Reply));
        Assert.AreEqual(0, InFlight());
        var afterCancel = Open(d);
        await AwaitCondition(() => InFlight() == 1);
        Assert.AreEqual(12, f.Http.Calls.Count);
        Assert.AreEqual("plugin_snapshot_limit", Code(await Open(e).Reply));
        OpenGates(d);
        Assert.IsTrue(JsonDocument.Parse(await afterCancel.Reply).RootElement.GetProperty("result").TryGetProperty("snapshotId", out _));
    }

    private static async Task AwaitCondition(Func<bool> condition)
    {
        var deadline = DateTime.UtcNow.AddSeconds(10);
        while (!condition()) { Assert.IsTrue(DateTime.UtcNow < deadline, "condition was not reached in time"); await Task.Delay(10); }
    }

    private static string SnapshotCorpus(params (string Collection, string Fields, int Rows, int Blob)[] sources)
    {
        string version = string.Join(",", sources.Select(source => $"\"{source.Collection}\":{{\"schemaRevision\":\"s1\",\"dataRevision\":1,\"clockPeriod\":\"\",\"computed\":{{}}}}"));
        string listed = string.Join(",", sources.Select(source =>
        {
            string[] fields = source.Fields.Split(',');
            string Cell(string field, int index) => field switch { "id" => $"\"{source.Collection}-{index}\"", "amount" => "\"0.01\"", "body" => "\"hello\"", _ => JsonSerializer.Serialize(new string('x', source.Blob)) };
            string items = string.Join(",", Enumerable.Range(0, source.Rows).Select(index => "{" + string.Join(",", fields.Select(field => $"\"{field}\":{Cell(field, index)}")) + "}"));
            // The Go corpus guards every row by id, projected or not: one [id, digest] pair per row aligned with items.
            string guards = "[" + string.Join(",", Enumerable.Range(0, source.Rows).Select(index => $"[\"{source.Collection}-{index}\",\"g\"]")) + "]";
            return $"{{\"collection\":\"{source.Collection}\",\"fields\":[{string.Join(",", fields.Select(field => $"\"{field}\""))}],\"computedFields\":[],\"items\":[{items}],\"rowGuards\":{guards},\"filteredRows\":{source.Rows},\"totalRows\":{source.Rows},\"complete\":true}}";
        }));
        return $"{{\"contract\":\"vibetable.plugin-snapshot.v1\",\"versions\":{{{version}}},\"sources\":[{listed}],\"binding\":7}}";
    }

    private static string SnapshotQuery(params (string Collection, string Fields)[] sources) => JsonSerializer.Serialize(new
    {
        contract = "vibetable.plugin-snapshot.v1",
        sources = sources.Select(source => new { query = new { contract = "vibetable.plugin-query.v2", collection = source.Collection, fields = source.Fields.Split(',') } }),
    });

    private static (string Collection, int Rows, int? NextOffset)[] Chunks(JsonElement batch) => batch.GetProperty("chunks").EnumerateArray()
        .Select(chunk => (chunk.GetProperty("collection").GetString()!, chunk.GetProperty("items").GetArrayLength(), chunk.GetProperty("nextOffset").ValueKind == JsonValueKind.Null ? (int?)null : chunk.GetProperty("nextOffset").GetInt32())).ToArray();

    [TestMethod]
    public async Task SnapshotReadBatchCrossesSourcesWithOneValidationPerBatchKeepsEmptySourcesAndStopsAtTheRowBudget()
    {
        using var f = new Fixture();
        f.Http.SnapshotResultBody = SnapshotCorpus(("orders", "id,amount", 150, 0), ("notes", "body", 100, 0), ("empty", "body", 0, 0), ("tail", "id", 1, 0));
        string request = SnapshotQuery(("orders", "id,amount"), ("notes", "body"), ("empty", "body"), ("tail", "id"));
        var opened = (await f.Call("snapshot.open", request)).GetProperty("result");
        string id = opened.GetProperty("snapshotId").GetString()!;
        int Validations() => f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate");
        // Batch 1: 150 orders rows plus the first 50 notes rows = exactly 200 rows, one validation.
        var first = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))).GetProperty("result");
        Assert.AreEqual(1, Validations());
        Assert.AreSequenceEqual(new[] { ("orders", 150, (int?)null), ("notes", 50, (int?)50) }, Chunks(first));
        var orders = first.GetProperty("chunks")[0];
        var notes = first.GetProperty("chunks")[1];
        Assert.AreEqual("orders-0", orders.GetProperty("items")[0].GetProperty("id").GetString());
        Assert.AreEqual("g", orders.GetProperty("rowGuards").GetProperty("orders-149").GetString());
        Assert.IsFalse(orders.GetProperty("rowGuards").TryGetProperty("notes-0", out _));
        Assert.AreEqual(150, orders.GetProperty("rowGuards").EnumerateObject().Count());
        // notes projects body only; its guards still follow its rows by id, sliced to this chunk's range.
        Assert.AreEqual(50, notes.GetProperty("rowGuards").EnumerateObject().Count());
        Assert.AreEqual("g", notes.GetProperty("rowGuards").GetProperty("notes-49").GetString());
        Assert.IsFalse(notes.GetProperty("rowGuards").TryGetProperty("notes-50", out _));
        Assert.IsFalse(notes.GetProperty("items")[0].TryGetProperty("id", out _));
        Assert.IsTrue(notes.GetProperty("complete").GetBoolean());
        Assert.AreEqual(first.GetProperty("versions").GetRawText(), opened.GetProperty("versions").GetRawText());
        Assert.AreEqual("notes", first.GetProperty("next").GetProperty("collection").GetString());
        Assert.AreEqual(50, first.GetProperty("next").GetProperty("offset").GetInt32());
        // Batch 2 continues at notes 50 with its own single validation: the remaining notes rows,
        // the empty source as a zero-row chunk and tail all fit, so the walk is exhausted.
        var second = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = first.GetProperty("next") }))).GetProperty("result");
        Assert.AreEqual(2, Validations());
        Assert.AreSequenceEqual(new[] { ("notes", 50, (int?)null), ("empty", 0, (int?)null), ("tail", 1, (int?)null) }, Chunks(second));
        Assert.AreEqual(JsonValueKind.Null, second.GetProperty("next").ValueKind);
        // A batch from the middle: the position names the source and the row, earlier sources are not re-served.
        var partial = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = new { collection = "notes", offset = 97 } }))).GetProperty("result");
        Assert.AreEqual(3, Validations());
        Assert.AreSequenceEqual(new[] { ("notes", 3, (int?)null), ("empty", 0, (int?)null), ("tail", 1, (int?)null) }, Chunks(partial));
        Assert.AreEqual(JsonValueKind.Null, partial.GetProperty("next").ValueKind);
        Assert.AreEqual("[\"notes-97\",\"notes-98\",\"notes-99\"]", JsonSerializer.Serialize(partial.GetProperty("chunks")[0].GetProperty("rowGuards").EnumerateObject().Select(guard => guard.Name).ToArray()));
        // The handle stays open: a single-source page and a re-read from the start still work.
        var page = (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "tail", offset = 0, pageSize = 200 }))).GetProperty("result");
        Assert.AreEqual(1, page.GetProperty("items").GetArrayLength());
        Assert.AreEqual(4, Validations());
        var again = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))).GetProperty("result");
        Assert.AreSequenceEqual(new[] { ("orders", 150, (int?)null), ("notes", 50, (int?)50) }, Chunks(again));
        Assert.AreEqual(50, again.GetProperty("next").GetProperty("offset").GetInt32());
        Assert.AreEqual(5, Validations());
        // Positions are explicit and validated: unknown collections are read denials, bad shapes or
        // offsets are capability errors after that batch's validation, and none of them drops the handle.
        Assert.AreEqual("plugin_read_denied", Code(await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = new { collection = "missing", offset = 0 } }))));
        foreach (string malformed in new[] { "5", "[]", "\"orders\"", "{}", "\"{\\\"collection\\\":\\\"orders\\\"}\"", "\"{\\\"collection\\\":\\\"orders\\\",\\\"offset\\\":-1}\"", "\"{\\\"collection\\\":\\\"orders\\\",\\\"offset\\\":1.5}\"", "\"{\\\"collection\\\":\\\"orders\\\",\\\"offset\\\":151}\"", "\"{\\\"collection\\\":\\\"orders\\\",\\\"offset\\\":0,\\\"extra\\\":1}\"" })
            Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.readBatch", $"{{\"snapshotId\":\"{id}\",\"next\":{malformed}}}")), malformed);
        Assert.AreEqual(15, Validations());
        // An unknown top-level member fails before the validation.
        Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null, extra = 1 }))));
        Assert.AreEqual(15, Validations());
        // An exhausted source at its end offset is an empty chunk, not an error, and the walk continues.
        var tail = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = new { collection = "notes", offset = 100 } }))).GetProperty("result");
        Assert.AreSequenceEqual(new[] { ("notes", 0, (int?)null), ("empty", 0, (int?)null), ("tail", 1, (int?)null) }, Chunks(tail));
        Assert.AreEqual(JsonValueKind.Null, tail.GetProperty("next").ValueKind);
        Assert.AreEqual(16, Validations());
        Assert.IsTrue((await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = id }))).GetProperty("result").GetProperty("released").GetBoolean());
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))));
        Assert.AreEqual(16, Validations());
        // A batch the authority refuses keeps its code, drops the handle and never falls back to data.query.
        string reopened = (await f.Call("snapshot.open", request)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        f.Http.SnapshotValidationCode = "plugin_read_denied";
        Assert.AreEqual("plugin_read_denied", Code(await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = reopened, next = (object?)null }))));
        f.Http.SnapshotValidationCode = null;
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = reopened, next = (object?)null }))));
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = reopened, collection = "orders", offset = 0, pageSize = 1 }))));
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() is "data.query" or "dataset.open"));
    }

    [TestMethod]
    public async Task SnapshotGuardsFollowDataQueryWithoutLeakingUnprojectedIdsAndOnlyWhileIdIsReadable()
    {
        using var f = new Fixture();
        async Task<string> Open() => (await f.Call("snapshot.open", SnapshotQuery(("notes", "body")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Task<JsonElement> Page(string handle) => f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = handle, collection = "notes", offset = 0, pageSize = 200 }));
        // Opened while id was granted: the corpus guards every row although no row projects id,
        // and any offset slices both together.
        f.Http.SnapshotResultBody = SnapshotCorpus(("notes", "body", 3, 0));
        string id = await Open();
        var page = (await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "notes", offset = 1, pageSize = 1 }))).GetProperty("result");
        Assert.AreEqual("hello", page.GetProperty("items")[0].GetProperty("body").GetString());
        Assert.IsFalse(page.GetProperty("items")[0].TryGetProperty("id", out _));
        Assert.AreEqual("g", page.GetProperty("rowGuards").GetProperty("notes-1").GetString());
        Assert.AreEqual(1, page.GetProperty("rowGuards").EnumerateObject().Count());
        Assert.IsFalse(page.GetProperty("rowGuards").TryGetProperty("notes-0", out _));
        // Revoked id: the page stays readable, row identity stops, no id reaches the reply anywhere.
        f.Http.SnapshotIdentityDenied.Add("notes");
        page = (await Page(id)).GetProperty("result");
        Assert.AreEqual(3, page.GetProperty("items").GetArrayLength());
        Assert.AreEqual(0, page.GetProperty("rowGuards").EnumerateObject().Count());
        Assert.IsFalse(page.GetRawText().Contains("notes-", StringComparison.Ordinal));
        // A reply without an identity map serves no row identity either.
        f.Http.SnapshotIdentityDenied.Clear();
        f.Http.SnapshotValidationBody = """{"valid":true}""";
        Assert.AreEqual(0, (await Page(id)).GetProperty("result").GetProperty("rowGuards").EnumerateObject().Count());
        f.Http.SnapshotValidationBody = null;
        // Opened while id was not granted: the corpus carries null pairs, so a later id grant
        // never conjures guards the corpus does not hold.
        f.Http.SnapshotResultBody = SnapshotCorpus(("notes", "body", 2, 0)).Replace("[\"notes-0\",\"g\"]", "null").Replace("[\"notes-1\",\"g\"]", "null");
        string bare = await Open();
        Assert.AreEqual(2, (await Page(bare)).GetProperty("result").GetProperty("items").GetArrayLength());
        Assert.AreEqual(0, (await Page(bare)).GetProperty("result").GetProperty("rowGuards").EnumerateObject().Count());
        foreach (string handle in new[] { id, bare }) Assert.IsTrue((await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = handle }))).GetProperty("result").GetProperty("released").GetBoolean());
        // A corpus whose guards are not aligned with items is never published and frees its capacity;
        // one without the member at all is not even a capability reply.
        string aligned = SnapshotCorpus(("notes", "body", 2, 0));
        var guards = "\"rowGuards\":[[\"notes-0\",\"g\"],[\"notes-1\",\"g\"]]";
        foreach (var (malformed, expected) in new[]
        {
            (aligned.Replace(guards, "\"rowGuards\":{}"), "plugin_snapshot_limit"),
            (aligned.Replace(guards, "\"rowGuards\":[[\"notes-0\",\"g\"]]"), "plugin_snapshot_limit"),
            (aligned.Replace("\"rowGuards\":", "\"guards\":"), "plugin_capability_invalid"),
        })
        {
            f.Http.SnapshotResultBody = malformed;
            Assert.AreEqual(expected, Code(await f.Call("snapshot.open", SnapshotQuery(("notes", "body")))), malformed);
        }
        f.Http.SnapshotResultBody = aligned;
        Assert.IsTrue((await f.Call("snapshot.open", SnapshotQuery(("notes", "body")))).TryGetProperty("result", out _));
        Assert.IsTrue((await f.Call("snapshot.open", SnapshotQuery(("notes", "body")))).TryGetProperty("result", out _));
    }

    [TestMethod]
    public async Task SnapshotCancellationDuringTheEndorsementRoundTripReleasesTheExistingHandleNotOnlyNewOpens()
    {
        using var f = new Fixture();
        f.Http.SnapshotResultBody = SnapshotCorpus(("orders", "id,amount", 3, 0));
        string request = SnapshotQuery(("orders", "id,amount"));
        foreach (string method in new[] { "snapshot.page", "snapshot.readBatch" })
        {
            string id = (await f.Call("snapshot.open", request)).GetProperty("result").GetProperty("snapshotId").GetString()!;
            string parameters = method == "snapshot.page" ? JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 1 }) : JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null });
            string wire = f.Wire(method, parameters);
            string requestId = JsonDocument.Parse(wire).RootElement.GetProperty("requestId").GetString()!;
            // The plugin cancels while the authority endorses the request, i.e. after the handle
            // existed and before the slice was delivered.
            f.Http.OnSnapshotValidate = async _ => { await f.Api.HandleAsync(f.Surface.SurfaceToken, f.Source, f.Wire("request.cancel", JsonSerializer.Serialize(new { requestId }))); };
            JsonElement reply = JsonDocument.Parse(await f.Api.HandleAsync(f.Surface.SurfaceToken, f.Source, wire)).RootElement;
            f.Http.OnSnapshotValidate = null;
            Assert.AreEqual("plugin_cancel_requested", Code(reply), method);
            // The pre-existing handle is gone: no authority round trip and no re-service of the
            // page that was never delivered ...
            int calls = f.Http.Calls.Count;
            Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call(method, parameters)), method);
            Assert.AreEqual(calls, f.Http.Calls.Count, method);
            // ... and its capacity is free again.
            string first = (await f.Call("snapshot.open", request)).GetProperty("result").GetProperty("snapshotId").GetString()!;
            string second = (await f.Call("snapshot.open", request)).GetProperty("result").GetProperty("snapshotId").GetString()!;
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", request)), method);
            foreach (string handle in new[] { first, second }) Assert.IsTrue((await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = handle }))).GetProperty("result").GetProperty("released").GetBoolean());
        }
    }

    [TestMethod]
    public async Task SnapshotValidateReplyMustEndorseTheHandleAndExpiryIsRecheckedAfterTheAuthorityRoundTrip()
    {
        using var f = new Fixture();
        async Task<string> Open() => (await f.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Task<JsonElement> Page(string handle) => f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = handle, collection = "orders", offset = 0, pageSize = 1 }));
        // Only an explicit valid === true endorses a handle; anything else is stale, drops the
        // handle and never slices.
        foreach (string reply in new[] { """{"valid":false}""", "{}", """{"valid":"true"}""", "[]" })
        {
            string id = await Open();
            f.Http.SnapshotValidationBody = reply;
            Assert.AreEqual("plugin_snapshot_stale", Code(await Page(id)), reply);
            int calls = f.Http.Calls.Count;
            Assert.AreEqual("plugin_snapshot_stale", Code(await Page(id)), reply);
            Assert.AreEqual(calls, f.Http.Calls.Count, reply);
            f.Http.SnapshotValidationBody = null;
        }
        // Absolute expiry is re-checked after the await: a handle that expires while the
        // authority endorses it is never sliced from.
        f.Api.SnapshotLifetime = TimeSpan.FromMilliseconds(300);
        string shortLived = await Open();
        f.Http.OnSnapshotValidate = async token => await Task.Delay(700, token);
        Assert.AreEqual("plugin_snapshot_stale", Code(await Page(shortLived)));
        f.Http.OnSnapshotValidate = null;
        Assert.AreEqual(5, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
        // Released while the authority endorses it: stale after the await, nothing sliced.
        f.Api.SnapshotLifetime = TimeSpan.FromMinutes(5);
        string released = await Open();
        f.Http.OnSnapshotValidate = async _ => { await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = released })); };
        Assert.AreEqual("plugin_snapshot_stale", Code(await Page(released)));
        f.Http.OnSnapshotValidate = null;
        Assert.AreEqual(6, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
    }

    [TestMethod]
    public async Task SnapshotReadBatchHonoursTheByteBudgetAndFortySourcesOf280RowsNeedTwoBatches()
    {
        using (var f = new Fixture())
        {
            f.Http.SnapshotResultBody = SnapshotCorpus(("big", "id,blob", 4, 70_000), ("small", "body", 2, 0));
            string id = (await f.Call("snapshot.open", SnapshotQuery(("big", "id,blob"), ("small", "body")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var first = await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }));
            // 3 x 70 KiB rows fit the 256 KiB item budget, the fourth would not; the chunk still made progress.
            Assert.AreSequenceEqual(new[] { ("big", 3, (int?)3) }, Chunks(first.GetProperty("result")));
            Assert.AreEqual("big", first.GetProperty("result").GetProperty("next").GetProperty("collection").GetString());
            Assert.IsTrue(Encoding.UTF8.GetByteCount(first.GetProperty("result").GetProperty("chunks")[0].GetProperty("items").GetRawText()) is > 262144 - 70_000 and <= 262144);
            var second = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = first.GetProperty("result").GetProperty("next") }))).GetProperty("result");
            Assert.AreSequenceEqual(new[] { ("big", 1, (int?)null), ("small", 2, (int?)null) }, Chunks(second));
            Assert.AreEqual(JsonValueKind.Null, second.GetProperty("next").ValueKind);
            Assert.AreEqual(2, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
        }
        using (var f = new Fixture())
        {
            f.Http.SnapshotResultBody = SnapshotCorpus(Enumerable.Range(0, 40).Select(index => ($"s{index}", "id,amount", 7, 0)).ToArray());
            string id = (await f.Call("snapshot.open", SnapshotQuery(Enumerable.Range(0, 40).Select(index => ($"s{index}", "id,amount")).ToArray()))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var first = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))).GetProperty("result");
            var second = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = first.GetProperty("next") }))).GetProperty("result");
            Assert.AreEqual("""{"collection":"s28","offset":4}""", first.GetProperty("next").GetRawText());
            Assert.AreEqual("null", second.GetProperty("next").GetRawText());
            var chunks = first.GetProperty("chunks").EnumerateArray().Concat(second.GetProperty("chunks").EnumerateArray()).ToArray();
            Assert.AreEqual(200, first.GetProperty("chunks").EnumerateArray().Sum(chunk => chunk.GetProperty("items").GetArrayLength()));
            Assert.AreEqual(80, second.GetProperty("chunks").EnumerateArray().Sum(chunk => chunk.GetProperty("items").GetArrayLength()));
            Assert.AreEqual(40, chunks.Select(chunk => chunk.GetProperty("collection").GetString()).Distinct().Count());
            // Every row exactly once across the two batches, in request order.
            Assert.AreEqual(280, chunks.SelectMany(chunk => chunk.GetProperty("items").EnumerateArray()).Select(row => row.GetProperty("id").GetString()).Distinct().Count());
            Assert.AreEqual("s0-0", chunks[0].GetProperty("items")[0].GetProperty("id").GetString());
            Assert.AreEqual("s39-6", chunks[^1].GetProperty("items")[6].GetProperty("id").GetString());
            Assert.AreEqual(2, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
        }
        using (var f = new Fixture())
        {
            // The item budget belongs to the whole batch: once a source filled most of it, the next
            // source's first row no longer fits and that source starts the following batch as a whole.
            f.Http.SnapshotResultBody = SnapshotCorpus(("a", "id,blob", 2, 100_000), ("b", "id,blob", 2, 100_000), ("c", "body", 1, 0));
            string id = (await f.Call("snapshot.open", SnapshotQuery(("a", "id,blob"), ("b", "id,blob"), ("c", "body")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var first = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))).GetProperty("result");
            Assert.AreSequenceEqual(new[] { ("a", 2, (int?)null) }, Chunks(first));
            Assert.AreEqual("""{"collection":"b","offset":0}""", first.GetProperty("next").GetRawText());
            Assert.IsTrue(first.GetProperty("chunks").EnumerateArray().Sum(chunk => Encoding.UTF8.GetByteCount(chunk.GetProperty("items").GetRawText())) <= 262144);
            var second = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = first.GetProperty("next") }))).GetProperty("result");
            Assert.AreSequenceEqual(new[] { ("b", 2, (int?)null), ("c", 1, (int?)null) }, Chunks(second));
            Assert.AreEqual("null", second.GetProperty("next").GetRawText());
            Assert.IsTrue(second.GetProperty("chunks").EnumerateArray().Sum(chunk => Encoding.UTF8.GetByteCount(chunk.GetProperty("items").GetRawText())) <= 262144);
            Assert.AreEqual(2, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
        }
    }

    [TestMethod]
    public async Task SnapshotBatchBudgetCountsEveryChunkContainerAndSeparatorTheBridgeEncodes()
    {
        static int Enc(string json) => Encoding.UTF8.GetByteCount(json);
        // What one batch carries, as the bridge encodes it: per chunk its two containers and fixed
        // members plus the separator in front of it, per row its item, its separator and the guard
        // member it becomes ("id":digest) with its own separator.
        static int Chunk(string name) => Enc(JsonSerializer.Serialize(new { collection = name, items = Array.Empty<string>(), rowGuards = new Dictionary<string, string>(), nextOffset = 1000, complete = true })) + 1;
        static int Row(string id, int blob) => Enc(JsonSerializer.Serialize(new { id, blob = new string('x', blob) })) + 1 + Enc(JsonSerializer.Serialize(new Dictionary<string, string> { [id] = "g" })) - 2 + 1;
        const int budget = 262144;
        static int Encoded(JsonElement batch) => batch.GetProperty("chunks").EnumerateArray().Sum(chunk => Enc(chunk.GetProperty("items").GetRawText()) + Enc(chunk.GetProperty("rowGuards").GetRawText()));
        static async Task<(JsonElement First, string Next, JsonElement Second)> Read(Fixture f, string id)
        {
            var first = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))).GetProperty("result");
            var second = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = first.GetProperty("next") }))).GetProperty("result");
            return (first, first.GetProperty("next").GetRawText(), second);
        }
        using (var f = new Fixture())
        {
            // Four rows that fill the budget exactly are served together; one byte more per row
            // moves the fourth to the following batch although its item alone would still fit.
            int blob = (budget - Chunk("big") - 4 * Row("big-0", 0)) / 4;
            Assert.IsTrue(Chunk("big") + 4 * Row("big-0", blob) <= budget, $"four rows of {blob} = {Chunk("big") + 4 * Row("big-0", blob)}");
            Assert.IsTrue(Chunk("big") + 4 * Row("big-0", blob + 1) > budget);
            f.Http.SnapshotResultBody = SnapshotCorpus(("big", "id,blob", 5, blob));
            string id = (await f.Call("snapshot.open", SnapshotQuery(("big", "id,blob")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var (first, _, second) = await Read(f, id);
            Assert.AreSequenceEqual(new[] { ("big", 4, (int?)4) }, Chunks(first));
            Assert.AreEqual("null", second.GetProperty("next").GetRawText());
            Assert.AreSequenceEqual(new[] { ("big", 1, (int?)null) }, Chunks(second));
            // The real items and guards fill the budget up to the chunk's own fixed members.
            Assert.IsTrue(Encoded(first) <= budget && Encoded(first) > budget - Chunk("big") - 8, $"encoded batch = {Encoded(first)}");
            Assert.IsTrue(Encoded(second) <= budget);
            f.Http.SnapshotResultBody = SnapshotCorpus(("big", "id,blob", 5, blob + 1));
            string tighter = (await f.Call("snapshot.open", SnapshotQuery(("big", "id,blob")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var (cut, next, rest) = await Read(f, tighter);
            Assert.AreSequenceEqual(new[] { ("big", 3, (int?)3) }, Chunks(cut));
            Assert.AreEqual("""{"collection":"big","offset":3}""", next);
            Assert.AreSequenceEqual(new[] { ("big", 2, (int?)null) }, Chunks(rest));
            Assert.IsTrue(Encoded(cut) <= budget && Encoded(rest) <= budget);
        }
        using (var f = new Fixture())
        {
            // Across sources the chunk itself (containers and fixed members) is charged before its
            // rows: an empty source rides along only while its whole chunk fits the remaining budget.
            int blob = (budget - 2 * Chunk("a")) / 3 - Row("a-0", 0);
            Assert.IsTrue(Chunk("a") + 3 * Row("a-0", blob) + Chunk("a") <= budget);
            Assert.IsTrue(Chunk("a") + 3 * Row("a-0", blob + 1) + Chunk("a") > budget);
            f.Http.SnapshotResultBody = SnapshotCorpus(("a", "id,blob", 3, blob), ("e", "body", 0, 0), ("c", "body", 1, 0));
            string id = (await f.Call("snapshot.open", SnapshotQuery(("a", "id,blob"), ("e", "body"), ("c", "body")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var (first, next, second) = await Read(f, id);
            Assert.AreSequenceEqual(new[] { ("a", 3, (int?)null), ("e", 0, (int?)null) }, Chunks(first));
            Assert.AreEqual("""{"collection":"c","offset":0}""", next);
            Assert.AreSequenceEqual(new[] { ("c", 1, (int?)null) }, Chunks(second));
            Assert.AreEqual("null", second.GetProperty("next").GetRawText());
            Assert.IsTrue(Encoded(first) <= budget && Encoded(second) <= budget);
            f.Http.SnapshotResultBody = SnapshotCorpus(("a", "id,blob", 3, blob + 1), ("e", "body", 0, 0), ("c", "body", 1, 0));
            string tighter = (await f.Call("snapshot.open", SnapshotQuery(("a", "id,blob"), ("e", "body"), ("c", "body")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var (cut, resume, rest) = await Read(f, tighter);
            Assert.AreSequenceEqual(new[] { ("a", 3, (int?)null) }, Chunks(cut));
            Assert.AreEqual("""{"collection":"e","offset":0}""", resume);
            Assert.AreSequenceEqual(new[] { ("e", 0, (int?)null), ("c", 1, (int?)null) }, Chunks(rest));
            Assert.AreEqual("null", rest.GetProperty("next").GetRawText());
            Assert.IsTrue(Encoded(cut) <= budget && Encoded(rest) <= budget);
        }
        using (var f = new Fixture())
        {
            // Unchanged: the batch's very first row is unconditional even above the batch budget;
            // the next source's chunk then no longer fits and starts the following batch.
            f.Http.SnapshotResultBody = SnapshotCorpus(("huge", "id,blob", 1, 300_000), ("c", "body", 1, 0));
            string id = (await f.Call("snapshot.open", SnapshotQuery(("huge", "id,blob"), ("c", "body")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var (first, next, second) = await Read(f, id);
            Assert.AreSequenceEqual(new[] { ("huge", 1, (int?)null) }, Chunks(first));
            Assert.AreEqual("""{"collection":"c","offset":0}""", next);
            Assert.AreSequenceEqual(new[] { ("c", 1, (int?)null) }, Chunks(second));
            Assert.AreEqual("null", second.GetProperty("next").GetRawText());
            Assert.AreEqual(2, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
        }
    }

    [TestMethod]
    public async Task SnapshotOpenWhoseValidateEnvelopeCannotFitTheFrameIsRefusedAsAWhole()
    {
        using var f = new Fixture();
        // A source named by one long id and a version vector padded to size: the open reply returns
        // two rows and fits the frame easily, while the validate request every page and batch of
        // the handle starts with carries the plugin's own sources plus that vector.
        static string Open(string ids) => JsonSerializer.Serialize(new
        {
            contract = "vibetable.plugin-snapshot.v1",
            sources = new[] { new { query = new { contract = "vibetable.plugin-query.v2", collection = "orders", fields = new[] { "id" }, ids = new[] { ids } } } },
        });
        static string Padded(int pad) => SnapshotBody.Replace("\"notes\":{\"schemaRevision\":\"s1\"", "\"notes\":{\"schemaRevision\":\"s1" + new string('x', pad) + "\"", StringComparison.Ordinal);
        int Validations() => f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate");
        int Sent() => Encoding.UTF8.GetByteCount(f.Http.Calls.Last(call => call.GetProperty("method").GetString() == "snapshot.validate").GetRawText());
        string handle = (await f.Call("snapshot.open", Open(""))).GetProperty("result").GetProperty("snapshotId").GetString()!;
        await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = handle, collection = "orders", offset = 0, pageSize = 1 }));
        int baseline = Sent();
        await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = handle }));
        // Sized so the validate envelope is exactly the frame limit: measured with the real
        // encoding, it is sent and the handle serves.
        const int pad = 440_000;
        string ids = new('x', 1_048_576 - baseline - pad);
        f.Http.SnapshotResultBody = Padded(pad);
        string exact = (await f.Call("snapshot.open", Open(ids))).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.IsTrue((await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = exact, collection = "orders", offset = 0, pageSize = 1 }))).GetProperty("result").GetProperty("items").GetArrayLength() == 1);
        Assert.IsTrue(Sent() == 1_048_576, $"validate envelope = {Sent()} bytes, baseline {baseline}");
        // One byte more: the authority answers the open, then no handle is published, so no validate
        // is ever attempted for it and the capacity it reserved is handed back (both slots still
        // take an open afterwards).
        f.Http.SnapshotResultBody = Padded(pad + 1);
        int calls = f.Http.Calls.Count;
        Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", Open(ids))));
        Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", Open(ids))));
        Assert.AreEqual(calls + 2, f.Http.Calls.Count);
        Assert.AreEqual(2, Validations());
        await f.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = exact }));
        f.Http.SnapshotResultBody = SnapshotBody;
        string second = (await f.Call("snapshot.open", Open(ids))).GetProperty("result").GetProperty("snapshotId").GetString()!;
        string third = (await f.Call("snapshot.open", Open(""))).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.AreNotEqual(second, third);
        Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", Open(""))));
        Assert.AreEqual(2, Validations());
    }

    [TestMethod]
    public async Task SnapshotBindingIsHostOnlyReplayedOnValidateAndItsShapeIsCheckedBeforeTheHandleExists()
    {
        using var f = new Fixture();
        // The authority must name the record the corpus was authorized under: a positive audit
        // sequence or null. Anything else is a capability error after that one authority call:
        // no handle is published and no capacity is consumed.
        foreach (string malformed in new[]
        {
            SnapshotBody.Replace(",\"binding\":7", "", StringComparison.Ordinal),
            SnapshotBody.Replace("\"binding\":7", "\"binding\":\"7\"", StringComparison.Ordinal),
            SnapshotBody.Replace("\"binding\":7", "\"binding\":0", StringComparison.Ordinal),
            SnapshotBody.Replace("\"binding\":7", "\"binding\":-1", StringComparison.Ordinal),
            SnapshotBody.Replace("\"binding\":7", "\"binding\":1.5", StringComparison.Ordinal),
            SnapshotBody.Replace("\"binding\":7", "\"binding\":[7]", StringComparison.Ordinal),
        })
        {
            f.Http.SnapshotResultBody = malformed;
            Assert.AreEqual("plugin_capability_invalid", Code(await f.Call("snapshot.open", SnapshotRequest)), malformed);
        }
        Assert.AreEqual(6, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.open"));
        f.Http.SnapshotResultBody = SnapshotBody;
        for (int i = 0; i < 2; i++) Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
        Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
        // null is a legitimate binding (no completed model record) and is replayed as null.
        using var g = new Fixture();
        g.Http.SnapshotResultBody = SnapshotBody.Replace("\"binding\":7", "\"binding\":null", StringComparison.Ordinal);
        var opened = (await g.Call("snapshot.open", SnapshotRequest)).GetProperty("result");
        string id = opened.GetProperty("snapshotId").GetString()!;
        Assert.IsFalse(opened.TryGetProperty("binding", out _));
        Assert.IsTrue((await g.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = id, collection = "orders", offset = 0, pageSize = 1 }))).TryGetProperty("result", out _));
        var validation = g.Http.Calls.Single(call => call.GetProperty("method").GetString() == "snapshot.validate").GetProperty("request");
        Assert.AreEqual(JsonValueKind.Null, validation.GetProperty("binding").ValueKind);
        Assert.AreEqual("vibetable.plugin-snapshot.v1", validation.GetProperty("contract").GetString());
    }

    [TestMethod]
    public async Task SnapshotBudgetsCountTheBytesTheBridgeActuallyEncodes()
    {
        // The bridge's default JSON encoder escapes every non-ASCII character to \uXXXX: a CJK
        // run is three bytes in Go's raw reply and six on the wire, '<' is one and six. The corpus
        // and batch budgets are measured on the encoded form, never on the raw JSON length.
        using (var f = new Fixture())
        {
            // 180k '<' = 180 KB raw, well inside Go's budget, but 1.08 MB encoded: over the frame on its own.
            f.Http.SnapshotResultBody = SnapshotCorpus(("markup", "id,blob", 1, 180_000)).Replace(new string('x', 180_000), new string('<', 180_000), StringComparison.Ordinal);
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotQuery(("markup", "id,blob")))));
            // 80k CJK = 240 KB raw and 480 KB encoded: one row fits the corpus and a reply, two do not.
            string Rows(int rows) => SnapshotCorpus(("cjk", "id,blob", rows, 80_000)).Replace(new string('x', 80_000), new string('字', 80_000), StringComparison.Ordinal);
            f.Http.SnapshotResultBody = Rows(2);
            Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotQuery(("cjk", "id,blob")))));
            f.Http.SnapshotResultBody = Rows(1);
            string id = (await f.Call("snapshot.open", SnapshotQuery(("cjk", "id,blob")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var batch = await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }));
            Assert.AreSequenceEqual(new[] { ("cjk", 1, (int?)null) }, Chunks(batch.GetProperty("result")));
            var row = batch.GetProperty("result").GetProperty("chunks")[0].GetProperty("items")[0];
            Assert.AreEqual(80_000, row.GetProperty("blob").GetString()!.Length);
            Assert.IsTrue(Encoding.UTF8.GetByteCount(batch.GetRawText()) is > 400_000 and <= 1024 * 1024, "the row is served whole, escaped the way the bridge encodes it");
            Assert.IsTrue(batch.GetRawText().Contains("\\u5B57", StringComparison.Ordinal));
        }
        using (var f = new Fixture())
        {
            // Two 40k-CJK rows are 240 KB raw (one batch under raw accounting) but 480 KB encoded:
            // the batch budget cuts after the first row and the following batch serves the second.
            f.Http.SnapshotResultBody = SnapshotCorpus(("cjk", "id,blob", 2, 40_000)).Replace(new string('x', 40_000), new string('字', 40_000), StringComparison.Ordinal);
            string id = (await f.Call("snapshot.open", SnapshotQuery(("cjk", "id,blob")))).GetProperty("result").GetProperty("snapshotId").GetString()!;
            var first = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = (object?)null }))).GetProperty("result");
            Assert.AreSequenceEqual(new[] { ("cjk", 1, (int?)1) }, Chunks(first));
            Assert.AreEqual("""{"collection":"cjk","offset":1}""", first.GetProperty("next").GetRawText());
            var second = (await f.Call("snapshot.readBatch", JsonSerializer.Serialize(new { snapshotId = id, next = first.GetProperty("next") }))).GetProperty("result");
            Assert.AreSequenceEqual(new[] { ("cjk", 1, (int?)null) }, Chunks(second));
            Assert.AreEqual("null", second.GetProperty("next").GetRawText());
            Assert.AreEqual(2, f.Http.Calls.Count(call => call.GetProperty("method").GetString() == "snapshot.validate"));
        }
    }

    [TestMethod]
    public async Task SnapshotAdmissionSweepsExpiredHandlesInsteadOfWaitingForTheTimer()
    {
        using var f = new Fixture();
        f.Api.SnapshotLifetime = TimeSpan.FromMilliseconds(150);
        string first = (await f.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
        Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
        await Task.Delay(400);
        // The cleanup timer runs every 30 s; admission cannot wait for it. Both handles are past
        // their absolute lifetime, so the surface admits fresh opens without reaching the limit.
        f.Api.SnapshotLifetime = TimeSpan.FromMinutes(5);
        Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
        Assert.IsTrue((await f.Call("snapshot.open", SnapshotRequest)).TryGetProperty("result", out _));
        Assert.AreEqual("plugin_snapshot_limit", Code(await f.Call("snapshot.open", SnapshotRequest)));
        // An expired handle is stale with or without a sweep, and never costs an authority round trip.
        Assert.AreEqual("plugin_snapshot_stale", Code(await f.Call("snapshot.page", JsonSerializer.Serialize(new { snapshotId = first, collection = "orders", offset = 0, pageSize = 1 }))));
        Assert.IsFalse(f.Http.Calls.Any(call => call.GetProperty("method").GetString() == "snapshot.validate"));
    }

    [TestMethod]
    public async Task CursorReleaseIsScopedIdempotentAndDoesNotCallAuthority()
    {
        using var fixture = new Fixture();
        const string query = "{\"collection\":\"orders\"}";
        string cursor = (await fixture.Call("data.query", query)).GetProperty("result").GetProperty("nextCursor").GetString()!;
        var foreign = fixture.Surfaces.Open(fixture.Context.ProjectKey, "com.example.foreign", PluginPackageRevision.Create("C:/plugins/foreign", new string('b', 64)), "ui/index.html");
        fixture.Surfaces.BindIdentity(foreign.SurfaceToken, fixture.Context, 1, "3.x", installationId: "foreign-install");
        int calls = fixture.Http.Calls.Count;
        string foreignReply = await fixture.Api.HandleAsync(foreign.SurfaceToken, foreign.DocumentUri.AbsoluteUri,
            fixture.Wire("data.releaseCursor", JsonSerializer.Serialize(new { cursor }), token: foreign.SurfaceToken));
        Assert.IsTrue(JsonDocument.Parse(foreignReply).RootElement.GetProperty("result").GetProperty("released").GetBoolean());
        Assert.AreEqual(calls, fixture.Http.Calls.Count);
        var continued = await fixture.Call("data.query", JsonSerializer.Serialize(new { collection = "orders", cursor }));
        Assert.IsFalse(continued.TryGetProperty("error", out _), "foreign release must not remove the owner's handle");
        string next = continued.GetProperty("result").GetProperty("nextCursor").GetString()!;
        calls = fixture.Http.Calls.Count;
        foreach (string handle in new[] { next, next, "unknown" })
            Assert.IsTrue((await fixture.Call("data.releaseCursor", JsonSerializer.Serialize(new { cursor = handle }))).GetProperty("result").GetProperty("released").GetBoolean());
        Assert.AreEqual(calls, fixture.Http.Calls.Count);
        Assert.AreEqual("plugin_capability_invalid", Code(await fixture.Call("data.releaseCursor", "{\"cursor\":\"x\",\"collection\":\"orders\"}")));
        fixture.Surfaces.Close(fixture.Surface.SurfaceToken);
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("data.releaseCursor", JsonSerializer.Serialize(new { cursor = next }))));
    }

    [TestMethod]
    public async Task DataVersionsIsExactlyOneNormalAuthorityRequestWithAClosedShapeAndNoFallback()
    {
        using var fixture = new Fixture();
        const string version = """{"schemaRevision":"schema_0001","dataRevision":0,"clockPeriod":"","computed":{}}""";
        const string parameters = """{"contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"tbl","fields":["id","name"]}],"versions":{"tbl":""" + version + """}}""";
        fixture.Http.VersionsResultBody = """{"contract":"vibetable.plugin-data-versions.v1","versions":{"tbl":""" + version + """},"stale":[]}""";
        // The frame request is forwarded verbatim (projections and prior versions
        // included) as one ordinary authority request; nothing is resolved locally.
        var result = await fixture.Call("data.versions", parameters);
        Assert.AreEqual("vibetable.plugin-data-versions.v1", result.GetProperty("result").GetProperty("contract").GetString());
        string forwarded = fixture.Http.Calls.Single().GetProperty("request").GetRawText();
        Assert.IsTrue(JsonNode.DeepEquals(JsonNode.Parse(parameters)!["sources"], JsonNode.Parse(forwarded)!["sources"]));
        Assert.IsTrue(JsonNode.DeepEquals(JsonNode.Parse(parameters)!["versions"], JsonNode.Parse(forwarded)!["versions"]));
        // Authority rejections keep their own code: a permission failure is never
        // reported as a missing capability and there is no second request.
        fixture.Http.VersionsErrorCode = "plugin_read_denied";
        Assert.AreEqual("plugin_read_denied", Code(await fixture.Call("data.versions", parameters)));
        Assert.AreEqual(2, fixture.Http.Calls.Count);
        // Shape violations fail before the authority is contacted.
        foreach (string malformed in new[]
        {
            "[]", "{}", """{"contract":"vibetable.plugin-data-versions.v1"}""", """{"sources":[]}""",
            """{"contract":"vibetable.plugin-data-versions.v1","sources":[],"extra":1}""",
            """{"contract":"vibetable.plugin-data-versions.v1","contract":"x","sources":[]}""",
        })
            Assert.AreEqual("plugin_capability_invalid", Code(await fixture.Call("data.versions", malformed)));
        Assert.AreEqual(2, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task CursorCapacityStays64DuringConcurrentQueriesAndReleaseBypassesFullNormalSlots()
    {
        using var fixture = new Fixture();
        var handles = new List<string>();
        for (int i = 0; i < 64; i++) handles.Add((await fixture.Call("data.query", "{\"collection\":\"orders\"}")).GetProperty("result").GetProperty("nextCursor").GetString()!);
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var resume = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int pending = 0;
        fixture.Http.OnQuery = token => { if (Interlocked.Increment(ref pending) == 8) entered.TrySetResult(); return resume.Task.WaitAsync(token); };
        var queries = Enumerable.Range(0, 8).Select(_ => fixture.Call("data.query", "{\"collection\":\"orders\"}")).ToArray();
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        int authorityCalls = fixture.Http.Calls.Count;
        try
        {
            Assert.AreEqual("plugin_concurrency_limit", Code(await fixture.Call("data.query", "{\"collection\":\"orders\"}")));
            for (int i = 0; i < 4; i++)
                Assert.IsTrue((await fixture.Call("data.releaseCursor", JsonSerializer.Serialize(new { cursor = handles[i] }))).GetProperty("result").GetProperty("released").GetBoolean());
            Assert.AreEqual(authorityCalls, fixture.Http.Calls.Count);
        }
        finally { resume.TrySetResult(); }
        JsonElement[] results = await Task.WhenAll(queries);
        Assert.AreEqual(4, results.Count(result => !result.TryGetProperty("error", out _)));
        Assert.AreEqual(4, results.Count(result => result.TryGetProperty("error", out var error) && error.GetProperty("code").GetString() == "plugin_query_limit"));
        fixture.Http.OnQuery = null;
        Assert.AreEqual("plugin_query_limit", Code(await fixture.Call("data.query", "{\"collection\":\"orders\"}")));
        foreach (var result in results.Where(result => !result.TryGetProperty("error", out _))) handles.Add(result.GetProperty("result").GetProperty("nextCursor").GetString()!);
        foreach (string cursor in handles) await fixture.Call("data.releaseCursor", JsonSerializer.Serialize(new { cursor }));
        Assert.IsFalse((await fixture.Call("data.query", "{\"collection\":\"orders\"}")).TryGetProperty("error", out _));
    }

    [TestMethod]
    public async Task CancellingAfterDeliveryOrAtLateAuthorityReadDoesNotLeakQueryHandles()
    {
        using var fixture = new Fixture();
        for (int i = 0; i < 70; i++)
        {
            string wire = fixture.Wire("data.query", "{\"collection\":\"orders\"}");
            string id = JsonDocument.Parse(wire).RootElement.GetProperty("requestId").GetString()!;
            Task<string>? cancel = null;
            if (i % 2 == 0)
            {
                fixture.Http.AfterReadMethod = "data.query";
                fixture.Http.AfterCommitRead = () => cancel = fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source,
                    fixture.Wire("request.cancel", JsonSerializer.Serialize(new { requestId = id })));
            }
            JsonElement response = JsonDocument.Parse(await fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source, wire)).RootElement.Clone();
            fixture.Http.AfterCommitRead = null;
            if (i % 2 == 0)
            {
                Assert.IsNotNull(cancel); await cancel;
                Assert.AreEqual("plugin_cancel_requested", Code(response));
            }
            else
            {
                string cursor = response.GetProperty("result").GetProperty("nextCursor").GetString()!;
                await fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source,
                    fixture.Wire("request.cancel", JsonSerializer.Serialize(new { requestId = id })));
                Assert.AreEqual("plugin_cursor_invalid", Code(await fixture.Call("data.query", JsonSerializer.Serialize(new { collection = "orders", cursor }))));
            }
        }
        for (int i = 0; i < 64; i++) Assert.IsFalse((await fixture.Call("data.query", "{\"collection\":\"orders\"}")).TryGetProperty("error", out _));
        Assert.AreEqual("plugin_query_limit", Code(await fixture.Call("data.query", "{\"collection\":\"orders\"}")));
    }

    [TestMethod]
    public async Task QueryEnvelopeFailureReclaimsTheHandleAllocatedBeforeSerialization()
    {
        using var fixture = new Fixture();
        const string prefix = "{\"items\":[{\"text\":\"";
        const string suffix = "\"}],\"nextCursor\":\"go-cursor\",\"dependencies\":{}}";
        fixture.Http.QueryResultBody = prefix + new string('x', 1024 * 1024 - 32 - prefix.Length - suffix.Length) + suffix;
        Assert.AreEqual("plugin_message_too_large", Code(await fixture.Call("data.query", "{\"collection\":\"orders\"}")));
        fixture.Http.QueryResultBody = null;
        for (int i = 0; i < 64; i++) Assert.IsFalse((await fixture.Call("data.query", "{\"collection\":\"orders\"}")).TryGetProperty("error", out _));
    }

    [TestMethod]
    public async Task SharedOfflineOutcomeCorpusMatchesActualHostRoutingAndCommitBoundary()
    {
        var root = new DirectoryInfo(AppContext.BaseDirectory);
        while (root is not null && !File.Exists(Path.Combine(root.FullName, "contracts", "plugin-surface-api.v1.json"))) root = root.Parent;
        Assert.IsNotNull(root, "repository contract root must be available");
        using var corpus = JsonDocument.Parse(await File.ReadAllTextAsync(Path.Combine(root.FullName,
            "tests", "contract", "fixtures", "plugin-surface-outcomes-v1.json")));
        foreach (var item in corpus.RootElement.GetProperty("cases").EnumerateArray())
        {
            string scenario = item.GetProperty("scenario").GetString()!;
            using var fixture = new Fixture(risk: scenario == "read-only" ? "read" : "destructive")
            { Decision = scenario == "rejected" ? "rejected" : scenario == "cancel-before-approval" ? "cancel" : "approved" };
            fixture.Http.Conflict = scenario == "conflict";
            fixture.Http.LoseCommit = scenario == "lost-receipt";
            fixture.Http.Incomplete = scenario == "incomplete-receipt";
            JsonElement response;
            if (scenario == "unsupported-protocol") response = await fixture.Call("context.read", "{}", version: "vibetable.plugin-api.v99");
            else
            {
                response = await fixture.Call("mutation.preview", Plan);
                if (scenario != "read-only")
                {
                    string plan = response.GetProperty("result").GetProperty("planId").GetString()!;
                    response = await fixture.Call("mutation.submit", JsonSerializer.Serialize(new { planId = plan }));
                    Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", JsonSerializer.Serialize(new { planId = plan }))), scenario);
                }
            }
            if (item.TryGetProperty("code", out var expectedCode)) Assert.AreEqual(expectedCode.GetString(), Code(response), scenario);
            else Assert.AreEqual(item.GetProperty("status").GetString(), response.GetProperty("result").GetProperty("status").GetString(), scenario);
            Assert.AreEqual(item.GetProperty("commits").GetInt32(), fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"), scenario);
        }
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task SameWorkspaceRendererDrainKeepsSuccessOrUnknownForHistoryProjection(bool incomplete)
    {
        using var fixture = new Fixture(); fixture.Http.Incomplete = incomplete;
        Task? drain = null;
        fixture.Http.AfterCommitRead = () =>
        {
            fixture.DropRendererNotifications = true;
            fixture.Surfaces.CloseAll();
            drain = fixture.Api.DrainAsync();
        };
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
        Assert.IsNotNull(drain); await drain.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.IsTrue(fixture.DroppedRendererNotifications > 0);
        Assert.IsFalse(fixture.Surfaces.IsActive(fixture.Surface.SurfaceToken));
        var history = fixture.Tasks.ProjectHistory(fixture.Context);
        var task = history.Single().Snapshot.Deserialize<PluginRuntimeTaskSnapshot>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
        Assert.AreEqual(incomplete ? "aborted" : "succeeded", task.State);
        if (incomplete) Assert.AreEqual("plugin_commit_unknown", task.Error!.Code);
        Assert.AreEqual(fixture.Surface.SurfaceToken, task.SurfaceToken);
        Assert.AreEqual("record-surface", task.InstallationId);
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        Assert.IsFalse(fixture.Tasks.IsInteractionPending(new(fixture.LastInteraction!.RunId,
            fixture.LastInteraction.PendingConfirmation!.InteractionId, "approved")));
    }

    [TestMethod]
    public async Task SamePackageInstallationReplacementRetiresApiPlansWithoutFrameAcknowledgement()
    {
        using var fixture = new Fixture();
        using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), fixture.Surfaces);
        var revision = fixture.Surface.Revision;
        resources.RegisterInstalled(fixture.Surface.ProjectKey, fixture.Surface.PluginId, revision, fixture.Surface.InstallationId);
        string planId = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        fixture.Http.AfterReadMethod = "context.read";
        fixture.Http.AfterCommitRead = () => resources.RegisterInstalled(fixture.Surface.ProjectKey, fixture.Surface.PluginId, revision, "replacement-record");
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("context.read", "{}")));
        Assert.IsFalse(fixture.Surfaces.IsActive(fixture.Surface.SurfaceToken));
        var replacement = resources.OpenSurface(fixture.Surface.ProjectKey, fixture.Surface.PluginId, "ui/index.html");
        Assert.AreNotEqual(fixture.Surface.SurfaceToken, replacement.SurfaceToken);
        fixture.Surfaces.BindIdentity(replacement.SurfaceToken, fixture.Context, 1, "3.x", "1.0.0", "save", "destructive", "ui/index.html", installationId: "replacement-record");
        string response = await fixture.Api.HandleAsync(replacement.SurfaceToken, replacement.DocumentUri.AbsoluteUri,
            fixture.Wire("mutation.submit", $"{{\"planId\":\"{planId}\"}}", replacement.SurfaceToken));
        Assert.AreEqual("plugin_plan_stale", Code(response));
        Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "mutation.commit"));
    }

    [TestMethod]
    [DataRow("confirmation")]
    [DataRow("commit")]
    [DataRow("receipt")]
    public async Task SamePackageRevisionRetiresPlansConfirmationAndPreservesCommitOutcome(string boundary)
    {
        using var fixture = new Fixture();
        using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), fixture.Surfaces);
        var original = fixture.Surface;
        resources.RegisterInstalled(original.ProjectKey, original.PluginId, original.Revision, original.InstallationId, 1);
        await fixture.Call("context.read", "{}");
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        Action reload = () => resources.RegisterInstalled(original.ProjectKey, original.PluginId, original.Revision, original.InstallationId, 2);
        if (boundary == "confirmation") fixture.OnConfirmation = reload;
        else if (boundary == "commit") fixture.Http.OnCommit = reload;
        else fixture.Http.AfterCommitRead = reload;
        var response = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}").WaitAsync(TimeSpan.FromSeconds(3));
        Assert.IsFalse(fixture.Surfaces.IsActive(original.SurfaceToken));
        Assert.IsFalse(fixture.Tasks.IsInteractionPending(new(fixture.LastInteraction!.RunId, fixture.LastInteraction.PendingConfirmation!.InteractionId, "approved")));
        Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
        Assert.AreEqual(boundary == "confirmation" ? "cancelled" : boundary == "commit" ? "aborted" : "succeeded", task.State);
        if (boundary == "commit")
        {
            Assert.IsTrue(fixture.Http.CommitObservedCancellation);
            Assert.AreEqual("plugin_commit_unknown", Code(response));
            Assert.AreEqual("plugin_commit_unknown", task.Error!.Code);
        }
        if (boundary == "receipt") Assert.AreEqual("succeeded", response.GetProperty("result").GetProperty("status").GetString());
        int commits = fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit");
        Assert.AreEqual(boundary == "confirmation" ? 0 : 1, commits);
        Assert.AreEqual(HostPluginTaskCancelOutcome.Terminal, fixture.Tasks.RequestCancel(fixture.TaskId, out _, out _));
        fixture.Tasks.FinishNative(fixture.TaskId, "cancelled");
        Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var sticky));
        Assert.AreEqual(task.State, sticky.State);
        var replacement = resources.OpenSurface(original.ProjectKey, original.PluginId, "ui/index.html");
        Assert.AreNotEqual(original.SurfaceToken, replacement.SurfaceToken);
        fixture.Surfaces.BindIdentity(replacement.SurfaceToken, fixture.Context, 2, "3.x", "1.0.0", "save", "destructive", "ui/index.html", installationId: original.InstallationId);
        Assert.IsFalse(JsonDocument.Parse(await fixture.Api.HandleAsync(replacement.SurfaceToken, replacement.DocumentUri.AbsoluteUri, fixture.Wire("context.read", "{}", replacement.SurfaceToken))).RootElement.TryGetProperty("error", out _));
        Assert.AreEqual("plugin_plan_stale", Code(await fixture.Api.HandleAsync(replacement.SurfaceToken, replacement.DocumentUri.AbsoluteUri, fixture.Wire("mutation.submit", $"{{\"planId\":\"{plan}\"}}", replacement.SurfaceToken))));
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("context.read", "{}")));
    }

    [TestMethod]
    public async Task ContextUsesBoundInvocationThroughAuthorityProjection()
    {
        using var fixture = new Fixture();
        var context = new PluginRuntimeCommandContext("vibetable.command-context.v1", fixture.Context.ProjectKey, "orders",
            [JsonSerializer.SerializeToElement("selected")], JsonSerializer.SerializeToElement(new { hidden = "never copy directly" }),
            "zh-CN", "dark", "comfortable", JsonSerializer.SerializeToElement(new { id = "local-user" }), "1.0");
        fixture.Surfaces.BindActiveCollection(fixture.Context.ProjectKey, fixture.Surface.PluginId, fixture.Surface.ActionId, context);
        var response = await fixture.Call("context.read", "{}");
        var invocation = fixture.Http.Calls.Single().GetProperty("invocationContext");
        Assert.AreEqual("selected", invocation.GetProperty("selectedKeys")[0].GetString());
        Assert.AreEqual("local-user", invocation.GetProperty("user").GetProperty("id").GetString());
        Assert.IsFalse(response.GetProperty("result").TryGetProperty("querySnapshot", out _));
        Assert.AreEqual("dark", response.GetProperty("result").GetProperty("theme").GetString());
    }

    [TestMethod]
    public async Task BackgroundSurfaceAdmitsReadsOnlyAndRecoversAfterPromotion()
    {
        using var fixture = new Fixture(presentation: "application", phase: PluginSurfacePhases.Background);
        Assert.AreEqual(PluginSurfacePhases.Background, fixture.Surface.Phase);

        // Observation is admitted without an invocation context and reports the authoritative phase.
        var observed = await fixture.Call("context.read", "{}");
        Assert.AreEqual(PluginSurfacePhases.Background, observed.GetProperty("result").GetProperty("surfacePhase").GetString());
        Assert.IsFalse(observed.GetProperty("result").TryGetProperty("collection", out _), "Background is not bound to any table.");
        Assert.IsFalse(fixture.Http.Calls.Single().TryGetProperty("invocationContext", out _), "Background reads carry no selection context.");
        // The preparation set: pure-read model bindings, one snapshot, its release.
        Assert.AreEqual("completed", (await fixture.Call("model.read", "{}")).GetProperty("result").GetProperty("status").GetString());
        string snapshot = (await fixture.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.IsFalse((await fixture.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = snapshot }))).TryGetProperty("error", out _));
        int reads = fixture.Http.Calls.Count;

        // Everything else is refused host-side before any capability, dialog or write path runs —
        // including reads the preparation does not need (attachments, subscriptions, datasets, storage).
        foreach (var (method, parameters) in new[]
        {
            ("storage.set", "{}"), ("storage.get", "{}"), ("storage.delete", "{}"),
            ("mutation.submit", "{}"), ("transaction.configure", "{}"),
            ("output.print", "{}"), ("file.pickRead", "{}"), ("data.import.start", "{}"), ("data.export.start", "{}"),
            ("model.status", "{}"), ("model.preview", "{}"), ("model.catalog", "{}"), ("model.configure", "{}"),
            ("attachments.list", "{}"), ("attachments.read", "{}"), ("attachments.previewChange", "{}"),
            ("data.query", "{}"), ("data.subscribe", "{}"), ("data.describe", "{}"), ("dataset.open", "{}"), ("data.io.start", "{}"),
        })
            Assert.AreEqual("plugin_surface_background", Code(await fixture.Call(method, parameters)), method);
        Assert.AreEqual(reads, fixture.Http.Calls.Count, "Refused methods never reach the authority.");
        Assert.AreEqual(0, fixture.Prints, "A hidden surface cannot open a print dialog.");
        Assert.AreEqual(PluginSurfacePhases.Background, fixture.Surface.Phase, "Denied writes never promote a surface.");

        // Opening the application binds the active collection and promotes the surface.
        var context = new PluginRuntimeCommandContext("vibetable.command-context.v1", fixture.Context.ProjectKey, "orders",
            [JsonSerializer.SerializeToElement("selected")], null, "zh-CN", "dark", "comfortable", JsonSerializer.SerializeToElement(new { id = "local-user" }), "1.0");
        fixture.Surfaces.BindActiveCollection(fixture.Context.ProjectKey, fixture.Surface.PluginId, fixture.Surface.ActionId, context);
        fixture.Surfaces.TryGet(fixture.Surface.SurfaceToken, out var promoted);
        Assert.AreEqual(PluginSurfacePhases.Foreground, promoted!.Phase);
        var foreground = await fixture.Call("context.read", "{}");
        Assert.AreEqual(PluginSurfacePhases.Foreground, foreground.GetProperty("result").GetProperty("surfacePhase").GetString());
        Assert.AreEqual("selected", fixture.Http.Calls.Last().GetProperty("invocationContext").GetProperty("selectedKeys")[0].GetString(), "Promotion binds the active selection.");
        // Promotion restores exactly the declared capability: a refused read-set member and a write both pass.
        Assert.IsFalse((await fixture.Call("data.query", "{}")).TryGetProperty("error", out _));
        Assert.IsFalse((await fixture.Call("storage.set", "{}")).TryGetProperty("error", out _), "Promotion restores the declared write capability.");

        // Once adopted, a promoted surface follows the foreground stale rules like any other.
        fixture.Surfaces.BindActiveCollection(fixture.Context.ProjectKey, fixture.Surface.PluginId, fixture.Surface.ActionId,
            context with { Collection = "notes" });
        Assert.AreEqual("plugin_session_stale", Code(await fixture.Call("context.read", "{}")));
    }

    [TestMethod]
    public async Task BackgroundSurfaceHoldsOneSnapshotHandleAndPromotionRestoresTheForegroundAllowance()
    {
        using var fixture = new Fixture(presentation: "application", phase: PluginSurfacePhases.Background);
        // Up to four frames are pre-warmed: one handle each keeps the total of eight for the visible application.
        string first = (await fixture.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.AreEqual("plugin_snapshot_limit", Code(await fixture.Call("snapshot.open", SnapshotRequest)), "A second open is refused before the authority is asked.");
        // Released capacity is reusable in background.
        Assert.IsFalse((await fixture.Call("snapshot.release", JsonSerializer.Serialize(new { snapshotId = first }))).TryGetProperty("error", out _));
        string second = (await fixture.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        // Promotion restores the foreground allowance of two handles.
        var context = new PluginRuntimeCommandContext("vibetable.command-context.v1", fixture.Context.ProjectKey, "orders",
            [JsonSerializer.SerializeToElement("selected")], null, "zh-CN", "dark", "comfortable", JsonSerializer.SerializeToElement(new { id = "local-user" }), "1.0");
        fixture.Surfaces.BindActiveCollection(fixture.Context.ProjectKey, fixture.Surface.PluginId, fixture.Surface.ActionId, context);
        string third = (await fixture.Call("snapshot.open", SnapshotRequest)).GetProperty("result").GetProperty("snapshotId").GetString()!;
        Assert.AreNotEqual(second, third);
        Assert.AreEqual("plugin_snapshot_limit", Code(await fixture.Call("snapshot.open", SnapshotRequest)));
    }

    [TestMethod]
    public async Task SharedLifecycleContractMatchesTheHostBackgroundGate()
    {
        var root = new DirectoryInfo(AppContext.BaseDirectory);
        while (root is not null && !File.Exists(Path.Combine(root.FullName, "contracts", "plugin-surface-api.v1.json"))) root = root.Parent;
        Assert.IsNotNull(root, "repository contract root must be available");
        using var contract = JsonDocument.Parse(await File.ReadAllTextAsync(Path.Combine(root.FullName, "contracts", "plugin-surface-api.v1.json")));
        var lifecycle = contract.RootElement.GetProperty("lifecycle");
        Assert.AreEqual(PluginContractVersions.SurfaceLifecycle, lifecycle.GetProperty("contract").GetString());
        var limits = lifecycle.GetProperty("limits");
        Assert.AreEqual(PluginSurfaceSessionManager.MaxBackgroundSurfaces, limits.GetProperty("maxBackgroundSurfaces").GetInt32());
        Assert.AreEqual(PluginSurfaceApi.BackgroundSnapshotHandles, limits.GetProperty("snapshotHandlesPerBackgroundSurface").GetInt32());
        Assert.AreEqual(PluginSurfaceApi.ForegroundSnapshotHandles, limits.GetProperty("snapshotHandlesPerForegroundSurface").GetInt32());
        Assert.AreEqual(PluginSurfaceApi.SnapshotHandlesTotal, limits.GetProperty("snapshotHandlesTotal").GetInt32());
        CollectionAssert.AreEqual(new[] { PluginSurfacePhases.Background, PluginSurfacePhases.Foreground },
            lifecycle.GetProperty("phases").EnumerateArray().Select(item => item.GetString()).ToArray());
        var published = contract.RootElement.GetProperty("methods").EnumerateArray().Select(item => item.GetString()!).ToHashSet(StringComparer.Ordinal);
        var allowlist = lifecycle.GetProperty("background").GetProperty("allowlist").EnumerateArray().Select(item => item.GetString()!).ToHashSet(StringComparer.Ordinal);
        Assert.IsTrue(allowlist.SetEquals(PluginSurfaceApi.BackgroundMethods), "The published allowlist is exactly the Host gate.");
        Assert.IsTrue(allowlist.IsSubsetOf(published), "The allowlist never names a method outside the shared surface API.");
        // Real behaviour: every published method outside the allowlist is refused by the Host before any
        // capability runs, whatever parameters it carries.
        using var fixture = new Fixture(presentation: "application", phase: PluginSurfacePhases.Background);
        int refused = 0;
        foreach (string method in published.Where(method => !allowlist.Contains(method)).Order(StringComparer.Ordinal))
        {
            Assert.AreEqual("plugin_surface_background", Code(await fixture.Call(method, "{}")), method);
            refused++;
        }
        Assert.IsTrue(refused > 30, "The contract lists the write surface, not only the preparation set.");
        Assert.AreEqual(0, fixture.Http.Calls.Count, "Refused methods never reach the authority.");
        Assert.AreEqual(PluginSurfacePhases.Background, fixture.Surface.Phase);
    }

    [TestMethod]
    public async Task BackgroundReadsStayStaleAcrossSessionRetirement()
    {
        using var fixture = new Fixture(phase: PluginSurfacePhases.Background);
        Assert.IsFalse((await fixture.Call("context.read", "{}")).TryGetProperty("error", out _));
        fixture.RetireAfterReceipt();
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("context.read", "{}")),
            "Pre-warmed surfaces release with the workspace session like every other surface.");
    }

    [TestMethod]
    public async Task ActualSourceTokenVersionAndUnmappedIdentityFailBeforeAuthority()
    {
        using var fixture = new Fixture();
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("context.read", "{}", source: "https://evil.test/")));
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("context.read", "{}", token: "forged")));
        Assert.AreEqual("plugin_api_unsupported", Code(await fixture.Call("context.read", "{}", version: "vibetable.plugin-api.v99")));
        string extra = fixture.Wire("context.read", "{}").Replace("\"method\"", "\"pluginId\":\"forged\",\"method\"");
        Assert.AreEqual("plugin_capability_invalid", Code(await fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source, extra)));
        Assert.AreEqual("plugin_api_unsupported", Code(await fixture.Call("mutation.apply", "{}")));
        Assert.AreEqual("plugin_message_too_large", Code(await fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source, new string('x', 1024 * 1024 + 1))));
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task OversizeEnvelopeRejectsWithOnlyItsBoundedRootRequestId()
    {
        using var fixture = new Fixture();
        string raw = fixture.Wire("storage.set", "{\"key\":\"oversize\",\"value\":\"" + new string('x', 1024 * 1024 + 1) + "\"}");
        string id = JsonDocument.Parse(raw).RootElement.GetProperty("requestId").GetString()!;
        var reply = JsonDocument.Parse(await fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source, raw)).RootElement;
        Assert.AreEqual("plugin_message_too_large", reply.GetProperty("error").GetProperty("code").GetString());
        Assert.AreEqual(id, reply.GetProperty("requestId").GetString());
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task OversizeEnvelopeCannotBorrowAnUnsafeOrNestedRequestId()
    {
        using var fixture = new Fixture();
        string prefix = "{\"contract\":\"vibetable.plugin-api.v1\",\"surfaceToken\":\"owned\",\"method\":\"storage.set\",";
        foreach (string header in new[] {
            prefix + "\"params\":{\"requestId\":\"nested\",\"value\":\"",
            prefix + "\"requestId\":\"" + new string('i', 129) + "\",\"params\":\"",
            prefix + "\"requestId\":4,\"params\":\"",
            prefix + "\"requestId\":\"\",\"params\":\"",
            prefix + "\"requestId\":\"first\",\"requestId\":\"duplicate\",\"params\":\"",
            "{\"contract\":\"" + new string('h', 4096) + "\",\"requestId\":\"late\",\"params\":\"",
            "{\"contract\": false garbage,\"requestId\":\"forged\",\"params\":\"",
            "[" + prefix + "\"requestId\":\"not-root\",\"params\":\"" })
        {
            var reply = JsonDocument.Parse(await fixture.Api.HandleAsync(fixture.Surface.SurfaceToken, fixture.Source,
                header + new string('x', 1024 * 1024 + 1))).RootElement;
            Assert.AreEqual("plugin_message_too_large", reply.GetProperty("error").GetProperty("code").GetString());
            Assert.AreEqual(JsonValueKind.Null, reply.GetProperty("requestId").ValueKind);
        }
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task MissingInstallationIdentityFailsBeforeAuthority()
    {
        using var fixture = new Fixture();
        fixture.Surfaces.BindIdentity(fixture.Surface.SurfaceToken, fixture.Context, 1, "3.x");
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("getContext", "{}")));
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    [TestMethod]
    public async Task LongLivedSurfaceHasPerRequestBudgetAndOpaqueSingleUseCursors()
    {
        using var fixture = new Fixture();
        for (int i = 0; i < 100; i++) Assert.IsFalse((await fixture.Call("context.read", "{}")).TryGetProperty("error", out _));
        var first = await fixture.Call("data.query", "{\"collection\":\"orders\"}");
        string cursor = first.GetProperty("result").GetProperty("nextCursor").GetString()!;
        Assert.AreNotEqual("go-cursor", cursor);
        var peerSurface = fixture.Surfaces.Open(fixture.Context.ProjectKey, "com.example.other", PluginPackageRevision.Create("C:/plugins/other", new string('b', 64)), "ui/index.html");
        fixture.Surfaces.BindIdentity(peerSurface.SurfaceToken, fixture.Context, 1, "3.x", installationId: "record-surface");
        string peerReply = await fixture.Api.HandleAsync(peerSurface.SurfaceToken, peerSurface.DocumentUri.AbsoluteUri,
            fixture.Wire("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}", token: peerSurface.SurfaceToken));
        Assert.AreEqual("plugin_cursor_invalid", Code(peerReply));
        using var other = new Fixture(new PluginProjectContext("local:22222222222242228222222222222222", "workspace:9", 9));
        Assert.AreEqual("plugin_cursor_invalid", Code(await other.Call("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}")));
        var fetched = await fixture.Call("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}");
        Assert.IsFalse(fetched.TryGetProperty("error", out _));
        Assert.AreEqual("go-cursor", fixture.Http.Calls[^1].GetProperty("cursor").GetString());
        Assert.AreEqual("plugin_cursor_invalid", Code(await fixture.Call("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}")));
        fixture.Surfaces.BindIdentity(fixture.Surface.SurfaceToken, fixture.Context, 2, "3.x", installationId: "record-surface");
        Assert.AreEqual("plugin_session_stale", Code(await fixture.Call("context.read", "{}")));
    }

    [TestMethod]
    public async Task PaginationCursorBindsQueryByJsonSemanticsNotPropertyOrder()
    {
        using var fixture = new Fixture();
        string query = """{"contract":"vibetable.plugin-query.v2","collection":"orders","fields":["id","name"],"filters":[{"field":"name","operator":"eq","value":{"deep":{"a":1,"b":[1,2]}}}]}""";
        string first = (await fixture.Call("data.query", query)).GetProperty("result").GetProperty("nextCursor").GetString()!;
        // Same query with top-level and nested object properties reordered stays valid.
        string reordered = """{"fields":["id","name"],"filters":[{"operator":"eq","field":"name","value":{"deep":{"b":[1,2],"a":1}}}],"collection":"orders","contract":"vibetable.plugin-query.v2","cursor":"CURSOR"}""".Replace("CURSOR", first, StringComparison.Ordinal);
        var continued = await fixture.Call("data.query", reordered);
        Assert.IsFalse(continued.TryGetProperty("error", out _));
        Assert.AreEqual("go-cursor", fixture.Http.Calls[^1].GetProperty("cursor").GetString());
        // Each negative keeps contract/filters identical to the first request and
        // changes exactly one target: fields content, fields order, or the nested b array.
        string[] continuations =
        [
            """{"contract":"vibetable.plugin-query.v2","collection":"orders","fields":["id","status"],"filters":[{"field":"name","operator":"eq","value":{"deep":{"a":1,"b":[1,2]}}}],"cursor":"CURSOR"}""",
            """{"contract":"vibetable.plugin-query.v2","collection":"orders","fields":["name","id"],"filters":[{"field":"name","operator":"eq","value":{"deep":{"a":1,"b":[1,2]}}}],"cursor":"CURSOR"}""",
            """{"contract":"vibetable.plugin-query.v2","collection":"orders","fields":["id","name"],"filters":[{"field":"name","operator":"eq","value":{"deep":{"a":1,"b":[1,3]}}}],"cursor":"CURSOR"}""",
        ];
        foreach (string continuation in continuations)
        {
            string cursor = (await fixture.Call("data.query", query)).GetProperty("result").GetProperty("nextCursor").GetString()!;
            Assert.AreEqual("plugin_cursor_invalid", Code(await fixture.Call("data.query", continuation.Replace("CURSOR", cursor, StringComparison.Ordinal))));
        }
    }

    [TestMethod]
    public async Task HostConfirmsExactPlanOnceAndLateCancelPreservesSuccessWithoutPythonGateway()
    {
        using var fixture = new Fixture { Decision = "approved" };
        Assert.IsNotNull(fixture.Tasks.CaptureNative());
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        Assert.AreEqual("plugin_capability_invalid", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\",\"approved\":true}}")));
        var submitted = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
        Assert.AreEqual("succeeded", submitted.GetProperty("result").GetProperty("status").GetString());
        string taskId = submitted.GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.IsTrue(fixture.Tasks.TryGetTask(taskId, out var sourceTask));
        Assert.AreEqual(fixture.Surface.SurfaceToken, sourceTask.SurfaceToken);
        Assert.AreEqual(fixture.Surface.SurfaceToken, JsonSerializer.SerializeToElement(sourceTask, new JsonSerializerOptions(JsonSerializerDefaults.Web)).GetProperty("surfaceToken").GetString());
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        Assert.AreEqual(HostPluginTaskCancelOutcome.Terminal, fixture.Tasks.RequestCancel(taskId, out var snapshot, out _));
        Assert.AreEqual("succeeded", snapshot.State);
        Assert.AreEqual(1, snapshot.InstallationRevision);
        Assert.AreEqual("record-surface", snapshot.InstallationId);
        Assert.AreEqual("com.example.surface", fixture.Http.Calls[^1].GetProperty("pluginId").GetString());
    }

    [TestMethod]
    public async Task DeleteEscalatesConfirmationRiskAndBindsTheExactPlan()
    {
        const string delete = """{"contract":"vibetable.surface-mutation-plan.v1","collection":"orders","schemaRevision":"schema_1","operations":[{"kind":"delete","primaryKey":"r1","expectedDigest":"sha256:existing"}]}""";
        foreach (string decision in new[] { "approved", "rejected", "cancel" })
        {
            using var fixture = new Fixture(risk: "write") { Decision = decision };
            string plan = (await fixture.Call("mutation.preview", delete)).GetProperty("result").GetProperty("planId").GetString()!;
            Assert.AreEqual("plugin_capability_invalid", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\",\"approved\":true}}")));
            Assert.AreEqual("plugin_api_unsupported", Code(await fixture.Call("interaction.resolve", "{\"decision\":\"approved\"}")));
            var response = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
            var confirmation = fixture.LastInteraction!.PendingConfirmation!;
            Assert.AreEqual("destructive", confirmation.Risk);
            Assert.AreEqual(1, confirmation.Preview.AffectedCount);
            Assert.AreEqual("orders", confirmation.Preview.Summary[0]["collection"].GetString());
            Assert.AreEqual("delete", confirmation.Preview.SampleRows[0]["kind"].GetString());
            Assert.AreEqual("r1", confirmation.Preview.SampleRows[0]["primaryKey"].GetString());
            Assert.AreEqual("sha256:existing", confirmation.Preview.SampleRows[0]["expectedDigest"].GetString());
            Assert.IsFalse(confirmation.Preview.SampleRows[0].ContainsKey("values"));
            int commits = fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit");
            Assert.AreEqual(decision == "approved" ? 1 : 0, commits);
            if (decision == "approved")
            {
                Assert.AreEqual("succeeded", response.GetProperty("result").GetProperty("status").GetString());
                var committed = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "mutation.commit");
                Assert.AreEqual(delete, committed.GetProperty("request").GetRawText());
            }
            Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        }
        using var read = new Fixture(risk: "read");
        Assert.AreEqual("plugin_write_denied", Code(await read.Call("mutation.preview", delete)));
        Assert.AreEqual(0, read.Http.Calls.Count);
    }

    [TestMethod]
    public async Task DeletePlansFromEveryPresentationAreNativeOnlyAndRendererCannotConsume()
    {
        const string delete = """{"contract":"vibetable.surface-mutation-plan.v1","collection":"orders","schemaRevision":"schema_1","operations":[{"kind":"delete","primaryKey":"r1","expectedDigest":"sha256:existing"}]}""";
        foreach (var (presentation, risk) in new[] { ("surface", "write"), ("surface", "destructive"), ("application", "write"), ("headless", "destructive") })
        {
            using var fixture = new Fixture(risk: risk, presentation: presentation) { Decision = "approved" };
            var requested = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            fixture.OnConfirmation = () => requested.TrySetResult(); // Hold the exact decision for this test.
            string plan = (await fixture.Call("mutation.preview", delete)).GetProperty("result").GetProperty("planId").GetString()!;
            var submitted = fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
            // The pending confirmation exists and is native-only: neither a
            // renderer approve nor a renderer reject may consume it.
            await requested.Task.WaitAsync(TimeSpan.FromSeconds(5));
            string runId = fixture.LastInteraction!.RunId!;
            string interactionId = fixture.LastInteraction.PendingConfirmation!.InteractionId!;
            Assert.IsTrue(fixture.Tasks.RequiresNativeConfirmation(runId), presentation);
            Assert.AreEqual("native-required", fixture.Tasks.BeginInteractionResolve(new(runId, interactionId, "approved")).Status);
            Assert.AreEqual("native-required", fixture.Tasks.BeginInteractionResolve(new(runId, interactionId, "rejected")).Status);
            Assert.IsTrue(fixture.Tasks.IsInteractionPending(new(runId, interactionId, "approved")));
            Assert.AreEqual(0, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
            // Only the exact one-shot native-host decision completes the submit.
            Assert.AreEqual("resolved", fixture.Tasks.BeginInteractionResolve(new(runId, interactionId, "approved"), fromNativeHost: true).Status);
            Assert.AreEqual("succeeded", (await submitted).GetProperty("result").GetProperty("status").GetString());
            // The settled run keeps its terminal state; even the native entry cannot resolve it again.
            Assert.AreEqual("expired", fixture.Tasks.BeginInteractionResolve(new(runId, interactionId, "approved"), fromNativeHost: true).Status);
            Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
            Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        }
        // A cancelled delete run clears its pending interaction; neither the
        // renderer nor the native entry can revive or consume it afterwards.
        using var cancelled = new Fixture(risk: "write") { Decision = "cancel" };
        string cancelledPlan = (await cancelled.Call("mutation.preview", delete)).GetProperty("result").GetProperty("planId").GetString()!;
        await cancelled.Call("mutation.submit", $"{{\"planId\":\"{cancelledPlan}\"}}");
        Assert.IsFalse(cancelled.Tasks.IsInteractionPending(new(cancelled.LastInteraction!.RunId!, cancelled.LastInteraction.PendingConfirmation!.InteractionId!, "approved")));
        Assert.AreEqual("expired", cancelled.Tasks.BeginInteractionResolve(new(cancelled.LastInteraction.RunId!, cancelled.LastInteraction.PendingConfirmation.InteractionId!, "approved"), fromNativeHost: true).Status);
        Assert.AreEqual(0, cancelled.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
    }

    [TestMethod]
    public async Task RejectionAndCancelWriteNothingAndLostReceiptIsUnknownWithoutReplay()
    {
        foreach (string decision in new[] { "rejected", "cancel", "approved" })
        {
            using var fixture = new Fixture { Decision = decision };
            fixture.Http.LoseCommit = decision == "approved";
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            var response = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
            int commits = fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit");
            if (decision == "approved")
            {
                Assert.AreEqual("plugin_commit_unknown", Code(response)); Assert.AreEqual(1, commits);
                Assert.AreEqual(HostPluginTaskCancelOutcome.Terminal, fixture.Tasks.RequestCancel(fixture.TaskId, out var terminal, out _));
                Assert.AreEqual("aborted", terminal.State);
                Assert.AreEqual("plugin_commit_unknown", terminal.Error!.Code);
                Assert.IsNull(fixture.Tasks.FinishNative(fixture.TaskId, "succeeded"));
                Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var sticky));
                Assert.AreEqual("aborted", sticky.State);
            }
            else { Assert.AreEqual(0, commits); if (decision == "rejected") Assert.AreEqual("rejected", response.GetProperty("result").GetProperty("status").GetString()); }
            Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        }
    }

    [TestMethod]
    public async Task InstallationRetirementCancelsPendingConfirmationWithoutFrameAcknowledgement()
    {
        using var fixture = new Fixture { Decision = "installation-retire" };
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        JsonElement response = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")
            .WaitAsync(TimeSpan.FromSeconds(3));
        Assert.AreEqual(0, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        Assert.IsTrue(response.TryGetProperty("error", out _));
        Assert.IsFalse(fixture.Surfaces.IsActive(fixture.Surface.SurfaceToken));
        Assert.IsFalse(fixture.Tasks.IsInteractionPending(new(fixture.LastInteraction!.RunId,
            fixture.LastInteraction.PendingConfirmation!.InteractionId, "approved")));
    }

    [TestMethod]
    public async Task DeleteReceiptMustMatchBothOperationAndPrimaryKey()
    {
        const string delete = """{"contract":"vibetable.surface-mutation-plan.v1","collection":"orders","schemaRevision":"schema_1","operations":[{"kind":"delete","primaryKey":"r1","expectedDigest":"sha256:existing"}]}""";
        foreach (var (kind, id) in new[] { ("update", "r1"), ("delete", "other") })
        {
            using var fixture = new Fixture(risk: "write");
            fixture.Http.CommitResultBody = JsonSerializer.Serialize(new
            { contractVersion = "2.0", status = "applied", affectedRows = new[] { new { recordId = id, operation = kind } } });
            string plan = (await fixture.Call("mutation.preview", delete)).GetProperty("result").GetProperty("planId").GetString()!;
            Assert.AreEqual("plugin_commit_unknown", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
            Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        }
    }

    [TestMethod]
    public async Task AuthorityConflictIsDefiniteAndIncompleteSuccessReceiptRemainsUnknown()
    {
        foreach (bool conflict in new[] { true, false })
        {
            using var fixture = new Fixture { Decision = "approved" };
            fixture.Http.Conflict = conflict;
            fixture.Http.Incomplete = !conflict;
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            var result = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
            Assert.AreEqual(conflict ? "mutation.digest_conflict" : "plugin_commit_unknown", Code(result));
            Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        }
    }

    [TestMethod]
    public async Task AuditStartFailurePreventsConfirmationAndMutation()
    {
        using var fixture = new Fixture();
        fixture.Http.FailAuditStart = true;
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        Assert.AreEqual("plugin_audit_failed", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        Assert.AreEqual(0, fixture.Confirmations);
        Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "mutation.commit"));
    }

    [TestMethod]
    public async Task CommitHttpFailureWithoutRollbackProofIsUnknownAndNeverReplayed()
    {
        foreach (string body in new[]
        {
            """{"code":"plugin_request_failed","message":"plugin request could not be completed"}""",
            """{"code":"plugin_commit_unknown","commitOutcome":"unknown","message":"plugin request could not be completed"}""",
            """{"code":"mutation.digest_conflict","commitOutcome":"unknown","message":"plugin request could not be completed"}""",
            """{"code":"mutation.digest_conflict","message":"plugin request could not be completed"}""",
            """{"code":"plugin_request_failed","commitOutcome":"not-committed","message":"plugin request could not be completed"}""",
            """{"code":"mutation.future_conflict","commitOutcome":"not-committed","message":"plugin request could not be completed"}""",
            """{"code":"transaction.same_target_violation","commitOutcome":"unknown"}""",
            """{"code":"transaction.plan_stale","commitOutcome":"not-committed"}""",
        })
        {
            using var fixture = new Fixture();
            fixture.Http.CommitErrorBody = body;
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            Assert.AreEqual("plugin_commit_unknown", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
            var start = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.start");
            string taskId = start.GetProperty("request").GetProperty("taskId").GetString()!;
            Assert.IsTrue(fixture.Tasks.TryGetTask(taskId, out var task));
            Assert.AreEqual("aborted", task.State);
            Assert.AreEqual("plugin_commit_unknown", task.Error!.Code);
            var finish = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.finish");
            Assert.AreEqual("unknown", finish.GetProperty("request").GetProperty("outcome").GetString());
            Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
            Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        }
    }

    [TestMethod]
    public async Task ExplicitAuthorityProofPreservesPermissionRejectionAndCommitCancellationIsUnknown()
    {
        foreach (string code in new[] { "plugin_write_denied", "plugin_cursor_stale", "mutation.relation.restricted", "transaction.same_target_violation", "transaction.balance_violation", "transaction.decimal_sign_violation", "transaction.at_most_one_violation" })
        {
            using var fixture = new Fixture();
            fixture.Http.CommitErrorBody = JsonSerializer.Serialize(new { code, commitOutcome = "not-committed" });
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            Assert.AreEqual(code, Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
            var start = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.start");
            Assert.IsTrue(fixture.Tasks.TryGetTask(start.GetProperty("request").GetProperty("taskId").GetString()!, out var task));
            Assert.AreEqual("failed", task.State);
            Assert.AreEqual(code, task.Error!.Code);
            var audit = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.finish").GetProperty("request");
            Assert.AreEqual("failed", audit.GetProperty("outcome").GetString());
            Assert.AreEqual(code, audit.GetProperty("errorCode").GetString());
            Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        }
        using var cancelled = new Fixture();
        cancelled.Http.OnCommit = () => cancelled.Tasks.RequestCancel(cancelled.TaskId, out _, out _);
        string pending = (await cancelled.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        var cancelledSubmit = await cancelled.Call("mutation.submit", $"{{\"planId\":\"{pending}\"}}");
        Assert.IsTrue(cancelled.Http.CommitObservedCancellation,
            "cancel-in-flight seam never observed the requested cancellation within its watchdog");
        Assert.AreEqual("plugin_commit_unknown", Code(cancelledSubmit));
        Assert.IsTrue(cancelled.Tasks.TryGetTask(cancelled.TaskId, out var unknown));
        Assert.AreEqual("aborted", unknown.State);
        Assert.AreEqual("plugin_commit_unknown", unknown.Error!.Code);
        Assert.AreEqual(1, cancelled.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
    }

    [TestMethod]
    public async Task RetirementAuditsOriginalRunWithoutCurrentSurfaceOrGeneration()
    {
        using var fixture = new Fixture { Decision = "retire" };
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        var response = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
        Assert.IsTrue(response.TryGetProperty("error", out _));
        var start = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.start");
        var finish = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.finish");
        Assert.AreEqual(start.GetProperty("requestId").GetString(), finish.GetProperty("requestId").GetString());
        Assert.AreEqual(fixture.Context.ProjectKey, finish.GetProperty("projectKey").GetString());
        Assert.AreEqual(7, finish.GetProperty("sessionEpoch").GetInt32());
        Assert.AreEqual("cancelled", finish.GetProperty("request").GetProperty("outcome").GetString());
        Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "mutation.commit"));
        await fixture.Api.DrainAsync();
    }

    [TestMethod]
    public async Task ReceivedCommitReceiptSurvivesLateRetirementWithoutNewWorkspaceNotification()
    {
        foreach (string status in new[] { "applied", "replayed", "invalid" })
        {
            bool invalid = status == "invalid";
            using var fixture = new Fixture();
            fixture.Http.ReplayCommit = status == "replayed";
            fixture.Http.Incomplete = invalid;
            fixture.Http.AfterCommitRead = fixture.RetireAfterReceipt;
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            var response = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
            if (invalid) Assert.AreEqual("plugin_commit_unknown", Code(response));
            else Assert.AreEqual("succeeded", response.GetProperty("result").GetProperty("status").GetString());
            Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
            Assert.AreEqual(0, fixture.LateNotifications);
            fixture.Tasks.SetWorkspaceContext(fixture.Context);
            Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
            Assert.AreEqual(invalid ? "aborted" : "succeeded", task.State);
            if (!invalid) Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "audit.finish"));
        }
    }

    [TestMethod]
    public async Task ActualBoundActionAndRiskReachTaskConfirmationAndAuthority()
    {
        foreach (string risk in new[] { "write", "destructive" })
        {
            using var fixture = new Fixture(actionId: "actual-" + risk, risk: risk);
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            var result = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
            Assert.AreEqual("succeeded", result.GetProperty("result").GetProperty("status").GetString());
            Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
            Assert.AreEqual("actual-" + risk, task.ActionId);
            Assert.AreEqual(risk, task.Risk);
            Assert.AreEqual(task.ActionId, fixture.LastInteraction!.ActionId);
            Assert.AreEqual(risk, fixture.LastInteraction.PendingConfirmation!.Risk);
            foreach (var call in fixture.Http.Calls)
            {
                Assert.AreEqual(task.ActionId, call.GetProperty("actionId").GetString());
                Assert.AreEqual("ui/index.html", call.GetProperty("entry").GetString());
                Assert.IsFalse(call.TryGetProperty("risk", out _));
            }
        }
        using var read = new Fixture(actionId: "read-page", risk: "read");
        Assert.AreEqual("plugin_write_denied", Code(await read.Call("mutation.preview", Plan)));
        Assert.AreEqual(0, read.Http.Calls.Count);
    }

    [TestMethod]
    public async Task ReadResponseStillRejectsLateRetiredSurface()
    {
        using var fixture = new Fixture();
        fixture.Http.AfterReadMethod = "data.query";
        fixture.Http.AfterCommitRead = fixture.RetireAfterReceipt;
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call("data.query", "{\"collection\":\"orders\"}")));
        Assert.AreEqual(0, fixture.LateNotifications);
    }

    [TestMethod]
    public async Task PrintOnlyOpensCurrentApplicationAndNeverAcceptsCallerContent()
    {
        using var app = new Fixture(presentation: "application");
        var result = await app.Call("output.print", "{}");
        Assert.IsTrue(result.GetProperty("result").GetProperty("opened").GetBoolean());
        Assert.AreEqual(1, app.Prints);
        Assert.AreEqual("context.read", app.Http.Calls.Single().GetProperty("method").GetString());
        foreach (string parameters in new[] { "{\"html\":\"private\"}", "{\"url\":\"file:///x\"}", "{\"surfaceToken\":\"other\"}" })
            Assert.AreEqual("plugin_capability_invalid", Code(await app.Call("output.print", parameters)));
        Assert.AreEqual(1, app.Prints);
        Assert.AreEqual("plugin_source_denied", Code(await app.Call("output.print", "{}", source: "https://evil.test")));
        foreach (string presentation in new[] { "surface", "custom", "headless" })
        {
            using var hidden = new Fixture(presentation: presentation);
            Assert.AreEqual("plugin_print_denied", Code(await hidden.Call("output.print", "{}")));
            Assert.AreEqual(0, hidden.Prints);
            Assert.AreEqual(0, hidden.Http.Calls.Count);
        }
    }

    [TestMethod]
    public async Task PrintPreparationCannotReviveRetiredReboundOrBackgroundSurface()
    {
        foreach (string transition in new[] { "retire", "revision", "background", "generation", "cancel" })
        {
            using var app = new Fixture(presentation: "application");
            var prepared = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
            app.PreparePrint = (_, _) => prepared.Task;
            string request = app.Wire("output.print", "{}");
            var pending = app.Api.HandleAsync(app.Surface.SurfaceToken, app.Source, request);
            if (transition == "retire") app.Surfaces.Close(app.Surface.SurfaceToken);
            if (transition == "revision") app.Surfaces.BindIdentity(app.Surface.SurfaceToken, app.Context, 2, "3.x", presentation: "application", installationId: "replacement");
            if (transition == "generation") app.RetireAfterReceipt();
            if (transition == "cancel")
            {
                string id = JsonDocument.Parse(request).RootElement.GetProperty("requestId").GetString()!;
                await app.Call("request.cancel", JsonSerializer.Serialize(new { requestId = id }));
            }
            prepared.SetResult(transition != "background");
            var result = JsonDocument.Parse(await pending).RootElement;
            Assert.IsTrue(result.TryGetProperty("error", out _), transition);
            Assert.AreEqual(0, app.Prints, transition);
        }
    }

    [TestMethod]
    public async Task PrintRechecksGoAuthorityAfterDeferredPreparationBeforeNativeOutput()
    {
        // These are Go's actual disable/package and reinstall/revision rejections.
        foreach (string code in new[] { "plugin_read_denied", "plugin_cursor_stale" })
        {
            using var app = new Fixture(presentation: "application");
            var prepared = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
            app.PreparePrint = (_, _) => prepared.Task;
            var pending = app.Call("output.print", "{}");
            app.Http.ContextError = code; // Go changed; Host catalog/surface cache is deliberately still current.
            prepared.SetResult(true);
            var result = await pending;
            Assert.IsTrue(result.TryGetProperty("error", out _), "authority revoked during preparation must prevent native output");
            Assert.AreEqual(code, Code(result));
            Assert.AreEqual(0, app.Prints);
            Assert.AreEqual(1, app.PrintReleases);
        }
    }

    [TestMethod]
    public void PrintLayoutIsBoundedAndBelongsToTheExactCurrentDocument()
    {
        var source = new Uri("https://synthetic.plugins.vibetable.local/ui/index.html?entry=1");
        Assert.AreEqual(3822, ProductWebViewBridge.ReadPluginPrintHeight(source,
            JsonSerializer.Serialize(new { source = source.AbsoluteUri + "#detail", height = 3822 })));
        foreach (int height in new[] { 0, -1, 100001 })
            Assert.AreEqual("plugin_print_limit", Assert.ThrowsExactly<PluginSurfacePolicyException>(() =>
                ProductWebViewBridge.ReadPluginPrintHeight(source, JsonSerializer.Serialize(new { source = source.AbsoluteUri, height }))).Code);
        foreach (string changed in new[] { "https://other.test/ui/index.html?entry=1", source.AbsoluteUri + "&other=1", "file:///x" })
            Assert.AreEqual("plugin_print_denied", Assert.ThrowsExactly<PluginSurfacePolicyException>(() =>
                ProductWebViewBridge.ReadPluginPrintHeight(source, JsonSerializer.Serialize(new { source = changed, height = 3822 }))).Code);
    }

    [TestMethod]
    public async Task NativePrintFailureKeepsIsolatedUnknownDialogAndOpenedReceiptSurvivesLateRetirement()
    {
        using var failed = new Fixture(presentation: "application");
        failed.OnPrint = () => throw new InvalidOperationException("synthetic native failure");
        Assert.AreEqual("plugin_print_failed", Code(await failed.Call("output.print", "{}")));
        Assert.AreEqual(0, failed.PrintReleases);
        using var denied = new Fixture(presentation: "application");
        denied.PreparePrint = (_, _) => Task.FromResult(false);
        Assert.AreEqual("plugin_print_denied", Code(await denied.Call("output.print", "{}")));
        Assert.AreEqual(1, denied.PrintReleases);
        using var opened = new Fixture(presentation: "application");
        opened.OnPrint = opened.RetireAfterReceipt;
        Assert.IsTrue((await opened.Call("output.print", "{}")).GetProperty("result").GetProperty("opened").GetBoolean());
        Assert.AreEqual(0, opened.PrintReleases);
    }

    private const string Plan = """{"contract":"vibetable.surface-mutation-plan.v1","collection":"orders","schemaRevision":"schema_1","operations":[{"kind":"update","primaryKey":"r1","expectedDigest":"sha256:existing","values":{"name":"new"}}]}""";
    private static string? Code(JsonElement result) => result.GetProperty("error").GetProperty("code").GetString();
    private static string? Code(string result) => Code(JsonDocument.Parse(result).RootElement);

    [TestMethod]
    [DataRow(false, false)]
    [DataRow(true, false)]
    [DataRow(false, true)]
    [DataRow(true, true)]
    public async Task CompletedModelRefreshesCatalogWithoutDowngradingItsReceipt(bool refreshFails, bool retire)
    {
        using var fixture = new Fixture();
        int refreshes = 0;
        fixture.RefreshModelCatalog = (generation, _) =>
        {
            Assert.AreEqual(fixture.Context.SessionGeneration, generation.Identity.SessionEpoch);
            refreshes++;
            if (retire) fixture.RetireAfterReceipt();
            if (refreshFails) throw new IOException("synthetic catalog refresh failure");
            return Task.CompletedTask;
        };
        JsonElement response = await fixture.Call("model.configure", "{\"mode\":\"create\"}");
        Assert.AreEqual("completed", response.GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual(1, refreshes);
        Assert.AreEqual(0, fixture.LateNotifications);
        Assert.AreEqual(0, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "model.commit"));
    }

    [TestMethod]
    [DataRow("completed")]
    [DataRow("partial")]
    [DataRow("wrong-request")]
    [DataRow("wrong-installation")]
    [DataRow("incomplete")]
    public async Task ModelCommitReceiptSettlesCapturedTaskAfterRetirement(string receipt)
    {
        using var fixture = new Fixture();
        fixture.Http.ModelPreviewBody = """{"contract":"vibetable.plugin-model.v1","requestId":"model-request","installationId":"record-surface","installationRevision":1,"model":{"modelId":"items","version":1},"status":"pending","total":2}""";
        fixture.Http.ModelCommitBody = JsonSerializer.Serialize(new
        {
            contract = "vibetable.plugin-model.v1",
            requestId = receipt == "wrong-request" ? "other" : "model-request",
            installationId = receipt == "wrong-installation" ? "other" : "record-surface",
            installationRevision = 1,
            model = new { modelId = "items", version = 1 },
            status = receipt == "partial" ? "partial" : "completed",
            completed = receipt == "incomplete" ? 1 : 2, total = 2,
        });
        fixture.Http.AfterReadMethod = "model.commit";
        fixture.Http.AfterCommitRead = fixture.RetireAfterReceipt;
        var response = await fixture.Call("model.configure", "{\"mode\":\"create\"}");
        if (receipt == "completed") Assert.AreEqual("completed", response.GetProperty("result").GetProperty("status").GetString());
        else Assert.IsTrue(response.TryGetProperty("error", out _));
        Assert.AreEqual(0, fixture.LateNotifications);
        fixture.Tasks.SetWorkspaceContext(fixture.Context);
        Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
        Assert.AreEqual(receipt == "completed" ? "succeeded" : "failed", task.State);
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "model.commit"));
    }

    [TestMethod]
    [DataRow("model.read")]
    [DataRow("model.status")]
    public async Task ModelReadsStillRejectLateRetirement(string method)
    {
        using var fixture = new Fixture();
        fixture.Http.AfterReadMethod = method;
        fixture.Http.AfterCommitRead = fixture.RetireAfterReceipt;
        Assert.AreEqual("plugin_source_denied", Code(await fixture.Call(method, "{}")));
    }

    [STATestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public void NativeRepairShowsOldFieldAndExplainsMissingSelectionBeforePreview(bool hasReplacement)
    {
        var catalog = JsonSerializer.SerializeToElement(new
        {
            model = new { entities = new[] { new { key = "items", label = "项目", fields = new[] { new { key = "name", label = "名称", type = "text" } } } } },
            tables = new[] { new { tableId = "old-table", displayName = "项目", schemaRevision = "rev1" } },
            bindings = new { items = new { tableId = "old-table", fields = new { name = "old-field" } } },
            diagnostics = new[] { new { code = "plugin_model_field_access_denied", path = "items.name" } }
        });
        var fields = new List<object> { new { fieldId = "wrong-type", displayName = "数量", logicalType = "number" } };
        if (hasReplacement) fields.Add(new { fieldId = "new-field", displayName = "替代名称", logicalType = "text" });
        var schema = JsonSerializer.SerializeToElement(new { tableId = "old-table", fields });
        using var lifetime = new CancellationTokenSource();
        var window = NativePluginModelWindow.CreateMappingWindow(catalog, (_, _, _) => Task.FromResult(schema), lifetime);
        window.ShowActivated = false;
        window.ShowInTaskbar = false;
        window.Opacity = 0;
        Exception? failure = null;
        window.ContentRendered += (_, _) =>
        {
            try
            {
                var layout = (DockPanel)window.Content;
                var actions = ((StackPanel)layout.Children[0]).Children;
                var body = ((StackPanel)((ScrollViewer)layout.Children[1]).Content).Children;
                void Click(string id) => actions.OfType<Button>().Single(b => AutomationProperties.GetAutomationId(b) == id)
                    .RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
                Assert.IsTrue(body.OfType<TextBlock>().Any(t => t.Text.Contains("items.name → old-table/old-field — 字段不可访问")));
                var field = body.OfType<ComboBox>().Single(c => AutomationProperties.GetAutomationId(c) == "PluginModelField_items_name");
                Assert.AreEqual(hasReplacement ? 1 : 0, field.Items.Count);
                Assert.IsNull(field.SelectedItem);
                Click("plugin-model-preview");
                Assert.IsTrue(window.IsVisible);
                Assert.IsNull(window.Mappings);
                string feedback = actions.OfType<TextBlock>().Single().Text;
                StringAssert.Contains(feedback, "items.name");
                StringAssert.Contains(feedback, hasReplacement ? "选择字段" : "没有兼容字段");
                if (hasReplacement) field.SelectedIndex = 0;
                Click(hasReplacement ? "plugin-model-preview" : "plugin-model-cancel");
            }
            catch (Exception error) { failure = error; }
            finally { if (window.IsVisible) window.Close(); }
        };
        bool? accepted = window.ShowDialog();
        if (failure is not null) System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(failure).Throw();
        Assert.AreEqual(hasReplacement, accepted);
        if (hasReplacement)
            Assert.AreEqual("new-field", window.Mappings!.Value.GetProperty("items").GetProperty("fields").GetProperty("name").GetString());
        else
            Assert.IsNull(window.Mappings);
    }

    [TestMethod]
    public void NativeModelSuggestionsPreferKnownIdentitiesAndLeaveAmbiguousNamesForReview()
    {
        var logical = JsonSerializer.SerializeToElement(new { key = "title", label = "名称" });
        JsonElement Choice(string id, string label) => JsonSerializer.SerializeToElement(new { fieldId = id, displayName = label });
        JsonElement[] choices = [Choice("a", "其它"), Choice("b", "名称")];
        Assert.AreEqual("a", NativePluginModelWindow.SuggestedId(logical, choices, "fieldId", "a"));
        Assert.AreEqual("b", NativePluginModelWindow.SuggestedId(logical, choices, "fieldId", null));
        Assert.IsNull(NativePluginModelWindow.SuggestedId(logical, choices, "fieldId", "missing"));
        Assert.IsNull(NativePluginModelWindow.SuggestedId(logical, [.. choices, Choice("c", "title")], "fieldId", null));
        Assert.IsNull(NativePluginModelWindow.SuggestedId(logical, [choices[0]], "fieldId", null));
        Assert.AreEqual("a", NativePluginModelWindow.SuggestedId(logical, [choices[0]], "fieldId", null, true));
    }

    [TestMethod]
    public void NativeRelationSuggestionsRequireOneCompatiblePhysicalPair()
    {
        JsonElement Logical(string key, string cardinality) => JsonSerializer.SerializeToElement(new { key, label = key, relation = new { cardinality } });
        object Field(string id, string label, string target, string reciprocal, string cardinality) => new
            { fieldId = id, displayName = label, logicalType = "relation", targetTableId = target, reciprocalFieldId = reciprocal, cardinality };
        JsonElement Schema(string id, params object[] fields) => JsonSerializer.SerializeToElement(new { tableId = id, fields });
        var left = Logical("links", "many");
        var right = Logical("owner", "one");
        var a = Schema("a", Field("x1", "links", "b", "y1", "many"), Field("x2", "second", "b", "y2", "many"));
        var b = Schema("b", Field("y1", "first", "a", "x1", "one"), Field("y2", "owner", "a", "x2", "one"));
        Assert.IsNull(NativePluginModelWindow.SuggestRelationPair(left, a, null, null, right, b, null, null));
        Assert.AreEqual(("x2", "y2"), NativePluginModelWindow.SuggestRelationPair(left, a, null, null, right, b, null, "y2"));
        Assert.IsNull(NativePluginModelWindow.SuggestRelationPair(left, a, null, "x1", right, b, null, "y2"));
        Assert.IsNull(NativePluginModelWindow.SuggestRelationPair(left, a, null, "x1", right,
            Schema("b", Field("y1", "owner", "a", "x1", "many")), null, null));
    }

    [TestMethod]
    public async Task NativeModelCatalogLoadsEverySummaryThenOnlySelectedFieldsWithRevision()
    {
        var calls = new List<JsonElement>();
        Task<JsonElement> Read(JsonElement request, CancellationToken token)
        {
            token.ThrowIfCancellationRequested();
            calls.Add(request.Clone());
            string response = request.TryGetProperty("tableId", out var selected)
                ? request.TryGetProperty("offset", out _) ? """{"tableId":"selected","schemaRevision":"schema_0007","fields":[{"fieldId":"last"}],"nextOffset":null}"""
                    : """{"tableId":"selected","schemaRevision":"schema_0007","fields":[{"fieldId":"first"}],"nextOffset":32}"""
                : request.TryGetProperty("afterTableId", out _) ? """{"tables":[{"tableId":"selected","schemaRevision":"schema_0007"}],"nextAfterTableId":null}"""
                    : """{"model":{},"tables":[{"tableId":"other","schemaRevision":"schema_0001"}],"nextAfterTableId":"other"}""";
            if (request.TryGetProperty("tableId", out selected))
            {
                Assert.AreEqual("selected", selected.GetString());
                Assert.AreEqual("schema_0007", request.GetProperty("schemaRevision").GetString());
            }
            return Task.FromResult(JsonDocument.Parse(response).RootElement.Clone());
        }
        var catalog = await PluginSurfaceApi.ReadModelCatalogAsync(Read, CancellationToken.None);
        Assert.AreEqual(2, catalog.GetProperty("tables").GetArrayLength());
        Assert.IsTrue(calls.All(call => !call.TryGetProperty("tableId", out _)));
        var fields = await PluginSurfaceApi.ReadModelCatalogAsync(Read, CancellationToken.None, "selected", "schema_0007");
        Assert.AreEqual(2, fields.GetProperty("fields").GetArrayLength());
        Assert.AreEqual(4, calls.Count);
        Assert.AreEqual(32, calls[3].GetProperty("offset").GetInt32());
        using var cancelled = new CancellationTokenSource(); cancelled.Cancel();
        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() => PluginSurfaceApi.ReadModelCatalogAsync(Read, cancelled.Token));
        Assert.AreEqual(4, calls.Count);
    }

    [TestMethod]
    public async Task MissingPriorCollectionShowsNativeBlockedPlanWithoutAdmission()
    {
        using var fixture = new Fixture();
        fixture.Http.ModelCatalogBody = """{"tables":[],"nextAfterTableId":null,"status":"blocked","model":{"entities":[]},"bindings":{"assets":{"tableId":"original","fields":{}}},"diagnostics":[{"code":"plugin_model_history_unknown","path":"assets"}]}""";
        JsonElement? shown = null;
        fixture.ObserveModel = prompt => shown = prompt.Clone();
        var reply = await fixture.Call("model.configure", """{"mode":"repair"}""");
        Assert.AreEqual(JsonValueKind.Null, reply.GetProperty("result").ValueKind);
        Assert.AreEqual("original", shown!.Value.GetProperty("bindings").GetProperty("assets").GetProperty("tableId").GetString());
        Assert.AreEqual("assets", shown.Value.GetProperty("diagnostics")[0].GetProperty("path").GetString());
        CollectionAssert.AreEqual(new[] { "model.catalog" }, fixture.Http.Calls.Select(r => r.GetProperty("method").GetString()).ToArray());
    }

    [TestMethod]
    [DataRow("model.catalog")]
    [DataRow("model.preview")]
    [DataRow("model.commit")]
    public async Task FrameCannotCallNativeModelProtocol(string method)
    {
        using var fixture = new Fixture();
        Assert.AreEqual("plugin_api_unsupported", Code(await fixture.Call(method, "{}")));
        Assert.AreEqual(0, fixture.Http.Calls.Count);
    }

    private const string TransactionPreviewBody = """
        {"contract":"vibetable.plugin-transactions.v1","workspaceId":"11111111-1111-4111-8111-111111111111","installationId":"record-surface","installationRevision":1,"requestId":"business-id","epoch":1,"status":"prepared",
        "plan":{"contract":"vibetable.transaction-plan.v1","modelId":"orders","operations":[{"entity":"headers","kind":"create","tempId":"head","values":{"name":"new"}},{"entity":"lines","kind":"create","tempId":"line","values":{"header":{"entity":"headers","tempId":"head"}}}]},
        "model":{"bindings":{"headers":{"tableId":"table-head","fields":{"name":"field-name"}},"lines":{"tableId":"table-line","fields":{"header":"field-header"}}}},
        "declaration":{"contract":"vibetable.plugin-transactions.v1","modelId":"orders","version":1,"decimals":[],"rules":[{"id":"unique","kind":"unique","entity":"headers","fields":["name"]}]},"newIds":{"headers":{"head":"actual-head"},"lines":{"line":"actual-line"}},
        "previewRevision":1,"dataRevisions":{"table-head":"data_0","table-line":"data_0"},"effects":[{"tableId":"table-head","recordId":"actual-head","operation":"insert","schemaRevision":"schema_1"},{"tableId":"table-line","recordId":"actual-line","operation":"insert","schemaRevision":"schema_1"}]}
        """;
    private const string TransactionAppliedBody = """
        {"contract":"vibetable.plugin-transactions.v1","workspaceId":"11111111-1111-4111-8111-111111111111","installationId":"record-surface","requestId":"business-id","status":"applied","newIds":{"headers":{"head":"actual-head"},"lines":{"line":"actual-line"}},
        "receipt":{"status":"applied","changeSetId":"change-1","rows":[{"tableId":"table-head","recordId":"actual-head","operation":"insert","revision":"row_1","digest":"guard-head"},{"tableId":"table-line","recordId":"actual-line","operation":"insert","revision":"row_1","digest":"guard-line"}],"revisions":{"table-head":"data_1","table-line":"data_1"},"emittedEvents":[],"warnings":[]}}
        """;

    [TestMethod]
    public async Task TransactionSubmissionUsesNativeOnlyFullCrossTableConfirmationAndIndependentOperations()
    {
        using var fixture = new Fixture();
        fixture.Http.TransactionStatusBody = TransactionPreviewBody;
        fixture.OnConfirmation = () =>
        {
            var interaction = fixture.LastInteraction!;
            var decision = new PluginResolveInteractionParams(interaction.RunId, interaction.PendingConfirmation!.InteractionId, "approved");
            Assert.AreEqual("native-required", fixture.Tasks.BeginInteractionResolve(decision).Status);
            Assert.AreEqual(2, interaction.PendingConfirmation.Preview.SampleRows.Count);
            Assert.AreEqual("table-head", interaction.PendingConfirmation.Preview.SampleRows[0]["tableId"].GetString());
            Assert.AreEqual("actual-line", interaction.PendingConfirmation.Preview.SampleRows[1]["recordId"].GetString());
            Assert.AreEqual("resolved", fixture.Tasks.BeginInteractionResolve(decision, fromNativeHost: true).Status);
        };
        for (int i = 0; i < 2; i++)
            Assert.AreEqual("applied", (await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}")).GetProperty("result").GetProperty("status").GetString());
        var commits = fixture.Http.Calls.Where(call => call.GetProperty("method").GetString() == "transaction.commit").ToArray();
        Assert.AreEqual(2, commits.Length);
        Assert.AreNotEqual(commits[0].GetProperty("requestId").GetString(), commits[1].GetProperty("requestId").GetString());
        Assert.IsTrue(commits.All(call => call.GetProperty("request").GetProperty("requestId").GetString() == "business-id"));
        Assert.AreEqual(2, fixture.Tasks.ProjectHistory(fixture.Context).Count);
        Assert.IsFalse(fixture.Api.HasWrites);
    }

    [TestMethod]
    [DataRow("workspaceId")]
    [DataRow("installationId")]
    [DataRow("requestId")]
    [DataRow("mapping")]
    [DataRow("table")]
    [DataRow("operation")]
    [DataRow("missing-row")]
    public async Task TransactionRejectsMismatchedCommitReceiptsAsUnknown(string mismatch)
    {
        using var fixture = new Fixture();
        var receipt = System.Text.Json.Nodes.JsonNode.Parse(TransactionAppliedBody)!;
        switch (mismatch)
        {
            case "mapping": receipt["newIds"]!["headers"]!["head"] = "wrong"; break;
            case "table": receipt["receipt"]!["rows"]![0]!["tableId"] = "table-line"; break;
            case "operation": receipt["receipt"]!["rows"]![0]!["operation"] = "update"; break;
            case "missing-row": receipt["receipt"]!["rows"]!.AsArray().RemoveAt(1); break;
            default: receipt[mismatch] = "wrong"; break;
        }
        fixture.Http.TransactionCommitBody = receipt.ToJsonString();
        Assert.AreEqual("plugin_commit_unknown", Code(await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}")));
        Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
        Assert.AreEqual("aborted", task.State);
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "transaction.commit"));
    }

    [TestMethod]
    public async Task TransactionSuccessSurvivesRetirementAndLateCancellationAndStatusNeverSubmits()
    {
        using var fixture = new Fixture();
        fixture.Http.AfterReadMethod = "transaction.commit";
        fixture.Http.AfterCommitRead = fixture.RetireAfterReceipt;
        var response = await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}");
        Assert.AreEqual("applied", response.GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual(0, fixture.LateNotifications);
        fixture.Tasks.SetWorkspaceContext(fixture.Context);
        Assert.AreEqual(HostPluginTaskCancelOutcome.Terminal, fixture.Tasks.RequestCancel(fixture.TaskId, out var task, out _));
        Assert.AreEqual("succeeded", task.State);
        using var reopened = new Fixture();
        reopened.Http.TransactionStatusBody = TransactionAppliedBody;
        Assert.AreEqual("applied", (await reopened.Call("transaction.status", "{\"requestId\":\"business-id\"}")).GetProperty("result").GetProperty("status").GetString());
        reopened.Http.TransactionStatusBody = """{"contract":"vibetable.plugin-transactions.v1","requestId":"business-id","status":"unknown"}""";
        Assert.AreEqual("unknown", (await reopened.Call("transaction.submit", "{\"requestId\":\"business-id\"}")).GetProperty("result").GetProperty("status").GetString());
        Assert.IsFalse(reopened.Http.Calls.Any(call => call.GetProperty("method").GetString() == "transaction.commit"));
    }

    [TestMethod]
    public async Task TransactionRejectionCancelsBeforeCommitAndFrameCannotCallCommit()
    {
        using var fixture = new Fixture { Decision = "rejected" };
        Assert.AreEqual(JsonValueKind.Null, (await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}")).GetProperty("result").ValueKind);
        Assert.AreEqual("plugin_api_unsupported", Code(await fixture.Call("transaction.commit", "{\"requestId\":\"business-id\"}")));
        Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "transaction.commit"));
        using var cancelled = new Fixture { Decision = "cancel" };
        Assert.AreEqual("plugin_cancel_requested", Code(await cancelled.Call("transaction.submit", "{\"requestId\":\"business-id\"}")));
        Assert.IsFalse(cancelled.Http.Calls.Any(call => call.GetProperty("method").GetString() == "transaction.commit"));
    }

    [TestMethod]
    public async Task TransactionStatusRejectsNewIdMappingWithoutItsInsertReceipt()
    {
        using var fixture = new Fixture();
        fixture.Http.TransactionStatusBody = TransactionAppliedBody.Replace("\"head\":\"actual-head\"", "\"head\":\"unproven-id\"", StringComparison.Ordinal);
        Assert.AreEqual("plugin_commit_unknown", Code(await fixture.Call("transaction.status", "{\"requestId\":\"business-id\"}")));
        Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "transaction.commit"));
    }

    [TestMethod]
    public async Task TransactionConfigurationShowsNullableSourceFieldsAndQuantitySign()
    {
        using var fixture = new Fixture { Decision = "rejected" };
        var declaration = """
            {"contract":"vibetable.plugin-transactions.v1","modelId":"orders","version":2,
             "rules":[{"id":"sources","kind":"atMostOne","entity":"lines","fields":["header","other"]},
                      {"id":"owner","kind":"sameTarget","entity":"lines","left":[{"field":"header","target":"headers"}],"right":[{"field":"other","target":"headers"}],"skipEmptyLeftEnd":true}],
             "decimals":[{"entity":"headers","field":"name","storage":"decimalText","scale":3,"digits":12,"sign":"positive"}]}
            """;
        await fixture.Call("transaction.configure", "{\"declaration\":" + declaration + "}");
        var rows = fixture.LastInteraction!.PendingConfirmation!.Preview.SampleRows;
        Assert.AreEqual("field-header", rows[0]["fieldIds"].GetProperty("header").GetString());
        Assert.AreEqual("field-other", rows[0]["fieldIds"].GetProperty("other").GetString());
        Assert.IsTrue(rows[1]["skipEmptyLeftEnd"].GetBoolean());
        Assert.AreEqual("positive", rows[2]["sign"].GetString());
    }

    [TestMethod]
    [DataRow(1, 4096, "image/png", true, true)]
    [DataRow(2, 4096, "image/png", true, false)]
    [DataRow(1, 4097, "image/png", true, false)]
    [DataRow(1, 4096, "image/jpeg", true, false)]
    [DataRow(1, 4096, "image/png", false, false)]
    public void ModelFilePickerMatchesTheProtectedArchivePolicy(int maxFiles, int maxBytesPerFile,
        string mimeType, bool protectedFile, bool expected)
    {
        var declaration = JsonSerializer.SerializeToElement(new {
            type = "file", file = new { maxFiles = 1, maxBytesPerFile = 4096, allowedMimeTypes = new[] { "image/png" } }
        });
        var candidate = JsonSerializer.SerializeToElement(new {
            logicalType = "file", file = new { maxFiles, maxBytesPerFile, allowedMimeTypes = new[] { mimeType },
                @protected = protectedFile, thumbs = Array.Empty<string>() }
        });
        Assert.AreEqual(expected, NativePluginModelWindow.Compatible(declaration, candidate));
    }

    [TestMethod]
    [DataRow(202, 1, true)]
    [DataRow(205, 1, true)]
    [DataRow(206, 0, false)]
    [DataRow(1, 67, true)]
    [DataRow(1, 68, false)]
    public async Task TransactionConfigurationBoundsRulesAndDecimalsIndependently(int rules, int decimals, bool permitted)
    {
        using var fixture = new Fixture { Decision = "rejected" };
        var path = new[] { new { field = "header", target = "headers" } };
        var declaration = new {
            contract = "vibetable.plugin-transactions.v1", modelId = "orders", version = 1,
            rules = Enumerable.Range(0, rules).Select(i => new { id = $"rule{i}", kind = "sameTarget", entity = "lines", left = path, right = path }),
            decimals = Enumerable.Range(0, decimals).Select(_ => new { entity = "headers", field = "name", storage = "decimalText", scale = 2, digits = 8 }),
        };
        var response = await fixture.Call("transaction.configure", JsonSerializer.Serialize(new { declaration }));
        if (!permitted)
        {
            Assert.AreEqual("transaction.rules_invalid", Code(response));
            Assert.AreEqual(0, fixture.Confirmations);
            return;
        }
        var preview = fixture.LastInteraction!.PendingConfirmation!.Preview;
        Assert.AreEqual(rules + decimals, preview.SampleRows.Count);
        var hop = preview.SampleRows[0]["left"][0];
        Assert.AreEqual("field-header", hop.GetProperty("fieldId").GetString());
        Assert.AreEqual("table-head", hop.GetProperty("targetTableId").GetString());
        Assert.AreEqual(JsonValueKind.Null, response.GetProperty("result").ValueKind);
    }

    [TestMethod]
    [DataRow(false, 1L)]
    [DataRow(true, 1L)]
    [DataRow(false, 2147483648L)]
    public async Task TransactionConfigurationShowsExactRulesAndRequiresNativeApproval(bool omitDecimals, long version)
    {
        using var fixture = new Fixture();
        fixture.OnConfirmation = () =>
        {
            var interaction = fixture.LastInteraction!;
            var confirmation = interaction.PendingConfirmation!;
            StringAssert.Contains(confirmation.Title, "停用或卸载");
            Assert.AreEqual("unique", confirmation.Preview.SampleRows[0]["kind"].GetString());
            Assert.AreEqual("table-head", confirmation.Preview.SampleRows[0]["binding"].GetProperty("tableId").GetString());
            var request = new PluginResolveInteractionParams(interaction.RunId, confirmation.InteractionId, "approved");
            Assert.AreEqual("native-required", fixture.Tasks.BeginInteractionResolve(request).Status);
            fixture.Tasks.BeginInteractionResolve(request, fromNativeHost: true);
        };
        var declarationNode = System.Text.Json.Nodes.JsonNode.Parse(TransactionPreviewBody)!["declaration"]!;
        if (omitDecimals) declarationNode.AsObject().Remove("decimals");
        declarationNode["version"] = version;
        fixture.Http.TransactionConfigureVersion = version;
        var declaration = JsonSerializer.SerializeToElement(declarationNode);
        var response = await fixture.Call("transaction.configure", JsonSerializer.Serialize(new { declaration }));
        Assert.AreEqual("configured", response.GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual(version, response.GetProperty("result").GetProperty("version").GetInt64());
        var sent = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "transaction.configure");
        Assert.AreEqual(declaration.GetRawText(), sent.GetProperty("request").GetProperty("declaration").GetRawText());
    }

    [TestMethod]
    public async Task TransactionDrainWaitsForBothIndependentOperationsUsingSameBusinessKey()
    {
        using var fixture = new Fixture();
        var first = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var second = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var bothEntered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int commits = 0;
        fixture.Http.OnTransactionCommit = async _ =>
        {
            int index = Interlocked.Increment(ref commits);
            if (index == 2) bothEntered.TrySetResult();
            await (index == 1 ? first.Task : second.Task);
        };
        var one = fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}");
        var two = fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}");
        await bothEntered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        var drain = fixture.Api.DrainAsync();
        first.SetResult();
        await one.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.IsFalse(drain.IsCompleted);
        Assert.IsTrue(fixture.Api.HasWrites);
        second.SetResult();
        await two.WaitAsync(TimeSpan.FromSeconds(3));
        await drain.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.IsFalse(fixture.Api.HasWrites);
        Assert.AreEqual(2, fixture.Tasks.ProjectHistory(fixture.Context).Count);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task TransactionConfirmationIncludesDerivedRowsAndReceiptMustCoverExactlyThoseEffects(bool omitDerivedReceipt)
    {
        using var fixture = new Fixture();
        var prepared = System.Text.Json.Nodes.JsonNode.Parse(TransactionPreviewBody)!;
        prepared["previewRevision"] = 7;
        prepared["effects"]!.AsArray().Add(System.Text.Json.Nodes.JsonNode.Parse("""{"tableId":"table-head","recordId":"existing-reciprocal","operation":"update","schemaRevision":"schema_1","expectedDigest":"guard-before"}"""));
        fixture.Http.TransactionStatusBody = prepared.ToJsonString();
        if (!omitDerivedReceipt)
        {
            var applied = System.Text.Json.Nodes.JsonNode.Parse(TransactionAppliedBody)!;
            applied["receipt"]!["rows"]!.AsArray().Add(System.Text.Json.Nodes.JsonNode.Parse("""{"tableId":"table-head","recordId":"existing-reciprocal","operation":"update","revision":"row_2","digest":"guard-after"}"""));
            fixture.Http.TransactionCommitBody = applied.ToJsonString();
        }
        fixture.OnConfirmation = () =>
        {
            var interaction = fixture.LastInteraction!;
            var confirmation = interaction.PendingConfirmation!;
            Assert.AreEqual(3, confirmation.Preview.AffectedCount);
            var derived = confirmation.Preview.SampleRows.Single(row => row["recordId"].GetString() == "existing-reciprocal");
            Assert.IsTrue(derived["derived"].GetBoolean());
            Assert.AreEqual("table-head", derived["binding"].GetProperty("tableId").GetString());
            Assert.AreEqual("guard-before", derived["expectedDigest"].GetString());
            fixture.Tasks.BeginInteractionResolve(new(interaction.RunId, confirmation.InteractionId, "approved"), fromNativeHost: true);
        };
        var response = await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}");
        if (omitDerivedReceipt) Assert.AreEqual("plugin_commit_unknown", Code(response));
        else Assert.AreEqual("applied", response.GetProperty("result").GetProperty("status").GetString());
        var sent = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "transaction.commit");
        Assert.AreEqual(7L, sent.GetProperty("request").GetProperty("previewRevision").GetInt64());
    }

    [TestMethod]
    public async Task TransactionMissingAuthoritativeEffectsCannotReachConfirmationOrCommit()
    {
        using var fixture = new Fixture();
        var prepared = System.Text.Json.Nodes.JsonNode.Parse(TransactionPreviewBody)!;
        prepared.AsObject().Remove("effects");
        fixture.Http.TransactionStatusBody = prepared.ToJsonString();
        Assert.AreEqual("plugin_commit_unknown", Code(await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}")));
        Assert.AreEqual(0, fixture.Confirmations);
        Assert.IsFalse(fixture.Http.Calls.Any(call => call.GetProperty("method").GetString() == "transaction.commit"));
    }

    [TestMethod]
    [DataRow("transaction.plan_stale", "not-committed", "transaction.plan_stale", "failed")]
    [DataRow("transaction.unique_violation", "not-committed", "transaction.unique_violation", "failed")]
    [DataRow("plugin_read_denied", "not-committed", "plugin_read_denied", "failed")]
    [DataRow("plugin_commit_unknown", "unknown", "plugin_commit_unknown", "aborted")]
    [DataRow("transaction.plan_stale", null, "plugin_commit_unknown", "aborted")]
    [DataRow("transaction.same_target_violation", "not-committed", "transaction.same_target_violation", "failed")]
    [DataRow("transaction.balance_violation", "unknown", "plugin_commit_unknown", "aborted")]
    [DataRow("transaction.future_conflict", "not-committed", "plugin_commit_unknown", "aborted")]
    public async Task TransactionHttpOutcomePreservesProvenRejectionsAndNeverReplays(
        string code, string? outcome, string expectedCode, string expectedState)
    {
        using var fixture = new Fixture();
        fixture.Http.TransactionCommitErrorBody = outcome is null
            ? JsonSerializer.Serialize(new { code, message = "plugin request could not be completed" })
            : JsonSerializer.Serialize(new { code, commitOutcome = outcome, message = "plugin request could not be completed" });
        Assert.AreEqual(expectedCode, Code(await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}")));
        Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
        Assert.AreEqual(expectedState, task.State);
        Assert.AreEqual(expectedCode, task.Error!.Code);
        Assert.AreEqual(1, fixture.Confirmations);
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "transaction.commit"));
    }

    [TestMethod]
    [DataRow("transaction.balance_violation", "not-committed", "transaction.balance_violation", "failed")]
    [DataRow("transaction.balance_violation", "unknown", "plugin_commit_unknown", "aborted")]
    [DataRow("transaction.future_conflict", "not-committed", "plugin_commit_unknown", "aborted")]
    public async Task TransactionConfigurationChecksAuthorityOutcome(string code, string outcome, string expectedCode, string expectedState)
    {
        using var fixture = new Fixture();
        fixture.Http.TransactionConfigureErrorBody = JsonSerializer.Serialize(new { code, commitOutcome = outcome });
        var declaration = JsonDocument.Parse(TransactionPreviewBody).RootElement.GetProperty("declaration").Clone();
        Assert.AreEqual(expectedCode, Code(await fixture.Call("transaction.configure", JsonSerializer.Serialize(new { declaration }))));
        Assert.IsTrue(fixture.Tasks.TryGetTask(fixture.TaskId, out var task));
        Assert.AreEqual(expectedState, task.State);
        Assert.AreEqual(expectedCode, task.Error!.Code);
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "transaction.configure"));
    }

    [TestMethod]
    public async Task TransactionStaleConfirmationRequiresExplicitRefreshAndNewNativeDecision()
    {
        using var fixture = new Fixture();
        fixture.Http.TransactionCommitErrorBody = """{"code":"transaction.plan_stale","commitOutcome":"not-committed"}""";
        var response = await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}");
        Assert.AreEqual("transaction.plan_stale", Code(response));
        Assert.AreEqual(1, fixture.Confirmations);
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "transaction.commit"));
        var prepared = System.Text.Json.Nodes.JsonNode.Parse(TransactionPreviewBody)!;
        prepared["previewRevision"] = 2;
        fixture.Http.TransactionStatusBody = prepared.ToJsonString();
        var refresh = await fixture.Call("transaction.preview", JsonSerializer.Serialize(new
            { requestId = "business-id", plan = JsonSerializer.SerializeToElement(prepared["plan"]), refresh = true }));
        Assert.AreEqual(2, refresh.GetProperty("result").GetProperty("previewRevision").GetInt32());
        Assert.AreEqual(1, fixture.Confirmations);
        fixture.Http.TransactionCommitErrorBody = null;
        Assert.AreEqual("applied", (await fixture.Call("transaction.submit", "{\"requestId\":\"business-id\"}")).GetProperty("result").GetProperty("status").GetString());
        Assert.AreEqual(2, fixture.Confirmations);
        var commits = fixture.Http.Calls.Where(call => call.GetProperty("method").GetString() == "transaction.commit").ToArray();
        Assert.AreEqual(2, commits.Length);
        Assert.AreEqual(1, commits[0].GetProperty("request").GetProperty("previewRevision").GetInt32());
        Assert.AreEqual(2, commits[1].GetProperty("request").GetProperty("previewRevision").GetInt32());
    }

    private sealed class Fixture : IDisposable, IWebReplySink
    {
        internal PluginSurfaceSessionManager Surfaces { get; } = new();
        internal PluginProjectContext Context { get; } = new("local:11111111111141118111111111111111", "workspace:7", 7);
        internal HostPluginTaskRegistry Tasks { get; }
        internal PluginSurfaceSession Surface { get; }
        internal string Source => Surface.DocumentUri.AbsoluteUri;
        internal Peer Http { get; } = new();
        internal PluginSurfaceApi Api { get; }
        internal string Decision { get; init; } = "approved";
        internal int Confirmations { get; private set; }
        internal int Prints { get; private set; }
        internal Action? OnPrint { get; set; }
        internal Action<JsonElement>? ObserveModel { get; set; }
        internal int PrintReleases { get; private set; }
        internal Func<PluginSurfaceSession, CancellationToken, Task<bool>> PreparePrint { get; set; } = (_, _) => Task.FromResult(true);
        internal Func<ProductSidecarGenerationSnapshot, CancellationToken, Task> RefreshModelCatalog { get; set; } = (_, _) => Task.CompletedTask;
        internal PluginRuntimeInteractionSnapshot? LastInteraction { get; private set; }
        internal Action? OnConfirmation { get; set; }
        internal int LateNotifications { get; private set; }
        internal bool DropRendererNotifications { get; set; }
        internal int DroppedRendererNotifications { get; private set; }
        private bool _retired;
        internal void RetireAfterReceipt()
        {
            _retired = true;
            _generationCurrent = false;
            _generation = null;
            Api.Retire(Surface.SurfaceToken);
            var next = new PluginProjectContext("local:22222222222242228222222222222222", "workspace:8", 8);
            _authority.Transition(next);
            Tasks.RetireContext();
            Tasks.SetWorkspaceContext(next);
        }
        private bool _generationCurrent = true;
        private ProductSidecarGenerationSnapshot? _generation;
        private readonly ProductAuthorityEpoch _authority = new();
        private string? _taskId;
        internal string TaskId => _taskId!;
        internal Fixture(PluginProjectContext? context = null, string actionId = "save", string risk = "destructive", string presentation = "surface", string phase = PluginSurfacePhases.Foreground)
        {
            Context = context ?? Context;
            _authority.Transition(Context);
            Tasks = new(_authority);
            Tasks.SetWorkspaceContext(Context);
            var revision = PluginPackageRevision.Create("C:/plugins/synthetic", new string('a', 64));
            Surface = Surfaces.Open(Context.ProjectKey, "com.example.surface", revision, "ui/index.html", phase: phase);
            Surfaces.BindIdentity(Surface.SurfaceToken, Context, 1, "3.x", "1.0.0", actionId, risk, "ui/index.html", presentation: presentation, installationId: "record-surface");
            Surfaces.TryGet(Surface.SurfaceToken, out var bound);
            Surface = bound!;
            _generation = new ProductSidecarGenerationSnapshot(this, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:1/bootstrap"), new Uri("http://127.0.0.1:1/"), "X-VibeTable-Session", "private-test-secret"),
                new ProductSidecarIdentity(Guid.ParseExact(Context.ProjectKey["local:".Length..], "N").ToString("D"), Context.SessionGeneration, 1, "claim"), [],
                action => _generationCurrent && action());
            Api = new(Surfaces, Tasks, () => _generation, this, Http,
                preparePrint: (surface, _, token) => PreparePrint(surface, token),
                showPrint: () => { Prints++; OnPrint?.Invoke(); },
                releasePrint: _ => { PrintReleases++; return Task.CompletedTask; },
                refreshModelCatalog: (generation, token) => RefreshModelCatalog(generation, token),
                confirmModel: (plan, _) => { ObserveModel?.Invoke(plan); return Task.FromResult(true); });
        }
        internal string Wire(string method, string parameters, string? token = null, string? version = null) => JsonSerializer.Serialize(new
        { contract = version ?? PluginSurfaceApi.Contract, surfaceToken = token ?? Surface.SurfaceToken, requestId = Guid.NewGuid().ToString("N"), method, @params = JsonDocument.Parse(parameters).RootElement });
        internal async Task<JsonElement> Call(string method, string parameters, string? source = null, string? token = null, string? version = null)
        { string reply = await Api.HandleAsync(Surface.SurfaceToken, source ?? Source, Wire(method, parameters, token, version)); return JsonDocument.Parse(reply).RootElement.Clone(); }
        public void PostNotification(string type, object? payload)
        {
            if (DropRendererNotifications) { DroppedRendererNotifications++; return; }
            if (_retired) LateNotifications++;
            if (payload is not PluginEventEnvelope envelope) return;
            if (type == "plugin.task.changed") _taskId = envelope.EntityId;
            if (type == "plugin.interaction.requested")
            {
                Confirmations++;
                var interaction = envelope.Snapshot.Deserialize<PluginRuntimeInteractionSnapshot>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
                LastInteraction = interaction;
                if (OnConfirmation is { } onConfirmation) { onConfirmation(); return; }
                if (interaction.PendingConfirmation!.Preview.SampleRows[0].TryGetValue("values", out var values))
                    Assert.AreEqual("new", values.GetProperty("name").GetString());
                if (Decision == "retire")
                {
                    _generationCurrent = false;
                    _generation = null;
                    Api.Retire(Surface.SurfaceToken);
                    Surfaces.Close(Surface.SurfaceToken);
                }
                else if (Decision == "installation-retire") Surfaces.CloseForInstallation(Surface.ProjectKey, Surface.PluginId);
                else if (Decision == "cancel") Tasks.RequestCancel(_taskId!, out _, out _);
                else Tasks.BeginInteractionResolve(new(interaction.RunId, interaction.PendingConfirmation.InteractionId, Decision),
                    fromNativeHost: Tasks.RequiresNativeConfirmation(interaction.RunId));
            }
        }
        public void PostResponse(string type, string? requestId, object? payload) { }
        public void PostOperationFailed(string? requestId, string message, string? code = null, string? operation = null, string? operationId = null) { }
        public void Dispose() { Api.Dispose(); _authority.Dispose(); }
    }

    private sealed class ReceiptStream(byte[] bytes, Action after) : MemoryStream(bytes)
    {
        private bool _finished;
        public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken token = default)
        {
            int count = await base.ReadAsync(buffer, token);
            if (count == 0 && !_finished) { _finished = true; after(); }
            return count;
        }
    }

    private sealed class Peer : HttpMessageHandler
    {
        internal List<JsonElement> Calls { get; } = [];
        internal Action? AfterCommitRead { get; set; }
        internal string AfterReadMethod { get; set; } = "mutation.commit";
        internal bool ReplayCommit { get; set; }
        internal bool LoseCommit { get; set; }
        internal bool Conflict { get; set; }
        internal Func<CancellationToken, Task>? OnQuery { get; set; }
        internal Func<CancellationToken, Task>? OnDatasetOpen { get; set; }
        internal Func<CancellationToken, Task>? OnSnapshotOpen { get; set; }
        internal Func<CancellationToken, Task>? OnSnapshotValidate { get; set; }
        /// <summary>Overrides the validate reply wholesale (missing endorsement, missing identity map).</summary>
        internal string? SnapshotValidationBody { get; set; }
        /// <summary>Collections whose id grant the authority reports as revoked: validate answers identity=false for them.</summary>
        internal HashSet<string> SnapshotIdentityDenied { get; } = new(StringComparer.Ordinal);
        /// <summary>Observes every authority call (method, principal, request) before any other hook replies.</summary>
        internal Func<JsonElement, CancellationToken, Task>? OnCall { get; set; }
        internal string SnapshotResultBody { get; set; } = SnapshotBody;
        internal string? SnapshotValidationCode { get; set; }
        internal string? SnapshotOpenCode { get; set; }
        internal string DatasetResultBody { get; set; } = DatasetBody;
        internal string? DatasetValidationCode { get; set; }
        internal string? VersionsResultBody { get; set; }
        internal string? VersionsErrorCode { get; set; }
        internal string? QueryResultBody { get; set; }
        internal bool Incomplete { get; set; }
        internal bool FailAuditStart { get; set; }
        internal string? ContextError { get; set; }
        internal string? CommitErrorBody { get; set; }
        internal string? CommitResultBody { get; set; }
        internal string? ModelPreviewBody { get; set; }
        internal string? ModelCatalogBody { get; set; }
        internal string? ModelCommitBody { get; set; }
        internal string TransactionStatusBody { get; set; } = TransactionPreviewBody;
        internal string TransactionCommitBody { get; set; } = TransactionAppliedBody;
        internal long TransactionConfigureVersion { get; set; } = 1;
        internal string? TransactionCommitErrorBody { get; set; }
        internal string? TransactionConfigureErrorBody { get; set; }
        internal Func<CancellationToken, Task>? OnTransactionCommit { get; set; }
        internal Action? OnCommit { get; set; }
        internal bool CommitObservedCancellation { get; private set; }
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            Assert.AreEqual("private-test-secret", request.Headers.GetValues("X-VibeTable-Session").Single());
            var call = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token)).RootElement.Clone();
            Calls.Add(call);
            if (OnCall is { } onCall) await onCall(call, token);
            string method = call.GetProperty("method").GetString()!;
            if (method == "transaction.configure" && TransactionConfigureErrorBody is { } configureError)
                return new(HttpStatusCode.Conflict) { Content = new StringContent(configureError) };
            if (method == "transaction.commit" && OnTransactionCommit is { } transactionCommit) await transactionCommit(token);
            if (method == "transaction.commit" && TransactionCommitErrorBody is { } transactionError)
                return new(HttpStatusCode.Conflict) { Content = new StringContent(transactionError) };
            if (method == "data.query" && OnQuery is { } onQuery) await onQuery(token);
            if (method == "dataset.open" && OnDatasetOpen is { } onDataset) await onDataset(token);
            if (method == "snapshot.open" && OnSnapshotOpen is { } onSnapshot) await onSnapshot(token);
            if (method == "snapshot.validate" && OnSnapshotValidate is { } onSnapshotValidate) await onSnapshotValidate(token);
            if (method == "snapshot.open" && SnapshotOpenCode is { } snapshotOpenCode) return new(HttpStatusCode.Conflict) { Content = new StringContent(JsonSerializer.Serialize(new { code = snapshotOpenCode })) };
            if (method == "snapshot.validate" && SnapshotValidationCode is { } snapshotCode) return new(HttpStatusCode.Conflict) { Content = new StringContent(JsonSerializer.Serialize(new { code = snapshotCode })) };
            if (method == "data.versions" && VersionsErrorCode is { } versionsCode) return new(HttpStatusCode.Conflict) { Content = new StringContent(JsonSerializer.Serialize(new { code = versionsCode })) };
            if (method == "dataset.validate" && DatasetValidationCode is { } datasetCode) return new(HttpStatusCode.Conflict) { Content = new StringContent(JsonSerializer.Serialize(new { code = datasetCode })) };
            if (method == "mutation.commit" && OnCommit is { } onCommit)
            {
                onCommit();
                // RequestCancel flips the task lifetime synchronously, but this
                // commit's linked token only observes it through CancelAsync
                // callbacks; hang token-aware until the cancellation reaches the
                // in-flight commit instead of assuming it already did. A 10s
                // watchdog bounds the hang; the test fails explicitly if the
                // cancellation never arrives instead of silently succeeding.
                Task hang = Task.Delay(Timeout.InfiniteTimeSpan, token);
                Task watchdog = Task.Delay(TimeSpan.FromSeconds(10));
                if (await Task.WhenAny(hang, watchdog) == hang) CommitObservedCancellation = true;
                token.ThrowIfCancellationRequested();
            }
            if (method == "context.read" && ContextError is { } contextError) return new(HttpStatusCode.Forbidden) { Content = new StringContent(JsonSerializer.Serialize(new { code = contextError })) };
            if (method == "audit.start" && FailAuditStart) return new(HttpStatusCode.Conflict) { Content = new StringContent("""{"code":"plugin_audit_failed"}""") };
            if (method == "mutation.commit" && LoseCommit) throw new HttpRequestException("synthetic lost receipt");
            if (method == "mutation.commit" && CommitErrorBody is { } errorBody) return new(HttpStatusCode.Conflict) { Content = new StringContent(errorBody) };
            if (method == "mutation.commit" && Conflict) return new(HttpStatusCode.Conflict) { Content = new StringContent("""{"code":"mutation.digest_conflict","commitOutcome":"not-committed"}""") };

            string result = method switch
            {
                "dataset.open" => DatasetResultBody,
                "dataset.validate" => """{"valid":true}""",
                "snapshot.open" => SnapshotResultBody,
                "snapshot.validate" => SnapshotValidationBody ?? JsonSerializer.Serialize(new
                {
                    valid = true,
                    // Per collection: whether the plugin may currently read id (row guards may be served).
                    identity = JsonDocument.Parse(SnapshotResultBody).RootElement.GetProperty("sources").EnumerateArray()
                        .ToDictionary(source => source.GetProperty("collection").GetString()!, source => !SnapshotIdentityDenied.Contains(source.GetProperty("collection").GetString()!)),
                }),
                "data.versions" when VersionsResultBody is not null => VersionsResultBody,
                "data.query" => """{"items":[],"nextCursor":"go-cursor","dependencies":{}}""",
                "mutation.preview" => """{"affectedCount":1}""",
                "model.catalog" when ModelCatalogBody is not null => ModelCatalogBody,
                "model.preview" => ModelPreviewBody ?? """{"status":"completed"}""",
                "model.commit" => ModelCommitBody ?? "{}",
                "model.read" => """{"status":"completed","bindings":{"headers":{"tableId":"table-head","fields":{"name":"field-name"}},"lines":{"tableId":"table-line","fields":{"header":"field-header","other":"field-other"}}}}""",
                "transaction.configure" => JsonSerializer.Serialize(new { contract = "vibetable.plugin-transactions.v1", status = "configured", modelId = "orders", version = TransactionConfigureVersion }),
                "transaction.preview" or "transaction.status" => TransactionStatusBody,
                "transaction.commit" => TransactionCommitBody,
                "mutation.commit" => JsonSerializer.Serialize(new { contractVersion = "2.0", status = "applied", affectedRows = call.GetProperty("request").GetProperty("operations").EnumerateArray().Select(operation => new { recordId = "r1", operation = operation.GetProperty("kind").GetString() == "create" ? "insert" : operation.GetProperty("kind").GetString() }) }),
                _ => "{}",
            };
            if (method == "data.query" && QueryResultBody is { } queryResult) result = queryResult;
            if (method == "mutation.commit" && CommitResultBody is { } commitResult) result = commitResult;
            if (method == "mutation.commit" && Incomplete) result = """{"contractVersion":"2.0","status":"applied","affectedRows":[]}""";
            if (method == "mutation.commit" && ReplayCommit) result = result.Replace("applied", "replayed", StringComparison.Ordinal);
            return new(HttpStatusCode.OK) { Content = method == AfterReadMethod && AfterCommitRead is { } after
                ? new StreamContent(new ReceiptStream(Encoding.UTF8.GetBytes(result), after)) : new StringContent(result) };
        }
    }
}
