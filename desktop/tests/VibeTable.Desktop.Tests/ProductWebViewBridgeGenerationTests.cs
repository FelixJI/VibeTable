using System.IO;
using System.Windows;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Wpf;
using VibeTable.Desktop.Services;

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
