using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for G5.2 integrity check and GC planning.
/// </summary>
[TestClass]
public sealed class IntegrityGcServiceTests
{
    private static string MakeBackupRoot()
    {
        var dir = Path.Combine(Path.GetTempPath(), "vibetable-gc-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(dir);
        var backup = Path.Combine(dir, ".backup");
        Directory.CreateDirectory(backup);
        return backup;
    }

    private static string MakeWorkFile(string content = "gc content")
    {
        var path = Path.Combine(Path.GetTempPath(), "vibetable-gc-wf-" + Guid.NewGuid().ToString("N")[..8] + ".bin");
        File.WriteAllText(path, content);
        return path;
    }

    [TestMethod]
    public void GenerateGcPlan_EmptyWorkspace_ReturnsZero()
    {
        var backup = MakeBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var service = new IntegrityGcService(
                backup, new ContentObjectStore(backup),
                new RevisionStore(backup, json),
                new RefStore(backup, json), json
            );
            var plan = service.GenerateGcPlan("2026-07-15T00:00:00Z");
            Assert.AreEqual(0, plan.TotalCandidates);
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void GenerateGcPlan_FormalRevisions_NotIncluded()
    {
        var backup = MakeBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var objs = new ContentObjectStore(backup);
            var revs = new RevisionStore(backup, json);
            var refs = new RefStore(backup, json);
            var vs = new WorkspaceVersionService(backup, objs, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "scheme-1", "main", "", "f.docx", "2026-07-15T00:00:00Z"));
            vs.CommitFormal(
                MakeWorkFile("formal"), "f.docx", "doc-1", "scheme-1",
                null, 1, "main/V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z"
            );

            var service = new IntegrityGcService(backup, objs, revs, refs, json);
            var plan = service.GenerateGcPlan("2026-07-15T02:00:00Z");
            // Formal revisions are NEVER GC candidates.
            Assert.AreEqual(0, plan.TotalCandidates);
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void IsObjectReferenced_DetectsSharing()
    {
        var backup = MakeBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var objs = new ContentObjectStore(backup);
            var revs = new RevisionStore(backup, json);
            var refs = new RefStore(backup, json);
            var vs = new WorkspaceVersionService(backup, objs, revs, refs, json);

            vs.InitializeWorkspace(new WorkspaceManifest(1, "ws-1", "T", "2026-07-15T00:00:00Z"));
            vs.InitializeScheme(new RefManifest(1, "doc-1", "scheme-1", "main", "", "f.docx", "2026-07-15T00:00:00Z"));

            // Commit two revisions with the same content (deduplication → same hash).
            var workFile = MakeWorkFile("same content");
            var r1 = vs.CommitFormal(workFile, "f.docx", "doc-1", "scheme-1",
                null, 1, "V1", "app/docx", "u1", "d1", null, "2026-07-15T01:00:00Z");
            var r2 = vs.CommitFormal(workFile, "f.docx", "doc-1", "scheme-1",
                r1.RevisionId, 2, "V2", "app/docx", "u1", "d1", null, "2026-07-15T02:00:00Z");

            var service = new IntegrityGcService(backup, objs, revs, refs, json);
            // The Object is referenced by both r1 and r2 (same content → same hash).
            // Excluding r1, r2 still references it.
            Assert.IsTrue(service.IsObjectReferenced(r1.ContentHash, r1.RevisionId, "doc-1"));
            // Excluding a nonexistent revision, both r1 and r2 reference it.
            Assert.IsTrue(service.IsObjectReferenced(r1.ContentHash, "nonexistent", "doc-1"));
        }
        finally { Cleanup(backup); }
    }

    [TestMethod]
    public void GcPlan_Note_MentionsQuarantineAndSafety()
    {
        var backup = MakeBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var service = new IntegrityGcService(
                backup, new ContentObjectStore(backup),
                new RevisionStore(backup, json),
                new RefStore(backup, json), json
            );
            var plan = service.GenerateGcPlan("2026-07-15T00:00:00Z");
            Assert.IsTrue(plan.Note.Contains("quarantine", StringComparison.OrdinalIgnoreCase));
            Assert.IsTrue(plan.Note.Contains("NEVER", StringComparison.OrdinalIgnoreCase));
        }
        finally { Cleanup(backup); }
    }

    private static void Cleanup(string backup)
    {
        try { if (Directory.Exists(Path.GetDirectoryName(backup)!)) Directory.Delete(Path.GetDirectoryName(backup)!, recursive: true); }
        catch { }
    }
}
