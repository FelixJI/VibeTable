using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceApiTests
{
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
        foreach (string code in new[] { "plugin_write_denied", "plugin_cursor_stale", "mutation.relation.restricted" })
        {
            using var fixture = new Fixture();
            fixture.Http.CommitErrorBody = JsonSerializer.Serialize(new { code, commitOutcome = "not-committed" });
            string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
            Assert.AreEqual(code, Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
            var start = fixture.Http.Calls.Single(call => call.GetProperty("method").GetString() == "audit.start");
            Assert.IsTrue(fixture.Tasks.TryGetTask(start.GetProperty("request").GetProperty("taskId").GetString()!, out var task));
            Assert.AreEqual("failed", task.State);
            Assert.AreEqual(code, task.Error!.Code);
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
        internal Fixture(PluginProjectContext? context = null, string actionId = "save", string risk = "destructive", string presentation = "surface")
        {
            Context = context ?? Context;
            _authority.Transition(Context);
            Tasks = new(_authority);
            Tasks.SetWorkspaceContext(Context);
            var revision = PluginPackageRevision.Create("C:/plugins/synthetic", new string('a', 64));
            Surface = Surfaces.Open(Context.ProjectKey, "com.example.surface", revision, "ui/index.html");
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
                confirmModel: (_, _) => Task.FromResult(true));
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
        internal string? QueryResultBody { get; set; }
        internal bool Incomplete { get; set; }
        internal bool FailAuditStart { get; set; }
        internal string? ContextError { get; set; }
        internal string? CommitErrorBody { get; set; }
        internal string? CommitResultBody { get; set; }
        internal string? ModelPreviewBody { get; set; }
        internal string? ModelCommitBody { get; set; }
        internal string TransactionStatusBody { get; set; } = TransactionPreviewBody;
        internal string TransactionCommitBody { get; set; } = TransactionAppliedBody;
        internal long TransactionConfigureVersion { get; set; } = 1;
        internal string? TransactionCommitErrorBody { get; set; }
        internal Func<CancellationToken, Task>? OnTransactionCommit { get; set; }
        internal Action? OnCommit { get; set; }
        internal bool CommitObservedCancellation { get; private set; }
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            Assert.AreEqual("private-test-secret", request.Headers.GetValues("X-VibeTable-Session").Single());
            var call = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token)).RootElement.Clone();
            Calls.Add(call);
            string method = call.GetProperty("method").GetString()!;
            if (method == "transaction.commit" && OnTransactionCommit is { } transactionCommit) await transactionCommit(token);
            if (method == "transaction.commit" && TransactionCommitErrorBody is { } transactionError)
                return new(HttpStatusCode.Conflict) { Content = new StringContent(transactionError) };
            if (method == "data.query" && OnQuery is { } onQuery) await onQuery(token);
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
                "data.query" => """{"items":[],"nextCursor":"go-cursor","dependencies":{}}""",
                "mutation.preview" => """{"affectedCount":1}""",
                "model.preview" => ModelPreviewBody ?? """{"status":"completed"}""",
                "model.commit" => ModelCommitBody ?? "{}",
                "model.read" => """{"status":"completed","bindings":{"headers":{"tableId":"table-head","fields":{"name":"field-name"}},"lines":{"tableId":"table-line","fields":{"header":"field-header"}}}}""",
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
