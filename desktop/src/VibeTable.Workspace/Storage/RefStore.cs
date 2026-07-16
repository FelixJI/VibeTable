using System.IO;
using VibeTable.Workspace.Domain;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Mutable scheme-head pointer store with expected-head CAS (compare-and-swap).
///
/// Refs are stored at <c>refs/{documentId}/{schemeId}.json</c>. They are one of
/// the few mutable files in the version kernel. Updates use temp-file + atomic
/// rename. The <see cref="UpdateHead"/> method performs expected-head CAS:
/// if the current head does not match <paramref name="expectedHeadRevisionId"/>,
/// the update fails (concurrent modification detected) and both revisions are
/// preserved.
/// </summary>
public sealed class RefStore
{
    private readonly string _refsRoot;
    private readonly AtomicJsonStore _json;

    public RefStore(string backupRoot, AtomicJsonStore json)
    {
        _refsRoot = Path.Combine(backupRoot, "refs");
        _json = json;
    }

    /// <summary>
    /// Returns the file path for a ref.
    /// </summary>
    public string GetPath(string documentId, string schemeId)
    {
        return Path.Combine(_refsRoot, documentId, schemeId + ".json");
    }

    /// <summary>
    /// Read the current ref for a scheme. Returns null if not found.
    /// </summary>
    public RefManifest? Read(string documentId, string schemeId)
    {
        return _json.Read<RefManifest>(GetPath(documentId, schemeId));
    }

    /// <summary>
    /// Initialize a new ref (only if it does not exist yet).
    /// </summary>
    public void Initialize(RefManifest refManifest)
    {
        var path = GetPath(refManifest.DocumentId, refManifest.SchemeId);
        if (File.Exists(path))
            throw new InvalidOperationException(
                $"ref for {refManifest.DocumentId}/{refManifest.SchemeId} already exists; use UpdateHead");

        _json.Write(path, refManifest);
    }

    /// <summary>
    /// Atomically update a ref's head using expected-head CAS.
    ///
    /// If the current head does not match <paramref name="expectedHeadRevisionId"/>,
    /// a <see cref="RefCasConflictException"/> is thrown. The caller must handle
    /// the conflict by preserving both revisions and creating a conflict record.
    /// </summary>
    public RefManifest UpdateHead(
        string documentId,
        string schemeId,
        string expectedHeadRevisionId,
        string newHeadRevisionId,
        string updatedAt
    )
    {
        var current = Read(documentId, schemeId);
        if (current is null)
            throw new InvalidOperationException(
                $"ref for {documentId}/{schemeId} does not exist; use Initialize first");

        if (current.HeadRevisionId != expectedHeadRevisionId)
            throw new RefCasConflictException(
                documentId,
                schemeId,
                expectedHeadRevisionId,
                current.HeadRevisionId);

        var updated = current with
        {
            HeadRevisionId = newHeadRevisionId,
            UpdatedAt = updatedAt,
        };
        _json.Write(GetPath(documentId, schemeId), updated);
        return updated;
    }
}

/// <summary>
/// Thrown when a ref CAS update detects a concurrent modification.
/// Both revisions (expected and actual) are preserved; the caller must
/// handle the conflict rather than silently overwriting.
/// </summary>
public sealed class RefCasConflictException(
    string documentId,
    string schemeId,
    string expectedHead,
    string actualHead
) : Exception(
    $"ref CAS conflict for {documentId}/{schemeId}: " +
    $"expected head {expectedHead}, actual head {actualHead}")
{
    public string DocumentId { get; } = documentId;
    public string SchemeId { get; } = schemeId;
    public string ExpectedHead { get; } = expectedHead;
    public string ActualHead { get; } = actualHead;
}
