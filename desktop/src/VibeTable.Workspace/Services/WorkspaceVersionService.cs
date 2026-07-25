using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Reconciliation;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Services;

/// <summary>
/// Orchestrates the atomic commit of a file version into the .backup kernel.
///
/// Commit state machine:
/// <code>
/// Dirty → StableRead → StageCopy → HashVerified → ObjectCommitted
///      → RevisionCommitted → RefCASCommitted → PublishPending
/// </code>
///
/// Each step can be retried after a crash. The scanner continues from the
/// last immutable artifact or isolates incomplete staging.
/// </summary>
public sealed class WorkspaceVersionService
{
    private readonly string _backupRoot;
    private readonly string _stagingRoot;
    private readonly ContentObjectStore _objects;
    private readonly RevisionStore _revisions;
    private readonly RefStore _refs;
    private readonly AtomicJsonStore _json;
    private readonly RevisionPublishOutboxStore _publishOutbox;

    public WorkspaceVersionService(
        string backupRoot,
        ContentObjectStore objects,
        RevisionStore revisions,
        RefStore refs,
        AtomicJsonStore json
    )
    {
        _backupRoot = backupRoot;
        _stagingRoot = Path.Combine(backupRoot, ".staging");
        _objects = objects;
        _revisions = revisions;
        _refs = refs;
        _json = json;
        _publishOutbox = new RevisionPublishOutboxStore(backupRoot, json);
    }

    /// <summary>
    /// The stages of the commit state machine, for diagnostics and recovery.
    /// </summary>
    public enum CommitStage
    {
        Dirty,
        StableRead,
        StageCopy,
        HashVerified,
        ObjectCommitted,
        RevisionCommitted,
        RefCasCommitted,
        PublishPending,
    }

    /// <summary>
    /// Result of a formal commit.
    /// </summary>
    public sealed record CommitOutcome(
        CommitStage FinalStage,
        string ContentHash,
        long Size,
        string RevisionId,
        bool RefUpdated,
        string? ConflictMessage
    );

    /// <summary>
    /// Commit a file as a formal version.
    ///
    /// Steps:
    /// 1. StableRead — verify the working file is readable.
    /// 2. StageCopy — copy to .staging/*.partial.
    /// 3. HashVerified — compute SHA-256 of staging copy; re-check working file unchanged.
    /// 4. ObjectCommitted — atomically move/copy to objects/{hash}.blob.
    /// 5. RevisionCommitted — write immutable Revision JSON.
    /// 6. RefCASCommitted — expected-head CAS update of Ref.
    /// 7. PublishPending — return for the metadata publisher/outbox.
    /// </summary>
    public CommitOutcome CommitFormal(
        string workingFilePath,
        string workingRelativePath,
        string documentId,
        string schemeId,
        string? parentRevisionId,
        int sequence,
        string versionLabel,
        string mimeType,
        string createdBy,
        string? deviceId,
        string? comment,
        string createdAt
    )
    {
        // Validate the relative path.
        WorkspacePathGuard.ValidateRelativePath(workingRelativePath);
        createdAt = UtcRfc3339Timestamp.Canonicalize(
            createdAt,
            nameof(createdAt));
        ValidateRevisionParent(
            documentId,
            schemeId,
            parentRevisionId,
            sequence);

        // Stage 1: StableRead.
        if (!File.Exists(workingFilePath))
            throw new FileNotFoundException("working file not found", workingFilePath);
        var stage = CommitStage.StableRead;

        // Stage 2: StageCopy.
        Directory.CreateDirectory(_stagingRoot);
        var stagingPath = Path.Combine(_stagingRoot, Guid.NewGuid().ToString("N") + ".partial");
        File.Copy(workingFilePath, stagingPath, overwrite: true);
        stage = CommitStage.StageCopy;

        // Stage 3: HashVerified.
        var hash = ContentObjectStore.ComputeHash(stagingPath);
        var size = new FileInfo(stagingPath).Length;
        // Re-check working file hasn't changed during staging.
        var workingHash = ContentObjectStore.ComputeHash(workingFilePath);
        if (workingHash != hash)
        {
            // Clean up staging.
            TryDelete(stagingPath);
            throw new InvalidOperationException(
                "working file changed during staging; aborting commit");
        }
        stage = CommitStage.HashVerified;

        // Stage 4: ObjectCommitted.
        var objectPath = _objects.GetObjectPath(hash);
        if (!File.Exists(objectPath))
        {
            Directory.CreateDirectory(Path.GetDirectoryName(objectPath)!);
            // Move the staging file to the object store (atomic on same volume).
            File.Move(stagingPath, objectPath);
        }
        else
        {
            // Object already exists (deduplication); clean up staging.
            TryDelete(stagingPath);
        }
        stage = CommitStage.ObjectCommitted;

        // Verify object hash.
        var verifyHash = ContentObjectStore.ComputeHash(objectPath);
        if (verifyHash != hash)
            throw new InvalidOperationException("object hash verification failed after commit");
        stage = CommitStage.ObjectCommitted;

        // Stage 5: RevisionCommitted.
        var revisionId = Guid.NewGuid().ToString("N");
        var revision = new RevisionManifest(
                    FormatVersion: RevisionManifest.CurrentFormatVersion,
                    RevisionId: revisionId,
                    DocumentId: documentId,
                    SchemeId: schemeId,
                    ParentRevisionId: parentRevisionId,
                    SourceRevisionId: null,
                    RestoredFromRevisionId: null,
                    Sequence: sequence,
                    VersionLabel: versionLabel,
                    Kind: RevisionKind.Formal,
                    ContentHash: hash,
                    Size: size,
                    MimeType: mimeType,
                    WorkingRelativePath: workingRelativePath,
                    CreatedAt: createdAt,
                    CreatedBy: createdBy,
                    DeviceId: deviceId,
                    Comment: comment
                );
        _revisions.Write(revision);
        stage = CommitStage.RevisionCommitted;

        // Stage 6: RefCASCommitted.
        bool refUpdated;
        string? conflictMessage = null;
        try
        {
            var expectedHead = parentRevisionId ?? "";
            _refs.UpdateHead(documentId, schemeId, expectedHead, revisionId, createdAt);
            refUpdated = true;
            stage = CommitStage.RefCasCommitted;
            _publishOutbox.Enqueue(revision);
            stage = CommitStage.PublishPending;
        }
        catch (RefCasConflictException ex)
        {
            // CAS conflict — preserve the revision, do not overwrite the ref.
            // The revision still belongs in the durable metadata publication
            // stream even though it must never advance the main ref.
            refUpdated = false;
            conflictMessage = ex.Message;
            new RefConflictResolver(
                _backupRoot,
                _revisions,
                _refs,
                _json)
                .ResolveConflict(
                    documentId,
                    schemeId,
                    revisionId,
                    createdAt);
            _publishOutbox.Enqueue(revision);
            stage = CommitStage.PublishPending;
        }

        return new CommitOutcome(stage, hash, size, revisionId, refUpdated, conflictMessage);
    }

    /// <summary>
    /// Initialize a workspace's .backup structure.
    /// </summary>
    public void InitializeWorkspace(WorkspaceManifest manifest)
    {
        Directory.CreateDirectory(_backupRoot);
        Directory.CreateDirectory(_stagingRoot);
        Directory.CreateDirectory(Path.Combine(_backupRoot, "objects"));
        Directory.CreateDirectory(Path.Combine(_backupRoot, "revisions"));
        Directory.CreateDirectory(Path.Combine(_backupRoot, "refs"));
        Directory.CreateDirectory(Path.Combine(_backupRoot, "documents"));
        Directory.CreateDirectory(Path.Combine(_backupRoot, "folders"));
        Directory.CreateDirectory(Path.Combine(_backupRoot, "outbox", "revisions"));

        var workspacePath = Path.Combine(_backupRoot, "workspace.json");
        _json.Write(workspacePath, manifest);
    }

    /// <summary>
    /// Initialize a scheme ref (only if it does not exist yet).
    /// </summary>
    public void InitializeScheme(RefManifest refManifest)
    {
        _refs.Initialize(refManifest);
    }

    private static void TryDelete(string path)
    {
        try { if (File.Exists(path)) File.Delete(path); }
        catch { /* best effort during cleanup */ }
    }

    private void ValidateRevisionParent(
        string documentId,
        string schemeId,
        string? parentRevisionId,
        int sequence)
    {
        if (parentRevisionId is null)
        {
            if (sequence != 1)
                throw new InvalidOperationException(
                    "a root revision must have sequence 1");
            return;
        }

        var parent = _revisions.Read(documentId, parentRevisionId)
            ?? throw new InvalidOperationException(
                $"parent revision {parentRevisionId} not found in document {documentId}");
        if (!string.Equals(parent.DocumentId, documentId, StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                "parent revision must belong to the same document and scheme");
        }
        if (!string.Equals(parent.SchemeId, schemeId, StringComparison.Ordinal))
        {
            // A newly-created scheme initially points at its source revision.
            // Its first local revision is the only valid cross-scheme edge.
            var targetRef = _refs.Read(documentId, schemeId);
            bool isBranchRoot = sequence == 1
                && _revisions.ListByScheme(documentId, schemeId).Count == 0
                && string.Equals(
                    targetRef?.HeadRevisionId,
                    parentRevisionId,
                    StringComparison.Ordinal);
            if (!isBranchRoot)
            {
                throw new InvalidOperationException(
                    "parent revision must belong to the same document and scheme");
            }
            return;
        }
        if (sequence != parent.Sequence + 1)
            throw new InvalidOperationException(
                "revision sequence must immediately follow its parent");
    }
}
