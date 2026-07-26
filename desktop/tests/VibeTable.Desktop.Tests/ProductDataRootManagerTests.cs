using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductDataRootManagerTests
{
    [TestMethod]
    public void DefaultAndSelectedRootsUseOneStableProductFolder()
    {
        string program = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "program");
        string selected = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "selected");

        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(program, "VibeTableData")),
            ProductDataRootManager.DefaultDataRoot(program));
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(selected, "VibeTableData")),
            ProductDataRootManager.DataRootForSelectedFolder(selected));
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(selected, "VibeTableData")),
            ProductDataRootManager.DataRootForSelectedFolder(
                Path.Combine(selected, "VibeTableData")));
    }

    [TestMethod]
    public void TransactionalMigrationCopiesAndVerifiesWithoutDeletingSource()
    {
        string sandbox = Path.Combine(
            Path.GetTempPath(),
            "VibeTable.DataRoot.Tests",
            Guid.NewGuid().ToString("N"));
        string source = Path.Combine(sandbox, "source");
        string target = Path.Combine(sandbox, "target");
        try
        {
            Directory.CreateDirectory(Path.Combine(source, "pocketbase"));
            File.WriteAllText(
                Path.Combine(source, "pocketbase", "data.db"),
                "database");
            File.WriteAllText(Path.Combine(source, "settings.json"), "{}");

            ProductDataRootManager.MigrateDirectoryTransactional(
                source,
                target);

            Assert.IsTrue(File.Exists(
                Path.Combine(target, "pocketbase", "data.db")));
            Assert.AreEqual(
                "{}",
                File.ReadAllText(Path.Combine(target, "settings.json")));
            Assert.IsTrue(File.Exists(
                Path.Combine(target, ".vibetable-data-root.json")));
            Assert.IsTrue(Directory.Exists(source));
            Assert.IsTrue(File.Exists(
                Path.Combine(source, "pocketbase", "data.db")));
        }
        finally
        {
            if (Directory.Exists(sandbox))
            {
                Directory.Delete(sandbox, recursive: true);
            }
        }
    }

    [TestMethod]
    public void TransactionalMigrationRejectsNestedTargets()
    {
        string sandbox = Path.Combine(
            Path.GetTempPath(),
            "VibeTable.DataRoot.Tests",
            Guid.NewGuid().ToString("N"));
        string source = Path.Combine(sandbox, "source");
        try
        {
            Directory.CreateDirectory(source);
            Assert.ThrowsExactly<InvalidOperationException>(() =>
                ProductDataRootManager.MigrateDirectoryTransactional(
                    source,
                    Path.Combine(source, "nested")));
        }
        finally
        {
            if (Directory.Exists(sandbox))
            {
                Directory.Delete(sandbox, recursive: true);
            }
        }
    }
}
