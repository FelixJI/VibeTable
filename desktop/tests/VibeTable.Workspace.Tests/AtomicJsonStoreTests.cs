using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for the G2.2 AtomicJsonStore.
/// </summary>
[TestClass]
public sealed class AtomicJsonStoreTests
{
    [TestMethod]
    public void WriteThenRead_RoundTrips()
    {
        var tmpDir = Path.Combine(Path.GetTempPath(), "vibetable-store-" + Guid.NewGuid().ToString("N")[..8]);
        var path = Path.Combine(tmpDir, "workspace.json");
        try
        {
            Directory.CreateDirectory(tmpDir);
            var store = new AtomicJsonStore();
            var manifest = new WorkspaceManifest(
                FormatVersion: 1,
                WorkspaceId: "ws-001",
                Name: "Test",
                CreatedAt: "2026-07-15T10:00:00Z"
            );
            store.Write(path, manifest);

            var read = store.Read<WorkspaceManifest>(path);
            Assert.IsNotNull(read);
            Assert.AreEqual("ws-001", read!.WorkspaceId);
            Assert.AreEqual(1, read.FormatVersion);
        }
        finally
        {
            if (Directory.Exists(tmpDir))
                Directory.Delete(tmpDir, recursive: true);
        }
    }

    [TestMethod]
    public void Read_NonExistentFile_ReturnsDefault()
    {
        var store = new AtomicJsonStore();
        var result = store.Read<WorkspaceManifest>(Path.Combine(Path.GetTempPath(), "nonexistent.json"));
        Assert.IsNull(result);
    }

    [TestMethod]
    public void Write_OverwritesExistingAtomically()
    {
        var tmpDir = Path.Combine(Path.GetTempPath(), "vibetable-store-" + Guid.NewGuid().ToString("N")[..8]);
        var path = Path.Combine(tmpDir, "ref.json");
        try
        {
            Directory.CreateDirectory(tmpDir);
            var store = new AtomicJsonStore();

            store.Write(path, new RefManifest(
                FormatVersion: 1,
                DocumentId: "d-1",
                SchemeId: "s-1",
                SchemeName: "main",
                HeadRevisionId: "rev-1",
                WorkingRelativePath: "main.docx",
                UpdatedAt: "2026-07-15T10:00:00Z"
            ));

            store.Write(path, new RefManifest(
                FormatVersion: 1,
                DocumentId: "d-1",
                SchemeId: "s-1",
                SchemeName: "main",
                HeadRevisionId: "rev-2",
                WorkingRelativePath: "main.docx",
                UpdatedAt: "2026-07-15T11:00:00Z"
            ));

            var read = store.Read<RefManifest>(path);
            Assert.IsNotNull(read);
            Assert.AreEqual("rev-2", read!.HeadRevisionId);
        }
        finally
        {
            if (Directory.Exists(tmpDir))
                Directory.Delete(tmpDir, recursive: true);
        }
    }

    [TestMethod]
    public void Write_DoesNotLeaveTempFile()
    {
        var tmpDir = Path.Combine(Path.GetTempPath(), "vibetable-store-" + Guid.NewGuid().ToString("N")[..8]);
        var path = Path.Combine(tmpDir, "rev.json");
        try
        {
            Directory.CreateDirectory(tmpDir);
            var store = new AtomicJsonStore();
            store.Write(path, new
            {
                formatVersion = 1,
                revisionId = "rev-1",
            });

            Assert.IsFalse(File.Exists(path + ".tmp"), "temp file must not remain after write");
        }
        finally
        {
            if (Directory.Exists(tmpDir))
                Directory.Delete(tmpDir, recursive: true);
        }
    }

    [TestMethod]
    public void ReadWithFormatCheck_KnownVersion_ReturnsManifestAndKnown()
    {
        var tmpDir = Path.Combine(Path.GetTempPath(), "vibetable-store-" + Guid.NewGuid().ToString("N")[..8]);
        var path = Path.Combine(tmpDir, "workspace.json");
        try
        {
            Directory.CreateDirectory(tmpDir);
            var store = new AtomicJsonStore();
            store.Write(path, new WorkspaceManifest(1, "ws-1", "Test", "2026-01-01"));

            var (manifest, known) = store.ReadWithFormatCheck<WorkspaceManifest>(
                path,
                expectedFormatVersion: 1,
                getFormatVersion: m => m.FormatVersion
            );
            Assert.IsNotNull(manifest);
            Assert.IsTrue(known);
        }
        finally
        {
            if (Directory.Exists(tmpDir))
                Directory.Delete(tmpDir, recursive: true);
        }
    }

    [TestMethod]
    public void ReadWithFormatCheck_UnknownVersion_ReturnsManifestAndUnknown()
    {
        var tmpDir = Path.Combine(Path.GetTempPath(), "vibetable-store-" + Guid.NewGuid().ToString("N")[..8]);
        var path = Path.Combine(tmpDir, "workspace.json");
        try
        {
            Directory.CreateDirectory(tmpDir);
            var store = new AtomicJsonStore();
            // Write a future format version that the current code doesn't know.
            store.Write(path, new WorkspaceManifest(99, "ws-1", "Future", "2026-01-01"));

            var (manifest, known) = store.ReadWithFormatCheck<WorkspaceManifest>(
                path,
                expectedFormatVersion: 1,
                getFormatVersion: m => m.FormatVersion
            );
            Assert.IsNotNull(manifest);
            Assert.IsFalse(known);
        }
        finally
        {
            if (Directory.Exists(tmpDir))
                Directory.Delete(tmpDir, recursive: true);
        }
    }
}
