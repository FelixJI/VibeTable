using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// WebView2 host boundary as seen from the ViewModel. The implementation owned
/// by <c>MainWindow</c> performs the real <c>EnsureCoreWebView2Async</c>,
/// virtual-host mapping, navigation gating, and message hookup; tests inject a
/// fake so the startup state machine can be exercised without STA / WebView2.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="LoadAsync"/> represents the async work of bringing the
/// hardened WebView2 up to the point where the bundled web-grid has been
/// navigated to. A failure here (e.g. WebView2 runtime missing, renderer
/// crash) propagates as a faulted task and the ViewModel transitions to
/// <c>Faulted</c>.
/// </para>
/// </remarks>
public interface IWebViewBridge
{
    /// <summary>
    /// Brings the WebView2 host up: ensures the runtime, applies the virtual
    /// host -&gt; folder mapping for <c>https://app.vibetable.local/</c>, attaches
    /// navigation / NewWindow / ProcessFailed handlers, and navigates to the
    /// web-grid's <c>index.html</c>. Resolves once the navigation has been
    /// issued; the renderer-side <c>app.ready</c> message is delivered through
    /// the typed <c>WebMessageRouter</c>.
    /// </summary>
    Task LoadAsync(CancellationToken cancellationToken);
}
