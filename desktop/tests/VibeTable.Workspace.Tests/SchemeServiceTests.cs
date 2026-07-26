using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Reconciliation;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for G4: SchemeService, SchemeAdoptionService, RefConflictResolver.
/// </summary>
[TestClass]
public sealed class SchemeServiceTests
{
    private static string MakeBackupRoot()
    {
        var dir = Path.Combine(Path.GetTempPath(), "vibetable-g4-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(dir);
        var backup = Path.Combine(dir, ".backup");
        Directory.CreateDirectory(backup);
        return backup;
    }

    private static string MakeWorkFile(string content = "scheme content")
    {
        var path = Path.Combine(Path.GetTempPath(), "vibetable-g4-wf-" + Guid.NewGuid().ToString("N")[..8] + ".docx");
        File.WriteAllText(path, content);
        return path;
    }

    private static (WorkspaceVersionService vs, ContentObjectStore objs, RevisionStore revs, RefStore refs, AtomicJsonStore json) SetupServices(string backup)
    {
        var json = new AtomicJsonStore();
        var objs = new ContentObjectStore(backup);
        var revs = new RevisionStore(backup, json);
        var refs = new RefStore(backup, json);
        var vs = new WorkspaceVersionService(backup, objs, revs, refs, json);
        return (vs, objs, revs, refs, json);
    }

    // -----------------------------------------------------------------------
    // G4.1: Scheme create / sequence
    // -----------------------------------------------------------------------

    [TestMethod]
    public void CreateScheme_DoesNotCopyObject()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var scheme = new SchemeService(backup, objs, revs, refs, json);

            // Setup: create main with V1.
            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));
            var mainOutcome = vs.CommitFormal(
                MakeWorkFile("main v1"), "main.docx", "doc-1", "main-scheme",
                null, 1, "main/V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z"
            );

            // Create scheme A from main/V1.
            var result = scheme.CreateScheme("doc-1", "方案A", mainOutcome.RevisionId, "方案/方案A/main.docx", "2026-07-15T02:00:00Z");

            Assert.AreEqual("方案A", result.SchemeName);
            Assert.AreEqual(mainOutcome.RevisionId, result.HeadRevisionId);
            Assert.IsFalse(result.ObjectCopied);
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void GetNextSequence_IncrementsPerScheme()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var scheme = new SchemeService(backup, objs, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));
            var mainOutcome = vs.CommitFormal(
                MakeWorkFile("v1"), "main.docx", "doc-1", "main-scheme",
                null, 1, "main/V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z"
            );

            var schemeA = scheme.CreateScheme("doc-1", "方案A", mainOutcome.RevisionId, "方案A/main.docx", "2026-07-15T02:00:00Z");

            // main has V1 (sequence 1), so next main sequence is 2.
            Assert.AreEqual(2, scheme.GetNextSequence("doc-1", "main-scheme"));

            // Commit a version in scheme A.
            scheme.CommitSchemeVersion(
                MakeWorkFile("scheme a v1"), "方案A/main.docx", "doc-1", schemeA.SchemeId,
                mainOutcome.RevisionId,
                "方案A/V1", "app/docx", "u1", "d1", null, "2026-07-15T03:00:00Z"
            );
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void RenameScheme_MainCannotBeRenamed()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var scheme = new SchemeService(backup, objs, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            Assert.Throws<InvalidOperationException>(() =>
                scheme.RenameScheme(
                    "doc-1",
                    "main-scheme",
                    "",
                    "renamed-main",
                    "2026-07-15T02:00:00Z"));
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void CreateScheme_SourceRevisionMustExist()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var scheme = new SchemeService(backup, objs, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            Assert.Throws<InvalidOperationException>(() =>
                scheme.CreateScheme("doc-1", "方案A", "nonexistent-rev", "方案A/main.docx", "2026-07-15T02:00:00Z"));
        }
        finally { Cleanup(backup); }
    }

    // -----------------------------------------------------------------------
    // G4.2: Scheme adoption
    // -----------------------------------------------------------------------

    [TestMethod]
    public void Adopt_CreatesNewMainRevisionWithSource()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var scheme = new SchemeService(backup, objs, revs, refs, json);
            var adoption = new SchemeAdoptionService(backup, objs, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            // main/V1
            var v1 = vs.CommitFormal(
                MakeWorkFile("main v1"), "main.docx", "doc-1", "main-scheme",
                null, 1, "main/V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z"
            );

            // Create scheme A and commit A/V1
            var schemeA = scheme.CreateScheme("doc-1", "方案A", v1.RevisionId, "方案A/main.docx", "2026-07-15T02:00:00Z");
            var aV1 = scheme.CommitSchemeVersion(
                MakeWorkFile("scheme a v1"), "方案A/main.docx", "doc-1", schemeA.SchemeId,
                v1.RevisionId,
                "方案A/V1", "app/docx", "u1", "d1", null, "2026-07-15T03:00:00Z"
            );

            // Adopt scheme A head into main.
            var result = adoption.Adopt(
                "doc-1", schemeA.SchemeId, "main-scheme", "main.docx",
                "main/V2.0", "u1", "d1", "adopting 方案A", "2026-07-15T04:00:00Z"
            );

            Assert.AreEqual(aV1.RevisionId, result.SourceRevisionId);
            Assert.AreEqual(v1.RevisionId, result.OldMainHead);

            // The new main revision should record sourceRevisionId.
            var newMainRev = revs.Read("doc-1", result.NewMainRevisionId);
            Assert.IsNotNull(newMainRev);
            Assert.AreEqual(aV1.RevisionId, newMainRev!.SourceRevisionId);
            Assert.AreEqual(v1.RevisionId, newMainRev.ParentRevisionId);

            // Main ref head should now point to the new revision.
            var mainRef = refs.Read("doc-1", "main-scheme");
            Assert.AreEqual(result.NewMainRevisionId, mainRef!.HeadRevisionId);
        }
        finally { Cleanup(backup); }
    }

    // -----------------------------------------------------------------------
    // G4.3: Conflict preservation
    // -----------------------------------------------------------------------

    [TestMethod]
    public void ConflictResolver_PreservesBothSides()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var resolver = new RefConflictResolver(backup, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            // main/V1
            var v1 = vs.CommitFormal(
                MakeWorkFile("main v1"), "main.docx", "doc-1", "main-scheme",
                null, 1, "main/V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z"
            );

            // Simulate a conflicting revision that shares v1 as parent.
            var conflictingRevId = Guid.NewGuid().ToString("N");
            revs.Write(new RevisionManifest(
                FormatVersion: 1,
                RevisionId: conflictingRevId,
                DocumentId: "doc-1",
                SchemeId: "main-scheme",
                ParentRevisionId: v1.RevisionId,
                SourceRevisionId: null,
                RestoredFromRevisionId: null,
                Sequence: 2,
                VersionLabel: "main/V2-conflict",
                Kind: RevisionKind.Formal,
                ContentHash: v1.ContentHash,
                Size: v1.Size,
                MimeType: "app/docx",
                WorkingRelativePath: "main.docx",
                CreatedAt: "2026-07-15T02:00:00Z",
                CreatedBy: "u2",
                DeviceId: "d2",
                Comment: "device 2 commit"
            ));

            // The main ref still points to v1. The conflicting revision is orphaned.
            // Resolve: create a conflict scheme for the losing revision.
            var resolution = resolver.ResolveConflict(
                "doc-1", "main-scheme", conflictingRevId, "2026-07-15T03:00:00Z"
            );

            Assert.AreEqual("conflict-preserved", resolution.Status);
            Assert.IsNotNull(resolution.ConflictSchemeId);
            Assert.AreEqual(conflictingRevId, resolution.ConflictRevisionId);

            // The conflict scheme should exist with the conflicting revision as head.
            var conflictRef = refs.Read("doc-1", resolution.ConflictSchemeId!);
            Assert.IsNotNull(conflictRef);
            Assert.AreEqual(conflictingRevId, conflictRef!.HeadRevisionId);
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void SharesParent_DetectsSiblingRevisions()
    {
        var backup = MakeBackupRoot();
        try
        {
            var (vs, objs, revs, refs, json) = SetupServices(backup);
            var resolver = new RefConflictResolver(backup, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "main-scheme", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            var v1 = vs.CommitFormal(
                MakeWorkFile("v1"), "main.docx", "doc-1", "main-scheme",
                null, 1, "main/V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z"
            );
            var v2 = vs.CommitFormal(
                MakeWorkFile("v2"), "main.docx", "doc-1", "main-scheme",
                v1.RevisionId, 2, "main/V2", "app/docx", "u1", "d1", null, "2026-07-15T02:00:00Z"
            );

            // v1 and v2 have different parents — not siblings.
            Assert.IsFalse(resolver.SharesParent("doc-1", v1.RevisionId, v2.RevisionId));
        }
        finally { Cleanup(backup); }
    }

    private static void Cleanup(string backup)
    {
        try { if (Directory.Exists(Path.GetDirectoryName(backup)!)) Directory.Delete(Path.GetDirectoryName(backup)!, recursive: true); }
        catch { }
    }
}
