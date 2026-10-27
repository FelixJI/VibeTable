using System.Reflection;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductTableCompositionTests
{
    [TestMethod]
    public async Task RealSubgraphConstructsWithoutWindowOrRuntimeStartAndForwardsOneNotification()
    {
        await using var fixture = new Fixture();
        using var owner = fixture.Create();
        Assert.AreEqual(0, fixture.SidecarResolutions);
        Assert.AreEqual(0, fixture.BackendResolutions);
        Assert.AreEqual(1, Subscriptions(owner.Workspace, "Notification"));
        Assert.AreEqual(1, Subscriptions(owner.TableGateway, "BindingChanged"));
        Assert.AreEqual(1, Subscriptions(fixture.Runtime, "ProductSidecarCurrentChanged"));
        Assert.IsFalse(await owner.Workspace.UpdateCellAsync("orders", 1, "name", "a", "b", "schema", requestId: "edit-1"));
        FakeWebReplySink.Reply reply = fixture.Reply.Replies.Single();
        Assert.AreEqual("table.editRejected", reply.Type);
        Assert.AreEqual("edit-1", reply.RequestId);
        Assert.AreEqual(0, fixture.BackendResolutions);
    }

    [TestMethod]
    public async Task StageDisposalPreservesLaterStagesAndBorrowedOwners()
    {
        await using var fixture = new Fixture();
        using var owner = fixture.Create();
        owner.DisposeSidecarGateway();
        owner.DisposeSidecarGateway();
        Assert.AreEqual(0, Subscriptions(fixture.Runtime, "ProductSidecarCurrentChanged"));
        Assert.AreEqual(1, Subscriptions(owner.Workspace, "Notification"));
        owner.Dispatcher.RetireDatabaseOpensAfterAuthorityTransition(null);
        await Assert.ThrowsExactlyAsync<BackendUnavailableException>(() => owner.TableGateway.ListTablesAsync(CancellationToken.None));

        owner.DisposeRequests();
        owner.DisposeRequests();
        Assert.ThrowsExactly<ObjectDisposedException>(() => owner.Dispatcher.RetireDatabaseOpensAfterAuthorityTransition(null));
        Assert.AreEqual(1, Subscriptions(owner.Workspace, "Notification"));
        await owner.Workspace.UpdateCellAsync("orders", 1, "name", "a", "b", "schema");
        Assert.HasCount(1, fixture.Reply.Replies);

        owner.DisposeWorkspace();
        owner.DisposeWorkspace();
        owner.Dispose();
        Assert.AreEqual(0, Subscriptions(owner.Workspace, "Notification"));
        Assert.AreEqual(0, Subscriptions(owner.TableGateway, "BindingChanged"));
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() => owner.TableGateway.ListTablesAsync(CancellationToken.None));
        await owner.Workspace.UpdateCellAsync("orders", 1, "name", "b", "c", "schema");
        Assert.HasCount(1, fixture.Reply.Replies, "Retained workspace references cannot forward after their owner releases them.");
        AssertBorrowedOwnersAlive(fixture);
    }

    [TestMethod]
    [DataRow("none")]
    [DataRow("sidecar")]
    [DataRow("requests")]
    [DataRow("workspace")]
    public async Task DisposeCompletesEveryRemainingStageIdempotently(string completedStage)
    {
        await using var fixture = new Fixture();
        var owner = fixture.Create();
        if (completedStage == "sidecar") owner.DisposeSidecarGateway();
        if (completedStage == "requests") owner.DisposeRequests();
        if (completedStage == "workspace") owner.DisposeWorkspace();
        owner.Dispose();
        owner.Dispose();
        Assert.AreEqual(0, Subscriptions(fixture.Runtime, "ProductSidecarCurrentChanged"));
        Assert.AreEqual(0, Subscriptions(owner.Workspace, "Notification"));
        Assert.AreEqual(0, Subscriptions(owner.TableGateway, "BindingChanged"));
        Assert.ThrowsExactly<ObjectDisposedException>(() => owner.Dispatcher.RetireDatabaseOpensAfterAuthorityTransition(null));
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() => owner.TableGateway.ListTablesAsync(CancellationToken.None));
        AssertBorrowedOwnersAlive(fixture);
    }

    [TestMethod]
    public async Task RegistrationConflictCleansNewSubscriptionAndPreservesTheOriginalOwner()
    {
        await using var fixture = new Fixture();
        using var original = new ExistingLifecycle();
        Func<CancellationToken, Task> originalBinding = _ => Task.CompletedTask;
        fixture.Runtime.RegisterProductSidecarGatewayLifecycle(original, originalBinding);
        Assert.ThrowsExactly<InvalidOperationException>(() => fixture.Create());
        Assert.AreEqual(0, Subscriptions(fixture.Runtime, "ProductSidecarCurrentChanged"),
            "The new lifecycle subscribed before the final registration rejected it; failure must unsubscribe it.");
        Assert.AreSame(original, Field<IProductSidecarGatewayLifecycle>(fixture.Runtime, "_productSidecarGatewayLifecycle"));
        Assert.AreSame(originalBinding, Field<Func<CancellationToken, Task>>(fixture.Runtime, "_bindProductGateways"));
        Assert.AreEqual(0, original.DisposeCalls);
        Assert.AreEqual(0, fixture.SidecarResolutions);
        Assert.AreEqual(0, fixture.BackendResolutions);
        AssertBorrowedOwnersAlive(fixture);
    }

    [TestMethod]
    public async Task WorkspaceStageCancelsCoordinatorAndDisposesItsTimerAndTokenSource()
    {
        await using var fixture = new Fixture();
        using var owner = fixture.Create();
        var coordinator = Field<GridStateCoordinator>(owner, "_coordinator");
        using JsonDocument query = JsonDocument.Parse("{}");
        Task<TablePage> waiting = coordinator.RequestQueryAsync("orders", query.RootElement, CancellationToken.None);
        var source = Field<CancellationTokenSource>(coordinator, "_queryCts");
        CancellationToken token = source.Token;
        var timer = Field<ITimer>(coordinator, "_queryDebounce");
        Assert.IsTrue(timer.Change(Timeout.InfiniteTimeSpan, Timeout.InfiniteTimeSpan));
        owner.DisposeSidecarGateway();
        owner.DisposeRequests();
        Assert.IsFalse(token.IsCancellationRequested, "The workspace/coordinator must survive the earlier shutdown stages.");
        owner.DisposeWorkspace();
        Assert.IsTrue(token.IsCancellationRequested);
        Assert.IsFalse(timer.Change(Timeout.InfiniteTimeSpan, Timeout.InfiniteTimeSpan));
        Assert.ThrowsExactly<ObjectDisposedException>(() => { _ = source.Token; });
        await Assert.ThrowsAsync<OperationCanceledException>(() => waiting.WaitAsync(TimeSpan.FromSeconds(2)));
        Assert.IsEmpty(fixture.Reply.Replies);
    }

    [TestMethod]
    public async Task ResetCallbackFailureStillReleasesWorkspaceAndGateway()
    {
        await using var fixture = new Fixture();
        using var owner = fixture.Create();
        var coordinator = Field<GridStateCoordinator>(owner, "_coordinator");
        coordinator.SelectionSnapshotChanged += _ => throw new InvalidOperationException("isolated selection callback");
        Assert.ThrowsExactly<InvalidOperationException>(() => owner.DisposeWorkspace());
        Assert.AreEqual(0, Subscriptions(owner.Workspace, "Notification"));
        Assert.AreEqual(0, Subscriptions(owner.TableGateway, "BindingChanged"));
        Assert.AreEqual(1, Field<int>(owner.ProductWorkspace, "_disposed"));
        await Assert.ThrowsExactlyAsync<ObjectDisposedException>(() => owner.TableGateway.ListTablesAsync(CancellationToken.None));
        owner.Dispose();
    }
    private static void AssertBorrowedOwnersAlive(Fixture fixture)
    {
        var context = new PluginProjectContext("local:test", "test:1", 1);
        fixture.Authority.Transition(context);
        fixture.Bindings.SetAfterAuthorityTransition(context);
        Assert.IsNotNull(fixture.Bindings.BeginOpen("still-alive-" + Guid.NewGuid().ToString("N")).Binding);
        Assert.AreEqual(1, Subscriptions(fixture.Sessions, "Changed"), "The borrowed session filter remains subscribed.");
        Assert.IsFalse(Field<bool>(fixture.Filter, "_disposed"));
        Assert.IsFalse(Field<bool>(fixture.Sessions, "_disposed"));
        Assert.IsFalse(Field<bool>(fixture.Runtime, "_disposed"));
    }

    // Subscription ownership is the resource contract; no production test-only factories are needed.
    private static int Subscriptions(object owner, string name)
        => Field<Delegate?>(owner, name)?.GetInvocationList().Length ?? 0;

    private static T Field<T>(object owner, string name)
    {
        FieldInfo? field = owner.GetType().GetField(name, BindingFlags.Instance | BindingFlags.NonPublic);
        Assert.IsNotNull(field, name);
        return (T)field.GetValue(owner)!;
    }

    private sealed class ExistingLifecycle : IProductSidecarGatewayLifecycle
    {
        public int DisposeCalls { get; private set; }
        public Task<bool> TryReplaceAsync(ProductSidecarGenerationSnapshot snapshot, CancellationToken cancellationToken)
            => Task.FromResult(true);
        public bool Clear(ProductSidecarGenerationSnapshot expectedSnapshot) => true;
        public void Dispose() => DisposeCalls++;
    }

    private sealed class Fixture : IAsyncDisposable
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(), "vibetable-table-composition-" + Guid.NewGuid().ToString("N"));
        public Fixture()
        {
            Directory.CreateDirectory(_root);
            Runtime = new ProductionWorkspaceRuntimeFactory(
                () => { SidecarResolutions++; throw new InvalidOperationException("No sidecar startup is allowed."); },
                () => { BackendResolutions++; throw new InvalidOperationException("No Python startup is allowed."); });
            Sessions = new WorkspaceSessionManager(new WorkspaceRegistry(_root), Runtime);
            Filter = new WorkspaceSessionEnvelopeFilter(Sessions);
            Bindings = new PluginProjectContextBindingRegistry(Authority);
        }
        public int SidecarResolutions { get; private set; }
        public int BackendResolutions { get; private set; }
        public ProductionWorkspaceRuntimeFactory Runtime { get; }
        public WorkspaceSessionManager Sessions { get; }
        public WorkspaceSessionEnvelopeFilter Filter { get; }
        public ProductAuthorityEpoch Authority { get; } = new();
        public PluginProjectContextBindingRegistry Bindings { get; }
        public FakeWebReplySink Reply { get; } = new();
        public ProductTableComposition Create() => new(Runtime, Sessions, Filter, Reply, new FakeDatabasePicker(null),
            Authority, Bindings, () => true, () => false, () => false, _ => { }, "test-host", _ => Task.CompletedTask);
        public async ValueTask DisposeAsync()
        {
            Bindings.Dispose();
            Authority.Dispose();
            Filter.Dispose();
            await Sessions.DisposeAsync();
            await Runtime.DisposeAsync();
            Directory.Delete(_root, recursive: true);
        }
    }
}
