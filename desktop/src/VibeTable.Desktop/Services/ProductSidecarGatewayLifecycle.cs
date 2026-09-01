using System.Diagnostics;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Services;

internal interface IProductSidecarGatewayCandidate :
    IProductSidecarRpcForwarder,
    IDisposable
{
    Task<ProductSidecarCapabilities> GetCapabilitiesAsync(
        CancellationToken cancellationToken);
}

internal interface IProductSidecarGenerationAuthority
{
    event Action? CurrentChanged;

    bool TryUseCurrent(
        ProductSidecarGenerationSnapshot snapshot,
        Func<bool> action);
}

internal interface IProductSidecarForwarderBinding
{
    bool TryReplace(
        IProductSidecarRpcForwarder? expected,
        IProductSidecarRpcForwarder replacement);

    bool Clear(IProductSidecarRpcForwarder expected);
}

internal interface IProductSidecarGatewayLifecycle : IDisposable
{
    Task<bool> TryReplaceAsync(
        ProductSidecarGenerationSnapshot snapshot,
        CancellationToken cancellationToken);

    bool Clear(ProductSidecarGenerationSnapshot expectedSnapshot);
}

internal sealed class ProductSidecarGenerationSnapshot
{
    private readonly ProductSidecarRegistration[] _registrations;
    private readonly IReadOnlyList<ProductSidecarRegistration> _readOnlyRegistrations;

    internal ProductSidecarGenerationSnapshot(
        object runtimeAuthority,
        PocketBaseAdminContext context,
        ProductSidecarIdentity identity,
        IReadOnlyCollection<ProductSidecarRegistration> registrations)
    {
        RuntimeAuthority = runtimeAuthority
            ?? throw new ArgumentNullException(nameof(runtimeAuthority));
        ArgumentNullException.ThrowIfNull(context);
        ArgumentNullException.ThrowIfNull(identity);
        ArgumentNullException.ThrowIfNull(registrations);
        Context = new PocketBaseAdminContext(
            context.BootstrapUri,
            context.Origin,
            context.SessionHeaderName,
            context.SessionSecret);
        Identity = new ProductSidecarIdentity(
            identity.WorkspaceId,
            identity.SessionEpoch,
            identity.FenceEpoch,
            identity.ClaimId);
        _registrations = registrations.ToArray();
        _readOnlyRegistrations = Array.AsReadOnly(_registrations);
    }

    internal object RuntimeAuthority { get; }
    internal PocketBaseAdminContext Context { get; }
    internal ProductSidecarIdentity Identity { get; }
    internal IReadOnlyList<ProductSidecarRegistration> Registrations =>
        _readOnlyRegistrations;

    internal bool Matches(ProductSidecarGenerationSnapshot other)
    {
        ArgumentNullException.ThrowIfNull(other);
        return ReferenceEquals(RuntimeAuthority, other.RuntimeAuthority)
            && Context == other.Context
            && Identity == other.Identity;
    }
}

internal sealed class ProductSidecarGenerationSnapshotCache
{
    private ProductSidecarGenerationSnapshot? _current;

    internal ProductSidecarGenerationSnapshot GetOrCreate(
        object runtimeAuthority,
        PocketBaseAdminContext context,
        ProductSidecarIdentity identity,
        IReadOnlyCollection<ProductSidecarRegistration> registrations)
    {
        var candidate = new ProductSidecarGenerationSnapshot(
            runtimeAuthority,
            context,
            identity,
            registrations);
        if (_current?.Matches(candidate) == true)
            return _current;
        _current = candidate;
        return candidate;
    }

    internal void Clear() => _current = null;
}

internal sealed class ProductSidecarGatewayLifecycle :
    IProductSidecarGatewayLifecycle
{
    private readonly object _gate = new();
    private readonly IProductSidecarGenerationAuthority _authority;
    private readonly IProductSidecarForwarderBinding _binding;
    private readonly Func<
        ProductSidecarGenerationSnapshot,
        IProductSidecarGatewayCandidate> _candidateFactory;
    private readonly CancellationTokenSource _lifetime = new();
    private BoundGateway? _bound;
    private PendingAttempt? _pending;
    private long _attempt;
    private bool _disposed;

    internal ProductSidecarGatewayLifecycle(
        IProductSidecarGenerationAuthority authority,
        IProductSidecarForwarderBinding binding,
        Func<ProductSidecarGenerationSnapshot, IProductSidecarGatewayCandidate>
            candidateFactory)
    {
        _authority = authority ?? throw new ArgumentNullException(nameof(authority));
        _binding = binding ?? throw new ArgumentNullException(nameof(binding));
        _candidateFactory = candidateFactory
            ?? throw new ArgumentNullException(nameof(candidateFactory));
        _authority.CurrentChanged += OnCurrentChanged;
    }

    internal ProductSidecarGatewayLifecycle(
        ProductionWorkspaceRuntimeFactory authority,
        WorkspaceRequestDispatcher binding)
        : this(
            authority,
            binding,
            snapshot => new ProductSidecarHttpGateway(
                snapshot.Context,
                snapshot.Identity,
                snapshot.Registrations))
    {
    }

    public async Task<bool> TryReplaceAsync(
        ProductSidecarGenerationSnapshot snapshot,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        cancellationToken.ThrowIfCancellationRequested();
        PendingAttempt pending;
        PendingAttempt? previous;
        CancellationToken lifetimeToken;
        long attempt;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (!_authority.TryUseCurrent(snapshot, () => true))
                return false;
            if (ReferenceEquals(_bound?.Snapshot, snapshot))
                return true;
            pending = new PendingAttempt(
                snapshot,
                new CancellationTokenSource());
            attempt = ++_attempt;
            previous = _pending;
            _pending = pending;
            lifetimeToken = _lifetime.Token;
        }
        previous?.Cancel();
        using CancellationTokenSource call =
            CancellationTokenSource.CreateLinkedTokenSource(
                cancellationToken,
                lifetimeToken,
                pending.Token);
        using CancellationTokenRegistration callerCancellation =
            cancellationToken.Register(
                () => OnCallerCancellation(pending));

        IProductSidecarGatewayCandidate? candidate = null;
        try
        {
            candidate = _candidateFactory(snapshot);
            _ = await candidate.GetCapabilitiesAsync(call.Token)
                .ConfigureAwait(false);
            IProductSidecarGatewayCandidate? retired = null;
            bool published;
            lock (_gate)
            {
                if (_disposed || attempt != _attempt)
                {
                    published = false;
                }
                else
                {
                    IProductSidecarGatewayCandidate? expected = _bound?.Gateway;
                    published = _authority.TryUseCurrent(
                        snapshot,
                        () => _binding.TryReplace(expected, candidate));
                    if (published)
                    {
                        retired = expected;
                        _bound = new BoundGateway(snapshot, candidate);
                    }
                }
            }
            DisposeRetired(retired);
            if (!published)
            {
                cancellationToken.ThrowIfCancellationRequested();
                candidate.Dispose();
            }
            return published;
        }
        catch (OperationCanceledException) when (
            pending.IsCancellationRequested
            && !cancellationToken.IsCancellationRequested)
        {
            candidate?.Dispose();
            return false;
        }
        catch
        {
            candidate?.Dispose();
            throw;
        }
        finally
        {
            lock (_gate)
            {
                if (ReferenceEquals(_pending, pending))
                    _pending = null;
            }
            pending.Dispose();
        }
    }

    public bool Clear(ProductSidecarGenerationSnapshot expectedSnapshot)
    {
        ArgumentNullException.ThrowIfNull(expectedSnapshot);
        IProductSidecarGatewayCandidate? retired = null;
        PendingAttempt? pending = null;
        bool cleared = false;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (ReferenceEquals(_pending?.Snapshot, expectedSnapshot))
            {
                pending = _pending;
                _pending = null;
                _attempt++;
            }
            if (ReferenceEquals(_bound?.Snapshot, expectedSnapshot))
            {
                retired = _bound.Gateway;
                _bound = null;
                cleared = _binding.Clear(retired);
            }
            if (pending is null && retired is null)
                return false;
        }
        pending?.Cancel();
        DisposeRetired(retired);
        return pending is not null || cleared;
    }

    public void Dispose()
    {
        BoundGateway? bound;
        PendingAttempt? pending;
        lock (_gate)
        {
            if (_disposed)
                return;
            _disposed = true;
            _attempt++;
            bound = _bound;
            _bound = null;
            pending = _pending;
            _pending = null;
            if (bound is not null)
                _binding.Clear(bound.Gateway);
        }
        _authority.CurrentChanged -= OnCurrentChanged;
        pending?.Cancel();
        _lifetime.Cancel();
        DisposeRetired(bound?.Gateway);
        _lifetime.Dispose();
    }

    private void OnCurrentChanged()
    {
        IProductSidecarGatewayCandidate? retired = null;
        PendingAttempt? pending = null;
        lock (_gate)
        {
            if (_disposed)
                return;
            if (_pending is not null
                && !_authority.TryUseCurrent(_pending.Snapshot, () => true))
            {
                pending = _pending;
                _pending = null;
                _attempt++;
            }
            if (_bound is not null
                && !_authority.TryUseCurrent(_bound.Snapshot, () => true))
            {
                retired = _bound.Gateway;
                _bound = null;
                _binding.Clear(retired);
            }
        }
        pending?.Cancel();
        DisposeRetired(retired);
    }

    private void OnCallerCancellation(PendingAttempt pending)
    {
        lock (_gate)
        {
            if (!_disposed && ReferenceEquals(_pending, pending))
                _attempt++;
        }
    }

    private static void DisposeRetired(
        IProductSidecarGatewayCandidate? retired)
    {
        if (retired is null)
            return;
        try
        {
            retired.Dispose();
        }
        catch (Exception exception)
        {
            Trace.TraceError(
                "product_sidecar.retired_gateway_dispose_failed:" +
                exception.GetType().Name);
        }
    }

    private sealed record BoundGateway(
        ProductSidecarGenerationSnapshot Snapshot,
        IProductSidecarGatewayCandidate Gateway);

    private sealed class PendingAttempt : IDisposable
    {
        private CancellationTokenSource? _cancellation;

        internal PendingAttempt(
            ProductSidecarGenerationSnapshot snapshot,
            CancellationTokenSource cancellation)
        {
            Snapshot = snapshot;
            _cancellation = cancellation;
        }

        internal ProductSidecarGenerationSnapshot Snapshot { get; }
        internal CancellationToken Token =>
            Volatile.Read(ref _cancellation)?.Token
            ?? new CancellationToken(canceled: true);
        internal bool IsCancellationRequested =>
            Volatile.Read(ref _cancellation)?.IsCancellationRequested != false;

        internal void Cancel()
        {
            try
            {
                Volatile.Read(ref _cancellation)?.Cancel();
            }
            catch (ObjectDisposedException)
            {
            }
        }

        public void Dispose() =>
            Interlocked.Exchange(ref _cancellation, null)?.Dispose();
    }
}
