using System.Runtime.InteropServices;
using System.Windows.Threading;

namespace VibeTable.Desktop.Services;

/// <summary>Clipboard outcomes kept strictly apart: empty text is a success, never a failure state.</summary>
internal enum TextClipboardOutcome { Succeeded, Unavailable, Busy, Failed, TimedOut }

internal sealed record TextClipboardResult(
    TextClipboardOutcome Outcome, string Text = "", bool WriteMayHaveCompleted = false);

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
/// Runs OLE on one process-wide background STA, never the UI dispatcher.
/// Cancellation ends the wait, not an OLE call that has already started;
/// the occupied slot stays busy until that call really ends. No queued
/// retries or replacement threads, and no late result after cancellation.
/// </summary>
internal sealed class WindowsTextClipboard : ITextClipboard
{
    private const int ClipbrdECantOpen = unchecked((int)0x800401D0);
    private static readonly Lazy<StaBoundary> SharedSta = new(StaBoundary.Create);
    private readonly StaBoundary _sta;
    private readonly IWindowsTextClipboardApi _api;

    public WindowsTextClipboard() : this(new WpfTextClipboardApi()) { }

    internal WindowsTextClipboard(IWindowsTextClipboardApi api) : this(SharedSta.Value, api) { }

    // The injected dispatcher must be a dedicated STA, independent of the UI.
    internal WindowsTextClipboard(Dispatcher dispatcher, IWindowsTextClipboardApi api)
        : this(new StaBoundary(Task.FromResult(dispatcher ?? throw new ArgumentNullException(nameof(dispatcher)))), api) { }

    private WindowsTextClipboard(StaBoundary sta, IWindowsTextClipboardApi api)
    {
        _sta = sta;
        _api = api ?? throw new ArgumentNullException(nameof(api));
    }

    public Task<TextClipboardResult> ReadTextAsync(CancellationToken token)
        => RunAsync(() => _api.ContainsText() ? _api.GetText() : "", false, token);

    public Task<TextClipboardResult> WriteTextAsync(string text, CancellationToken token)
        => RunAsync(() => { _api.SetText(text); return ""; }, true, token);

    private async Task<TextClipboardResult> RunAsync(
        Func<string> operation, bool isWrite, CancellationToken token)
    {
        if (token.IsCancellationRequested)
            return new TextClipboardResult(TextClipboardOutcome.Unavailable);
        if (!_sta.TryAcquire())
            return new TextClipboardResult(TextClipboardOutcome.Busy);

        var completion = new TaskCompletionSource<TextClipboardResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        object gate = new();
        bool started = false;
        using var cancellation = token.Register(() =>
        {
            lock (gate)
                completion.TrySetResult(new TextClipboardResult(
                    TextClipboardOutcome.Unavailable, WriteMayHaveCompleted: isWrite && started));
        });
        _ = ExecuteOnStaAsync();
        return await completion.Task.ConfigureAwait(false);

        async Task ExecuteOnStaAsync()
        {
            TextClipboardResult result;
            try
            {
                Dispatcher dispatcher = await _sta.DispatcherTask.ConfigureAwait(false);
                if (dispatcher.HasShutdownStarted || dispatcher.HasShutdownFinished)
                {
                    result = new TextClipboardResult(TextClipboardOutcome.Unavailable);
                }
                else
                {
                    string text = await dispatcher.InvokeAsync(() =>
                    {
                        lock (gate)
                        {
                            // Cancellation and entry commit together: a cancelled queued write cannot run.
                            if (completion.Task.IsCompleted)
                                throw new OperationCanceledException(token);
                            started = true;
                        }
                        return operation();
                    }, DispatcherPriority.Send).Task.ConfigureAwait(false);
                    result = new TextClipboardResult(TextClipboardOutcome.Succeeded, text);
                }
            }
            catch (Exception exception) when (
                exception is OperationCanceledException or COMException
                    or InvalidOperationException or OutOfMemoryException)
            {
                result = new TextClipboardResult(Classify(exception));
            }
            catch (Exception exception)
            {
                _sta.Release();
                completion.TrySetException(exception);
                return;
            }
            _sta.Release();
            completion.TrySetResult(result);
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

    private sealed class StaBoundary(Task<Dispatcher> dispatcherTask)
    {
        private int _occupied;
        public Task<Dispatcher> DispatcherTask { get; } = dispatcherTask;
        public bool TryAcquire() => Interlocked.CompareExchange(ref _occupied, 1, 0) == 0;
        public void Release() => Volatile.Write(ref _occupied, 0);

        public static StaBoundary Create()
        {
            var ready = new TaskCompletionSource<Dispatcher>(TaskCreationOptions.RunContinuationsAsynchronously);
            var thread = new Thread(() =>
            {
                ready.SetResult(Dispatcher.CurrentDispatcher);
                Dispatcher.Run();
            }) { IsBackground = true, Name = "VibeTable clipboard STA" };
            thread.SetApartmentState(ApartmentState.STA);
            thread.Start();
            return new StaBoundary(ready.Task);
        }
    }
}
