using System;
using System.Collections.Concurrent;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Desktop.Services;

internal sealed record CorrelatedRequestFailure(string Message, string Code);

internal sealed record CorrelatedRequestPolicy(
    string UnavailableMessage,
    string UnavailableCode,
    string MissingRequestIdMessage,
    string InvalidRequestCode,
    string DuplicateMessage,
    string DuplicateCode,
    string CancelledMessage,
    string CancelledCode,
    string TimeoutMessage,
    string TimeoutCode,
    Func<Exception, CorrelatedRequestFailure> MapFailure,
    Action<string, string> TraceFailure);

/// <summary>
/// Runs one closed, correlated renderer request lifecycle. Product controllers
/// retain payload validation and gateway operations; this class is the sole
/// owner of duplicate detection, session linking, timeout, cancellation,
/// bounded concurrency, and late-response suppression.
/// </summary>
internal sealed class CorrelatedRequestRunner<TGateway>
    where TGateway : class
{
    private readonly IWebReplySink _reply;
    private readonly TimeSpan _timeout;
    private readonly Func<CancellationToken> _sessionToken;
    private readonly CorrelatedRequestPolicy _policy;
    private readonly ConcurrentDictionary<string, RequestState> _requests = new();
    private TGateway? _gateway;

    public CorrelatedRequestRunner(
        IWebReplySink reply,
        TimeSpan timeout,
        Func<CancellationToken> sessionToken,
        CorrelatedRequestPolicy policy)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _timeout = timeout > TimeSpan.Zero
            ? timeout
            : throw new ArgumentOutOfRangeException(nameof(timeout));
        _sessionToken = sessionToken ?? throw new ArgumentNullException(nameof(sessionToken));
        _policy = policy ?? throw new ArgumentNullException(nameof(policy));
    }

    public void SetGateway(TGateway gateway)
        => Interlocked.Exchange(
            ref _gateway,
            gateway ?? throw new ArgumentNullException(nameof(gateway)));

    public void Cancel(string requestId)
    {
        if (!_requests.TryGetValue(requestId, out RequestState? state)) return;
        state.MarkCancelledByRenderer();
        state.TryCancel();
        if (state.TryMarkCancellationReply())
        {
            _reply.PostOperationFailed(
                requestId,
                _policy.CancelledMessage,
                _policy.CancelledCode);
        }
    }

    public async Task RunAsync<TResult>(
        RoutedWebRequest request,
        string responseType,
        Func<TGateway, CancellationToken, Task<TResult>> operation,
        SemaphoreSlim? concurrencyGate = null)
    {
        TGateway? gateway = Volatile.Read(ref _gateway);
        if (gateway is null)
        {
            _reply.PostOperationFailed(
                request.RequestId,
                _policy.UnavailableMessage,
                _policy.UnavailableCode);
            return;
        }
        if (string.IsNullOrWhiteSpace(request.RequestId))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                _policy.MissingRequestIdMessage,
                _policy.InvalidRequestCode);
            return;
        }

        CancellationToken sessionToken = _sessionToken();
        using var cancellation = sessionToken.CanBeCanceled
            ? CancellationTokenSource.CreateLinkedTokenSource(sessionToken)
            : new CancellationTokenSource();
        cancellation.CancelAfter(_timeout);
        var state = new RequestState(cancellation);
        if (!_requests.TryAdd(request.RequestId, state))
        {
            _reply.PostOperationFailed(
                request.RequestId,
                _policy.DuplicateMessage,
                _policy.DuplicateCode);
            return;
        }

        bool leaseHeld = false;
        try
        {
            if (concurrencyGate is not null)
            {
                await concurrencyGate.WaitAsync(cancellation.Token).ConfigureAwait(false);
                leaseHeld = true;
            }
            TResult result = await operation(gateway, cancellation.Token)
                .ConfigureAwait(false);
            if (!cancellation.IsCancellationRequested
                && _requests.TryGetValue(request.RequestId, out RequestState? current)
                && ReferenceEquals(current, state))
            {
                _reply.PostResponse(responseType, request.RequestId, result);
            }
        }
        catch (OperationCanceledException)
        {
            PostCancellation(request.RequestId, state, sessionToken);
        }
        catch (Exception exception)
        {
            if (state.CancelledByRenderer || cancellation.IsCancellationRequested)
            {
                PostCancellation(request.RequestId, state, sessionToken);
                return;
            }
            CorrelatedRequestFailure failure = _policy.MapFailure(exception);
            _policy.TraceFailure(request.Type, failure.Code);
            _reply.PostOperationFailed(request.RequestId, failure.Message, failure.Code);
        }
        finally
        {
            if (leaseHeld) concurrencyGate!.Release();
            _requests.TryRemove(request.RequestId, out _);
        }
    }

    private void PostCancellation(
        string? requestId,
        RequestState state,
        CancellationToken sessionToken)
    {
        if (!state.TryMarkCancellationReply()) return;
        bool timeout = !state.CancelledByRenderer && !sessionToken.IsCancellationRequested;
        _reply.PostOperationFailed(
            requestId,
            timeout ? _policy.TimeoutMessage : _policy.CancelledMessage,
            timeout ? _policy.TimeoutCode : _policy.CancelledCode);
    }

    private sealed class RequestState(CancellationTokenSource cancellation)
    {
        private int _cancelledByRenderer;
        private int _cancellationReplyPosted;

        public bool CancelledByRenderer => Volatile.Read(ref _cancelledByRenderer) != 0;

        public void MarkCancelledByRenderer()
            => Interlocked.Exchange(ref _cancelledByRenderer, 1);

        public void TryCancel()
        {
            try { cancellation.Cancel(); }
            catch (ObjectDisposedException) { }
        }

        public bool TryMarkCancellationReply()
            => Interlocked.Exchange(ref _cancellationReplyPosted, 1) == 0;
    }
}
