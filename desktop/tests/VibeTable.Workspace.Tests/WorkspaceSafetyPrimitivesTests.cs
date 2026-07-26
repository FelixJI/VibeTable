using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

[TestClass]
public sealed class WorkspaceSafetyPrimitivesTests
{
    [TestMethod]
    public void ArchiveScheme_PersistsArchivedState_AndRejectsMain()
    {
        using var fixture = new VersionFixture();
        var schemes = fixture.CreateSchemeService();
        var branch = schemes.CreateScheme(
            "doc-1",
            "Proposal A",
            fixture.V1.RevisionId,
            "schemes/proposal-a/report.txt",
            "2026-07-26T00:02:00Z");

        schemes.ArchiveScheme(
            "doc-1",
            branch.SchemeId,
            branch.HeadRevisionId!,
            "2026-07-26T00:03:00Z");

        var persisted = new RefStore(fixture.BackupRoot, new AtomicJsonStore())
            .Read("doc-1", branch.SchemeId);
        Assert.IsNotNull(persisted);
        Assert.AreEqual(SchemeStatus.Archived, persisted.Status);
        Assert.Throws<InvalidOperationException>(() => schemes.ArchiveScheme(
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            "2026-07-26T00:04:00Z"));
    }

    [TestMethod]
    public void SchemeMetadataMutations_RejectStaleObservedHeadWithoutOverwritingRef()
    {
        using var fixture = new VersionFixture();
        var schemes = fixture.CreateSchemeService();
        var branch = schemes.CreateScheme(
            "doc-1",
            "Proposal A",
            fixture.V1.RevisionId,
            "schemes/proposal-a/report.txt",
            "2026-07-26T00:02:00Z");
        string branchPath = Path.Combine(
            fixture.Root,
            "schemes",
            "proposal-a",
            "report.txt");
        Directory.CreateDirectory(Path.GetDirectoryName(branchPath)!);
        File.WriteAllText(branchPath, "proposal version");
        var committed = schemes.CommitSchemeVersion(
            branchPath,
            "schemes/proposal-a/report.txt",
            "doc-1",
            branch.SchemeId,
            branch.HeadRevisionId!,
            "Proposal V1",
            "text/plain",
            "local",
            null,
            null,
            "2026-07-26T00:03:00Z");

        Assert.Throws<RefCasConflictException>(() => schemes.RenameScheme(
            "doc-1",
            branch.SchemeId,
            branch.HeadRevisionId!,
            "Stale rename",
            "2026-07-26T00:04:00Z"));
        Assert.Throws<RefCasConflictException>(() => schemes.ArchiveScheme(
            "doc-1",
            branch.SchemeId,
            branch.HeadRevisionId!,
            "2026-07-26T00:04:00Z"));

        var persisted = fixture.Refs.Read("doc-1", branch.SchemeId);
        Assert.IsNotNull(persisted);
        Assert.AreEqual(committed.RevisionId, persisted.HeadRevisionId);
        Assert.AreEqual("Proposal A", persisted.SchemeName);
        Assert.AreEqual(SchemeStatus.Active, persisted.Status);
    }

    [TestMethod]
    public void CommitSchemeVersion_UsesCallerExpectedHead_ForCas()
    {
        using var fixture = new VersionFixture();
        var schemes = fixture.CreateSchemeService();
        File.WriteAllText(fixture.WorkingPath, "version two");
        var v2 = schemes.CommitSchemeVersion(
            fixture.WorkingPath,
            "report.txt",
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            "V2",
            "text/plain",
            "local",
            null,
            null,
            "2026-07-26T00:02:00Z");
        File.WriteAllText(fixture.WorkingPath, "stale writer");

        var stale = schemes.CommitSchemeVersion(
            fixture.WorkingPath,
            "report.txt",
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            "V2-stale",
            "text/plain",
            "local",
            null,
            null,
            "2026-07-26T00:03:00Z");

        Assert.IsFalse(stale.RefUpdated);
        Assert.AreEqual(
            v2.RevisionId,
            fixture.Refs.Read("doc-1", VersionFixture.MainSchemeId)!.HeadRevisionId);
        var preserved = fixture.Revisions.Read("doc-1", stale.RevisionId);
        Assert.IsNotNull(preserved);
        Assert.AreEqual(fixture.V1.RevisionId, preserved.ParentRevisionId);
        Assert.IsTrue(fixture.Outbox.ListByDocument("doc-1")
            .Any(revision => revision.RevisionId == stale.RevisionId));
    }

    [TestMethod]
    public void RestoreRevisionAsFormal_CreatesNewRestoreRevisionWithoutRewritingHistory()
    {
        using var fixture = new VersionFixture();
        File.WriteAllText(fixture.WorkingPath, "version two");
        var v2 = fixture.Versions.CommitFormal(
            fixture.WorkingPath,
            "report.txt",
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            2,
            "V2",
            "text/plain",
            "local",
            null,
            null,
            "2026-07-26T00:02:00Z");

        var restored = fixture.Versions.RestoreRevisionAsFormal(
            "doc-1",
            VersionFixture.MainSchemeId,
            v2.RevisionId,
            fixture.V1.RevisionId,
            "Restore V1",
            "local",
            null,
            "restore prior content",
            "2026-07-26T00:03:00Z");

        Assert.IsTrue(restored.RefUpdated);
        var manifest = fixture.Revisions.Read("doc-1", restored.RevisionId);
        Assert.IsNotNull(manifest);
        Assert.AreEqual(RevisionKind.Restore, manifest.Kind);
        Assert.AreEqual(v2.RevisionId, manifest.ParentRevisionId);
        Assert.AreEqual(fixture.V1.RevisionId, manifest.RestoredFromRevisionId);
        Assert.AreEqual(fixture.V1.ContentHash, manifest.ContentHash);
        Assert.AreEqual(
            restored.RevisionId,
            fixture.Refs.Read("doc-1", VersionFixture.MainSchemeId)!.HeadRevisionId);
        Assert.AreEqual(3, fixture.Revisions.ListByDocument("doc-1").Count);
        Assert.IsTrue(fixture.Outbox.ListByDocument("doc-1")
            .Any(revision => revision.RevisionId == restored.RevisionId));
    }

    [TestMethod]
    public void RestoreRevisionAsFormal_OnCasConflict_PreservesRevisionAndTargetRef()
    {
        using var fixture = new VersionFixture();
        File.WriteAllText(fixture.WorkingPath, "version two");
        var v2 = fixture.Versions.CommitFormal(
            fixture.WorkingPath,
            "report.txt",
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            2,
            "V2",
            "text/plain",
            "local",
            null,
            null,
            "2026-07-26T00:02:00Z");

        var conflicted = fixture.Versions.RestoreRevisionAsFormal(
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            fixture.V1.RevisionId,
            "Stale restore",
            "local",
            null,
            null,
            "2026-07-26T00:03:00Z");

        Assert.IsFalse(conflicted.RefUpdated);
        Assert.AreEqual(
            v2.RevisionId,
            fixture.Refs.Read("doc-1", VersionFixture.MainSchemeId)!.HeadRevisionId);
        var preserved = fixture.Revisions.Read("doc-1", conflicted.RevisionId);
        Assert.IsNotNull(preserved);
        Assert.AreEqual(fixture.V1.RevisionId, preserved.ParentRevisionId);
        Assert.AreEqual(fixture.V1.RevisionId, preserved.RestoredFromRevisionId);
        Assert.IsTrue(fixture.Outbox.ListByDocument("doc-1")
            .Any(revision => revision.RevisionId == conflicted.RevisionId));
    }

    [TestMethod]
    public void CompensateRestoreHead_RollsBackOnlyTheExactRestoreHead()
    {
        using var fixture = new VersionFixture();
        File.WriteAllText(fixture.WorkingPath, "version two");
        var v2 = fixture.Versions.CommitFormal(
            fixture.WorkingPath,
            "report.txt",
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            2,
            "V2",
            "text/plain",
            "local",
            null,
            null,
            "2026-07-26T00:02:00Z");
        var restored = fixture.Versions.RestoreRevisionAsFormal(
            "doc-1",
            VersionFixture.MainSchemeId,
            v2.RevisionId,
            fixture.V1.RevisionId,
            "Restore V1",
            "local",
            null,
            null,
            "2026-07-26T00:03:00Z");

        var compensated = fixture.Versions.CompensateRestoreHead(
            "doc-1",
            VersionFixture.MainSchemeId,
            restored.RevisionId,
            v2.RevisionId,
            "2026-07-26T00:04:00Z");

        Assert.IsTrue(compensated.RefRolledBack);
        Assert.AreEqual(
            v2.RevisionId,
            fixture.Refs.Read("doc-1", VersionFixture.MainSchemeId)!.HeadRevisionId);
        Assert.IsNotNull(fixture.Revisions.Read("doc-1", restored.RevisionId));
        Assert.IsTrue(fixture.Outbox.ListByDocument("doc-1")
            .Any(revision => revision.RevisionId == restored.RevisionId));

        var secondAttempt = fixture.Versions.CompensateRestoreHead(
            "doc-1",
            VersionFixture.MainSchemeId,
            restored.RevisionId,
            v2.RevisionId,
            "2026-07-26T00:05:00Z");
        Assert.IsFalse(secondAttempt.RefRolledBack);
    }

    [TestMethod]
    public void RestoreTransactionJournal_RoundTripsStagesAndDeletes()
    {
        using var fixture = new VersionFixture();
        const string transactionId = "0123456789abcdef0123456789abcdef";
        const string restoreRevisionId = "fedcba9876543210fedcba9876543210";
        var journal = new WorkspaceVersionService.RestoreTransactionJournal(
            WorkspaceVersionService.RestoreTransactionJournal.CurrentFormatVersion,
            transactionId,
            "doc-1",
            VersionFixture.MainSchemeId,
            fixture.V1.RevisionId,
            restoreRevisionId,
            fixture.V1.RevisionId,
            "report.txt",
            $"report.txt.restore-{transactionId}.partial",
            fixture.V1.ContentHash,
            fixture.V1.Size,
            WorkspaceVersionService.RestoreTransactionStage.Prepared,
            "2026-07-26T00:02:00Z");

        fixture.Versions.PrepareRestoreTransaction(journal);
        var prepared = fixture.Versions.ListRestoreTransactions().Single();
        Assert.AreEqual(
            WorkspaceVersionService.RestoreTransactionStage.Prepared,
            prepared.Stage);

        var committed = fixture.Versions.MarkRestoreRefCommitted(transactionId);
        Assert.AreEqual(
            WorkspaceVersionService.RestoreTransactionStage.RefCommitted,
            committed.Stage);

        fixture.Versions.DeleteRestoreTransaction(transactionId);
        Assert.AreEqual(0, fixture.Versions.ListRestoreTransactions().Count);
    }

    private sealed class VersionFixture : IDisposable
    {
        public const string MainSchemeId = "main-scheme";

        public VersionFixture()
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-workspace-safety-" + Guid.NewGuid().ToString("N"));
            BackupRoot = Path.Combine(Root, ".backup");
            WorkingPath = Path.Combine(Root, "report.txt");
            Directory.CreateDirectory(Root);
            File.WriteAllText(WorkingPath, "version one");
            var json = new AtomicJsonStore();
            Objects = new ContentObjectStore(BackupRoot);
            Revisions = new RevisionStore(BackupRoot, json);
            Refs = new RefStore(BackupRoot, json);
            Outbox = new RevisionPublishOutboxStore(BackupRoot, json);
            Versions = new WorkspaceVersionService(
                BackupRoot,
                Objects,
                Revisions,
                Refs,
                json);
            Versions.InitializeWorkspace(new WorkspaceManifest(
                1,
                "workspace-1",
                "Workspace",
                "2026-07-26T00:00:00Z"));
            Versions.InitializeScheme(new RefManifest(
                1,
                "doc-1",
                MainSchemeId,
                "main",
                "",
                "report.txt",
                "2026-07-26T00:00:00Z"));
            V1 = Versions.CommitFormal(
                WorkingPath,
                "report.txt",
                "doc-1",
                MainSchemeId,
                null,
                1,
                "V1",
                "text/plain",
                "local",
                null,
                null,
                "2026-07-26T00:01:00Z");
        }

        public string Root { get; }
        public string BackupRoot { get; }
        public string WorkingPath { get; }
        public ContentObjectStore Objects { get; }
        public RevisionStore Revisions { get; }
        public RefStore Refs { get; }
        public RevisionPublishOutboxStore Outbox { get; }
        public WorkspaceVersionService Versions { get; }
        public WorkspaceVersionService.CommitOutcome V1 { get; }

        public SchemeService CreateSchemeService()
            => new(
                BackupRoot,
                Objects,
                Revisions,
                Refs,
                new AtomicJsonStore());

        public void Dispose()
        {
            try
            {
                if (Directory.Exists(Root))
                    Directory.Delete(Root, recursive: true);
            }
            catch
            {
            }
        }
    }
}
