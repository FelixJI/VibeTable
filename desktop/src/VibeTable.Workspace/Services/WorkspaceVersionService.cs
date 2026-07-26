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

    public sealed record RefCompensationOutcome(
        bool RefRolledBack,
        string? ConflictMessage
    );

    public enum RestoreTransactionStage
    {
        Prepared,
        RefCommitted,
    }

    public sealed record RestoreTransactionJournal(
        int FormatVersion,
        string TransactionId,
        string DocumentId,
        string SchemeId,
        string PreviousHeadRevisionId,
        string RestoreRevisionId,
        string RestoredFromRevisionId,
        string WorkingRelativePath,
        string StagedRelativePath,
        string ContentHash,
        long Size,
        RestoreTransactionStage Stage,
        string CreatedAt)
    {
        public const int CurrentFormatVersion = 1;
    }

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
    /// Restores immutable historical content as a new formal restore revision.
    /// The method accepts no filesystem or object-store path. It reuses the
    /// selected revision's verified content object, parents the new revision to
    /// the caller-observed head, and advances the target ref with expected-head
    /// CAS. A conflict preserves and enqueues the new revision without moving
    /// the target ref.
    /// </summary>
    public CommitOutcome RestoreRevisionAsFormal(
        string documentId,
        string schemeId,
        string expectedHeadRevisionId,
        string restoredFromRevisionId,
        string versionLabel,
        string createdBy,
        string? deviceId,
        string? comment,
        string createdAt,
        string? revisionId = null)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(documentId);
        ArgumentException.ThrowIfNullOrWhiteSpace(schemeId);
        ArgumentNullException.ThrowIfNull(expectedHeadRevisionId);
        ArgumentException.ThrowIfNullOrWhiteSpace(restoredFromRevisionId);
        ArgumentException.ThrowIfNullOrWhiteSpace(versionLabel);
        createdAt = UtcRfc3339Timestamp.Canonicalize(
            createdAt,
            nameof(createdAt));

        var targetRef = _refs.Read(documentId, schemeId)
            ?? throw new InvalidOperationException(
                $"scheme {schemeId} not found in document {documentId}");
        var restoredFrom = _revisions.Read(documentId, restoredFromRevisionId)
            ?? throw new InvalidOperationException(
                $"restore revision {restoredFromRevisionId} not found in document {documentId}");
        if (!string.Equals(
            restoredFrom.DocumentId,
            documentId,
            StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                "restore revision must belong to the same document");
        }

        string? parentRevisionId = string.IsNullOrEmpty(expectedHeadRevisionId)
            ? null
            : expectedHeadRevisionId;
        int sequence;
        if (parentRevisionId is null)
        {
            sequence = 1;
        }
        else
        {
            var parent = _revisions.Read(documentId, parentRevisionId)
                ?? throw new InvalidOperationException(
                    $"expected head revision {parentRevisionId} not found in document {documentId}");
            sequence = string.Equals(parent.SchemeId, schemeId, StringComparison.Ordinal)
                ? parent.Sequence + 1
                : 1;
        }

        string objectPath = _objects.GetObjectPath(restoredFrom.ContentHash);
        if (!File.Exists(objectPath))
            throw new FileNotFoundException(
                "restore content object not found",
                restoredFrom.ContentHash);
        var objectInfo = new FileInfo(objectPath);
        if (objectInfo.Length != restoredFrom.Size
            || !string.Equals(
                ContentObjectStore.ComputeHash(objectPath),
                restoredFrom.ContentHash,
                StringComparison.Ordinal))
        {
            throw new InvalidDataException(
                "restore content object failed integrity verification");
        }

        revisionId = string.IsNullOrWhiteSpace(revisionId)
            ? Guid.NewGuid().ToString("N")
            : DocumentCatalogStore.ValidateIdentifier(
                revisionId,
                nameof(revisionId));
        var revision = new RevisionManifest(
            FormatVersion: RevisionManifest.CurrentFormatVersion,
            RevisionId: revisionId,
            DocumentId: documentId,
            SchemeId: schemeId,
            ParentRevisionId: parentRevisionId,
            SourceRevisionId: null,
            RestoredFromRevisionId: restoredFromRevisionId,
            Sequence: sequence,
            VersionLabel: versionLabel,
            Kind: RevisionKind.Restore,
            ContentHash: restoredFrom.ContentHash,
            Size: restoredFrom.Size,
            MimeType: restoredFrom.MimeType,
            WorkingRelativePath: targetRef.WorkingRelativePath,
            CreatedAt: createdAt,
            CreatedBy: createdBy,
            DeviceId: deviceId,
            Comment: comment);
        _revisions.Write(revision);

        bool refUpdated;
        string? conflictMessage = null;
        try
        {
            _refs.UpdateHead(
                documentId,
                schemeId,
                expectedHeadRevisionId,
                revisionId,
                createdAt);
            refUpdated = true;
        }
        catch (RefCasConflictException ex)
        {
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
        }

        _publishOutbox.Enqueue(revision);
        return new CommitOutcome(
            CommitStage.PublishPending,
            revision.ContentHash,
            revision.Size,
            revision.RevisionId,
            refUpdated,
            conflictMessage);
    }

    /// <summary>
    /// Best-effort compensation for a restore whose working-copy
    /// materialization failed after the restore ref was committed. The ref is
    /// moved back only when it still points to the exact restore revision.
    /// The immutable restore revision and its outbox entry are intentionally
    /// preserved for diagnostics and reconciliation.
    /// </summary>
    public RefCompensationOutcome CompensateRestoreHead(
        string documentId,
        string schemeId,
        string restoreRevisionId,
        string previousHeadRevisionId,
        string updatedAt)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(documentId);
        ArgumentException.ThrowIfNullOrWhiteSpace(schemeId);
        ArgumentException.ThrowIfNullOrWhiteSpace(restoreRevisionId);
        ArgumentNullException.ThrowIfNull(previousHeadRevisionId);
        updatedAt = UtcRfc3339Timestamp.Canonicalize(
            updatedAt,
            nameof(updatedAt));

        var restore = _revisions.Read(documentId, restoreRevisionId)
            ?? throw new InvalidOperationException(
                $"restore revision {restoreRevisionId} not found in document {documentId}");
        if (restore.Kind != RevisionKind.Restore
            || !string.Equals(restore.SchemeId, schemeId, StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                "only a restore revision from the target scheme can be compensated");
        }
        if (!string.IsNullOrEmpty(previousHeadRevisionId))
        {
            var previous = _revisions.Read(documentId, previousHeadRevisionId)
                ?? throw new InvalidOperationException(
                    $"previous head revision {previousHeadRevisionId} not found in document {documentId}");
            if (!string.Equals(
                previous.SchemeId,
                schemeId,
                StringComparison.Ordinal))
            {
                throw new InvalidOperationException(
                    "the compensation head must belong to the target scheme");
            }
        }

        try
        {
            _refs.UpdateHead(
                documentId,
                schemeId,
                restoreRevisionId,
                previousHeadRevisionId,
                updatedAt);
            return new RefCompensationOutcome(true, null);
        }
        catch (RefCasConflictException ex)
        {
            return new RefCompensationOutcome(false, ex.Message);
        }
    }

    public void PrepareRestoreTransaction(RestoreTransactionJournal transaction)
    {
        ValidateRestoreTransaction(transaction);
        string path = GetRestoreTransactionPath(transaction.TransactionId);
        if (File.Exists(path))
            throw new InvalidOperationException(
                $"restore transaction {transaction.TransactionId} already exists");
        _json.Write(path, transaction);
    }

    public RestoreTransactionJournal MarkRestoreRefCommitted(
        string transactionId)
    {
        string path = GetRestoreTransactionPath(transactionId);
        var current = _json.Read<RestoreTransactionJournal>(path)
            ?? throw new InvalidOperationException(
                $"restore transaction {transactionId} not found");
        ValidateRestoreTransaction(current);
        var updated = current with
        {
            Stage = RestoreTransactionStage.RefCommitted,
        };
        _json.Write(path, updated);
        return updated;
    }

    public IReadOnlyList<RestoreTransactionJournal> ListRestoreTransactions()
    {
        string directory = GetRestoreTransactionsDirectory();
        if (!Directory.Exists(directory))
            return [];

        var result = new List<RestoreTransactionJournal>();
        foreach (string file in Directory.GetFiles(directory, "*.json"))
        {
            var transaction = _json.Read<RestoreTransactionJournal>(file)
                ?? throw new InvalidDataException(
                    $"restore transaction journal is unreadable: {Path.GetFileName(file)}");
            ValidateRestoreTransaction(transaction);
            string expectedPath = GetRestoreTransactionPath(
                transaction.TransactionId);
            if (!string.Equals(
                Path.GetFullPath(file),
                Path.GetFullPath(expectedPath),
                StringComparison.OrdinalIgnoreCase))
            {
                throw new InvalidDataException(
                    "restore transaction filename does not match its transaction id");
            }
            result.Add(transaction);
        }
        return result
            .OrderBy(transaction => transaction.CreatedAt, StringComparer.Ordinal)
            .ThenBy(transaction => transaction.TransactionId, StringComparer.Ordinal)
            .ToArray();
    }

    public void DeleteRestoreTransaction(string transactionId)
    {
        string path = GetRestoreTransactionPath(transactionId);
        if (File.Exists(path)) File.Delete(path);
    }

    private string GetRestoreTransactionsDirectory()
        => Path.Combine(_backupRoot, "restore-transactions");

    private string GetRestoreTransactionPath(string transactionId)
        => Path.Combine(
            GetRestoreTransactionsDirectory(),
            DocumentCatalogStore.ValidateIdentifier(
                transactionId,
                nameof(transactionId)) + ".json");

    private static void ValidateRestoreTransaction(
        RestoreTransactionJournal transaction)
    {
        if (transaction.FormatVersion != RestoreTransactionJournal.CurrentFormatVersion)
            throw new InvalidDataException(
                "unsupported restore transaction journal format");
        DocumentCatalogStore.ValidateIdentifier(
            transaction.TransactionId,
            nameof(transaction.TransactionId));
        DocumentCatalogStore.ValidateIdentifier(
            transaction.DocumentId,
            nameof(transaction.DocumentId));
        DocumentCatalogStore.ValidateIdentifier(
            transaction.SchemeId,
            nameof(transaction.SchemeId));
        if (!string.IsNullOrEmpty(transaction.PreviousHeadRevisionId))
        {
            DocumentCatalogStore.ValidateIdentifier(
                transaction.PreviousHeadRevisionId,
                nameof(transaction.PreviousHeadRevisionId));
        }
        DocumentCatalogStore.ValidateIdentifier(
            transaction.RestoreRevisionId,
            nameof(transaction.RestoreRevisionId));
        DocumentCatalogStore.ValidateIdentifier(
            transaction.RestoredFromRevisionId,
            nameof(transaction.RestoredFromRevisionId));
        WorkspacePathGuard.ValidateRelativePath(transaction.WorkingRelativePath);
        WorkspacePathGuard.ValidateRelativePath(transaction.StagedRelativePath);
        string expectedStagedRelativePath =
            transaction.WorkingRelativePath
            + $".restore-{transaction.TransactionId}.partial";
        if (!string.Equals(
            transaction.StagedRelativePath,
            expectedStagedRelativePath,
            StringComparison.Ordinal))
        {
            throw new InvalidDataException(
                "restore transaction staging path is not bound to its target");
        }
        if (transaction.Size < 0
            || transaction.ContentHash.Length != 64
            || transaction.ContentHash.Any(character =>
                !Uri.IsHexDigit(character)))
        {
            throw new InvalidDataException(
                "restore transaction content identity is invalid");
        }
        _ = UtcRfc3339Timestamp.Canonicalize(
            transaction.CreatedAt,
            nameof(transaction.CreatedAt));
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
        Directory.CreateDirectory(GetRestoreTransactionsDirectory());

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
