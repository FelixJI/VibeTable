using System.Collections.Frozen;
using System.Collections.Generic;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Session-local opaque handles for document actions. Renderer-visible handles
/// contain no path information and expire automatically.
/// </summary>
public sealed class DocumentCapabilityStore
{
    public static readonly TimeSpan DefaultTtl = TimeSpan.FromMinutes(10);

    private readonly object _gate = new();
    private readonly Dictionary<string, DocumentCapabilityDescriptor> _entries =
        new(StringComparer.Ordinal);
    private readonly Dictionary<string, DocumentRevisionCapabilityDescriptor> _revisions =
        new(StringComparer.Ordinal);
    private readonly Dictionary<string, DocumentSchemeCapabilityDescriptor> _schemes =
        new(StringComparer.Ordinal);
    private readonly Func<DateTimeOffset> _clock;
    private readonly TimeSpan _ttl;
    private long _epoch;

    public DocumentCapabilityStore(
        Func<DateTimeOffset>? clock = null,
        TimeSpan? ttl = null)
    {
        _clock = clock ?? (() => DateTimeOffset.UtcNow);
        _ttl = ttl ?? DefaultTtl;
    }

    public string Issue(
        string workspaceId,
        string documentId,
        string? linkId,
        string relativePath,
        IEnumerable<string> capabilities,
        string? currentRevisionId = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceId);
        ArgumentException.ThrowIfNullOrWhiteSpace(documentId);
        ArgumentException.ThrowIfNullOrWhiteSpace(relativePath);
        ArgumentNullException.ThrowIfNull(capabilities);

        var grantedCapabilities = capabilities.ToFrozenSet(StringComparer.Ordinal);
        lock (_gate)
        {
            var now = _clock();
            PruneExpiredLocked(now);
            string handle = $"doc-{Guid.NewGuid():N}";
            _entries[handle] = new DocumentCapabilityDescriptor(
                workspaceId,
                documentId,
                linkId,
                relativePath,
                currentRevisionId,
                grantedCapabilities,
                now + _ttl,
                _epoch);
            return handle;
        }
    }

    public DocumentCapabilityDescriptor Resolve(string handle, string requiredCapability)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(handle);
        ArgumentException.ThrowIfNullOrWhiteSpace(requiredCapability);

        lock (_gate)
        {
            if (!_entries.TryGetValue(handle, out var descriptor))
                throw new DocumentCapabilityException(
                    "文档授权已失效，请刷新文件列表。",
                    "DOCUMENT_HANDLE_INVALID");
            if (descriptor.Epoch != _epoch)
            {
                _entries.Remove(handle);
                throw new DocumentCapabilityException(
                    "文档授权已失效，请刷新文件列表。",
                    "DOCUMENT_HANDLE_INVALID");
            }
            if (_clock() >= descriptor.ExpiresAt)
            {
                _entries.Remove(handle);
                throw new DocumentCapabilityException(
                    "文档授权已过期，请刷新文件列表。",
                    "DOCUMENT_HANDLE_EXPIRED");
            }
            if (!descriptor.Capabilities.Contains(requiredCapability))
                throw new DocumentCapabilityException(
                    "当前文档不允许执行此操作。",
                    "DOCUMENT_CAPABILITY_DENIED");
            return descriptor;
        }
    }

    public string IssueRevision(
        string workspaceId,
        string documentId,
        string revisionId,
        IEnumerable<string> capabilities)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceId);
        ArgumentException.ThrowIfNullOrWhiteSpace(documentId);
        ArgumentException.ThrowIfNullOrWhiteSpace(revisionId);
        ArgumentNullException.ThrowIfNull(capabilities);

        var grantedCapabilities = capabilities.ToFrozenSet(StringComparer.Ordinal);
        lock (_gate)
        {
            var now = _clock();
            PruneExpiredLocked(now);
            string handle = $"rev-{Guid.NewGuid():N}";
            _revisions[handle] = new DocumentRevisionCapabilityDescriptor(
                workspaceId,
                documentId,
                revisionId,
                grantedCapabilities,
                now + _ttl,
                _epoch);
            return handle;
        }
    }

    public DocumentRevisionCapabilityDescriptor ResolveRevision(
        string handle,
        string requiredCapability)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(handle);
        ArgumentException.ThrowIfNullOrWhiteSpace(requiredCapability);

        lock (_gate)
        {
            if (!_revisions.TryGetValue(handle, out var descriptor))
                throw new DocumentCapabilityException(
                    "版本授权已失效，请重新打开版本历史。",
                    "REVISION_HANDLE_INVALID");
            if (descriptor.Epoch != _epoch)
            {
                _revisions.Remove(handle);
                throw new DocumentCapabilityException(
                    "版本授权已失效，请重新打开版本历史。",
                    "REVISION_HANDLE_INVALID");
            }
            if (_clock() >= descriptor.ExpiresAt)
            {
                _revisions.Remove(handle);
                throw new DocumentCapabilityException(
                    "版本授权已过期，请重新打开版本历史。",
                    "REVISION_HANDLE_EXPIRED");
            }
            if (!descriptor.Capabilities.Contains(requiredCapability))
                throw new DocumentCapabilityException(
                    "当前版本不允许执行此操作。",
                    "REVISION_CAPABILITY_DENIED");
            return descriptor;
        }
    }

    public string IssueScheme(
        string workspaceId,
        string documentId,
        string schemeId,
        string observedHeadRevisionId,
        IEnumerable<string> capabilities)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceId);
        ArgumentException.ThrowIfNullOrWhiteSpace(documentId);
        ArgumentException.ThrowIfNullOrWhiteSpace(schemeId);
        ArgumentNullException.ThrowIfNull(observedHeadRevisionId);
        ArgumentNullException.ThrowIfNull(capabilities);

        var grantedCapabilities = capabilities.ToFrozenSet(StringComparer.Ordinal);
        lock (_gate)
        {
            var now = _clock();
            PruneExpiredLocked(now);
            string handle = $"scheme-{Guid.NewGuid():N}";
            _schemes[handle] = new DocumentSchemeCapabilityDescriptor(
                workspaceId,
                documentId,
                schemeId,
                observedHeadRevisionId,
                grantedCapabilities,
                now + _ttl,
                _epoch);
            return handle;
        }
    }

    public DocumentSchemeCapabilityDescriptor ResolveScheme(
        string handle,
        string requiredCapability)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(handle);
        ArgumentException.ThrowIfNullOrWhiteSpace(requiredCapability);

        lock (_gate)
        {
            if (!_schemes.TryGetValue(handle, out var descriptor))
                throw new DocumentCapabilityException(
                    "The scheme authorization is no longer valid. Refresh the scheme list.",
                    "SCHEME_HANDLE_INVALID");
            if (descriptor.Epoch != _epoch)
            {
                _schemes.Remove(handle);
                throw new DocumentCapabilityException(
                    "The scheme authorization is no longer valid. Refresh the scheme list.",
                    "SCHEME_HANDLE_INVALID");
            }
            if (_clock() >= descriptor.ExpiresAt)
            {
                _schemes.Remove(handle);
                throw new DocumentCapabilityException(
                    "The scheme authorization expired. Refresh the scheme list.",
                    "SCHEME_HANDLE_EXPIRED");
            }
            if (!descriptor.Capabilities.Contains(requiredCapability))
                throw new DocumentCapabilityException(
                    "This scheme does not allow the requested operation.",
                    "SCHEME_CAPABILITY_DENIED");
            return descriptor;
        }
    }

    public void RevokeAll()
    {
        lock (_gate)
        {
            _entries.Clear();
            _revisions.Clear();
            _schemes.Clear();
        }
    }

    /// <summary>
    /// Advances the capability generation and invalidates every handle issued
    /// by an earlier host session or workspace context.
    /// </summary>
    public long RotateEpoch()
    {
        lock (_gate)
        {
            _epoch = _epoch == long.MaxValue ? 0 : _epoch + 1;
            _entries.Clear();
            _revisions.Clear();
            _schemes.Clear();
            return _epoch;
        }
    }

    public void PruneExpired()
    {
        lock (_gate)
        {
            PruneExpiredLocked(_clock());
        }
    }

    private void PruneExpiredLocked(DateTimeOffset now)
    {
        foreach (string key in _entries
            .Where(entry => now >= entry.Value.ExpiresAt)
            .Select(entry => entry.Key)
            .ToArray())
        {
            _entries.Remove(key);
        }
        foreach (string key in _revisions
            .Where(entry => now >= entry.Value.ExpiresAt)
            .Select(entry => entry.Key)
            .ToArray())
        {
            _revisions.Remove(key);
        }
        foreach (string key in _schemes
            .Where(entry => now >= entry.Value.ExpiresAt)
            .Select(entry => entry.Key)
            .ToArray())
        {
            _schemes.Remove(key);
        }
    }
}

public sealed record DocumentCapabilityDescriptor(
    string WorkspaceId,
    string DocumentId,
    string? LinkId,
    string RelativePath,
    string? CurrentRevisionId,
    IReadOnlySet<string> Capabilities,
    DateTimeOffset ExpiresAt,
    long Epoch);

public sealed record DocumentRevisionCapabilityDescriptor(
    string WorkspaceId,
    string DocumentId,
    string RevisionId,
    IReadOnlySet<string> Capabilities,
    DateTimeOffset ExpiresAt,
    long Epoch);

public sealed record DocumentSchemeCapabilityDescriptor(
    string WorkspaceId,
    string DocumentId,
    string SchemeId,
    string ObservedHeadRevisionId,
    IReadOnlySet<string> Capabilities,
    DateTimeOffset ExpiresAt,
    long Epoch);

public sealed class DocumentCapabilityException : InvalidOperationException
{
    public DocumentCapabilityException(string message, string code)
        : base(message)
    {
        Code = code;
    }

    public string Code { get; }
}
