using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class HostProductRpcInvoker
{
    // Future provider adapters call these internal ports. Neither method is
    // registered in Product RPC or accepts renderer-provided source data.
    internal string RegisterSourceImportProvider(IHostSourceImportProvider provider)
    {
        lock (_gate) ObjectDisposedException.ThrowIf(_disposed, this);
        string? id = null;
        if (!_tryUseGoCurrent(() =>
            { id = _taskOwner.RegisterSourceImportProvider(_snapshot, _leases, provider, _handler); return true; }))
            throw Unavailable();
        return id!;
    }

    internal Task<HostSourceImportPreview> PrepareSourceImportAsync(string providerSessionId,
        HostSourceImportOptions options, CancellationToken token)
    {
        lock (_gate) ObjectDisposedException.ThrowIf(_disposed, this);
        return StartCurrent(() => _taskOwner.PrepareSourceImportAsync(providerSessionId, options, token), go: true);
    }

    private async Task<JsonElement> ReadSourceImportHistoryAsync(CancellationToken token)
    {
        if (!_snapshot.TryUseCurrent(() => true)) throw Unavailable();
        JsonElement files = await _taskOwner.ReadImportHistoryAsync(token).ConfigureAwait(false);
        HostSourceImportResult[] migrations = await StartCurrent(
            () => _sidecar.ReadSourceImportHistoryAsync(token), go: true).ConfigureAwait(false);
        if (!_snapshot.TryUseCurrent(() => true)) throw Unavailable();
        return JsonSerializer.SerializeToElement(new
        {
            items = files.GetProperty("items"),
            migrations = migrations.Select(result => result.Wire).ToArray(),
        }, WireOptions);
    }
}
