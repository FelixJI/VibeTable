using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

[TestClass]
public sealed class DocumentCatalogStoreTests
{
    [TestMethod]
    public void DocumentAndFolder_RoundTrip_ResolvesWorkingPath()
    {
        string root = CreateTempRoot();
        try
        {
            var store = new DocumentCatalogStore(Path.Combine(root, ".backup"), new AtomicJsonStore());
            store.WriteFolder(new FolderManifest(
                1, "folder-1", "workspace-1", null, "项目资料", "active", "2026-07-20T00:00:00Z"));
            var document = new DocumentManifest(
                1, "document-1", "workspace-1", "folder-1", "方案.docx",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "active", "2026-07-20T00:00:00Z");
            store.WriteDocument(document);

            var read = store.ReadDocument("document-1");

            Assert.IsNotNull(read);
            Assert.AreEqual("项目资料/方案.docx", store.ResolveWorkingRelativePath(read!));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void ResolveWorkingPath_RejectsReservedInternalDirectory()
    {
        string root = CreateTempRoot();
        try
        {
            var store = new DocumentCatalogStore(Path.Combine(root, ".backup"), new AtomicJsonStore());
            Assert.Throws<InvalidOperationException>(() => store.WriteFolder(new FolderManifest(
                1, "folder-1", "workspace-1", null, ".backup/objects", "active",
                "2026-07-20T00:00:00Z")));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void IdentifierTraversal_IsRejected()
    {
        string root = CreateTempRoot();
        try
        {
            var store = new DocumentCatalogStore(Path.Combine(root, ".backup"), new AtomicJsonStore());
            Assert.Throws<ArgumentException>(() => store.ReadDocument("../workspace.json"));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static string CreateTempRoot()
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-catalog-" + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(root);
        return root;
    }
}
