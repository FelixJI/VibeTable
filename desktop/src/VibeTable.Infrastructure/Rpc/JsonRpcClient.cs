using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;

namespace VibeTable.Infrastructure.Rpc;

/// <summary>
/// Correlated JSON-RPC 2.0 client over an injected <see cref="IJsonLineTransport"/>.
/// </summary>
/// <remarks>
/// <para>
/// A single reader task, started once at construction, drains the transport
/// and routes each inbound JSON object:
/// </para>
/// <list type="bullet">
/// <item>Responses carrying an <c>id</c> resolve the matching pending call
/// in <see cref="_pending"/>. Unknown ids are silently discarded so the loop
/// never crashes on a stray response from a cancelled or superseded request.</item>
/// <item>Objects with no <c>id</c> field are treated as notifications and
/// surface through <see cref="NotificationReceived"/>.</item>
/// <item>A clean EOF on the transport fails every outstanding call with
/// <see cref="BackendUnavailableException"/> so callers can distinguish
/// "backend gone" from a per-request rejection.</item>
/// </list>
/// <para>
/// All continuations fire with <see cref="TaskCreationOptions.RunContinuationsAsynchronously"/>
/// so a synchronous handler on the reader thread can never deadlock an
/// <see cref="InvokeAsync{TParams,TResult}"/> awaiter.
/// </para>
/// </remarks>
public sealed class JsonRpcClient : IAsyncDisposable
{
    private static readonly JsonSerializerOptions JsonOptions =
        new(JsonSerializerDefaults.Web);

    private readonly IJsonLineTransport _transport;
    private readonly ConcurrentDictionary<string, TaskCompletionSource<JsonElement>> _pending = new();
    private readonly Task _readerTask;
    private int _nextId;
    private int _disposed;
    private int _readerDead;

    public JsonRpcClient(IJsonLineTransport transport)
    {
        _transport = transport ?? throw new ArgumentNullException(nameof(transport));
        // Reader starts immediately so notifications that arrive before any
        // InvokeAsync call still fire NotificationReceived.
        _readerTask = Task.Run(ReaderLoopAsync);
    }

    /// <summary>
    /// Raised on the reader thread for every inbound JSON-RPC object that
    /// carries no <c>id</c> field. Handlers receive the method name and the
    /// <c>params</c> element (or <see cref="JsonValueKind.Undefined"/> when
    /// the notification omits <c>params</c>).
    /// </summary>
    public event Action<string, JsonElement>? NotificationReceived;

    public async Task<TResult> InvokeAsync<TParams, TResult>(
        string method,
        TParams parameters,
        CancellationToken token)
        where TParams : notnull
    {
        ThrowIfDisposed();
        if (string.IsNullOrEmpty(method))
        {
            throw new ArgumentException("method must be non-empty.", nameof(method));
        }
        if (parameters is null)
        {
            throw new ArgumentNullException(nameof(parameters));
        }

        token.ThrowIfCancellationRequested();

        // If the reader loop has already terminated (EOF / transport failure)
        // nothing will ever resolve a newly-created pending entry. Fail fast
        // instead of registering a TCS that would hang forever on await.
        if (Volatile.Read(ref _readerDead) != 0)
        {
            throw new BackendUnavailableException(
                "JSON-RPC reader has terminated; backend is unavailable.");
        }

        var id = AllocateId();
        var tcs = new TaskCompletionSource<JsonElement>(TaskCreationOptions.RunContinuationsAsynchronously);
        if (!_pending.TryAdd(id, tcs))
        {
            // Monotonic ids make this branch unreachable in practice.
            throw new InvalidOperationException($"Duplicate RPC id '{id}'.");
        }

        CancellationTokenRegistration registration = default;
        try
        {
            if (token.CanBeCanceled)
            {
                registration = token.Register(() =>
                {
                    if (_pending.TryRemove(id, out var cancelled))
                    {
                        cancelled.TrySetCanceled(token);
                    }
                });
            }

            var request = new RpcRequest<TParams>(
                Jsonrpc: "2.0",
                Id: id,
                Method: method,
                Params: parameters);
            var line = JsonSerializer.Serialize(request, JsonOptions);
            await _transport.WriteAsync(line, token).ConfigureAwait(false);

            // The reader loop resolves the TCS with the result element on
            // success, or faults it with RpcRemoteException on a backend
            // error. EOF / transport failure surfaces as
            // BackendUnavailableException.
            JsonElement resultElement = await tcs.Task.ConfigureAwait(false);

            return JsonSerializer.Deserialize<TResult>(resultElement.GetRawText(), JsonOptions)
                ?? throw new RpcException("RPC result deserialized to null.");
        }
        finally
        {
            registration.Dispose();
            // Best-effort cleanup: the reader may already have removed it.
            _pending.TryRemove(id, out _);
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return;
        }

        // Fail anything still outstanding so awaiters never hang.
        FailAllPending(new BackendUnavailableException(
            "JSON-RPC client was disposed while calls were still pending."));

        try
        {
            await _transport.DisposeAsync().ConfigureAwait(false);
        }
        catch
        {
            // Dispose must not throw; transport shutdown errors are not
            // actionable from here.
        }

        // Give the reader a chance to observe the closed transport and exit.
        try
        {
            await _readerTask.ConfigureAwait(false);
        }
        catch
        {
            // Reader exceptions are surfaced through individual calls; the
            // reader task itself is not awaited by callers.
        }
    }

    private async Task ReaderLoopAsync()
    {
        while (true)
        {
            JsonElement? frame;
            try
            {
                frame = await _transport.ReadAsync(CancellationToken.None).ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                // Transport errors are fatal: every pending call gets a
                // BackendUnavailableException so the caller knows the
                // backend stream is gone.
                FailAllPending(new BackendUnavailableException(
                    "JSON-RPC transport failed while reading.", ex));
                MarkReaderDead();
                return;
            }

            if (frame is null)
            {
                // Clean EOF — backend closed the stream deliberately.
                FailAllPending(new BackendUnavailableException(
                    "JSON-RPC backend closed the stream (clean EOF)."));
                MarkReaderDead();
                return;
            }

            try
            {
                RouteFrame(frame.Value);
            }
            catch
            {
                // A single bad frame must not kill the reader loop or strand
                // the rest of the pending calls. (System.Text.Json errors and
                // handler exceptions both land here.)
            }
        }
    }

    private void RouteFrame(JsonElement frame)
    {
        if (!frame.TryGetProperty("id", out var idElement))
        {
            // Notification: must have a "method".
            if (frame.TryGetProperty("method", out var methodElement)
                && methodElement.ValueKind == JsonValueKind.String)
            {
                var method = methodElement.GetString() ?? string.Empty;
                var parameters = frame.TryGetProperty("params", out var paramsElement)
                    ? paramsElement.Clone()
                    : default(JsonElement);
                NotificationReceived?.Invoke(method, parameters);
            }
            return;
        }

        // Response: correlate by id.
        string id = idElement.ValueKind == JsonValueKind.String
            ? idElement.GetString() ?? string.Empty
            : idElement.GetRawText();

        if (string.IsNullOrEmpty(id) || !_pending.TryRemove(id, out var pending))
        {
            // Unknown id (perhaps a response for an already-cancelled call).
            // Discard silently so the reader keeps going.
            return;
        }

        if (frame.TryGetProperty("error", out var errorElement)
            && errorElement.ValueKind == JsonValueKind.Object)
        {
            int code = errorElement.TryGetProperty("code", out var codeElement)
                && codeElement.ValueKind == JsonValueKind.Number
                    ? codeElement.GetInt32()
                    : 0;
            string message = errorElement.TryGetProperty("message", out var messageElement)
                && messageElement.ValueKind == JsonValueKind.String
                    ? messageElement.GetString() ?? "Unknown RPC error."
                    : "Unknown RPC error.";
            JsonElement? data = errorElement.TryGetProperty("data", out var dataElement)
                && (dataElement.ValueKind == JsonValueKind.Object
                    || dataElement.ValueKind == JsonValueKind.Array
                    || dataElement.ValueKind == JsonValueKind.String
                    || dataElement.ValueKind == JsonValueKind.Number
                    || dataElement.ValueKind == JsonValueKind.True
                    || dataElement.ValueKind == JsonValueKind.False)
                    ? dataElement.Clone()
                    : (JsonElement?)null;

            pending.TrySetException(new RpcRemoteException(code, message, data));
            return;
        }

        if (frame.TryGetProperty("result", out var resultElement))
        {
            pending.TrySetResult(resultElement.Clone());
            return;
        }

        // Malformed response — neither result nor error.
        pending.TrySetException(new RpcException(
            "JSON-RPC response carried neither 'result' nor 'error'."));
    }

    private void FailAllPending(BackendUnavailableException exception)
    {
        // Snapshot keys first to avoid mutating the dictionary while we iterate.
        var keys = new List<string>(_pending.Keys);
        foreach (var key in keys)
        {
            if (_pending.TryRemove(key, out var pending))
            {
                pending.TrySetException(exception);
            }
        }
    }

    private string AllocateId()
        => Interlocked.Increment(ref _nextId).ToString();

    private void MarkReaderDead()
    {
        // Publish BEFORE returning from the reader loop so a concurrent
        // InvokeAsync on another thread observes the terminal state and
        // fails fast instead of registering a TCS that can never resolve.
        Volatile.Write(ref _readerDead, 1);
    }

    private void ThrowIfDisposed()
    {
        if (Volatile.Read(ref _disposed) != 0)
        {
            throw new ObjectDisposedException(nameof(JsonRpcClient));
        }
    }
}
