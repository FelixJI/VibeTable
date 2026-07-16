using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Reconciliation;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Services;

/// <summary>
/// Integrity check, retention policy and garbage-collection planning.
///
/// G5.2 (implementation plan §10.2):
/// <list type="bullet">
/// <item>Periodic integrity check: Object hash, Revision parent chain,
///   Ref head consistency.</item>
/// <item>Retention: auto snapshots marked cleanable after 30 items or 30 days;
///   formal/restore revisions are permanent by default.</item>
/// <item>GC: generate a plan first, quarantine to a separate directory,
///   then physically delete only after retention period + independent backup.</item>
/// </list>
///
/// The GC NEVER deletes without a plan. The plan is reviewable and
/// reversible until physical deletion.
/// </summary>
public sealed class IntegrityGcService
{
    private readonly string _backupRoot;
    private readonly ContentObjectStore _objects;
    private readonly RevisionStore _revisions;
    private readonly RefStore _refs;
    private readonly AtomicJsonStore _json;

    /// <summary>
    /// Retention thresholds for auto snapshots.
    /// </summary>
    public const int MaxSnapshotsPerScheme = 30;
    public const int MaxSnapshotAgeDays = 30;

    private const string _safetyNote =
        "GC plan is read-only. Quarantine items to .backup/quarantine/ " +
        "before physical deletion. Formal and restore revisions are NEVER " +
        "included — only auto snapshots.";

    public IntegrityGcService(
        string backupRoot,
        ContentObjectStore objects,
        RevisionStore revisions,
        RefStore refs,
        AtomicJsonStore json
    )
    {
        _backupRoot = backupRoot;
        _objects = objects;
        _revisions = revisions;
        _refs = refs;
        _json = json;
    }

    /// <summary>
    /// An item in a GC plan.
    /// </summary>
    public sealed record GcPlanItem(
        string RevisionId,
        string DocumentId,
        string ContentHash,
        string Reason,
        bool CanDelete
    );

    /// <summary>
    /// A GC plan listing items eligible for cleanup.
    /// </summary>
    public sealed record GcPlan(
        int TotalCandidates,
        List<GcPlanItem> Items,
        string GeneratedAt,
        string Note
    );

    /// <summary>
    /// Generate a GC plan for cleanable snapshot revisions.
    ///
    /// This plan is READ-ONLY — it does not delete anything. The caller
    /// reviews the plan, quarantines items, and only physically deletes
    /// after confirming retention period + backup.
    /// </summary>
    public GcPlan GenerateGcPlan(string generatedAt)
    {
        var items = new List<GcPlanItem>();
        var revisionsDir = Path.Combine(_backupRoot, "revisions");
        if (!Directory.Exists(revisionsDir))
            return new GcPlan(0, items, generatedAt, _safetyNote);

        var cutoff = DateTime.UtcNow.AddDays(-MaxSnapshotAgeDays);

        foreach (var docDir in Directory.GetDirectories(revisionsDir))
        {
            var docId = Path.GetFileName(docDir);
            var revs = _revisions.ListByDocument(docId);

            // Group by scheme and find cleanable snapshots.
            var byScheme = revs.GroupBy(r => r.SchemeId);
            foreach (var group in byScheme)
            {
                var snapshots = group
                    .Where(r => r.Kind == RevisionKind.Snapshot)
                    .OrderBy(r => r.Sequence)
                    .ToList();

                // Keep the most recent N snapshots; mark older ones as cleanable.
                if (snapshots.Count > MaxSnapshotsPerScheme)
                {
                    var cleanable = snapshots.Take(snapshots.Count - MaxSnapshotsPerScheme);
                    foreach (var rev in cleanable)
                    {
                        items.Add(new GcPlanItem(
                            RevisionId: rev.RevisionId,
                            DocumentId: docId,
                            ContentHash: rev.ContentHash,
                            Reason: $"snapshot exceeds {MaxSnapshotsPerScheme}-item retention",
                            CanDelete: false // must verify no ref points to it + backup confirmed
                        ));
                    }
                }

                // Mark snapshots older than MaxSnapshotAgeDays as cleanable.
                foreach (var rev in snapshots)
                {
                    if (DateTime.TryParse(rev.CreatedAt, out var created) && created < cutoff)
                    {
                        // Only add if not already in the list.
                        if (!items.Any(i => i.RevisionId == rev.RevisionId))
                        {
                            items.Add(new GcPlanItem(
                                RevisionId: rev.RevisionId,
                                DocumentId: docId,
                                ContentHash: rev.ContentHash,
                                Reason: $"snapshot older than {MaxSnapshotAgeDays} days",
                                CanDelete: false
                            ));
                        }
                    }
                }
            }
        }

        return new GcPlan(
            TotalCandidates: items.Count,
            Items: items,
            GeneratedAt: generatedAt,
            Note: _safetyNote
        );
    }

    /// <summary>
    /// Check whether a revision's Object is referenced by any other revision.
    /// Used to determine if an Object can be safely GC'd after its last
    /// referencing revision is quarantined.
    /// </summary>
    public bool IsObjectReferenced(string contentHash, string excludeRevisionId, string documentId)
    {
        var revs = _revisions.ListByDocument(documentId);
        return revs.Any(r =>
            r.RevisionId != excludeRevisionId
            && r.ContentHash == contentHash);
    }
}
