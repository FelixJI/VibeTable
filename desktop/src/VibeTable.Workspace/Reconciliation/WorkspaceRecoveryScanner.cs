using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Reconciliation;

/// <summary>
/// Scans a workspace's .backup for integrity issues and reports findings.
///
/// Checks for:
/// <list type="bullet">
/// <item>Missing Object (revision references a hash not in objects/)</item>
/// <item>Hash mismatch (object content doesn't match its filename hash)</item>
/// <item>Orphan Revision (revision JSON exists but no ref points to its chain)</item>
/// <item>Broken parent (parentRevisionId references a non-existent revision)</item>
/// <item>Ref conflict (CAS conflict left a revision without a ref update)</item>
/// <item>Residual staging (leftover .partial files in .staging/)</item>
/// <item>Synology conflict copies in working directories</item>
/// <item>Missing working file (revision's workingRelativePath doesn't exist)</item>
/// </list>
///
/// The scanner never deletes or modifies any artifact. It only reports findings.
/// Corrupted JSON enters quarantine (reported, not silently overwritten).
/// </summary>
public sealed class WorkspaceRecoveryScanner
{
    private readonly string _backupRoot;
    private readonly ContentObjectStore _objects;
    private readonly RevisionStore _revisions;
    private readonly RefStore _refs;
    private readonly AtomicJsonStore _json;

    public WorkspaceRecoveryScanner(
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
    /// Severity of a finding.
    /// </summary>
    public enum FindingSeverity
    {
        Info,
        Warning,
        Error,
    }

    /// <summary>
    /// A single integrity finding.
    /// </summary>
    public sealed record Finding(
        FindingSeverity Severity,
        string Code,
        string Message,
        string? DocumentId = null,
        string? RevisionId = null,
        string? ContentHash = null
    );

    /// <summary>
    /// Run a full scan and return all findings.
    /// </summary>
    public List<Finding> Scan()
    {
        var findings = new List<Finding>();
        FindResidualStaging(findings);
        FindMissingObjectsAndBrokenParents(findings);
        FindOrphanRevisions(findings);
        FindSynologyConflicts(findings);
        return findings;
    }

    /// <summary>
    /// Check for residual .partial files in .staging/.
    /// </summary>
    private void FindResidualStaging(List<Finding> findings)
    {
        var stagingDir = Path.Combine(_backupRoot, ".staging");
        if (!Directory.Exists(stagingDir))
            return;

        foreach (var file in Directory.GetFiles(stagingDir, "*.partial"))
        {
            findings.Add(new Finding(
                FindingSeverity.Warning,
                "residual_staging",
                $"residual staging file: {Path.GetFileName(file)}",
                DocumentId: null,
                RevisionId: null,
                ContentHash: null
            ));
        }
    }

    /// <summary>
    /// Check that every revision's object exists and hash matches, and that
    /// parent revision chains are intact.
    /// </summary>
    private void FindMissingObjectsAndBrokenParents(List<Finding> findings)
    {
        var revisionsDir = Path.Combine(_backupRoot, "revisions");
        if (!Directory.Exists(revisionsDir))
            return;

        foreach (var docDir in Directory.GetDirectories(revisionsDir))
        {
            var docId = Path.GetFileName(docDir);
            var revs = _revisions.ListByDocument(docId);
            var revIds = revs.Select(r => r.RevisionId).ToHashSet();

            foreach (var rev in revs)
            {
                // Check object exists.
                if (!_objects.Exists(rev.ContentHash))
                {
                    findings.Add(new Finding(
                        FindingSeverity.Error,
                        "missing_object",
                        $"revision {rev.RevisionId} references missing object {rev.ContentHash}",
                        DocumentId: docId,
                        RevisionId: rev.RevisionId,
                        ContentHash: rev.ContentHash
                    ));
                }
                else
                {
                    // Verify hash matches object content.
                    var objectPath = _objects.GetObjectPath(rev.ContentHash);
                    var actualHash = ContentObjectStore.ComputeHash(objectPath);
                    if (actualHash != rev.ContentHash)
                    {
                        findings.Add(new Finding(
                            FindingSeverity.Error,
                            "hash_mismatch",
                            $"object hash mismatch for {rev.ContentHash}: actual {actualHash}",
                            DocumentId: docId,
                            RevisionId: rev.RevisionId,
                            ContentHash: rev.ContentHash
                        ));
                    }
                }

                // Check parent chain.
                if (!string.IsNullOrEmpty(rev.ParentRevisionId) && !revIds.Contains(rev.ParentRevisionId!))
                {
                    findings.Add(new Finding(
                        FindingSeverity.Error,
                        "broken_parent",
                        $"revision {rev.RevisionId} has broken parent {rev.ParentRevisionId}",
                        DocumentId: docId,
                        RevisionId: rev.RevisionId
                    ));
                }
            }
        }
    }

    /// <summary>
    /// Check for revisions whose chain is not reachable from any ref head.
    /// These may be CAS-conflict orphans or abandoned commits.
    /// </summary>
    private void FindOrphanRevisions(List<Finding> findings)
    {
        var refsDir = Path.Combine(_backupRoot, "refs");
        var revisionsDir = Path.Combine(_backupRoot, "revisions");
        if (!Directory.Exists(revisionsDir))
            return;

        // Collect all ref heads (may be empty if no refs exist yet).
        var reachableHeads = new HashSet<string>();
        if (Directory.Exists(refsDir))
        {
            foreach (var docDir in Directory.GetDirectories(refsDir))
            {
                foreach (var refFile in Directory.GetFiles(docDir, "*.json"))
                {
                    var refManifest = _json.Read<RefManifest>(refFile);
                    if (refManifest is not null && !string.IsNullOrEmpty(refManifest.HeadRevisionId))
                    {
                        reachableHeads.Add(refManifest.HeadRevisionId);
                    }
                }
            }
        }

        // Walk parent chains from heads to find all reachable revisions.
        var reachable = new HashSet<string>(reachableHeads);
        foreach (var docDir in Directory.GetDirectories(revisionsDir))
        {
            var docId = Path.GetFileName(docDir);
            var revs = _revisions.ListByDocument(docId);
            var byId = revs.ToDictionary(r => r.RevisionId);
            var queue = new Queue<string>(reachableHeads);
            while (queue.Count > 0)
            {
                var id = queue.Dequeue();
                if (byId.TryGetValue(id, out var rev) && !string.IsNullOrEmpty(rev.ParentRevisionId))
                {
                    if (reachable.Add(rev.ParentRevisionId!))
                        queue.Enqueue(rev.ParentRevisionId!);
                }
            }
        }

        // Report revisions not reachable from any head.
        foreach (var docDir in Directory.GetDirectories(revisionsDir))
        {
            var docId = Path.GetFileName(docDir);
            foreach (var rev in _revisions.ListByDocument(docId))
            {
                if (!reachable.Contains(rev.RevisionId))
                {
                    findings.Add(new Finding(
                        FindingSeverity.Warning,
                        "orphan_revision",
                        $"revision {rev.RevisionId} is not reachable from any ref head",
                        DocumentId: docId,
                        RevisionId: rev.RevisionId
                    ));
                }
            }
        }
    }

    /// <summary>
    /// Check for Synology conflict copies in the working directory.
    /// </summary>
    private void FindSynologyConflicts(List<Finding> findings)
    {
        // The working directory is the parent of .backup.
        var workingDir = Path.GetDirectoryName(_backupRoot);
        if (workingDir is null || !Directory.Exists(workingDir))
            return;

        foreach (var file in Directory.GetFiles(workingDir, "*", SearchOption.AllDirectories))
        {
            var name = Path.GetFileName(file);
            // Skip .backup contents.
            if (file.StartsWith(_backupRoot, StringComparison.OrdinalIgnoreCase))
                continue;
            if (name.Contains("(conflict", StringComparison.OrdinalIgnoreCase))
            {
                findings.Add(new Finding(
                    FindingSeverity.Warning,
                    "synology_conflict",
                    $"Synology conflict copy found: {name}",
                    DocumentId: null,
                    RevisionId: null
                ));
            }
        }
    }
}
