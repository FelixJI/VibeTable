using System.Net;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceApiTests
{
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
    public async Task LongLivedSurfaceHasPerRequestBudgetAndOpaqueSingleUseCursors()
    {
        using var fixture = new Fixture();
        for (int i = 0; i < 100; i++) Assert.IsFalse((await fixture.Call("context.read", "{}")).TryGetProperty("error", out _));
        var first = await fixture.Call("data.query", "{\"collection\":\"orders\"}");
        string cursor = first.GetProperty("result").GetProperty("nextCursor").GetString()!;
        Assert.AreNotEqual("go-cursor", cursor);
        var peerSurface = fixture.Surfaces.Open(fixture.Context.ProjectKey, "com.example.other", PluginPackageRevision.Create("C:/plugins/other", new string('b', 64)), "ui/index.html");
        fixture.Surfaces.BindIdentity(peerSurface.SurfaceToken, fixture.Context, 1, "3.x");
        string peerReply = await fixture.Api.HandleAsync(peerSurface.SurfaceToken, peerSurface.DocumentUri.AbsoluteUri,
            fixture.Wire("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}", token: peerSurface.SurfaceToken));
        Assert.AreEqual("plugin_cursor_invalid", Code(peerReply));
        using var other = new Fixture(new PluginProjectContext("local:22222222222242228222222222222222", "workspace:9", 9));
        Assert.AreEqual("plugin_cursor_invalid", Code(await other.Call("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}")));
        var fetched = await fixture.Call("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}");
        Assert.IsFalse(fetched.TryGetProperty("error", out _));
        Assert.AreEqual("go-cursor", fixture.Http.Calls[^1].GetProperty("cursor").GetString());
        Assert.AreEqual("plugin_cursor_invalid", Code(await fixture.Call("data.query", $"{{\"collection\":\"orders\",\"cursor\":\"{cursor}\"}}")));
        fixture.Surfaces.BindIdentity(fixture.Surface.SurfaceToken, fixture.Context, 2, "3.x");
        Assert.AreEqual("plugin_session_stale", Code(await fixture.Call("context.read", "{}")));
    }

    [TestMethod]
    public async Task HostConfirmsExactPlanOnceAndLateCancelPreservesSuccessWithoutPythonGateway()
    {
        using var fixture = new Fixture { Decision = "approved" };
        Assert.IsNull(fixture.Tasks.Capture());
        string plan = (await fixture.Call("mutation.preview", Plan)).GetProperty("result").GetProperty("planId").GetString()!;
        Assert.AreEqual("plugin_capability_invalid", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\",\"approved\":true}}")));
        var submitted = await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}");
        Assert.AreEqual("succeeded", submitted.GetProperty("result").GetProperty("status").GetString());
        string taskId = submitted.GetProperty("result").GetProperty("taskId").GetString()!;
        Assert.AreEqual(1, fixture.Http.Calls.Count(call => call.GetProperty("method").GetString() == "mutation.commit"));
        Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
        Assert.AreEqual(HostPluginTaskCancelOutcome.Terminal, fixture.Tasks.RequestCancel(taskId, out var snapshot, out _));
        Assert.AreEqual("succeeded", snapshot.State);
        Assert.AreEqual("com.example.surface", fixture.Http.Calls[^1].GetProperty("pluginId").GetString());
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
            if (decision == "approved") { Assert.AreEqual("plugin_commit_unknown", Code(response)); Assert.AreEqual(1, commits); }
            else { Assert.AreEqual(0, commits); if (decision == "rejected") Assert.AreEqual("rejected", response.GetProperty("result").GetProperty("status").GetString()); }
            Assert.AreEqual("plugin_plan_stale", Code(await fixture.Call("mutation.submit", $"{{\"planId\":\"{plan}\"}}")));
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

    private const string Plan = """{"contract":"vibetable.surface-mutation-plan.v1","collection":"orders","schemaRevision":"schema_1","operations":[{"kind":"update","primaryKey":"r1","expectedDigest":"sha256:existing","values":{"name":"new"}}]}""";
    private static string? Code(JsonElement result) => result.GetProperty("error").GetProperty("code").GetString();
    private static string? Code(string result) => Code(JsonDocument.Parse(result).RootElement);

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
        private readonly ProductAuthorityEpoch _authority = new();
        private string? _taskId;
        internal Fixture(PluginProjectContext? context = null)
        {
            Context = context ?? Context;
            _authority.Transition(Context);
            Tasks = new(_authority);
            Tasks.SetWorkspaceContext(Context);
            var revision = PluginPackageRevision.Create("C:/plugins/synthetic", new string('a', 64));
            Surface = Surfaces.Open(Context.ProjectKey, "com.example.surface", revision, "ui/index.html");
            Surfaces.BindIdentity(Surface.SurfaceToken, Context, 1, "3.x", "1.0.0");
            var generation = new ProductSidecarGenerationSnapshot(this, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:1/bootstrap"), new Uri("http://127.0.0.1:1/"), "X-VibeTable-Session", "private-test-secret"),
                new ProductSidecarIdentity(Guid.ParseExact(Context.ProjectKey["local:".Length..], "N").ToString("D"), Context.SessionGeneration, 1, "claim"), []);
            Api = new(Surfaces, Tasks, () => generation, this, Http);
        }
        internal string Wire(string method, string parameters, string? token = null, string? version = null) => JsonSerializer.Serialize(new
        { contract = version ?? PluginSurfaceApi.Contract, surfaceToken = token ?? Surface.SurfaceToken, requestId = Guid.NewGuid().ToString("N"), method, @params = JsonDocument.Parse(parameters).RootElement });
        internal async Task<JsonElement> Call(string method, string parameters, string? source = null, string? token = null, string? version = null)
        { string reply = await Api.HandleAsync(Surface.SurfaceToken, source ?? Source, Wire(method, parameters, token, version)); return JsonDocument.Parse(reply).RootElement.Clone(); }
        public void PostNotification(string type, object? payload)
        {
            if (payload is not PluginEventEnvelope envelope) return;
            if (type == "plugin.task.changed") _taskId = envelope.EntityId;
            if (type == "plugin.interaction.requested")
            {
                var interaction = envelope.Snapshot.Deserialize<PluginRuntimeInteractionSnapshot>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
                Assert.AreEqual("new", interaction.PendingConfirmation!.Preview.SampleRows[0]["values"].GetProperty("name").GetString());
                if (Decision == "cancel") Tasks.RequestCancel(_taskId!, out _, out _);
                else Tasks.BeginInteractionResolve(new(interaction.RunId, interaction.PendingConfirmation.InteractionId, Decision), out _);
            }
        }
        public void PostResponse(string type, string? requestId, object? payload) { }
        public void PostOperationFailed(string? requestId, string message, string? code = null, string? operation = null, string? operationId = null) { }
        public void Dispose() { Api.Dispose(); _authority.Dispose(); }
    }

    private sealed class Peer : HttpMessageHandler
    {
        internal List<JsonElement> Calls { get; } = [];
        internal bool LoseCommit { get; set; }
        internal bool Conflict { get; set; }
        internal bool Incomplete { get; set; }
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            Assert.AreEqual("private-test-secret", request.Headers.GetValues("X-VibeTable-Session").Single());
            var call = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token)).RootElement.Clone();
            Calls.Add(call);
            string method = call.GetProperty("method").GetString()!;
            if (method == "mutation.commit" && LoseCommit) throw new HttpRequestException("synthetic lost receipt");
            if (method == "mutation.commit" && Conflict) return new(HttpStatusCode.Conflict) { Content = new StringContent("""{"code":"mutation.digest_conflict"}""") };
            if (method == "mutation.commit" && Incomplete) return new(HttpStatusCode.OK) { Content = new StringContent("""{"contractVersion":"2.0","status":"applied","affectedRows":[]}""") };
            string result = method switch
            {
                "data.query" => """{"items":[],"nextCursor":"go-cursor","dependencies":{}}""",
                "mutation.preview" => """{"affectedCount":1}""",
                "mutation.commit" => """{"contractVersion":"2.0","status":"applied","affectedRows":[{"recordId":"r1","operation":"update"}]}""",
                _ => "{}",
            };
            return new(HttpStatusCode.OK) { Content = new StringContent(result) };
        }
    }
}
