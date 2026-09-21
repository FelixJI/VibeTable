using System;
using System.Collections.Concurrent;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Rpc;

/// <summary>A closed native file service bound to this Python process.</summary>
public interface IHostFileRequestHandler
{
    Task<JsonElement> HandleAsync(string action, JsonElement parameters, CancellationToken token);
    void Retire();
}

public sealed partial class JsonRpcClient
{
    private readonly object _hostFileGate = new();
    private readonly ConcurrentDictionary<string, Task> _hostFileCalls = new();
    private readonly CancellationTokenSource _hostFileLifetime = new();
    private IHostFileRequestHandler? _hostFileHandler;

    public void RegisterHostFileHandler(IHostFileRequestHandler handler)
    {
        ArgumentNullException.ThrowIfNull(handler);
        lock (_hostFileGate)
        {
            ThrowIfDisposed();
            if (Volatile.Read(ref _readerDead) != 0 || _hostFileHandler is not null)
                throw new InvalidOperationException("Host file binding is already attached or unavailable.");
            _hostFileHandler = handler;
        }
    }

    public void UnregisterHostFileHandler(IHostFileRequestHandler handler)
    {
        lock (_hostFileGate)
        {
            if (!ReferenceEquals(_hostFileHandler, handler)) return;
            _hostFileHandler = null;
        }
        handler.Retire();
    }

    private void RouteHostFileRequest(JsonElement frame, JsonElement idElement, JsonElement methodElement)
    {
        if (idElement.ValueKind != JsonValueKind.String
            || idElement.GetString() is not { Length: > 10 and <= 64 } id
            || !id.StartsWith("host-file:", StringComparison.Ordinal)
            || methodElement.ValueKind != JsonValueKind.String
            || !frame.TryGetProperty("params", out JsonElement parameters)
            || parameters.ValueKind != JsonValueKind.Object)
            throw new HostFileChannelException();
        string method = methodElement.GetString()!;
        string? action = method switch
        {
            "host.file.describe" => "describe", "host.file.openRead" => "openRead",
            "host.file.read" => "read", "host.file.closeRead" => "closeRead",
            "host.file.reserveImport" => "reserveImport", "host.file.settleImport" => "settleImport",
            "host.file.openWrite" => "openWrite", "host.file.write" => "write",
            "host.file.finishWrite" => "finishWrite", _ => null,
        };
        lock (_hostFileGate)
        {
            if (_hostFileLifetime.IsCancellationRequested || Volatile.Read(ref _disposed) != 0
                || _hostFileCalls.Count >= 32 || _hostFileCalls.ContainsKey(id))
                throw new HostFileChannelException();
            IHostFileRequestHandler? handler = _hostFileHandler;
            JsonElement captured = parameters.Clone();
            Task call = Task.Run(() => ReplyHostFileAsync(id, action, captured, handler));
            _hostFileCalls[id] = call;
            _ = call.ContinueWith(_ => _hostFileCalls.TryRemove(id, out Task? removed),
                CancellationToken.None, TaskContinuationOptions.ExecuteSynchronously, TaskScheduler.Default);
        }
    }

    private async Task ReplyHostFileAsync(
        string id, string? action, JsonElement parameters, IHostFileRequestHandler? handler)
    {
        object response;
        try
        {
            if (action is null || handler is null) throw new InvalidOperationException("Host file operation unavailable.");
            JsonElement result = await handler.HandleAsync(action, parameters, _hostFileLifetime.Token)
                .ConfigureAwait(false);
            response = new { jsonrpc = "2.0", id, result };
        }
        catch (Exception)
        {
            // Only a fixed public error crosses stdout, never paths or exception messages.
            response = new { jsonrpc = "2.0", id,
                error = new { code = -32098, message = "Host file operation rejected" } };
        }
        try
        {
            await _transport.WriteAsync(JsonSerializer.Serialize(response, JsonOptions), _hostFileLifetime.Token)
                .ConfigureAwait(false);
        }
        catch (Exception)
        {
            MarkReaderDead();
            FailAllPending(new BackendUnavailableException("Host file response channel failed."));
        }
    }

    private void RetireHostFiles()
    {
        IHostFileRequestHandler? handler;
        lock (_hostFileGate)
        {
            handler = _hostFileHandler;
            _hostFileHandler = null;
        }
        _hostFileLifetime.Cancel();
        handler?.Retire();
    }

    private async Task DrainHostFilesAsync()
    {
        Task[] calls;
        lock (_hostFileGate) calls = _hostFileCalls.Values.ToArray();
        await Task.WhenAll(calls).ConfigureAwait(false);
    }

    private sealed class HostFileChannelException : Exception;
}