namespace VibeTable.Desktop.Services;

/// <summary>Trusted native UI port. Source rows and credentials never cross WebView.</summary>
internal interface IHostSourceImportWizardSession : IDisposable
{
    Task<HostSourceImportPreview> PrepareAsync(IHostSourceImportProvider provider,
        HostSourceImportOptions options, CancellationToken token);
    string Start(HostSourceImportPreview preview);
    void DiscardPreview();
}

internal sealed class HostSourceImportWizardSession(
    Func<IHostSourceImportProvider, string> register,
    Func<string, HostSourceImportOptions, CancellationToken, Task<HostSourceImportPreview>> prepare,
    Func<HostSourceImportPreview, string> start,
    Action<string> discard,
    Action ensureCurrent) : IHostSourceImportWizardSession
{
    private string? _sessionId;
    private bool _disposed;
    private bool _preparing;
    private HostSourceImportPreview? _preview;

    public async Task<HostSourceImportPreview> PrepareAsync(IHostSourceImportProvider provider,
        HostSourceImportOptions options, CancellationToken token)
    {
        // This session is called only on the native window's dispatcher. The
        // window serializes operations and awaits them before disposal.
        if (_disposed || _preparing)
        {
            provider.Dispose();
            throw new InvalidOperationException("Source preview is unavailable.");
        }
        _preparing = true;
        string? pending = null;
        bool handedOff = false;
        try
        {
            ensureCurrent();
            token.ThrowIfCancellationRequested();
            DiscardPending();
            pending = register(provider);
            handedOff = true;
            _sessionId = pending;
            HostSourceImportPreview preview = await prepare(pending, options, token);
            ensureCurrent();
            token.ThrowIfCancellationRequested();
            if (_disposed) throw new OperationCanceledException(token);
            _preview = preview;
            return preview;
        }
        catch
        {
            if (handedOff) DiscardPending();
            else provider.Dispose();
            throw;
        }
        finally { _preparing = false; }
    }

    public string Start(HostSourceImportPreview preview)
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        ensureCurrent();
        if (_preparing || !ReferenceEquals(preview, _preview)
            || preview.ProviderSessionId != _sessionId)
            throw new InvalidOperationException("Source preview is no longer current.");
        string taskId = start(preview);
        // The registry owns the submitted provider through terminal receipt.
        _sessionId = null;
        _preview = null;
        return taskId;
    }

    private void DiscardPending()
    {
        string? previous = _sessionId;
        _sessionId = null;
        _preview = null;
        if (previous is not null) discard(previous);
    }

    public void DiscardPreview()
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        if (_preparing) throw new InvalidOperationException("Source preview is still running.");
        DiscardPending();
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        DiscardPending();
    }
}
