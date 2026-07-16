namespace VibeTable.Desktop;

/// <summary>
/// Coarse-grained UI startup state surfaced by <see cref="ViewModels.MainWindowViewModel"/>.
/// </summary>
/// <remarks>
/// <para>
/// Legal transitions (verbatim from the Task 9 brief):
/// </para>
/// <list type="bullet">
/// <item><see cref="StartingBackend"/> -&gt; <see cref="LoadingWeb"/> -&gt;
/// <see cref="Ready"/></item>
/// <item><see cref="StartingBackend"/> -&gt; <see cref="Faulted"/></item>
/// <item><see cref="LoadingWeb"/> -&gt; <see cref="Faulted"/></item>
/// <item><see cref="Ready"/> -&gt; <see cref="Faulted"/></item>
/// <item><see cref="Faulted"/> -&gt; <see cref="StartingBackend"/> (explicit
/// retry only, via <c>RetryCommand</c>)</item>
/// </list>
/// </remarks>
public enum StartupState
{
    /// <summary>
    /// The supervised backend is starting (spawn + handshake in progress).
    /// </summary>
    StartingBackend,

    /// <summary>
    /// The backend is ready; the hardened WebView2 is being brought up and
    /// the web-grid navigation has been issued.
    /// </summary>
    LoadingWeb,

    /// <summary>
    /// Both backend and WebView are ready; the grid is visible and the
    /// message router is accepting inbound requests.
    /// </summary>
    Ready,

    /// <summary>
    /// An irrecoverable failure occurred during startup or while running.
    /// The only legal next step is an explicit retry
    /// (<c>RetryCommand</c>).
    /// </summary>
    Faulted,
}
