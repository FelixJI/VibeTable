namespace VibeTable.Desktop.Services;

/// <summary>
/// Keeps renderer terminal transport outside database-open ownership. A broken
/// WebView sink is observable, but cannot invalidate the newly admitted open
/// or interrupt an authority transition that already retired the old one.
/// </summary>
internal sealed class DatabaseOpenTerminalPublisher(
    IWebReplySink reply,
    Action<string>? trace = null)
{
    public void PostRetiredCancellations(
        IEnumerable<string> openIds,
        string reason)
    {
        foreach (string openId in openIds)
        {
            try
            {
                reply.PostNotification(
                    "database.openCancelled",
                    new { openId, reason });
            }
            catch (Exception exception)
            {
                try
                {
                    trace?.Invoke(
                        "Database open cancellation terminal failed; " +
                        $"exception={exception.GetType().Name}");
                }
                catch
                {
                    // Diagnostics are subordinate to the already-linearized
                    // ownership transition as well.
                }
            }
        }
    }
}
