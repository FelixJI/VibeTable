using System;
using System.Collections.Generic;
using System.Threading;

namespace VibeTable.Desktop.Services;

public sealed class PluginProjectContextBinding
{
    internal PluginProjectContextBinding(
        PluginProjectContext context,
        long contextGeneration,
        long openGeneration,
        string openId,
        CancellationToken sessionToken)
    {
        Context = context;
        ContextGeneration = contextGeneration;
        OpenGeneration = openGeneration;
        OpenId = openId;
        SessionToken = sessionToken;
    }

    public PluginProjectContext Context { get; }
    public long ContextGeneration { get; }
    public long OpenGeneration { get; }
    public string OpenId { get; }
    public CancellationToken SessionToken { get; }
}

public sealed record PluginProjectContextOpenStart(
    PluginProjectContextBinding? Binding,
    IReadOnlyList<string> RetiredOpenIds);

/// <summary>
/// Owns context and per-open generations. Context transitions and newer opens
/// retire older operations immediately, but their token sources remain alive
/// until the operation releases its lease, so late token registrations stay
/// valid and observe cancellation rather than ObjectDisposedException.
/// </summary>
public sealed class PluginProjectContextBindingRegistry : IDisposable
{
    internal const int RecentOpenIdentityCapacity = 256;

    private readonly object _gate = new();
    private readonly Dictionary<long, OpenOperation> _operations = [];
    private readonly HashSet<string> _seenOpenIds = new(StringComparer.Ordinal);
    private readonly Queue<string> _recentOpenIds = new();
    private readonly ProductAuthorityEpoch _authority;
    private readonly bool _ownsAuthority;
    private PluginProjectContext? _context;
    private CancellationToken _contextToken;
    private long _contextGeneration;
    private long _openGeneration;
    private long? _currentOpenGeneration;
    private bool _disposed;

    public PluginProjectContextBindingRegistry()
        : this(new ProductAuthorityEpoch(), ownsAuthority: true)
    {
    }

    internal PluginProjectContextBindingRegistry(ProductAuthorityEpoch authority)
        : this(authority, ownsAuthority: false)
    {
    }

    private PluginProjectContextBindingRegistry(
        ProductAuthorityEpoch authority,
        bool ownsAuthority)
    {
        _authority = authority ?? throw new ArgumentNullException(nameof(authority));
        _ownsAuthority = ownsAuthority;
    }

    public IReadOnlyList<string> Set(
        PluginProjectContext? context,
        CancellationToken sessionToken = default)
    {
        _authority.Transition(context, sessionToken);
        return SetAfterAuthorityTransition(context, sessionToken);
    }

    internal IReadOnlyList<string> SetAfterAuthorityTransition(
        PluginProjectContext? context,
        CancellationToken sessionToken = default)
    {
        List<OpenOperation> retired;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            _contextGeneration += 1;
            _context = context;
            _contextToken = sessionToken;
            retired = RetireCurrentLocked();
            _seenOpenIds.Clear();
            _recentOpenIds.Clear();
        }
        CancelRetired(retired);
        return retired
            .Where(operation => operation.RendererAuthored)
            .Select(operation => operation.OpenId)
            .ToArray();
    }

    public PluginProjectContextOpenStart BeginOpen(string openId)
        => BeginOpen(openId, rendererAuthored: true);

    internal PluginProjectContextOpenStart BeginHostOpen()
        => BeginOpen($"host-open:{Guid.NewGuid():N}", rendererAuthored: false);

    internal PluginProjectContextOpenStart BeginOrCoalesceHostOpen()
    {
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (_currentOpenGeneration is long generation
                && _operations.TryGetValue(generation, out OpenOperation? operation)
                && !operation.RendererAuthored
                && !operation.TerminalClaimed
                && !operation.Source.IsCancellationRequested
                && _context is not null
                && _authority.IsCurrent(operation.AuthorityLease.Snapshot))
            {
                return new PluginProjectContextOpenStart(
                    new PluginProjectContextBinding(
                        _context,
                        _contextGeneration,
                        operation.Generation,
                        operation.OpenId,
                        operation.Source.Token),
                    []);
            }
        }
        return BeginHostOpen();
    }

    internal bool IsCurrent(PluginProjectContextBinding binding)
    {
        lock (_gate) return TryGetCurrentLocked(binding, out _);
    }

    private PluginProjectContextOpenStart BeginOpen(
        string openId,
        bool rendererAuthored)
    {
        if (string.IsNullOrWhiteSpace(openId) || openId.Length > 128)
            throw new ArgumentException("Open identity is invalid.", nameof(openId));
        List<OpenOperation> retired;
        PluginProjectContextBinding? binding = null;
        lock (_gate)
        {
            ObjectDisposedException.ThrowIf(_disposed, this);
            if (rendererAuthored && !_seenOpenIds.Add(openId))
                throw new InvalidOperationException("Database open identity was already used.");
            if (rendererAuthored)
            {
                _recentOpenIds.Enqueue(openId);
                while (_recentOpenIds.Count > RecentOpenIdentityCapacity)
                    _seenOpenIds.Remove(_recentOpenIds.Dequeue());
            }
            retired = RetireCurrentLocked();
            if (_context is not null)
            {
                ProductAuthoritySnapshot authority = _authority.Snapshot();
                if (authority.Context == _context
                    && _authority.TryAcquire(
                        authority,
                        out ProductAuthorityEpoch.ProductAuthorityOperationLease? authorityLease)
                    && authorityLease is not null)
                {
                    _openGeneration += 1;
                    var source = CancellationTokenSource.CreateLinkedTokenSource(
                        _contextToken,
                        authorityLease.Token);
                    var operation = new OpenOperation(
                        _openGeneration,
                        openId,
                        rendererAuthored,
                        source,
                        authorityLease);
                    _operations.Add(operation.Generation, operation);
                    _currentOpenGeneration = operation.Generation;
                    binding = new PluginProjectContextBinding(
                        _context,
                        _contextGeneration,
                        operation.Generation,
                        openId,
                        source.Token);
                }
            }
        }
        CancelRetired(retired);
        return new PluginProjectContextOpenStart(
            binding,
            retired
                .Where(operation => operation.RendererAuthored)
                .Select(operation => operation.OpenId)
                .ToArray());
    }

    public bool TryComplete(PluginProjectContextBinding binding, Action completion)
    {
        ArgumentNullException.ThrowIfNull(completion);
        lock (_gate)
        {
            if (!TryGetCurrentLocked(binding, out OpenOperation operation)) return false;
            if (!_authority.TryFinish(operation.AuthorityLease, completion)) return false;
            operation.TerminalClaimed = true;
            _currentOpenGeneration = null;
            return true;
        }
    }

    public bool TryClaimTerminal(PluginProjectContextBinding binding)
    {
        lock (_gate)
        {
            if (!_operations.TryGetValue(binding.OpenGeneration, out OpenOperation? operation)
                || operation.TerminalClaimed
                || operation.OpenId != binding.OpenId) return false;
            operation.TerminalClaimed = true;
            if (_currentOpenGeneration == binding.OpenGeneration)
                _currentOpenGeneration = null;
            return true;
        }
    }

    public void Release(PluginProjectContextBinding binding)
    {
        CancellationTokenSource? source = null;
        ProductAuthorityEpoch.ProductAuthorityOperationLease? authorityLease = null;
        lock (_gate)
        {
            if (_operations.TryGetValue(binding.OpenGeneration, out OpenOperation? operation)
                && operation.OpenId == binding.OpenId)
            {
                if (operation.RetirementCount > 0)
                {
                    operation.ReleaseRequested = true;
                }
                else
                {
                    _operations.Remove(binding.OpenGeneration);
                    source = operation.Source;
                    authorityLease = operation.AuthorityLease;
                    if (_currentOpenGeneration == binding.OpenGeneration)
                        _currentOpenGeneration = null;
                }
            }
        }
        source?.Dispose();
        authorityLease?.Dispose();
    }

    public void Dispose()
    {
        OpenOperation[] operations;
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
            _context = null;
            _contextGeneration += 1;
            _currentOpenGeneration = null;
            operations = [.. _operations.Values];
            foreach (OpenOperation operation in operations)
            {
                operation.TerminalClaimed = true;
                operation.RetirementCount += 1;
            }
        }
        CancelRetired(operations);
        if (_ownsAuthority) _authority.Dispose();
        // Pending operations still own their token source and may register a
        // cancellation callback while unwinding. Release disposes it after the
        // operation's finally block reaches the registry.
    }

    private bool TryGetCurrentLocked(
        PluginProjectContextBinding binding,
        out OpenOperation operation)
    {
        operation = null!;
        if (_disposed
            || _context != binding.Context
            || _contextGeneration != binding.ContextGeneration
            || _currentOpenGeneration != binding.OpenGeneration) return false;
        if (!_operations.TryGetValue(
                binding.OpenGeneration,
                out OpenOperation? candidate)
            || candidate is null
            || candidate.OpenId != binding.OpenId
            || candidate.TerminalClaimed
            || candidate.Source.IsCancellationRequested) return false;
        operation = candidate;
        return true;
    }

    private List<OpenOperation> RetireCurrentLocked()
    {
        if (_currentOpenGeneration is not long generation
            || !_operations.TryGetValue(generation, out OpenOperation? operation)
            || operation.TerminalClaimed)
        {
            _currentOpenGeneration = null;
            return [];
        }
        operation.TerminalClaimed = true;
        operation.RetirementCount += 1;
        _currentOpenGeneration = null;
        return [operation];
    }

    private void CancelRetired(IEnumerable<OpenOperation> operations)
    {
        foreach (OpenOperation operation in operations)
        {
            try
            {
                operation.Source.Cancel();
            }
            catch (AggregateException)
            {
                // Cancellation callbacks are third-party code. All callbacks
                // have run; their failures must not interrupt the authority
                // transition that already retired this operation.
            }
            finally
            {
                FinishRetirement(operation);
            }
        }
    }

    private void FinishRetirement(OpenOperation operation)
    {
        CancellationTokenSource? source = null;
        ProductAuthorityEpoch.ProductAuthorityOperationLease? authorityLease = null;
        lock (_gate)
        {
            operation.RetirementCount -= 1;
            if (operation.RetirementCount == 0
                && operation.ReleaseRequested
                && _operations.TryGetValue(
                    operation.Generation,
                    out OpenOperation? registered)
                && ReferenceEquals(registered, operation))
            {
                _operations.Remove(operation.Generation);
                source = operation.Source;
                authorityLease = operation.AuthorityLease;
            }
        }
        source?.Dispose();
        authorityLease?.Dispose();
    }

    private sealed class OpenOperation(
        long generation,
        string openId,
        bool rendererAuthored,
        CancellationTokenSource source,
        ProductAuthorityEpoch.ProductAuthorityOperationLease authorityLease)
    {
        public long Generation { get; } = generation;
        public string OpenId { get; } = openId;
        public bool RendererAuthored { get; } = rendererAuthored;
        public CancellationTokenSource Source { get; } = source;
        public ProductAuthorityEpoch.ProductAuthorityOperationLease AuthorityLease { get; } =
            authorityLease;
        public bool TerminalClaimed { get; set; }
        public int RetirementCount { get; set; }
        public bool ReleaseRequested { get; set; }
    }
}

internal static class UnavailablePluginBindings
{
    public static PluginProjectContextBindingRegistry Instance { get; } = new();
}
