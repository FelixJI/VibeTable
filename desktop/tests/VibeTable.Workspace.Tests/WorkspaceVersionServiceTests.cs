using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for the G2.3 Object/Revision/Ref atomic commit stores and
/// WorkspaceVersionService.
/// </summary>
[TestClass]
public sealed class WorkspaceVersionServiceTests
{
    private static string MakeTempBackupRoot()
    {
        var dir = Path.Combine(Path.GetTempPath(), "vibetable-ws-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(dir);
        Directory.CreateDirectory(Path.Combine(dir, ".backup"));
        return Path.Combine(dir, ".backup");
    }

    private static string MakeWorkingFile(string content = "Hello VibeTable")
    {
        var path = Path.Combine(Path.GetTempPath(), "vibetable-wf-" + Guid.NewGuid().ToString("N")[..8] + ".docx");
        File.WriteAllText(path, content);
        return path;
    }

    [TestMethod]
    public void ObjectStore_Commit_CreatesBlobAndReturnsHash()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var store = new ContentObjectStore(backup);
            var workFile = MakeWorkingFile("test content");
            var result = store.Commit(workFile);

            Assert.IsFalse(string.IsNullOrEmpty(result.ContentHash));
            Assert.AreEqual(12, result.Size);
            Assert.IsFalse(result.AlreadyExisted);
            Assert.IsTrue(store.Exists(result.ContentHash));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void ObjectStore_SameContent_Deduplicates()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var store = new ContentObjectStore(backup);
            var f1 = MakeWorkingFile("same");
            var f2 = MakeWorkingFile("same");

            var r1 = store.Commit(f1);
            var r2 = store.Commit(f2);

            Assert.AreEqual(r1.ContentHash, r2.ContentHash);
            Assert.IsTrue(r2.AlreadyExisted);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void ObjectStore_DifferentContent_DifferentHash()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var store = new ContentObjectStore(backup);
            var r1 = store.Commit(MakeWorkingFile("AAA"));
            var r2 = store.Commit(MakeWorkingFile("BBB"));
            Assert.AreNotEqual(r1.ContentHash, r2.ContentHash);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void ObjectStore_Restore_WritesContentBack()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var store = new ContentObjectStore(backup);
            var result = store.Commit(MakeWorkingFile("restore me"));

            var target = Path.Combine(backup, "restored.docx");
            store.Restore(result.ContentHash, target);

            Assert.IsTrue(File.Exists(target));
            Assert.AreEqual("restore me", File.ReadAllText(target));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void RevisionStore_WriteThenRead_RoundTrips()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var store = new RevisionStore(backup, json);
            var rev = MakeTestRevision("rev-1", "doc-1", "scheme-1");

            store.Write(rev);
            var read = store.Read("doc-1", "rev-1");

            Assert.IsNotNull(read);
            Assert.AreEqual("rev-1", read!.RevisionId);
            Assert.AreEqual("doc-1", read.DocumentId);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void RevisionStore_Overwrite_Identical_IsIdempotent()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var store = new RevisionStore(backup, json);
            var rev = MakeTestRevision("rev-1", "doc-1", "scheme-1");

            store.Write(rev);
            // Writing the same revision again should not throw (idempotent).
            store.Write(rev);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void RevisionStore_Overwrite_DifferentContent_Rejected()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var store = new RevisionStore(backup, json);
            var rev1 = MakeTestRevision("rev-1", "doc-1", "scheme-1", contentHash: "hash-a");
            store.Write(rev1);

            var rev2 = MakeTestRevision("rev-1", "doc-1", "scheme-1", contentHash: "hash-b");
            Assert.Throws<InvalidOperationException>(() => store.Write(rev2));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void RefStore_CAS_Update_Succeeds_WhenExpectedMatches()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var store = new RefStore(backup, json);
            var init = new RefManifest(1, "doc-1", "scheme-1", "main", "rev-1", "main.docx", "t1");
            store.Initialize(init);

            var updated = store.UpdateHead("doc-1", "scheme-1", "rev-1", "rev-2", "t2");
            Assert.AreEqual("rev-2", updated.HeadRevisionId);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void RefStore_CAS_Update_Fails_WhenExpectedMismatch()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var store = new RefStore(backup, json);
            var init = new RefManifest(1, "doc-1", "scheme-1", "main", "rev-1", "main.docx", "t1");
            store.Initialize(init);

            // Try to update with wrong expected head.
            Assert.Throws<RefCasConflictException>(() =>
                store.UpdateHead("doc-1", "scheme-1", "WRONG", "rev-2", "t2"));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void VersionService_CommitFormal_FullPipeline()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var objects = new ContentObjectStore(backup);
            var revisions = new RevisionStore(backup, json);
            var refs = new RefStore(backup, json);
            var service = new WorkspaceVersionService(backup, objects, revisions, refs, json);

            // Initialize workspace + scheme.
            service.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "Test", "2026-07-15T00:00:00Z"));
            service.InitializeScheme(new RefManifest(1, "doc-1", "scheme-1", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            var workFile = MakeWorkingFile("formal content");
            var outcome = service.CommitFormal(
                workingFilePath: workFile,
                workingRelativePath: "main.docx",
                documentId: "doc-1",
                schemeId: "scheme-1",
                parentRevisionId: null,
                sequence: 1,
                versionLabel: "main/V1",
                mimeType: "application/octet-stream",
                createdBy: "user-1",
                deviceId: "device-1",
                comment: "initial version",
                createdAt: "2026-07-15T10:00:00Z"
            );

            Assert.AreEqual(WorkspaceVersionService.CommitStage.RefCasCommitted, outcome.FinalStage);
            Assert.IsTrue(outcome.RefUpdated);
            Assert.IsFalse(string.IsNullOrEmpty(outcome.ContentHash));
            Assert.IsFalse(string.IsNullOrEmpty(outcome.RevisionId));

            // Verify the revision was written.
            var rev = revisions.Read("doc-1", outcome.RevisionId);
            Assert.IsNotNull(rev);
            Assert.AreEqual("main/V1", rev!.VersionLabel);

            // Verify the ref head was updated.
            var refHead = refs.Read("doc-1", "scheme-1");
            Assert.IsNotNull(refHead);
            Assert.AreEqual(outcome.RevisionId, refHead!.HeadRevisionId);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void VersionService_CAS_Conlict_PreservesRevision()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var objects = new ContentObjectStore(backup);
            var revisions = new RevisionStore(backup, json);
            var refs = new RefStore(backup, json);
            var service = new WorkspaceVersionService(backup, objects, revisions, refs, json);

            service.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "Test", "2026-07-15T00:00:00Z"));
            // Set ref head to "rev-X" (simulating a concurrent commit).
            service.InitializeScheme(new RefManifest(1, "doc-1", "scheme-1", "main", "rev-X", "main.docx", "2026-07-15T00:00:00Z"));

            var workFile = MakeWorkingFile("conflicting content");
            // Try to commit with parentRevisionId=null, but the ref head is "rev-X"
            // (expected head "" != actual "rev-X" → CAS conflict).
            var outcome = service.CommitFormal(
                workingFilePath: workFile,
                workingRelativePath: "main.docx",
                documentId: "doc-1",
                schemeId: "scheme-1",
                parentRevisionId: null,
                sequence: 1,
                versionLabel: "main/V1",
                mimeType: "application/octet-stream",
                createdBy: "user-1",
                deviceId: "device-1",
                comment: "should conflict",
                createdAt: "2026-07-15T10:00:00Z"
            );

            // The revision is committed but the ref is NOT updated.
            Assert.AreEqual(WorkspaceVersionService.CommitStage.RevisionCommitted, outcome.FinalStage);
            Assert.IsFalse(outcome.RefUpdated);
            Assert.IsNotNull(outcome.ConflictMessage);

            // The ref head is still "rev-X".
            var refHead = refs.Read("doc-1", "scheme-1");
            Assert.IsNotNull(refHead);
            Assert.AreEqual("rev-X", refHead!.HeadRevisionId);

            // But the revision exists.
            var rev = revisions.Read("doc-1", outcome.RevisionId);
            Assert.IsNotNull(rev);
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    private static RevisionManifest MakeTestRevision(
        string revId,
        string docId,
        string schemeId,
        string contentHash = "abcd1234"
    )
    {
        return new RevisionManifest(
            FormatVersion: 1,
            RevisionId: revId,
            DocumentId: docId,
            SchemeId: schemeId,
            ParentRevisionId: null,
            SourceRevisionId: null,
            RestoredFromRevisionId: null,
            Sequence: 1,
            VersionLabel: "main/V1",
            Kind: RevisionKind.Formal,
            ContentHash: contentHash,
            Size: 100,
            MimeType: "application/octet-stream",
            WorkingRelativePath: "main.docx",
            CreatedAt: "2026-07-15T10:00:00Z",
            CreatedBy: "user-1",
            DeviceId: "device-1",
            Comment: "test"
        );
    }

    private static void CleanupDir(string dir)
    {
        try { if (Directory.Exists(dir)) Directory.Delete(dir, recursive: true); }
        catch { /* best effort */ }
    }
}
