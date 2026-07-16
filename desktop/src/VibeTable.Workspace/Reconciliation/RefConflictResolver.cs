using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Reconciliation;

/// <summary>
/// Resolves expected-head CAS conflicts by preserving both sides.
///
/// When two devices commit from the same parent, both revisions are kept.
/// The non-current-head revision becomes a "conflict scheme candidate" —
/// a new scheme is created to hold the conflicting revision chain so the
/// user can review and adopt it manually.
///
/// Last-write-wins is NEVER used. Both sides are always preserved.
/// </summary>
public sealed class RefConflictResolver
{
    private readonly string _backupRoot;
    private readonly RevisionStore _revisions;
    private readonly RefStore _refs;
    private readonly AtomicJsonStore _json;

    public RefConflictResolver(
        string backupRoot,
        RevisionStore revisions,
        RefStore refs,
        AtomicJsonStore json
    )
    {
        _backupRoot = backupRoot;
        _revisions = revisions;
        _refs = refs;
        _json = json;
    }

    /// <summary>
    /// Result of conflict resolution.
    /// </summary>
    public sealed record ConflictResolution(
        string Status,
        string CurrentHeadRevisionId,
        string? ConflictSchemeId,
        string? ConflictRevisionId,
        string Message
    );

    /// <summary>
    /// Resolve a CAS conflict by creating a conflict scheme for the
    /// losing revision.
    ///
    /// <param name="documentId">The document.</param>
    /// <param name="schemeId">The scheme that had the conflict.</param>
    /// <param name="conflictingRevisionId">The revision that lost the CAS.</param>
    /// <param name="createdAt">Timestamp.</param>
    /// <returns>A conflict resolution result.</returns>
    public ConflictResolution ResolveConflict(
        string documentId,
        string schemeId,
        string conflictingRevisionId,
        string createdAt
    )
    {
        var currentRef = _refs.Read(documentId, schemeId)
            ?? throw new InvalidOperationException(
                $"scheme {schemeId} not found in document {documentId}");

        var conflictingRev = _revisions.Read(documentId, conflictingRevisionId)
            ?? throw new InvalidOperationException(
                $"conflicting revision {conflictingRevisionId} not found");

        // Create a new scheme to hold the conflicting revision chain.
        var conflictSchemeId = Guid.NewGuid().ToString("N");
        var conflictSchemeName = $"冲突候选 ({conflictingRev.VersionLabel})";

        var conflictRef = new RefManifest(
            FormatVersion: RefManifest.CurrentFormatVersion,
            DocumentId: documentId,
            SchemeId: conflictSchemeId,
            SchemeName: conflictSchemeName,
            HeadRevisionId: conflictingRevisionId,
            WorkingRelativePath: currentRef.WorkingRelativePath,
            UpdatedAt: createdAt
        );
        _refs.Initialize(conflictRef);

        return new ConflictResolution(
            Status: "conflict-preserved",
            CurrentHeadRevisionId: currentRef.HeadRevisionId,
            ConflictSchemeId: conflictSchemeId,
            ConflictRevisionId: conflictingRevisionId,
            Message: $"Conflict preserved: revision {conflictingRevisionId} moved to " +
                     $"conflict scheme '{conflictSchemeName}'. Current head {currentRef.HeadRevisionId} retained."
        );
    }

    /// <summary>
    /// Detect whether two revisions share the same parent (potential conflict).
    /// </summary>
    public bool SharesParent(
        string documentId,
        string revisionIdA,
        string revisionIdB
    )
    {
        var revA = _revisions.Read(documentId, revisionIdA);
        var revB = _revisions.Read(documentId, revisionIdB);
        if (revA is null || revB is null)
            return false;

        // Both must have a non-null parent that matches.
        return !string.IsNullOrEmpty(revA.ParentRevisionId)
            && revA.ParentRevisionId == revB.ParentRevisionId
            && revisionIdA != revisionIdB;
    }
}
