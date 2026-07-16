using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Sink for host -&gt; web notifications posted by the workspace flow. MainWindow's
/// WebViewBridge implements this to serialize and post typed notifications to
/// the WebView2 renderer.
/// </summary>
/// <remarks>
/// Phase A defines three notification types: <c>database.opened</c>,
/// <c>table.pageLoaded</c>, and the framework <c>operation.failed</c> (built by
/// <see cref="WebMessageRouter.BuildOperationFailed"/>). Task 10 adds
/// <c>table.datasetReady</c> as a client-mode completion signal.
/// </remarks>
public interface IWebReplySink
{
    /// <summary>
    /// Posts a typed notification (no requestId — these are fire-and-forget
    /// host events) to the WebView.
    /// </summary>
    void PostNotification(string type, object? payload);

    /// <summary>
    /// Posts an <c>operation.failed</c> reply correlated to the inbound
    /// <paramref name="requestId"/> (or uncorrelated when null).
    /// </summary>
    void PostOperationFailed(string? requestId, string message, string? code = null);
}
