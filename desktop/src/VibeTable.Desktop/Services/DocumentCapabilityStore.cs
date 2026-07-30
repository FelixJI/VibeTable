using System.Collections.Frozen;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Session-local capabilities for native file actions. This store is never a
/// document catalog: it only wraps a canonical Sidecar document UUID and path
/// for one active workspace session.
/// </summary>
public sealed class DocumentCapabilityStore
{
    public static readonly TimeSpan DefaultTtl = TimeSpan.FromMinutes(10);

    private readonly object _gate = new();
    private readonly Dictionary<string, DocumentCapabilityDescriptor> _entries =
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
        Guid workspaceId,
        ulong sessionEpoch,
        Guid documentId,
        string relativePath,
        Guid? effectiveRevisionId,
        IEnumerable<string> capabilities)
    {
        if (workspaceId == Guid.Empty || documentId == Guid.Empty ||
            sessionEpoch == 0)
            throw new ArgumentException("A canonical workspace session is required.");
        ArgumentException.ThrowIfNullOrWhiteSpace(relativePath);
        ArgumentNullException.ThrowIfNull(capabilities);

        lock (_gate)
        {
            PruneExpiredLocked(_clock());
            string handle = $"entry-{Guid.NewGuid():N}";
            _entries[handle] = new DocumentCapabilityDescriptor(
                workspaceId,
                sessionEpoch,
                documentId,
                relativePath,
                effectiveRevisionId,
                capabilities.ToFrozenSet(StringComparer.Ordinal),
                _clock() + _ttl,
                _epoch);
            return handle;
        }
    }

    public DocumentCapabilityDescriptor Resolve(
        string handle,
        string requiredCapability,
        Guid workspaceId,
        ulong sessionEpoch)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(handle);
        ArgumentException.ThrowIfNullOrWhiteSpace(requiredCapability);
        lock (_gate)
        {
            if (!_entries.TryGetValue(handle, out var descriptor) ||
                descriptor.Epoch != _epoch ||
                descriptor.WorkspaceId != workspaceId ||
                descriptor.SessionEpoch != sessionEpoch)
            {
                _entries.Remove(handle);
                throw InvalidHandle();
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

    public long RotateEpoch()
    {
        lock (_gate)
        {
            _epoch = _epoch == long.MaxValue ? 0 : _epoch + 1;
            _entries.Clear();
            return _epoch;
        }
    }

    public void RevokeAll()
    {
        lock (_gate)
            _entries.Clear();
    }

    private void PruneExpiredLocked(DateTimeOffset now)
    {
        foreach (string key in _entries
            .Where(entry => now >= entry.Value.ExpiresAt)
            .Select(entry => entry.Key)
            .ToArray())
            _entries.Remove(key);
    }

    private static DocumentCapabilityException InvalidHandle()
        => new(
            "文档授权已失效，请刷新文件列表。",
            "DOCUMENT_HANDLE_INVALID");
}

public sealed record DocumentCapabilityDescriptor(
    Guid WorkspaceId,
    ulong SessionEpoch,
    Guid DocumentId,
    string RelativePath,
    Guid? EffectiveRevisionId,
    IReadOnlySet<string> Capabilities,
    DateTimeOffset ExpiresAt,
    long Epoch);

public sealed class DocumentCapabilityException : InvalidOperationException
{
    public DocumentCapabilityException(string message, string code)
        : base(message) => Code = code;

    public string Code { get; }
}
