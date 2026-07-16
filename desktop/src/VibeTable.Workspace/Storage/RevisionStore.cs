using System.IO;
using VibeTable.Workspace.Domain;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Immutable revision JSON store.
///
/// Each revision is a separate file at
/// <c>revisions/{documentId}/{revisionId}.json</c>. Revisions are written
/// once and never modified. The store verifies immutability: writing to an
/// existing revision ID is rejected unless the content is byte-identical.
/// </summary>
public sealed class RevisionStore
{
    private readonly string _revisionsRoot;
    private readonly AtomicJsonStore _json;

    public RevisionStore(string backupRoot, AtomicJsonStore json)
    {
        _revisionsRoot = Path.Combine(backupRoot, "revisions");
        _json = json;
    }

    /// <summary>
    /// Returns the file path for a revision.
    /// </summary>
    public string GetPath(string documentId, string revisionId)
    {
        return Path.Combine(_revisionsRoot, documentId, revisionId + ".json");
    }

    /// <summary>
    /// Read a revision by ID. Returns null if not found.
    /// </summary>
    public RevisionManifest? Read(string documentId, string revisionId)
    {
        return _json.Read<RevisionManifest>(GetPath(documentId, revisionId));
    }

    /// <summary>
    /// Atomically write a revision. Rejects overwriting an existing revision
    /// unless the content is byte-identical (idempotent retry).
    /// </summary>
    public void Write(RevisionManifest revision)
    {
        var path = GetPath(revision.DocumentId, revision.RevisionId);

        // Check if the revision already exists.
        if (File.Exists(path))
        {
            // Idempotent: if the content is identical, return silently.
            var existing = Read(revision.DocumentId, revision.RevisionId);
            if (existing is not null && RevisionsMatch(existing, revision))
                return;

            // Content differs — immutability violation.
            throw new InvalidOperationException(
                $"revision {revision.RevisionId} already exists with different content " +
                "(revisions are immutable)");
        }

        _json.Write(path, revision);
    }

    /// <summary>
    /// List all revisions for a document (any scheme).
    /// </summary>
    public List<RevisionManifest> ListByDocument(string documentId)
    {
        var dir = Path.Combine(_revisionsRoot, documentId);
        if (!Directory.Exists(dir))
            return [];

        var result = new List<RevisionManifest>();
        foreach (var file in Directory.GetFiles(dir, "*.json"))
        {
            var rev = _json.Read<RevisionManifest>(file);
            if (rev is not null)
                result.Add(rev);
        }
        return result;
    }

    /// <summary>
    /// List all revisions for a specific scheme within a document.
    /// </summary>
    public List<RevisionManifest> ListByScheme(string documentId, string schemeId)
    {
        return ListByDocument(documentId)
            .Where(r => r.SchemeId == schemeId)
            .OrderBy(r => r.Sequence)
            .ToList();
    }

    private static bool RevisionsMatch(RevisionManifest a, RevisionManifest b)
    {
        return a.RevisionId == b.RevisionId
            && a.DocumentId == b.DocumentId
            && a.SchemeId == b.SchemeId
            && a.ContentHash == b.ContentHash
            && a.Sequence == b.Sequence
            && a.VersionLabel == b.VersionLabel
            && a.Kind == b.Kind;
    }
}
