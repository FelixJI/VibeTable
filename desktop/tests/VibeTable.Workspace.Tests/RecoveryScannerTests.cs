using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Reconciliation;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for the G2.4 recovery scanner.
/// </summary>
[TestClass]
public sealed class RecoveryScannerTests
{
    private static string MakeTempBackupRoot()
    {
        var dir = Path.Combine(Path.GetTempPath(), "vibetable-scan-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(dir);
        var backup = Path.Combine(dir, ".backup");
        Directory.CreateDirectory(backup);
        return backup;
    }

    [TestMethod]
    public void Scanner_ReportsResidualStaging()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            // Create a residual .partial file.
            var staging = Path.Combine(backup, ".staging");
            Directory.CreateDirectory(staging);
            File.WriteAllText(Path.Combine(staging, "abc.partial"), "incomplete");

            var json = new AtomicJsonStore();
            var scanner = new WorkspaceRecoveryScanner(
                backup,
                new ContentObjectStore(backup),
                new RevisionStore(backup, json),
                new RefStore(backup, json),
                json
            );

            var findings = scanner.Scan();
            Assert.IsTrue(findings.Any(f => f.Code == "residual_staging"));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void Scanner_ReportsMissingObject()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var revisions = new RevisionStore(backup, json);
            // Write a revision referencing a hash that doesn't exist in objects/.
            revisions.Write(new RevisionManifest(
                FormatVersion: 1,
                RevisionId: "rev-missing",
                DocumentId: "doc-1",
                SchemeId: "scheme-1",
                ParentRevisionId: null,
                SourceRevisionId: null,
                RestoredFromRevisionId: null,
                Sequence: 1,
                VersionLabel: "main/V1",
                Kind: RevisionKind.Formal,
                ContentHash: "nonexistenthash",
                Size: 100,
                MimeType: "application/octet-stream",
                WorkingRelativePath: "main.docx",
                CreatedAt: "2026-07-15T10:00:00Z",
                CreatedBy: "user-1",
                DeviceId: null,
                Comment: null
            ));

            var scanner = new WorkspaceRecoveryScanner(
                backup,
                new ContentObjectStore(backup),
                revisions,
                new RefStore(backup, json),
                json
            );

            var findings = scanner.Scan();
            Assert.IsTrue(findings.Any(f => f.Code == "missing_object" && f.Severity == WorkspaceRecoveryScanner.FindingSeverity.Error));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void Scanner_ReportsBrokenParent()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var objects = new ContentObjectStore(backup);
            var revisions = new RevisionStore(backup, json);

            // Commit an object so the revision's hash is valid.
            var workFile = Path.GetTempFileName();
            File.WriteAllText(workFile, "content");
            var objResult = objects.Commit(workFile);
            File.Delete(workFile);

            // Revision references a parent that doesn't exist.
            revisions.Write(new RevisionManifest(
                FormatVersion: 1,
                RevisionId: "rev-2",
                DocumentId: "doc-1",
                SchemeId: "scheme-1",
                ParentRevisionId: "ghost-parent",
                SourceRevisionId: null,
                RestoredFromRevisionId: null,
                Sequence: 2,
                VersionLabel: "main/V2",
                Kind: RevisionKind.Formal,
                ContentHash: objResult.ContentHash,
                Size: 7,
                MimeType: "application/octet-stream",
                WorkingRelativePath: "main.docx",
                CreatedAt: "2026-07-15T10:00:00Z",
                CreatedBy: "user-1",
                DeviceId: null,
                Comment: null
            ));

            var scanner = new WorkspaceRecoveryScanner(backup, objects, revisions, new RefStore(backup, json), json);
            var findings = scanner.Scan();
            Assert.IsTrue(findings.Any(f => f.Code == "broken_parent"));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void Scanner_ReportsOrphanRevision()
    {
        var backup = MakeTempBackupRoot();
        try
        {
            var json = new AtomicJsonStore();
            var objects = new ContentObjectStore(backup);
            var revisions = new RevisionStore(backup, json);
            var refs = new RefStore(backup, json);

            // Commit an object.
            var workFile = Path.GetTempFileName();
            File.WriteAllText(workFile, "content");
            var objResult = objects.Commit(workFile);
            File.Delete(workFile);

            // Write a revision but DON'T create a ref pointing to it.
            revisions.Write(new RevisionManifest(
                FormatVersion: 1,
                RevisionId: "rev-orphan",
                DocumentId: "doc-1",
                SchemeId: "scheme-1",
                ParentRevisionId: null,
                SourceRevisionId: null,
                RestoredFromRevisionId: null,
                Sequence: 1,
                VersionLabel: "main/V1",
                Kind: RevisionKind.Formal,
                ContentHash: objResult.ContentHash,
                Size: 7,
                MimeType: "application/octet-stream",
                WorkingRelativePath: "main.docx",
                CreatedAt: "2026-07-15T10:00:00Z",
                CreatedBy: "user-1",
                DeviceId: null,
                Comment: null
            ));

            var scanner = new WorkspaceRecoveryScanner(backup, objects, revisions, refs, json);
            var findings = scanner.Scan();
            Assert.IsTrue(findings.Any(f => f.Code == "orphan_revision"));
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    [TestMethod]
    public void Scanner_CleanWorkspace_NoFindings()
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
            service.InitializeScheme(new RefManifest(1, "doc-1", "scheme-1", "main", "", "main.docx", "2026-07-15T00:00:00Z"));

            var workFile = Path.GetTempFileName();
            File.WriteAllText(workFile, "clean content");
            service.CommitFormal(
                workFile, "main.docx", "doc-1", "scheme-1",
                null, 1, "main/V1", "application/octet-stream",
                "user-1", "device-1", null, "2026-07-15T10:00:00Z"
            );
            File.Delete(workFile);

            var scanner = new WorkspaceRecoveryScanner(backup, objects, revisions, refs, json);
            var findings = scanner.Scan();
            // A clean workspace should have zero error/warning findings.
            var problems = findings.Where(f => f.Severity != WorkspaceRecoveryScanner.FindingSeverity.Info).ToList();
            Assert.AreEqual(0, problems.Count, $"Unexpected findings: {string.Join(", ", problems.Select(f => f.Code))}");
        }
        finally
        {
            CleanupDir(Path.GetDirectoryName(backup)!);
        }
    }

    private static void CleanupDir(string dir)
    {
        try { if (Directory.Exists(dir)) Directory.Delete(dir, recursive: true); }
        catch { /* best effort */ }
    }
}
