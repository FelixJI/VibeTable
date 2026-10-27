using System.Net;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceOutputTests
{
    [TestMethod]
    public async Task LargeSealedOutputSavesAtomicallyAndSameAttemptKeepsSuccess()
    {
        await using var f = new Fixture();
        byte[] content = Enumerable.Range(0, 1_100_001).Select(i => (byte)(i % 251)).ToArray();
        string outputId = await f.Create(content);
        var saved = await f.Call("output.save", new { outputId, saveId = "save-once" });
        Assert.AreEqual("succeeded", saved.GetProperty("result").GetProperty("state").GetString(), saved.GetRawText());
        CollectionAssert.AreEqual(content, File.ReadAllBytes(f.Target));
        Assert.AreEqual(1, f.Picks);
        var replay = await f.Call("output.save", new { outputId, saveId = "save-once" });
        Assert.AreEqual(saved.GetProperty("result").GetRawText(), replay.GetProperty("result").GetRawText());
        Assert.AreEqual(1, f.Picks, "lost acknowledgement recovery must never reopen the picker");
        var status = await f.Call("output.status", new { outputId, saveId = "save-once" });
        Assert.IsTrue(status.GetProperty("result").GetProperty("committed").GetBoolean());
        Assert.IsFalse(status.GetRawText().Contains(f.Root, StringComparison.Ordinal));
        var changed = await f.Call("output.write", new { outputId, offset = 0, base64 = "eA==" });
        Assert.IsTrue(changed.TryGetProperty("error", out _), "an issued output cannot change bytes");
        Assert.AreEqual(0, Directory.GetFiles(f.Root, ".vibetable-*.tmp").Length);
        Assert.AreEqual(0, f.Leases);
        Assert.IsFalse(f.Http.Methods.Any(method => method.StartsWith("mutation.", StringComparison.Ordinal)), "export must not replay business writes");
    }

    [TestMethod]
    public async Task CancellationAndDiskFailurePreserveTheOriginalAndReturnDefiniteOutcomes()
    {
        await using var f = new Fixture();
        string outputId = await f.Create([1, 2, 3]);
        f.CancelPick = true;
        var cancelled = await f.Call("output.save", new { outputId, saveId = "cancelled" });
        Assert.AreEqual("cancelled", cancelled.GetProperty("result").GetProperty("state").GetString());
        Assert.AreEqual("original", File.ReadAllText(f.Target));
        f.CancelPick = false;
        using (FileStream held = new(f.Target, FileMode.Open, FileAccess.ReadWrite, FileShare.None))
        {
            var failed = await f.Call("output.save", new { outputId, saveId = "disk-locked" });
            Assert.AreEqual("failed", failed.GetProperty("result").GetProperty("state").GetString(), failed.GetRawText());
            Assert.IsFalse(failed.GetProperty("result").GetProperty("committed").GetBoolean());
        }
        Assert.AreEqual("original", File.ReadAllText(f.Target));
        Assert.AreEqual(0, Directory.GetFiles(f.Root, ".vibetable-*.tmp").Length);
        var retry = await f.Call("output.save", new { outputId, saveId = "explicit-retry" });
        Assert.AreEqual("succeeded", retry.GetProperty("result").GetProperty("state").GetString());
        CollectionAssert.AreEqual(new byte[] { 1, 2, 3 }, File.ReadAllBytes(f.Target));
    }

    [TestMethod]
    public async Task ClosedSurfaceBeforeCommitCannotReplaceButClosingAfterCommitKeepsSuccess()
    {
        await using (var f = new Fixture())
        {
            string outputId = await f.Create([1]);
            f.AfterPick = f.Surfaces.CloseAll;
            var rejected = await f.Call("output.save", new { outputId, saveId = "retired-before" });
            Assert.IsTrue(rejected.TryGetProperty("error", out _) || rejected.GetProperty("result").GetProperty("state").GetString() != "succeeded");
            Assert.AreEqual("original", File.ReadAllText(f.Target));
        }
        await using (var f = new Fixture())
        {
            string outputId = await f.Create([1]);
            f.AfterCommit = f.Surfaces.CloseAll;
            var saved = await f.Call("output.save", new { outputId, saveId = "retired-after" });
            Assert.AreEqual("succeeded", saved.GetProperty("result").GetProperty("state").GetString(), saved.GetRawText());
            CollectionAssert.AreEqual(new byte[] { 1 }, File.ReadAllBytes(f.Target));
        }
    }

    [TestMethod]
    public async Task OutputBudgetAndCapabilityMismatchNeverFallBackToOrdinaryFiles()
    {
        await using var f = new Fixture();
        var oversize = await f.Call("output.create", f.Options(33_554_433));
        Assert.AreEqual("plugin_file_limit", oversize.GetProperty("error").GetProperty("code").GetString());
        var unsupported = await f.Call("output.create", new { contract = "old-output", suggestedName = "out.xlsx", mediaType = "x", sizeBytes = 1, source = new { templateVersion = "t", dataVersion = "d", purpose = "p" } });
        Assert.AreEqual("plugin_api_unsupported", unsupported.GetProperty("error").GetProperty("code").GetString());
        string first = await f.Create([1]);
        string second = await f.Create([2]);
        var third = await f.Call("output.create", f.Options(1));
        Assert.AreEqual("plugin_file_limit", third.GetProperty("error").GetProperty("code").GetString());
        await f.Call("output.release", new { outputId = first });
        Assert.AreNotEqual(second, await f.Create([3]));
        Assert.AreEqual(0, f.Picks);
    }

    [TestMethod]
    public async Task ReopenHoldsTheContentGateAndRetirementCancelsBeforeDisposal()
    {
        await using var f = new Fixture();
        bool held = false;
        f.Http.OnOutputRead = () =>
        {
            var maps = (System.Collections.IDictionary)typeof(PluginSurfaceApi).GetField("_outputs",
                System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(f.Api)!;
            var outputs = (System.Collections.IDictionary)maps[f.Surface.SurfaceToken]!;
            object output = outputs.Values.Cast<object>().Single();
            var gate = (SemaphoreSlim)output.GetType().GetProperty("Gate",
                System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.GetValue(output)!;
            held = gate.CurrentCount == 0;
            f.Surfaces.CloseAll();
        };
        var closed = await f.Call("output.reopen", new { contract = PluginSurfaceApi.OutputContract, artifactId = "issued-artifact" });
        Assert.IsTrue(held, "reopen must own the same gate as release/TTL cleanup while reading and writing Content");
        Assert.IsTrue(closed.TryGetProperty("error", out _), closed.GetRawText());
        await f.Api.DrainAsync();
        Assert.AreEqual("original", File.ReadAllText(f.Target));
        Assert.AreEqual(0, f.Picks);
    }

    [TestMethod]
    public async Task ReopenUsesAuthoritativeSourceAndExactBytes()
    {
        await using var f = new Fixture();
        var opened = await f.Call("output.reopen", new { contract = PluginSurfaceApi.OutputContract, artifactId = "issued-artifact" });
        var result = opened.GetProperty("result");
        Assert.AreEqual("original-template", result.GetProperty("source").GetProperty("templateVersion").GetString());
        string outputId = result.GetProperty("outputId").GetString()!;
        var saved = await f.Call("output.save", new { outputId, saveId = "reprint" });
        Assert.AreEqual("succeeded", saved.GetProperty("result").GetProperty("state").GetString(), saved.GetRawText());
        CollectionAssert.AreEqual(new byte[] { 1, 2, 3 }, File.ReadAllBytes(f.Target));
    }

    private sealed class Fixture : IAsyncDisposable, IWebReplySink, INativeProductFileHost
    {
        internal string Root { get; } = Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "../../../../../../build/tests/plugin-output", Guid.NewGuid().ToString("N")));
        internal string Target => Path.Combine(Root, "issued.xlsx");
        internal PluginSurfaceSessionManager Surfaces { get; } = new();
        internal PluginSurfaceSession Surface { get; }
        internal HostSessionFileBroker Broker { get; }
        internal PluginSurfaceApi Api { get; }
        internal Peer Http { get; } = new();
        internal int Picks, Leases;
        internal bool CancelPick;
        internal Action? AfterPick, AfterCommit;
        private readonly ProductAuthorityEpoch _authority = new();
        internal Fixture()
        {
            Directory.CreateDirectory(Root);
            File.WriteAllText(Target, "original");
            var context = new PluginProjectContext("local:11111111111141118111111111111111", "workspace:7", 7);
            _authority.Transition(context);
            var tasks = new HostPluginTaskRegistry(_authority);
            tasks.SetWorkspaceContext(context);
            var opened = Surfaces.Open(context.ProjectKey, "com.example.outputs", PluginPackageRevision.Create("C:/synthetic", new string('a', 64)), "ui/index.html");
            Surfaces.BindIdentity(opened.SurfaceToken, context, 1, "3.x", "1.0.0", "save", "write", "ui/index.html", installationId: "installation");
            Surfaces.TryGet(opened.SurfaceToken, out var bound);
            Surface = bound!;
            var generation = new ProductSidecarGenerationSnapshot(this, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:1/bootstrap"), new Uri("http://127.0.0.1:1/"), "X-VibeTable-Session", "synthetic"),
                new ProductSidecarIdentity("11111111-1111-4111-8111-111111111111", 7, 1, "claim"), [], action => action());
            Broker = new HostSessionFileBroker(() =>
            {
                Interlocked.Increment(ref Leases);
                return new WorkspaceRequestEpochLease(new WorkspaceWireScope { Scope = "workspace", WorkspaceId = Guid.Parse("11111111-1111-4111-8111-111111111111"), SessionEpoch = 7, OperationId = Guid.NewGuid(), Sequence = 1 },
                    CancellationToken.None, () => Interlocked.Decrement(ref Leases));
            }, action =>
            {
                action();
                if (File.Exists(Target) && new FileInfo(Target).Length == 1) { var after = AfterCommit; AfterCommit = null; after?.Invoke(); }
            });
            Api = new(Surfaces, tasks, () => generation, this, Http, files: () => Broker, fileHost: this, pickFile: pick => Task.FromResult(pick()));
        }
        internal object Options(int size) => new { contract = PluginSurfaceApi.OutputContract, suggestedName = "issued.xlsx", mediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
            sizeBytes = size, source = new { templateVersion = "template-v1", dataVersion = "business-v7", purpose = "issued-copy" } };
        internal async Task<string> Create(byte[] content)
        {
            var created = await Call("output.create", Options(content.Length));
            string outputId = created.GetProperty("result").GetProperty("outputId").GetString()!;
            for (int offset = 0; offset < content.Length; offset += 262144)
            {
                var wrote = await Call("output.write", new { outputId, offset, base64 = Convert.ToBase64String(content, offset, Math.Min(262144, content.Length - offset)) });
                Assert.IsTrue(wrote.TryGetProperty("result", out _), wrote.GetRawText());
            }
            var sealedOutput = await Call("output.seal", new { outputId });
            Assert.IsTrue(sealedOutput.TryGetProperty("result", out _), sealedOutput.GetRawText());
            return outputId;
        }
        internal async Task<JsonElement> Call(string method, object parameters)
        {
            string raw = JsonSerializer.Serialize(new { contract = PluginSurfaceApi.Contract, surfaceToken = Surface.SurfaceToken, requestId = Guid.NewGuid().ToString("N"), method, @params = parameters });
            return JsonDocument.Parse(await Api.HandleAsync(Surface.SurfaceToken, Surface.DocumentUri.AbsoluteUri, raw)).RootElement.Clone();
        }
        public string? SelectAttachmentTarget(string suggestedName) { Picks++; AfterPick?.Invoke(); return CancelPick ? null : Target; }
        public string? SelectImportSource() => throw new NotSupportedException();
        public string? SelectExportTarget(string format, string defaultName) => throw new NotSupportedException();
        public NativeAttachmentSelection SelectAttachmentSources(bool replacement) => throw new NotSupportedException();
        public string CreateAttachmentPreviewPath(string suggestedName) => throw new NotSupportedException();
        public Task PreviewAttachmentAsync(string fullPath) => throw new NotSupportedException();
        public void PostNotification(string type, object? payload) { }
        public void PostResponse(string type, string? id, object? payload) { }
        public void PostOperationFailed(string? id, string message, string? code = null, string? operation = null, string? operationId = null) { }
        public async ValueTask DisposeAsync()
        {
            Api.Dispose(); await Api.DrainAsync(); Broker.Retire(); await Broker.DrainCompletion; _authority.Dispose();
            Directory.Delete(Root, true);
        }
    }
    private sealed class Peer : HttpMessageHandler
    {
        internal readonly List<string> Methods = [];
        internal Action? OnOutputRead;
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token)
        {
            using var body = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
            string method = body.RootElement.GetProperty("method").GetString()!;
            Methods.Add(method);
            if (method == "output.read") OnOutputRead?.Invoke();
            string result = method switch
            {
                "output.open" => "{\"displayName\":\"original.xlsx\",\"mediaType\":\"application/octet-stream\",\"sizeBytes\":3,\"source\":{\"templateVersion\":\"original-template\",\"dataVersion\":\"original-business\",\"purpose\":\"issued\"}}",
                "output.read" => "{\"base64\":\"AQID\",\"eof\":true}",
                _ => "{}"
            };
            return new(HttpStatusCode.OK) { Content = new StringContent(result) };
        }
    }
}
