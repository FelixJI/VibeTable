using System.Reflection;
using System.Text.Json;
using System.Windows.Threading;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class CatalogOpenLockOrderingTests
{
    [TestMethod]
    public async Task RetiredPreparationCannotCancelTheCurrentSelectionOrStartTheGateway()
    {
        var gateway = new FakeTableRpcGateway();
        var workspace = new TableWorkspaceService(gateway);
        using var selection = new CancellationTokenSource();
        FieldInfo field = typeof(TableWorkspaceService).GetField("_selectCts", BindingFlags.Instance | BindingFlags.NonPublic)!;
        field.SetValue(workspace, selection);
        await Assert.ThrowsAsync<OperationCanceledException>(() =>
            workspace.PrepareDatabaseOpenAsync("db", CancellationToken.None, _ => false));
        Assert.AreSame(selection, field.GetValue(workspace));
        Assert.IsFalse(selection.IsCancellationRequested);
        Assert.IsEmpty(gateway.OpenDatabaseCalls);
    }

    [TestMethod]
    public async Task PreparationCannotCancelASelectionInstalledAfterItsAtomicScopeStep()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] = new DatabaseOpenResult([], [], TestDisplayNames.For());
        var workspace = new TableWorkspaceService(gateway);
        using var previous = new CancellationTokenSource();
        using var replacement = new CancellationTokenSource();
        FieldInfo field = typeof(TableWorkspaceService).GetField("_selectCts", BindingFlags.Instance | BindingFlags.NonPublic)!;
        field.SetValue(workspace, previous);
        await workspace.PrepareDatabaseOpenAsync("db", CancellationToken.None, prepare =>
        {
            bool accepted = prepare();
            field.SetValue(workspace, replacement);
            return accepted;
        });
        Assert.IsTrue(previous.IsCancellationRequested);
        Assert.IsFalse(replacement.IsCancellationRequested);
        Assert.AreSame(replacement, field.GetValue(workspace));
        Assert.HasCount(1, gateway.OpenDatabaseCalls);
    }
    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task GatewayStartupDoesNotHoldSessionPublicationAgainstRuntimeCurrent(bool hostDriven)
    {
        var (sessions, leases, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            await using var factory = new ProductionWorkspaceRuntimeFactory(
                () => throw new InvalidOperationException("No runtime is started by this lock test"),
                () => new BackendLaunchOptions { Command = "unused" });
            object runtimeGate = typeof(ProductionWorkspaceRuntimeFactory).GetField("_gate",
                BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(factory)!;
            object sessionGate = typeof(WorkspaceSessionManager).GetField("_publicationGate",
                BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(sessions)!;
            var session = sessions.Current;
            var snapshot = new ProductSidecarGenerationSnapshot(factory, 1,
                new PocketBaseAdminContext(new Uri("http://127.0.0.1:8123/bootstrap"),
                    new Uri("http://127.0.0.1:8123/"), "X-VibeTable-Session", "synthetic"),
                new ProductSidecarIdentity(session.WorkspaceId!.Value.ToString("D"), session.SessionEpoch,
                    1, Guid.NewGuid().ToString("D")), [new("schema.list", "workspace")]);
            using var tasks = new HostDataIoTaskRegistry();
            using var runtimeHeld = new ManualResetEventSlim();
            using var startup = new ManualResetEventSlim();
            using var enterSession = new ManualResetEventSlim();
            using var sessionEntered = new ManualResetEventSlim();
            int cleanup = 0;
            bool gatewayHeldSession = false;
            bool cancellationHeldSession = false;
            var binding = new HostProductRpcBinding(factory, null, snapshot, ProductRpcRouteSelector.Default,
                tasks, _ => false, action =>
                {
                    if (Volatile.Read(ref cleanup) != 0) return false;
                    gatewayHeldSession = Monitor.IsEntered(sessionGate);
                    startup.Set();
                    // The real factory takes its lock before rejecting the inactive runtime.
                    return factory.TryUseHostGoBinding(null!, snapshot, action);
                });
            using var lazy = new LazyProductTableGateway(leases);
            lazy.Bind(binding);
            var workspace = new TableWorkspaceService(lazy);
            using var selection = new CancellationTokenSource();
            using var cancellation = selection.Token.Register(() => cancellationHeldSession = Monitor.IsEntered(sessionGate));
            typeof(TableWorkspaceService).GetField("_selectCts", BindingFlags.Instance | BindingFlags.NonPublic)!
                .SetValue(workspace, selection);
            using var authority = new ProductAuthorityEpoch();
            using var opens = new PluginProjectContextBindingRegistry(authority);
            PluginProjectContext context = PluginProjectContext.FromSession(session)!;
            authority.Transition(context);
            opens.SetAfterAuthorityTransition(context);
            var sink = new FakeWebReplySink();
            var renderer = new WorkspaceTableRequestController(workspace, new FakeDatabasePicker("db"), sink,
                () => null, pluginBindings: opens, sessionEnvelopeFilter: leases);
            using var host = new ProductWorkspaceController(sink, factory, sessions, new FakeDatabasePicker("db"),
                workspace, () => true, () => false, () => true, _ => { }, "test",
                retryDelay: _ => TimeSpan.Zero, guards: () => true, pluginContext: () => context,
                workspaceExpected: () => true, authority: authority, databaseOpens: opens, sessionEnvelopeFilter: leases);
            Exception? openError = null;
            Exception? currentError = null;
            var current = new Thread(() =>
            {
                try
                {
                    // This is the runtime -> session part of the existing realtime Current chain.
                    lock (runtimeGate)
                    {
                        runtimeHeld.Set();
                        if (!enterSession.Wait(TimeSpan.FromSeconds(5))) throw new TimeoutException("startup not observed");
                        sessions.TryUseCurrentSession(session.WorkspaceId!.Value, session.SessionEpoch,
                            () => { sessionEntered.Set(); return true; });
                    }
                }
                catch (Exception error) { currentError = error; }
            }) { IsBackground = true };
            var open = new Thread(() =>
            {
                Dispatcher dispatcher = Dispatcher.CurrentDispatcher;
                dispatcher.BeginInvoke(new Action(async () =>
                {
                    try
                    {
                        if (hostDriven) await host.SuperviseOpenAsync();
                        else await renderer.DispatchAsync(new RoutedWebRequest("database.openRequested", null,
                            JsonSerializer.SerializeToElement(new { openId = "lock-order-open" }), ""));
                    }
                    catch (Exception error) { openError = error; }
                    finally { dispatcher.BeginInvokeShutdown(DispatcherPriority.Send); }
                }));
                Dispatcher.Run();
            }) { IsBackground = true };
            open.SetApartmentState(ApartmentState.STA);
            current.Start();
            bool openStarted = false;
            try
            {
                Assert.IsTrue(runtimeHeld.Wait(TimeSpan.FromSeconds(5)));
                open.Start();
                openStarted = true;
                Assert.IsTrue(startup.Wait(TimeSpan.FromSeconds(5)), "the real Lazy/PocketBase/Host invoker startup must be exercised");
                enterSession.Set();
                Assert.IsTrue(sessionEntered.Wait(TimeSpan.FromSeconds(2)),
                    "runtime Current and open startup must not wait for each other's session/runtime lock");
                Assert.IsTrue(open.Join(TimeSpan.FromSeconds(5)));
                Assert.IsFalse(gatewayHeldSession);
                Assert.IsTrue(selection.IsCancellationRequested);
                Assert.IsFalse(cancellationHeldSession, "CTS.Cancel executes callbacks and also belongs outside the publication gate");
                Assert.IsNull(openError);
                Assert.IsNull(currentError);
                Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "database.opened"));
            }
            finally
            {
                Volatile.Write(ref cleanup, 1);
                enterSession.Set();
                // Only the controlled old-code lock cycle needs interruption; never leave its threads behind.
                if (openStarted && !open.Join(TimeSpan.FromMilliseconds(100))) open.Interrupt();
                Assert.IsTrue(!openStarted || open.Join(TimeSpan.FromSeconds(5)), "open thread cleanup failed");
                Assert.IsTrue(current.Join(TimeSpan.FromSeconds(5)), "Current thread cleanup failed");
            }
        }
        finally
        {
            await sessions.DisposeAsync();
            leases.Dispose();
            Directory.Delete(root, recursive: true);
        }
    }
}