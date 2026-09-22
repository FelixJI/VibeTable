using System.Text;
using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostSessionFileBrokerTests
{
    [TestMethod]
    public async Task WriteCommitsAtomicallyAndRepeatedReceiptDoesNotRewriteFile()
    {
        await using var f = new Fixture();
        string target = Path.Combine(f.Root, "结果.csv");
        File.WriteAllText(target, "old");
        string grant = await f.Issue(target, true);
        string transfer = (await f.Call("openWrite", new { grantId = grant })).GetProperty("transferId").GetString()!;
        await f.Call("write", new { transferId = transfer, offset = 0, base64 = Convert.ToBase64String(Encoding.UTF8.GetBytes("new")) });
        Assert.AreEqual("old", File.ReadAllText(target));
        Assert.AreEqual(1, f.Leases);
        JsonElement receipt = await f.Call("finishWrite", new { transferId = transfer, commit = true });
        Assert.IsTrue(receipt.GetProperty("committed").GetBoolean());
        Assert.AreEqual("new", File.ReadAllText(target));
        Assert.AreEqual(0, f.Leases);
        File.WriteAllText(target, "later");
        await f.Call("finishWrite", new { transferId = transfer, commit = true });
        Assert.AreEqual("later", File.ReadAllText(target));
        Assert.AreEqual(1, Directory.GetFiles(f.Root).Length);
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("openWrite", new { grantId = grant }));
    }

    [TestMethod]
    public async Task RetiringEpochClosesWritersAndCannotCommitIntoNewWorkspace()
    {
        await using var f = new Fixture();
        string target = Path.Combine(f.Root, "export.csv");
        File.WriteAllText(target, "old");
        string grant = await f.Issue(target, true);
        string transfer = (await f.Call("openWrite", new { grantId = grant })).GetProperty("transferId").GetString()!;
        await f.Call("write", new { transferId = transfer, offset = 0, base64 = "bmV3" });
        f.Epoch.Cancel();
        await f.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(0, f.Leases);
        Assert.AreEqual("old", File.ReadAllText(target));
        Assert.AreEqual(1, Directory.GetFiles(f.Root).Length);
        await Assert.ThrowsAsync<OperationCanceledException>(() => f.Call("finishWrite", new { transferId = transfer, commit = true }));
    }

    [TestMethod]
    public async Task ImportReservationSurvivesAdmissionExpiryAndRepeatedReleaseCannotUndoConsume()
    {
        await using var f = new Fixture();
        string source = Path.Combine(f.Root, "源.csv");
        File.WriteAllText(source, "x\n1");
        string grant = await f.Issue(source, false);
        string reserved = (await f.Call("reserveImport", new { grantId = grant, token = "plan" })).GetProperty("reservationId").GetString()!;
        Assert.AreEqual(reserved, (await f.Call("reserveImport", new { grantId = grant, token = "plan" })).GetProperty("reservationId").GetString());
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("reserveImport", new { grantId = grant, token = "different" }));
        f.Clock.Now = f.Clock.Now.AddMinutes(6);
        await f.Call("settleImport", new { reservationId = reserved, outcome = "consumed" });
        await f.Call("settleImport", new { reservationId = reserved, outcome = "consumed" });
        await Assert.ThrowsAsync<IOException>(() => f.Call("settleImport", new { reservationId = reserved, outcome = "released" }));
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("openRead", new { grantId = grant }));
    }

    [TestMethod]
    public async Task RevokedImportCannotBeReleasedBackToAvailable()
    {
        await using var f = new Fixture();
        string source = Path.Combine(f.Root, "source.csv");
        File.WriteAllText(source, "x");
        string grant = await f.Issue(source, false);
        string reserved = (await f.Call("reserveImport", new { grantId = grant, token = "plan" })).GetProperty("reservationId").GetString()!;
        await f.Broker.RevokeAsync(grant);
        await Assert.ThrowsAsync<IOException>(() => f.Call("settleImport", new { reservationId = reserved, outcome = "released" }));
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("openRead", new { grantId = grant }));
    }

    [TestMethod]
    public async Task PluginGrantRequiresMatchingRunDirectionAndCompleteReadConsumesIt()
    {
        await using var f = new Fixture();
        string source = Path.Combine(f.Root, "plugin.txt");
        File.WriteAllText(source, "data");
        JsonElement descriptor = await f.Broker.IssueAsync(source, false, "run-1", CancellationToken.None);
        Assert.IsFalse(descriptor.GetRawText().Contains(f.Root, StringComparison.Ordinal));
        string grant = descriptor.GetProperty("grantId").GetString()!;
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("openRead", new { grantId = grant, runId = "run-2" }));
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("openWrite", new { grantId = grant, runId = "run-1" }));
        string transfer = (await f.Call("openRead", new { grantId = grant, runId = "run-1" })).GetProperty("transferId").GetString()!;
        Assert.AreEqual("ZGF0YQ==", (await f.Call("read", new { transferId = transfer, maxBytes = 1024 })).GetProperty("base64").GetString());
        Assert.IsTrue((await f.Call("read", new { transferId = transfer, maxBytes = 1024 })).GetProperty("eof").GetBoolean());
        await f.Call("closeRead", new { transferId = transfer });
        await Assert.ThrowsAsync<InvalidOperationException>(() => f.Call("openRead", new { grantId = grant, runId = "run-1" }));
        Assert.AreEqual(0, f.Leases);
    }

    [TestMethod]
    public async Task AbortPreservesTargetAndExpiredReceiptsDoNotExhaustSession()
    {
        await using var f = new Fixture();
        string target = Path.Combine(f.Root, "result.csv");
        File.WriteAllText(target, "old");
        for (int i = 0; i < 130; i++)
        {
            string grant = await f.Issue(target, true);
            string transfer = (await f.Call("openWrite", new { grantId = grant })).GetProperty("transferId").GetString()!;
            await f.Call("finishWrite", new { transferId = transfer, commit = false });
            f.Clock.Now = f.Clock.Now.AddMinutes(6);
        }
        Assert.AreEqual("old", File.ReadAllText(target));
        Assert.AreEqual(1, Directory.GetFiles(f.Root).Length);
        Assert.AreEqual(0, f.Leases);
    }

    [TestMethod]
    public async Task LateCommitReceiptsSurviveNewGrantAdmission()
    {
        await using var f = new Fixture();
        string target = Path.Combine(f.Root, "late.csv");
        string source = Path.Combine(f.Root, "source.csv");
        File.WriteAllText(source, "x");
        string exportGrant = await f.Issue(target, true);
        string importGrant = await f.Issue(source, false);
        string transfer = (await f.Call("openWrite", new { grantId = exportGrant })).GetProperty("transferId").GetString()!;
        string reservation = (await f.Call("reserveImport", new { grantId = importGrant, token = "plan" })).GetProperty("reservationId").GetString()!;
        f.Clock.Now = f.Clock.Now.AddMinutes(6);
        JsonElement written = await f.Call("finishWrite", new { transferId = transfer, commit = true });
        JsonElement consumed = await f.Call("settleImport", new { reservationId = reservation, outcome = "consumed" });
        await f.Issue(Path.Combine(f.Root, "next.csv"), true);
        File.WriteAllText(target, "subsequent edit");
        Assert.IsTrue(JsonElement.DeepEquals(written, await f.Call("finishWrite", new { transferId = transfer, commit = true })));
        Assert.IsTrue(JsonElement.DeepEquals(consumed, await f.Call("settleImport", new { reservationId = reservation, outcome = "consumed" })));
        Assert.AreEqual("subsequent edit", File.ReadAllText(target));
        Assert.AreEqual(0, f.Leases);
    }
    [TestMethod]
    public async Task ExpiredGrantKeepsStructuredErrorAcrossActualNativeCallback()
    {
        await using var f = new Fixture();
        string grant = await f.Issue(Path.Combine(f.Root, "private-path-marker.csv"), true);
        f.Clock.Now = f.Clock.Now.AddMinutes(6);
        var transport = new FilePeer();
        await using var client = new JsonRpcClient(transport);
        client.RegisterHostFileHandler(f.Broker);
        await transport.In.Writer.WriteAsync(JsonSerializer.SerializeToElement(new
        { jsonrpc = "2.0", id = "host-file:expired-grant", method = "host.file.describe", @params = new { grantId = grant } }));
        JsonElement reply = await transport.Out.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("host-file:expired-grant", reply.GetProperty("id").GetString());
        JsonElement error = reply.GetProperty("error");
        Assert.AreEqual(-32050, error.GetProperty("code").GetInt32());
        Assert.AreEqual("path_grant_error", error.GetProperty("data").GetProperty("kind").GetString());
        Assert.IsFalse(reply.GetRawText().Contains("private-path-marker", StringComparison.Ordinal));
        Assert.IsFalse(reply.GetRawText().Contains(f.Root, StringComparison.Ordinal));
        Assert.AreEqual(0, f.Leases);
    }

    private sealed class FilePeer : IJsonLineTransport
    {
        internal readonly Channel<JsonElement> In = Channel.CreateUnbounded<JsonElement>();
        internal readonly Channel<JsonElement> Out = Channel.CreateUnbounded<JsonElement>();
        public async Task<JsonElement?> ReadAsync(CancellationToken token)
            => await In.Reader.WaitToReadAsync(token) ? await In.Reader.ReadAsync(token) : null;
        public Task WriteAsync(string line, CancellationToken token)
            => Out.Writer.WriteAsync(JsonDocument.Parse(line).RootElement.Clone(), token).AsTask();
        public ValueTask DisposeAsync() { In.Writer.TryComplete(); return ValueTask.CompletedTask; }
    }

    private sealed class Clock : TimeProvider
    {
        internal DateTimeOffset Now = DateTimeOffset.UtcNow;
        public override DateTimeOffset GetUtcNow() => Now;
    }

    private sealed class Fixture : IAsyncDisposable
    {
        internal readonly string Root = Path.Combine(Path.GetTempPath(), "vibetable-file-test-" + Guid.NewGuid().ToString("N"));
        internal readonly CancellationTokenSource Epoch = new();
        internal readonly Clock Clock = new();
        internal readonly HostSessionFileBroker Broker;
        internal int Leases;
        internal Fixture()
        {
            Directory.CreateDirectory(Root);
            Broker = new HostSessionFileBroker(() =>
            {
                Epoch.Token.ThrowIfCancellationRequested();
                Interlocked.Increment(ref Leases);
                return new WorkspaceRequestEpochLease(new WorkspaceWireScope
                { Scope = "workspace", WorkspaceId = Guid.NewGuid(), SessionEpoch = 1, OperationId = Guid.NewGuid(), Sequence = 1 },
                    Epoch.Token, () => Interlocked.Decrement(ref Leases));
            }, action => { Epoch.Token.ThrowIfCancellationRequested(); action(); }, Clock);
        }
        internal async Task<string> Issue(string path, bool write)
            => (await Broker.IssueAsync(path, write, null, CancellationToken.None)).GetProperty("grantId").GetString()!;
        internal Task<JsonElement> Call(string action, object parameters)
            => Broker.HandleAsync(action, JsonSerializer.SerializeToElement(parameters), CancellationToken.None);
        public async ValueTask DisposeAsync()
        {
            Broker.Retire();
            await Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
            Epoch.Dispose();
            Directory.Delete(Root, recursive: true);
        }
    }
}
