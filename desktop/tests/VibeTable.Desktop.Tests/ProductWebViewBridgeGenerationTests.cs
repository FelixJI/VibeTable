using System.IO;
using System.Windows;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Wpf;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// #482 renderer-generation regression: replacing the control or sealing for
/// recovery retires the generation, and a notification queued by the OLD
/// generation is dropped instead of being posted to a newer renderer.
/// </summary>
[TestClass]
public sealed class ProductWebViewBridgeGenerationTests
{
    private sealed class StaBridge : IDisposable
    {
        private readonly Thread _thread;
        private readonly TaskCompletionSource _ready = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _stopped = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private ProductWebViewBridge? _bridge;
        private Window? _window;
        private WebView2? _control;
        public readonly List<Action> Queued = [];
        public readonly List<bool> Commits = [];
        private readonly object _captureGate = new();

        public StaBridge()
        {
            _thread = new Thread(() =>
            {
                try
                {
                    _window = new Window();
                    _control = new WebView2();
                    _bridge = new ProductWebViewBridge(
                        _window,
                        _control,
                        new WebMessageRouter(_ => { }),
                        new PluginWebViewResourceHost(new PluginResourceHost(), new PluginSurfaceSessionManager()),
                        readiness: null,
                        processFailed: _ => { });
                    _bridge.UiEmission = action => { lock (_captureGate) Queued.Add(action); };
                    _bridge.EmissionCommitted += posted => { lock (_captureGate) Commits.Add(posted); };
                    _ready.TrySetResult();
                    Dispatcher.Run();
                }
                finally
                {
                    _stopped.TrySetResult();
                }
            });
            _thread.SetApartmentState(ApartmentState.STA);
            _thread.IsBackground = true;
            _thread.Start();
            if (!_ready.Task.Wait(TimeSpan.FromSeconds(10)))
            {
                throw new TimeoutException("STA bridge construction timed out.");
            }
        }

        public ProductWebViewBridge Bridge => _bridge!;
        public WebView2 Control => _control!;

        public WebView2 CreateReplacementControl()
        {
            return _window!.Dispatcher.Invoke(() => new WebView2())
                ?? throw new InvalidOperationException("replacement control creation failed");
        }

        public void PumpQueued()
        {
            Action[] pending;
            lock (_captureGate)
            {
                pending = Queued.ToArray();
                Queued.Clear();
            }
            foreach (Action action in pending)
            {
                action();
            }
        }

        public void Dispose()
        {
            _window?.Dispatcher.InvokeShutdown();
            if (!_stopped.Task.Wait(TimeSpan.FromSeconds(10)))
            {
                throw new TimeoutException("STA bridge shutdown timed out.");
            }
            _window = null;
            _control = null;
            _bridge = null;
        }
    }

    [TestMethod]
    public void ControlReplacementAndRecoverySealRetireTheRendererGeneration()
    {
        using var harness = new StaBridge();
        long before = harness.Bridge.RendererGeneration;
        Assert.AreEqual(0, before);

        WebView2 replacement = harness.CreateReplacementControl();
        harness.Bridge.AttachControl(replacement);
        Assert.AreEqual(1, harness.Bridge.RendererGeneration);

        harness.Bridge.SealForRendererRecovery();
        Assert.AreEqual(2, harness.Bridge.RendererGeneration);
    }

    [TestMethod]
    public async Task NavigatedRecoveryTimeoutRequiresFullRetryToReloadAndAwaitBusinessReadiness()
    {
        using var harness = new StaBridge();
        var cache = typeof(ProductWebViewBridge).GetField("_loadTask",
            System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!;
        cache.SetValue(harness.Bridge, Task.CompletedTask);
        var backend = new FakeBackendLifecycle();
        var business = new TaskCompletionSource();
        var freshNavigation = new TaskCompletionSource();
        int recoveries = 0;
        MainWindowViewModel vm = null!;
        vm = new MainWindowViewModel(backend, harness.Bridge, recoverRenderer: async reason =>
        {
            recoveries++;
            Assert.IsTrue(vm.TryBeginRendererRecovery(rendererFailure: reason.StartsWith("renderer:")));
            harness.Bridge.SealForRendererRecovery();
            harness.Bridge.AttachControl(harness.CreateReplacementControl());
            // Substitute only navigation completion at the existing bridge cache seam.
            cache.SetValue(harness.Bridge, recoveries == 1 ? Task.CompletedTask : freshNavigation.Task);
            await harness.Bridge.LoadAsync(CancellationToken.None);
            await business.Task;
            vm.CompleteRendererRecovery();
        });
        await vm.StartAsync(); Assert.AreEqual(StartupState.Ready, vm.State);
        vm.RecoverRendererCommand.Execute(null);
        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        Assert.IsTrue(((Task)cache.GetValue(harness.Bridge)!).IsCompletedSuccessfully);
        business.SetException(new TimeoutException("business app.ready was not received"));
        Assert.AreEqual(StartupState.Faulted, vm.State);
        Assert.IsTrue(vm.IsHostFallbackVisible);
        business = new TaskCompletionSource();
        long retiredGeneration = harness.Bridge.RendererGeneration;
        await vm.StartAsync();
        Assert.AreEqual(0, backend.StopCalls);
        Assert.AreEqual(2, recoveries, "full retry must re-enter the real renderer reset/reload boundary");
        Assert.IsTrue(harness.Bridge.RendererGeneration > retiredGeneration);
        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        freshNavigation.SetResult();
        Assert.AreEqual(StartupState.LoadingWeb, vm.State, "navigation success is not business app.ready");
        Assert.IsTrue(vm.IsHostFallbackVisible);
        business.SetResult();
        Assert.AreEqual(StartupState.Ready, vm.State);
        Assert.IsFalse(vm.IsRendererRecovering);
    }

    [TestMethod]
    public void QueuedNotificationFromARetiredGenerationIsDropped()
    {
        using var harness = new StaBridge();
        // Queued while generation 0 is current (no core attached yet — the
        // commit result must still be observable).
        harness.Bridge.PostNotification("host.startupStateChanged", new { phase = "starting" });
        harness.Bridge.SealForRendererRecovery();

        // Pump AFTER the seal: the queued emission belongs to the retired
        // generation and must not reach the new renderer.
        harness.PumpQueued();
        CollectionAssert.AreEqual(new bool[] { false }, harness.Commits.ToArray(),
            "a retired generation's queued notification must be dropped, not posted");
    }
}
