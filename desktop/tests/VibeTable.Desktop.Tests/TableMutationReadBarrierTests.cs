using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class TableMutationReadBarrierTests
{
    [TestMethod]
    public async Task ReadCancellationAndTableRetirementDoNotCancelOrForgetTheSentWrite()
    {
        using var barrier = new TableMutationReadBarrier();
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        Task write = barrier.TrackAsync(Request("a"), async settled => { settled(false); await release.Task; settled(true); });
        using var cancellation = new CancellationTokenSource();
        Task first = barrier.WaitAsync(Request("a"), cancellation.Token); cancellation.Cancel();
        await Assert.ThrowsAsync<OperationCanceledException>(() => first);
        Task second = barrier.WaitAsync(Request("a"), CancellationToken.None); barrier.RetireReads();
        await Assert.ThrowsAsync<OperationCanceledException>(() => second);
        await barrier.WaitAsync(Request("b"), CancellationToken.None);
        Task third = barrier.WaitAsync(Request("a"), CancellationToken.None); Assert.IsFalse(third.IsCompleted);
        release.SetResult(); await write; await third;
    }

    [TestMethod]
    public async Task AnUnprovenTerminalStaysFencedUntilTheAuthorityScopeRetires()
    {
        using var barrier = new TableMutationReadBarrier();
        await barrier.TrackAsync(Request("a"), settled => { settled(false); return Task.CompletedTask; });
        var failure = await Assert.ThrowsAsync<InvalidOperationException>(() => barrier.WaitAsync(Request("a"), CancellationToken.None));
        StringAssert.Contains(failure.Message, "重新打开工作区");
        barrier.RetireReads();
        await Assert.ThrowsAsync<InvalidOperationException>(() => barrier.WaitAsync(Request("a"), CancellationToken.None));
        await barrier.WaitAsync(Request("a", epoch: 2), CancellationToken.None);
        barrier.RetireReads(retireAuthority: true);
        await barrier.WaitAsync(Request("a"), CancellationToken.None);
    }

    [TestMethod]
    public async Task ACompletedHostFailureCannotMakeTheRecoveryQueryReadOldData()
    {
        var gateway = new FakeTableRpcGateway { NextUpdateCellException = new BackendUnavailableException("apply transport ended without a terminal") };
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(new TableWorkspaceService(gateway), new FakeDatabasePicker(null), sink,
            new GridStateCoordinator(gateway, _ => { }));
        await dispatcher.DispatchAsyncForTesting(Request("a") with {
            Type = "table.updateCellRequested", RequestId = "uncertain",
            Payload = JsonSerializer.SerializeToElement(new { table = "a", rowKey = "r", column = "name", oldValue = "before", newValue = "after", schemaRevision = "sr" }),
        });
        await dispatcher.DispatchAsyncForTesting(Request("a") with { Type = "table.queryRequested", RequestId = "recovery" });
        Assert.HasCount(0, gateway.RawViewQueries);
        var failure = sink.Replies.Single(reply => reply.RequestId == "recovery");
        Assert.AreEqual("operation.failed", failure.Type);
        using var payload = JsonDocument.Parse(JsonSerializer.Serialize(failure.Payload));
        StringAssert.Contains(payload.RootElement.GetProperty("message").GetString()!, "重新打开工作区");
        Assert.AreEqual("WRITE_TERMINAL_UNCONFIRMED", payload.RootElement.GetProperty("code").GetString());
    }

    [TestMethod]
    public async Task PendingPasteCannotClaimAWriteTerminalForTheRecoveryRead()
    {
        var gateway = new FakeTableRpcGateway { NextApplyPasteResult = new ApplyPasteResult("a", "pending", [], [], [], [], "apply") };
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(new TableWorkspaceService(gateway), new FakeDatabasePicker(null), sink,
            new GridStateCoordinator(gateway, _ => { }));
        await dispatcher.DispatchAsyncForTesting(Request("a") with {
            Type = "table.applyPasteRequested", RequestId = "paste",
            Payload = JsonSerializer.SerializeToElement(new { collection = "a", token = "token", idempotencyKey = "logical-write" }),
        });
        await dispatcher.DispatchAsyncForTesting(Request("a") with { RequestId = "query-after-pending" });
        Assert.HasCount(0, gateway.RawViewQueries);
        Assert.AreEqual("operation.failed", sink.Replies.Single(reply => reply.RequestId == "query-after-pending").Type);
    }

    [TestMethod]
    public async Task TheRealAuthorityTransitionHookPreservesUnknownOnResumeAndRetiresItForANewEpoch()
    {
        var gateway = new FakeTableRpcGateway { NextUpdateCellException = new BackendUnavailableException("unknown apply") };
        var sink = new FakeWebReplySink();
        var time = new ManualTimeProvider();
        var context = new PluginProjectContext("local:a", "a:7", 7);
        using var dispatcher = new WorkspaceRequestDispatcher(new TableWorkspaceService(gateway), new FakeDatabasePicker(null), sink,
            new GridStateCoordinator(gateway, _ => { }, time), pluginContext: () => context);
        await dispatcher.DispatchAsyncForTesting(Request("a") with {
            Type = "table.updateCellRequested", RequestId = "uncertain",
            Payload = JsonSerializer.SerializeToElement(new { table = "a", rowKey = "r", column = "name", oldValue = "before", newValue = "after", schemaRevision = "sr" }),
        });
        dispatcher.RetireDatabaseOpensAfterAuthorityTransition(null);
        dispatcher.RetireDatabaseOpensAfterAuthorityTransition(context);
        await dispatcher.DispatchAsyncForTesting(Request("a") with { RequestId = "same-epoch" });
        Assert.AreEqual("operation.failed", sink.Replies.Single(reply => reply.RequestId == "same-epoch").Type);
        dispatcher.RetireDatabaseOpensAfterAuthorityTransition(context with { SessionGeneration = 8, ProjectRevision = "a:8" });
        gateway.CursorOpenResults["a"] = new TablePage("a", [], [], 0, 100, 0, "remote");
        Task recovery = dispatcher.DispatchAsyncForTesting(Request("a") with { RequestId = "new-epoch" });
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs));
        await recovery.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual("table.pageLoaded", sink.Replies.Single(reply => reply.RequestId == "new-epoch").Type);
    }

    [TestMethod]
    public async Task AnEmptyDeleteThatNeverSubmitsCannotPoisonRecoveryReads()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.CursorOpenResults["a"] = new TablePage("a", [], [], 0, 100, 0, "remote");
        var sink = new FakeWebReplySink(); var time = new ManualTimeProvider();
        using var dispatcher = new WorkspaceRequestDispatcher(new TableWorkspaceService(gateway), new FakeDatabasePicker(null), sink,
            new GridStateCoordinator(gateway, _ => { }, time));
        await dispatcher.DispatchAsyncForTesting(Request("a") with {
            Type = "table.deleteRowsRequested", RequestId = "empty-delete",
            Payload = JsonSerializer.SerializeToElement(new { table = "a", rows = Array.Empty<object>(), schemaRevision = "sr" }),
        });
        Assert.HasCount(0, gateway.DeleteRowsCalls);
        Task query = dispatcher.DispatchAsyncForTesting(Request("a"));
        time.Advance(TimeSpan.FromMilliseconds(GridStateCoordinator.QueryDebounceMs)); await query;
        Assert.AreEqual("table.pageLoaded", sink.Replies.Single().Type);
    }

    private static RoutedWebRequest Request(string table, ulong epoch = 1) => new("table.queryRequested", "read",
        JsonSerializer.SerializeToElement(new { table, query = new { } }), "",
        new WorkspaceWireScope { Scope = "workspace", WorkspaceId = Guid.Parse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), SessionEpoch = epoch, OperationId = Guid.NewGuid(), Sequence = 1 });
}
