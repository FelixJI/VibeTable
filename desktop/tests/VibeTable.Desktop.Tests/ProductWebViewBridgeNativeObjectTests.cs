using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Windows;
using System.Windows.Threading;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using Microsoft.Web.WebView2.Wpf;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductWebViewBridgeNativeObjectTests
{
    [TestMethod]
    public void InspectNativeFileIngress_ConvertsAdditionalObjectsFailureToStableError()
    {
        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "file.uploadRequested",
                () => throw new InvalidOperationException("WebView2 getter failed"),
                _ => throw new AssertFailedException("path reader must not run"));

        Assert.AreEqual("NATIVE_OBJECTS_UNAVAILABLE", result.ErrorCode);
        Assert.AreEqual(
            "Native file objects could not be read by the desktop host.",
            result.ErrorMessage);
        Assert.IsNull(result.Paths);
    }

    [TestMethod]
    public void InspectNativeFileIngress_RejectsInvalidAdditionalObjectType()
    {
        object invalid = new();

        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "document.externalDropRequested",
                () => new[] { invalid },
                _ => null);

        Assert.AreEqual("INVALID_NATIVE_OBJECT", result.ErrorCode);
        Assert.AreEqual(1, result.ObjectCount);
        Assert.IsNull(result.Paths);
    }

    [TestMethod]
    public void InspectNativeFileIngress_RejectsNativeObjectsForUnapprovedRequestType()
    {
        bool readerCalled = false;

        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "dashboard.listRequested",
                () =>
                {
                    readerCalled = true;
                    return Array.Empty<object>();
                },
                _ => null);

        Assert.AreEqual("NATIVE_OBJECTS_NOT_ALLOWED", result.ErrorCode);
        Assert.IsFalse(readerCalled);
    }

    [TestMethod]
    public void InspectNativeFileIngress_ReturnsValidatedPaths()
    {
        object first = new();
        object second = new();
        var paths = new Dictionary<object, string>
        {
            [first] = @"C:\safe\one.txt",
            [second] = @"C:\safe\two.txt",
        };

        NativeFileIngressInspection result =
            ProductWebViewBridge.InspectNativeFileIngress(
                "file.replaceRequested",
                () => new[] { first, second },
                value => paths[value]);

        Assert.IsNull(result.ErrorCode);
        CollectionAssert.AreEqual(
            new[] { @"C:\safe\one.txt", @"C:\safe\two.txt" },
            result.Paths!.ToArray());
    }

    [TestMethod]
    public async Task GuardedResponseQueuedBeforeRetirementIsNotPostedWhenPumped()
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            var (bridge, pump, dispose) = CreateBridgeWithControllableEmissions();
            try
            {
                var gate = PluginRequestDispatcher.CaptureSharedReadEmitGate(
                    manager,
                    filter,
                    PluginProjectContext.FromSession(manager.Current)!);
                int gateRuns = 0;
                int commits = 0;
                Func<Func<bool>, bool> observed = post =>
                {
                    gateRuns++;
                    return gate(() =>
                    {
                        commits++;
                        return post();
                    });
                };

                Assert.IsTrue(bridge.TryPostResponse(
                    "plugin.catalog.list", "catalog-queued", null, observed));

                // A regular close retires the admitting session before the UI
                // queue pumps; the queued terminal must not reach the wire.
                await manager.CloseAsync("retire-before-pump");
                pump();

                Assert.AreEqual(1, gateRuns);
                Assert.AreEqual(0, commits);
            }
            finally
            {
                dispose();
            }
        }
        finally
        {
            await manager.DisposeAsync();
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task GuardedFailureQueuedBeforeRetirementIsNotPostedWhenPumped()
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            var (bridge, pump, dispose) = CreateBridgeWithControllableEmissions();
            try
            {
                var gate = PluginRequestDispatcher.CaptureSharedReadEmitGate(
                    manager,
                    filter,
                    PluginProjectContext.FromSession(manager.Current)!);
                int gateRuns = 0;
                int commits = 0;
                Func<Func<bool>, bool> observed = post =>
                {
                    gateRuns++;
                    return gate(() =>
                    {
                        commits++;
                        return post();
                    });
                };

                Assert.IsTrue(bridge.TryPostOperationFailed(
                    "catalog-queued-failure",
                    "Plugin operation failed.",
                    "PLUGIN_OPERATION_FAILED",
                    emitGate: observed));

                await manager.CloseAsync("retire-before-pump");
                pump();

                Assert.AreEqual(1, gateRuns);
                Assert.AreEqual(0, commits);
            }
            finally
            {
                dispose();
            }
        }
        finally
        {
            await manager.DisposeAsync();
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task GuardedEmissionCommitsWhileTheAdmittingSessionStaysCurrent()
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests.OpenRetirementSessionAsync();
        try
        {
            var (bridge, pump, dispose) = CreateBridgeWithControllableEmissions();
            try
            {
                var gate = PluginRequestDispatcher.CaptureSharedReadEmitGate(
                    manager,
                    filter,
                    PluginProjectContext.FromSession(manager.Current)!);
                int commits = 0;
                Func<Func<bool>, bool> observed = post =>
                    gate(() =>
                    {
                        commits++;
                        return post();
                    });

                Assert.IsTrue(bridge.TryPostResponse(
                    "plugin.catalog.list", "catalog-current-1", null, observed));
                // A second still-current emission keeps committing: same-context
                // republications never rotate the gate's (workspace, epoch).
                Assert.IsTrue(bridge.TryPostResponse(
                    "plugin.catalog.list", "catalog-current-2", null, observed));
                pump();

                Assert.AreEqual(2, commits);
            }
            finally
            {
                dispose();
            }
        }
        finally
        {
            await manager.DisposeAsync();
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task GuardedTerminalDoesNotReviveAfterProtectionFailureRollback()
    {
        var (manager, filter, root) = await PluginRequestDispatcherTests
            .OpenRetirementSessionAsync(new PluginRequestDispatcherTests.FailingProtectionHook());
        try
        {
            PluginProjectContext context = PluginProjectContext.FromSession(manager.Current)!;
            var (bridge, pump, dispose) = CreateBridgeWithControllableEmissions();
            try
            {
                // Admission-time sticky capture, mirroring the production
                // MainWindow wiring (the round-two current-session-only gate
                // was the failing pre-fix shape recorded in red-dotnet.txt).
                var gate = PluginRequestDispatcher.CaptureSharedReadEmitGate(manager, filter, context);
                int commits = 0;
                Func<Func<bool>, bool> observed = post =>
                    gate(() =>
                    {
                        commits++;
                        return post();
                    });

                Assert.IsTrue(bridge.TryPostResponse(
                    "plugin.catalog.list", "catalog-rollback", null, observed));
                Assert.IsTrue(bridge.TryPostOperationFailed(
                    "catalog-rollback-failure",
                    "Plugin operation failed.",
                    "PLUGIN_OPERATION_FAILED",
                    emitGate: observed));

                // Protection failure rolls the close back to the SAME
                // UUID/epoch Idle session. The close must not wait for the
                // queued terminals (the observer is disposed at capture).
                await Assert.ThrowsExactlyAsync<InvalidOperationException>(
                    () => manager.CloseAsync("protection-failure"));
                Assert.AreEqual(
                    WorkspaceSessionPhase.Idle,
                    manager.Current.Phase);
                Assert.AreEqual(
                    context.SessionGeneration,
                    PluginProjectContext.FromSession(manager.Current)!.SessionGeneration);

                pump();

                Assert.AreEqual(
                    0,
                    commits,
                    "A rolled-back same-epoch session must not revive old queued terminals.");

                // A read admitted after the rollback captures a fresh token
                // and keeps committing normally.
                var rolledGate = PluginRequestDispatcher.CaptureSharedReadEmitGate(
                    manager,
                    filter,
                    PluginProjectContext.FromSession(manager.Current)!);
                int rolledCommits = 0;
                Func<Func<bool>, bool> rolled = post => rolledGate(() =>
                {
                    rolledCommits++;
                    return post();
                });
                Assert.IsTrue(bridge.TryPostResponse(
                    "plugin.catalog.list", "catalog-after-rollback", null, rolled));
                pump();
                Assert.AreEqual(1, rolledCommits);
            }
            finally
            {
                dispose();
            }
        }
        finally
        {
            await manager.DisposeAsync();
            Directory.Delete(root, recursive: true);
        }
    }

    /// <summary>
    /// Constructs the real WPF bridge on an STA thread whose dispatcher keeps
    /// running, swaps the UI emission queue for a captured list, and returns a
    /// pump that runs the queued emission callbacks on that owning thread —
    /// exactly where the production dispatcher would execute them — plus a
    /// disposal that shuts the dispatcher down and joins the thread. The STA
    /// thread itself recycles the Window/WebView when its loop exits, so
    /// construction failures and timeouts cannot leak the thread; signals use
    /// TaskCompletionSource so a caller that gave up never races disposal, and
    /// a persistent stop flag ends even a late-starting worker.
    /// </summary>
    private static (ProductWebViewBridge Bridge, Action Pump, Action Dispose)
        CreateBridgeWithControllableEmissions()
    {
        ProductWebViewBridge? bridge = null;
        Dispatcher? dispatcher = null;
        Window? constructedWindow = null;
        WebView2? constructedWebView = null;
        Exception? construction = null;
        Exception? teardown = null;
        int stopRequested = 0;
        var queue = new System.Collections.Concurrent.ConcurrentQueue<Action>();
        var started = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var thread = new Thread(() =>
        {
            void RecycleControls()
            {
                Exception? windowFailure = null;
                if (constructedWindow is not null)
                {
                    try { constructedWindow.Close(); }
                    catch (Exception ex) { windowFailure = ex; }
                }
                Exception? webViewFailure = null;
                if (constructedWebView is not null)
                {
                    try { ((IDisposable?)constructedWebView)?.Dispose(); }
                    catch (Exception ex) { webViewFailure = ex; }
                }
                if (windowFailure is not null || webViewFailure is not null)
                {
                    teardown = windowFailure is not null && webViewFailure is not null
                        ? new AggregateException(windowFailure, webViewFailure)
                        : windowFailure ?? webViewFailure;
                }
            }

            Exception? localFailure = null;
            try
            {
                constructedWindow = new Window();
                constructedWebView = new WebView2();
                ProductWebViewBridge constructed = new ProductWebViewBridge(
                    constructedWindow,
                    constructedWebView,
                    new WebMessageRouter(_ => { }),
                    new PluginWebViewResourceHost(
                        new PluginResourceHost(),
                        new PluginSurfaceSessionManager()),
                    readiness: null,
                    processFailed: _ => { });
                constructed.UiEmission = queue.Enqueue;
                dispatcher = constructedWindow.Dispatcher;
                bridge = constructed;
            }
            catch (Exception ex)
            {
                // Never let the exception escape the thread: it is rethrown on
                // the caller below so the original failure stays visible.
                localFailure = ex;
            }
            finally
            {
                construction = localFailure;
                // Late completion is safe: no disposable signal is involved.
                started.TrySetResult();
            }
            if (localFailure is not null)
            {
                RecycleControls();
                return;
            }
            try
            {
                // Persistent stop, checked BEFORE entering the loop: a caller
                // that timed out while construction was still running never
                // pumps, and a worker reaching this point after that must not
                // enter Run at all.
                if (Volatile.Read(ref stopRequested) == 0)
                {
                    Dispatcher.Run();
                }
            }
            finally
            {
                RecycleControls();
            }
        })
        {
            IsBackground = true,
            Name = "vibetable-bridge-emission-tests",
        };
        thread.SetApartmentState(ApartmentState.STA);
        thread.Start();
        if (!started.Task.Wait(TimeSpan.FromSeconds(10)))
        {
            // Construction never signaled. Persist the stop request (the
            // worker checks it after constructing), best-effort shutdown of a
            // dispatcher that already exists, bounded join, explicit failure.
            Interlocked.CompareExchange(ref stopRequested, 1, 0);
            Dispatcher? stalled = dispatcher ?? Dispatcher.FromThread(thread);
            stalled?.BeginInvokeShutdown(System.Windows.Threading.DispatcherPriority.Background);
            if (!thread.Join(TimeSpan.FromSeconds(10)))
            {
                Assert.Fail(
                    "STA bridge construction timed out and the background thread never exited.");
            }
            Assert.Fail("STA bridge construction timed out.");
        }
        if (construction is not null)
        {
            // The thread skipped its dispatcher loop; join deterministically,
            // then propagate the original construction failure.
            Assert.IsTrue(
                thread.Join(TimeSpan.FromSeconds(10)),
                "Failed STA construction thread did not exit.");
            System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(construction).Throw();
        }
        Dispatcher owner = dispatcher!;
        Action pump = () =>
        {
            while (queue.TryDequeue(out Action? emit))
            {
                Exception? failure = null;
                var done = new TaskCompletionSource(
                    TaskCreationOptions.RunContinuationsAsynchronously);
                owner.BeginInvoke(() =>
                {
                    try { emit(); }
                    catch (Exception ex) { failure = ex; }
                    finally { done.TrySetResult(); }
                });
                if (!done.Task.Wait(TimeSpan.FromSeconds(10)))
                {
                    Assert.Fail("UI pump timed out.");
                }
                if (failure is not null)
                {
                    System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(failure).Throw();
                }
            }
        };
        Action dispose = () =>
        {
            owner.BeginInvokeShutdown(System.Windows.Threading.DispatcherPriority.Background);
            if (!thread.Join(TimeSpan.FromSeconds(10)))
            {
                Assert.Fail("UI emission thread did not shut down within the budget.");
            }
            if (teardown is not null)
            {
                // Normal-path control teardown failures stay visible instead
                // of being swallowed; a construction failure already
                // propagated above remains primary for its own path.
                System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(teardown).Throw();
            }
        };
        return (bridge!, pump, dispose);
    }
}
