using System.Text.Json;

namespace VibeTable.Desktop.Services;

internal sealed partial class HostProductRpcInvoker
{
    private HostSessionFileBroker? _files;
    private HostSessionFileBroker Files => _files ?? throw new InvalidOperationException("Host file binding is not enabled.");

    internal HostSessionFileBroker EnableHostFiles()
    {
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (_files is not null) return _files;
            var broker = new HostSessionFileBroker(CaptureLease, CommitCurrent);
            try { _client.RegisterHostFileHandler(broker); }
            catch { broker.Retire(); throw; }
            return _files = broker;
        }
    }

    private void CommitCurrent(Action action)
    {
        if (!_tryUseCurrent(() => { action(); return true; })) throw Unavailable();
    }

    private static bool IsNativeFileMethod(string method) => method is
        "path.registerImportSource" or "path.registerExportTarget" or "path.resolveGrant"
        or "path.requestImportSource" or "path.requestExportTarget" or "path.revokeExportTarget"
        or "file.applyHostChange" or "file.saveHostFile";

    private async Task<JsonElement> InvokeNativeFileAsync(string method, JsonElement parameters, CancellationToken token)
    {
        switch (method)
        {
            case "path.registerImportSource":
            case "path.registerExportTarget":
                string path = parameters.GetProperty("path").GetString() ?? throw new JsonException("Selected path is required.");
                string? mime = parameters.TryGetProperty("mimeType", out JsonElement media) && media.ValueKind == JsonValueKind.String
                    ? media.GetString() : null;
                return await Files.IssueAsync(path, method == "path.registerExportTarget", null, token, mime).ConfigureAwait(false);
            case "path.resolveGrant":
                return await Files.DescribeAsync(parameters.GetProperty("grantId").GetString()!, token).ConfigureAwait(false);
            case "path.revokeExportTarget":
                string id = parameters.GetProperty("grantId").GetString()!;
                await Files.RevokeAsync(id).ConfigureAwait(false);
                return await _client.InvokeAsync<JsonElement, JsonElement>("task.settleExport", parameters, token).ConfigureAwait(false);
            case "file.applyHostChange":
                return await _sidecar.ApplyHostFileChangeAsync(parameters, token).ConfigureAwait(false);
            case "file.saveHostFile":
                return await _sidecar.SaveHostFileAsync(parameters, CommitCurrent, token).ConfigureAwait(false);
            default:
                throw new InvalidOperationException("File selection requires the native picker.");
        }
    }
}
