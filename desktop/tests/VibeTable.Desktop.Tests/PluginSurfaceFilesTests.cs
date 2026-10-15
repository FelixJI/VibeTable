using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceFilesTests
{
    [TestMethod]
    public async Task NativePickerFilesAreOpaqueOwnedBoundedAndRetiredBeforeFinalSave()
    {
        await using var f = new Fixture();
        string id = (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!;
        Assert.IsFalse((await f.Call("file.read", JsonSerializer.Serialize(new { grantId = id }))).GetRawText().Contains(f.Root, StringComparison.Ordinal));
        Assert.IsTrue((await f.Call("file.read", JsonSerializer.Serialize(new { grantId = id }))).GetProperty("result").GetProperty("eof").GetBoolean());
        await f.Call("file.release", JsonSerializer.Serialize(new { grantId = id }));
        Assert.IsTrue((await f.Call("file.read", JsonSerializer.Serialize(new { grantId = id }))).TryGetProperty("error", out _));
        string write = (await f.Call("file.pickWrite", "{\"suggestedName\":\"out.txt\",\"mediaType\":\"text/plain\"}")).GetProperty("result").GetProperty("grantId").GetString()!;
        f.AfterAuthorize = () => f.Surfaces.CloseAll();
        var rejected = await f.Call("file.write", JsonSerializer.Serialize(new { grantId = write, offset = 0, base64 = Convert.ToBase64String(Encoding.UTF8.GetBytes("new")), finish = true }));
        Assert.IsTrue(rejected.TryGetProperty("error", out _)); Assert.AreEqual("old", File.ReadAllText(f.Target));
        Assert.AreEqual(0, Directory.GetFiles(f.Root, ".vibetable-*.tmp").Length);
    }

    [TestMethod]
    public async Task PickerCancellationAndInstallationRetirementCannotMintGrants()
    {
        await using var f = new Fixture(); f.CancelPick = true;
        Assert.AreEqual(JsonValueKind.Null, (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").ValueKind);
        f.CancelPick = false; f.AfterPick = () => f.Surfaces.CloseForInstallation(f.Surface.ProjectKey, f.Surface.PluginId);
        Assert.IsTrue((await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).TryGetProperty("error", out _));
        Assert.AreEqual(0, f.Leases);
    }

    [TestMethod]
    public async Task ImportUsesCapturedDataIoRunAndNativePreciseConfirmationWithoutLeakingTokens()
    {
        await using var f = new Fixture();
        var started = await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}");
        string taskId = started.GetProperty("result").GetProperty("taskId").GetString()!;
        var preview = await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }));
        string planId = preview.GetProperty("result").GetProperty("result").GetProperty("planId").GetString()!;
        Assert.IsFalse(preview.GetRawText().Contains("internal-go-token", StringComparison.Ordinal));
        Assert.IsFalse(preview.GetRawText().Contains("grant-", StringComparison.Ordinal));
        await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        var result = await f.Call("mutation.submit", JsonSerializer.Serialize(new { planId }));
        Assert.AreEqual("succeeded", result.GetProperty("result").GetProperty("status").GetString());
        Assert.IsTrue(f.NativeConfirmation); Assert.IsTrue(f.Http.Calls.Any(value => value.GetProperty("method").GetString() == "import.bind"));
        await f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId }));
        Assert.AreEqual("succeeded", (await f.Call("data.io.status", JsonSerializer.Serialize(new { taskId }))).GetProperty("result").GetProperty("state").GetString());
    }

    [TestMethod]
    public async Task AttachmentsReplaceInternalNamesAndTicketsWithSurfaceHandles()
    {
        await using var f = new Fixture();
        var listed = await f.Call("attachments.list", "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\"}");
        Assert.IsFalse(listed.GetRawText().Contains("internal-stored-name", StringComparison.Ordinal));
        string id = listed.GetProperty("result").GetProperty("items")[0].GetProperty("attachmentId").GetString()!;
        var read = await f.Call("attachments.read", JsonSerializer.Serialize(new { attachmentId = id, offset = 0 }));
        Assert.AreEqual("YWJj", read.GetProperty("result").GetProperty("base64").GetString());
        Assert.IsTrue((await f.Call("attachments.read", "{\"attachmentId\":\"foreign-handle\",\"offset\":0}")).TryGetProperty("error", out _));
        Assert.IsTrue((await f.Call("attachments.previewChange", JsonSerializer.Serialize(new { collection = "orders", recordId = "other", fieldId = "files", expectedDigest = "guard", removeAttachmentIds = new[] { id }, addFile = false }))).TryGetProperty("error", out _));
    }

    [TestMethod]
    public async Task VerifiedNativeSaveRemainsSuccessfulWhenTheSurfaceClosesImmediatelyAfterReplace()
    {
        await using var f = new Fixture();
        string id = (await f.Call("file.pickWrite", "{\"suggestedName\":\"out.txt\",\"mediaType\":\"text/plain\"}")).GetProperty("result").GetProperty("grantId").GetString()!;
        f.AfterNativeCommit = () => f.Surfaces.CloseAll();
        var reply = await f.Call("file.write", JsonSerializer.Serialize(new { grantId = id, offset = 0, base64 = "bmV3", finish = true }));
        Assert.IsTrue(reply.GetProperty("result").GetProperty("committed").GetBoolean());
        Assert.AreEqual("new", File.ReadAllText(f.Target)); Assert.AreEqual(0, f.Leases);
    }

    [TestMethod]
    public async Task FileCapacityReleasesAcrossRepeatedMountsAndAttachmentListsReuseOwnedHandles()
    {
        await using var f = new Fixture();
        var ids = new List<string>();
        for (int i = 0; i < 8; i++) ids.Add((await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!);
        Assert.IsTrue((await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).TryGetProperty("error", out _));
        foreach (string id in ids) await f.Call("file.release", JsonSerializer.Serialize(new { grantId = id }));
        for (int i = 0; i < 16; i++)
        {
            string id = (await f.Call("file.pickRead", "{\"mediaTypes\":[]}")).GetProperty("result").GetProperty("grantId").GetString()!;
            await f.Call("file.release", JsonSerializer.Serialize(new { grantId = id }));
        }
        string? prior = null;
        for (int i = 0; i < 70; i++)
        {
            var listed = await f.Call("attachments.list", "{\"collection\":\"orders\",\"recordId\":\"r1\",\"fieldId\":\"files\"}");
            string current = listed.GetProperty("result").GetProperty("items")[0].GetProperty("attachmentId").GetString()!;
            if (prior is not null) Assert.AreEqual(prior, current); prior = current;
        }
    }

    [TestMethod]
    public async Task RepeatedOwnedIoCancelCoalescesAndRevokesItsCapturedFileGrant()
    {
        await using var f = new Fixture(); f.IoRunning = true;
        string taskId = (await f.Call("data.import.start", "{\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"mode\":\"create_only\",\"upsertKey\":null,\"columnMapping\":[]}")).GetProperty("result").GetProperty("taskId").GetString()!;
        await Task.WhenAll(f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })), f.Call("data.io.cancel", JsonSerializer.Serialize(new { taskId })));
        Assert.AreEqual(1, f.IoCancels);
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Broker.HandleAsync("openRead", JsonSerializer.SerializeToElement(new { grantId = f.IoGrant, runId = f.IoRun }), CancellationToken.None));
        Assert.IsTrue((await f.Call("data.io.cancel", "{\"taskId\":\"foreign-task\"}")).TryGetProperty("error", out _));
        Assert.AreEqual(1, f.IoCancels);
    }

    private sealed class Fixture : IAsyncDisposable, IWebReplySink, INativeProductFileHost
    {
        internal string Root { get; } = Path.Combine(Path.GetTempPath(), "vibetable-plugin-files-" + Guid.NewGuid().ToString("N"));
        internal string Source => Path.Combine(Root, "source.csv");
        internal string Target => Path.Combine(Root, "out.txt");
        internal PluginSurfaceSessionManager Surfaces { get; } = new();
        internal PluginSurfaceSession Surface { get; }
        internal HostSessionFileBroker Broker { get; }
        internal PluginSurfaceApi Api { get; }
        internal Peer Http { get; } = new();
        internal bool CancelPick;
        internal Action? AfterPick;
        internal Action? AfterAuthorize { set => Http.AfterAuthorize = value; }
        internal int Leases;
        internal Action? AfterNativeCommit;
        internal bool NativeConfirmation;
        internal bool IoRunning;
        internal int IoCancels;
        internal string? IoGrant;
        internal string? IoRun;
        private readonly ProductAuthorityEpoch _authority = new();
        private readonly HostPluginTaskRegistry _tasks;
        private readonly PluginProjectContext _context = new("local:11111111111141118111111111111111", "workspace:7", 7);
        private readonly ProductSidecarGenerationSnapshot _generation;
        private JsonElement _status;
        internal Fixture()
        {
            Directory.CreateDirectory(Root); File.WriteAllText(Source, "name\nnew\n"); File.WriteAllText(Target, "old");
            _authority.Transition(_context); _tasks = new(_authority); _tasks.SetWorkspaceContext(_context);
            var surface = Surfaces.Open(_context.ProjectKey, "com.example.files", PluginPackageRevision.Create("C:/synthetic", new string('a', 64)), "ui/index.html");
            Surfaces.BindIdentity(surface.SurfaceToken, _context, 1, "3.x", "1.0.0", "save", "write", "ui/index.html", installationId: "installation");
            Surfaces.TryGet(surface.SurfaceToken, out var bound); Surface = bound!;
            _generation = new ProductSidecarGenerationSnapshot(this, 1, new PocketBaseAdminContext(new Uri("http://127.0.0.1:1/bootstrap"), new Uri("http://127.0.0.1:1/"), "X-VibeTable-Session", "synthetic"), new ProductSidecarIdentity("11111111-1111-4111-8111-111111111111", 7, 1, "claim"), [], action => action());
            Broker = new HostSessionFileBroker(() => { Interlocked.Increment(ref Leases); return new WorkspaceRequestEpochLease(new WorkspaceWireScope { Scope = "workspace", WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"), SessionEpoch = 7, OperationId = Guid.NewGuid(), Sequence = 1 }, CancellationToken.None, () => Interlocked.Decrement(ref Leases)); }, action => { action(); if (File.ReadAllText(Target) == "new") { var after = AfterNativeCommit; AfterNativeCommit = null; after?.Invoke(); } });
            Api = new(Surfaces, _tasks, () => _generation, this, Http, () => Broker, this, pick => Task.FromResult(pick()), () => new(Broker, StartIo, _ => _status, (_, _) => { IoCancels++; return Task.FromResult(_status); }, (_, _) => Task.CompletedTask));
        }
        private async Task<JsonElement> StartIo(string kind, JsonElement parameters, CancellationToken token)
        {
            string grantId = parameters.GetProperty("grantId").GetString()!; string runId = parameters.GetProperty("pluginScope").GetProperty("runId").GetString()!;
            IoGrant = grantId; IoRun = runId;
            var opened = await Broker.HandleAsync("openRead", JsonSerializer.SerializeToElement(new { grantId, runId }), token);
            string transferId = opened.GetProperty("transferId").GetString()!;
            for (; ; ) { var block = await Broker.HandleAsync("read", JsonSerializer.SerializeToElement(new { transferId, maxBytes = 262144 }), token); if (block.GetProperty("eof").GetBoolean()) break; }
            await Broker.HandleAsync("closeRead", JsonSerializer.SerializeToElement(new { transferId }), token);
            _status = JsonSerializer.SerializeToElement(new { taskId = "owned-task", kind, state = IoRunning ? "running" : "succeeded", progress = new { done = 1, total = 1, message = "parsed" }, result = new { token = new { token = "internal-go-token" }, summary = new { totalRows = 1, validRows = 1, errorRows = 0 }, rows = Array.Empty<object>() }, error = (string?)null }); return _status;
        }
        internal async Task<JsonElement> Call(string method, string parameters)
        { var raw = JsonSerializer.Serialize(new { contract = PluginSurfaceApi.Contract, surfaceToken = Surface.SurfaceToken, requestId = Guid.NewGuid().ToString("N"), method, @params = JsonDocument.Parse(parameters).RootElement }); return JsonDocument.Parse(await Api.HandleAsync(Surface.SurfaceToken, Surface.DocumentUri.AbsoluteUri, raw)).RootElement.Clone(); }
        public string? SelectImportSource() { AfterPick?.Invoke(); return CancelPick ? null : Source; }
        public string? SelectExportTarget(string format, string defaultName) => SelectAttachmentTarget(defaultName);
        public NativeAttachmentSelection SelectAttachmentSources(bool replacement) { AfterPick?.Invoke(); return new(CancelPick ? [] : [Source], true); }
        public string? SelectAttachmentTarget(string suggestedName) { AfterPick?.Invoke(); return CancelPick ? null : Target; }
        public string CreateAttachmentPreviewPath(string suggestedName) => throw new NotSupportedException();
        public Task PreviewAttachmentAsync(string fullPath) => throw new NotSupportedException();
        public void PostNotification(string type, object? payload)
        {
            if (type != "plugin.interaction.requested" || payload is not PluginEventEnvelope envelope) return;
            var interaction = envelope.Snapshot.Deserialize<PluginRuntimeInteractionSnapshot>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
            NativeConfirmation = _tasks.RequiresNativeConfirmation(interaction.RunId);
            _tasks.BeginInteractionResolve(new(interaction.RunId, interaction.PendingConfirmation!.InteractionId, "approved"), out _, fromNativeHost: true);
        }
        public void PostResponse(string type, string? id, object? payload) { }
        public void PostOperationFailed(string? id, string message, string? code = null, string? operation = null, string? operationId = null) { }
        public async ValueTask DisposeAsync() { Api.Dispose(); await Api.DrainAsync(); Broker.Retire(); await Broker.DrainCompletion; _authority.Dispose(); Directory.Delete(Root, true); }
    }
    private sealed class Peer : HttpMessageHandler
    {
        internal List<JsonElement> Calls { get; } = [];
        internal Action? AfterAuthorize;
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            var call = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token)).RootElement.Clone(); Calls.Add(call); string method = call.GetProperty("method").GetString()!;
            if (method == "file.authorize") { var after = AfterAuthorize; AfterAuthorize = null; after?.Invoke(); }
            string body = method switch
            {
                "data.describe" => "{\"schemaRevision\":\"schema_1\"}",
                "import.freeze" => "{\"attempt\":1,\"plan\":{\"contract\":\"vibetable.surface-mutation-plan.v1\",\"collection\":\"orders\",\"schemaRevision\":\"schema_1\",\"operations\":[{\"kind\":\"create\",\"values\":{\"name\":\"new\"}}]}}",
                "mutation.commit" => "{\"contractVersion\":\"2.0\",\"status\":\"applied\",\"affectedRows\":[{\"recordId\":\"r1\",\"operation\":\"insert\"}]}",
                "attachment.list" => "{\"items\":[{\"storedName\":\"internal-stored-name\",\"displayName\":\"safe.txt\",\"mediaType\":\"text/plain\",\"sizeBytes\":3}]}",
                "attachment.read" => "{\"base64\":\"YWJj\",\"eof\":true}",
                _ => "{}"
            };
            return new(HttpStatusCode.OK) { Content = new StringContent(body) };
        }
    }
}
