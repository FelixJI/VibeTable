using System.Runtime.InteropServices;
using System.Windows.Threading;

namespace VibeTable.Desktop.Services;

/// <summary>Clipboard outcomes kept strictly apart: empty text is a success, never a failure state.</summary>
internal enum TextClipboardOutcome { Succeeded, Unavailable, Busy, Failed, TimedOut }

internal sealed record TextClipboardResult(TextClipboardOutcome Outcome, string Text = "");

/// <summary>STA-dispatched Unicode text clipboard access bounded by the caller's token.</summary>
internal interface ITextClipboard
{
    Task<TextClipboardResult> ReadTextAsync(CancellationToken token);
    Task<TextClipboardResult> WriteTextAsync(string text, CancellationToken token);
}

/// <summary>Raw seam over the WPF clipboard so tests never touch the real OS clipboard.</summary>
internal interface IWindowsTextClipboardApi
{
    bool ContainsText();
    string GetText();
    void SetText(string text);
}

internal sealed class WpfTextClipboardApi : IWindowsTextClipboardApi
{
    public bool ContainsText() => System.Windows.Clipboard.ContainsText();
    public string GetText() => System.Windows.Clipboard.GetText();
    public void SetText(string text) => System.Windows.Clipboard.SetText(text);
}

/// <summary>
/// Marshals text clipboard access onto the WPF STA dispatcher. Cancellation
/// is enforced before any clipboard access (queued ops abort, the delegate
/// re-checks the token on entry); an op that already ran reports its real
/// result. Only the verified CLIPBRD_E_CANT_OPEN HRESULT maps to Busy; no
/// logs of clipboard contents, no retry (deadline owned by the controller).
/// </summary>
internal sealed class WindowsTextClipboard : ITextClipboard
{
    // CLIPBRD_E_CANT_OPEN from winerror.h: OpenClipboard failed because
    // another process holds the clipboard.
    private const int ClipbrdECantOpen = unchecked((int)0x800401D0);

    private readonly Dispatcher _dispatcher;
    private readonly IWindowsTextClipboardApi _api;

    public WindowsTextClipboard() : this(
        System.Windows.Application.Current?.Dispatcher
            ?? throw new InvalidOperationException("WindowsTextClipboard requires the WPF UI dispatcher."),
        new WpfTextClipboardApi())
    {
    }

    internal WindowsTextClipboard(Dispatcher dispatcher, IWindowsTextClipboardApi api)
    {
        _dispatcher = dispatcher ?? throw new ArgumentNullException(nameof(dispatcher));
        _api = api ?? throw new ArgumentNullException(nameof(api));
    }

    public Task<TextClipboardResult> ReadTextAsync(CancellationToken token)
        => RunAsync(_ => _api.ContainsText() ? _api.GetText() : "", token);

    public Task<TextClipboardResult> WriteTextAsync(string text, CancellationToken token)
        => RunAsync(_ => { _api.SetText(text); return ""; }, token);

    private async Task<TextClipboardResult> RunAsync(
        Func<CancellationToken, string> operation, CancellationToken token)
    {
        if (_dispatcher.HasShutdownStarted || _dispatcher.HasShutdownFinished)
        {
            return new TextClipboardResult(TextClipboardOutcome.Unavailable);
        }
        Task<string> onDispatcher;
        try
        {
            // The token is honoured at two points before any clipboard access:
            // InvokeAsync aborts the still-queued operation, and the delegate
            // entry check covers the just-dequeued race. Once past the check
            // the delegate runs synchronously to its real result.
            onDispatcher = _dispatcher.InvokeAsync(
                () =>
                {
                    token.ThrowIfCancellationRequested();
                    return operation(token);
                },
                DispatcherPriority.Send,
                token).Task;
        }
        catch (InvalidOperationException)
        {
            return new TextClipboardResult(TextClipboardOutcome.Unavailable);
        }
        try
        {
            string text = await onDispatcher.ConfigureAwait(false);
            return new TextClipboardResult(TextClipboardOutcome.Succeeded, text);
        }
        catch (Exception exception) when (
            exception is OperationCanceledException or COMException
                or InvalidOperationException or OutOfMemoryException)
        {
            return new TextClipboardResult(Classify(exception));
        }
    }

    private static TextClipboardOutcome Classify(Exception exception) => exception switch
    {
        COMException com => com.HResult == ClipbrdECantOpen
            ? TextClipboardOutcome.Busy
            : TextClipboardOutcome.Failed,
        OperationCanceledException => TextClipboardOutcome.Unavailable,
        _ => TextClipboardOutcome.Failed,
    };
}
