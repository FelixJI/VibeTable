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
/// <c>table.datasetReady</c> as the initial authoritative-window signal.
/// </remarks>
public interface IWebReplySink
{
    /// <summary>
    /// Posts a typed notification (no requestId — these are fire-and-forget
    /// host events) to the WebView.
    /// </summary>
    void PostNotification(string type, object? payload);

    /// <summary>Posts a typed result correlated to a renderer request.</summary>
    void PostResponse(string type, string? requestId, object? payload);

    /// <summary>
    /// Posts an <c>operation.failed</c> reply correlated to the inbound
    /// <paramref name="requestId"/> (or uncorrelated when null).
    /// </summary>
    void PostOperationFailed(
        string? requestId,
        string message,
        string? code = null,
        string? operation = null,
        string? operationId = null);

    /// <summary>
    /// Guarded correlated reply. The transport must invoke
    /// <paramref name="emitGate"/> with a synchronous commit callback at the
    /// actual emission moment — not merely when the reply is enqueued — so the
    /// scope check and the real post commit atomically against whatever the
    /// gate serializes on (e.g. workspace session publication). A null gate
    /// emits unconditionally. Synchronous sinks inherit the default commit
    /// below; async transports MUST override so the guard rides the UI queue
    /// callback that performs the real post.
    /// </summary>
    bool TryPostResponse(
        string type,
        string? requestId,
        object? payload,
        Func<Func<bool>, bool>? emitGate = null)
    {
        if (emitGate is null)
        {
            PostResponse(type, requestId, payload);
            return true;
        }
        return emitGate(() =>
        {
            PostResponse(type, requestId, payload);
            return true;
        });
    }

    /// <summary>
    /// Guarded <c>operation.failed</c> reply with the same emission-gate
    /// contract as <see cref="TryPostResponse"/>. Async transports MUST
    /// override; the default commits synchronously like the legacy
    /// <see cref="PostOperationFailed"/>.
    /// </summary>
    bool TryPostOperationFailed(
        string? requestId,
        string message,
        string? code = null,
        string? operation = null,
        string? operationId = null,
        Func<Func<bool>, bool>? emitGate = null)
    {
        if (emitGate is null)
        {
            PostOperationFailed(requestId, message, code, operation, operationId);
            return true;
        }
        return emitGate(() =>
        {
            PostOperationFailed(requestId, message, code, operation, operationId);
            return true;
        });
    }
}
