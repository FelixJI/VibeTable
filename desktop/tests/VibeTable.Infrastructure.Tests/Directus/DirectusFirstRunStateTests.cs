using System;
using System.IO;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

[TestClass]
public sealed class DirectusFirstRunStateTests
{
    [TestMethod]
    public void Inspect_FreshDirectoryIsFresh()
    {
        WithTemporaryDirectory(dir =>
        {
            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsTrue(status.IsFresh);
            Assert.IsFalse(status.IsRuntimeReady);
            Assert.IsTrue(status.NeedsRuntimeInitialization);
            Assert.IsFalse(status.IsExperienceIncomplete);
            Assert.IsFalse(status.IsExperienceComplete);
        });
    }

    [TestMethod]
    public void Inspect_BootstrappedWithoutSchemaStillNeedsRuntimeInitialization()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");

            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsFalse(status.IsFresh);
            Assert.IsFalse(status.IsRuntimeReady);
            Assert.IsTrue(status.NeedsRuntimeInitialization);
            Assert.IsFalse(status.IsExperienceIncomplete);
        });
    }

    [TestMethod]
    public void Inspect_RuntimeReadyWithoutExperienceCanResumeNonDestructively()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.SchemaMarker), "ok");

            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsTrue(status.IsRuntimeReady);
            Assert.IsFalse(status.NeedsRuntimeInitialization);
            Assert.IsTrue(status.IsExperienceIncomplete);
        });
    }

    [TestMethod]
    public void MarkExperienceComplete_MarksExperienceComplete()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.SchemaMarker), "ok");

            DirectusFirstRunState.MarkExperienceComplete(dir);
            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsTrue(status.IsExperienceComplete);
            Assert.IsTrue(status.IsRuntimeReady);
            Assert.IsFalse(status.NeedsRuntimeInitialization);
            Assert.IsFalse(status.IsExperienceIncomplete);
        });
    }

    [TestMethod]
    public void ResetUncompletedBootstrap_InterruptedRuntimeRemovesMarkersAndDatabaseFile()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            string dataDir = Path.Combine(dir, "data");
            Directory.CreateDirectory(dataDir);
            File.WriteAllText(Path.Combine(dataDir, "directus.sqlite"), "sqlite-bytes");
            File.WriteAllText(Path.Combine(dir, ".env"), "ADMIN_EMAIL=admin@example.com");

            DirectusFirstRunState.ResetUncompletedBootstrap(dir);

            Assert.IsFalse(File.Exists(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker)));
            Assert.IsFalse(File.Exists(Path.Combine(dir, DirectusFirstRunState.SchemaMarker)));
            Assert.IsFalse(File.Exists(Path.Combine(dataDir, "directus.sqlite")),
                "the SQLite database file must be deleted");
            Assert.IsFalse(Directory.Exists(dataDir),
                "the data directory must be removed once empty");
            Assert.IsTrue(File.Exists(Path.Combine(dir, ".env")),
                ".env must be preserved so KEY/SECRET do not rotate");
        });
    }

    [TestMethod]
    public void ResetUncompletedBootstrap_DeletesWalAndShmSidecars()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            string dataDir = Path.Combine(dir, "data");
            Directory.CreateDirectory(dataDir);
            File.WriteAllText(Path.Combine(dataDir, "directus.sqlite"), "main");
            File.WriteAllText(Path.Combine(dataDir, "directus.sqlite-wal"), "wal");
            File.WriteAllText(Path.Combine(dataDir, "directus.sqlite-shm"), "shm");

            DirectusFirstRunState.ResetUncompletedBootstrap(dir);

            Assert.IsFalse(File.Exists(Path.Combine(dataDir, "directus.sqlite")));
            Assert.IsFalse(File.Exists(Path.Combine(dataDir, "directus.sqlite-wal")),
                "-wal sidecar must be deleted");
            Assert.IsFalse(File.Exists(Path.Combine(dataDir, "directus.sqlite-shm")),
                "-shm sidecar must be deleted");
        });
    }

    [TestMethod]
    public void ResetUncompletedBootstrap_RefusesWhenExperienceComplete()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.SchemaMarker), "ok");
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.ExperienceMarker), "ok");
            string dataDir = Path.Combine(dir, "data");
            Directory.CreateDirectory(dataDir);
            File.WriteAllText(Path.Combine(dataDir, "directus.sqlite"), "sqlite-bytes");

            DirectusFirstRunState.ResetUncompletedBootstrap(dir);

            // Safety gate: a completed experience must never be reset.
            Assert.IsTrue(File.Exists(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker)));
            Assert.IsTrue(File.Exists(Path.Combine(dir, DirectusFirstRunState.SchemaMarker)));
            Assert.IsTrue(File.Exists(Path.Combine(dataDir, "directus.sqlite")));
        });
    }

    [TestMethod]
    public void ResetUncompletedBootstrap_RefusesRuntimeReadyWithoutExperience()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.SchemaMarker), "ok");
            string dataDir = Path.Combine(dir, "data");
            Directory.CreateDirectory(dataDir);
            string database = Path.Combine(dataDir, "directus.sqlite");
            File.WriteAllText(database, "sqlite-bytes");

            DirectusFirstRunState.ResetUncompletedBootstrap(dir);

            Assert.IsTrue(File.Exists(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker)));
            Assert.IsTrue(File.Exists(Path.Combine(dir, DirectusFirstRunState.SchemaMarker)));
            Assert.IsTrue(File.Exists(database));
        });
    }

    [TestMethod]
    public void ResetUncompletedBootstrap_FreshDirectoryIsNoOp()
    {
        WithTemporaryDirectory(dir =>
        {
            // No markers, no database. Reset must not throw.
            DirectusFirstRunState.ResetUncompletedBootstrap(dir);

            var status = DirectusFirstRunState.Inspect(dir);
            Assert.IsTrue(status.IsFresh);
        });
    }

    private static void WithTemporaryDirectory(Action<string> body)
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-first-run-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try { body(root); }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { /* best-effort */ }
        }
    }
}
