using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductDataRootManagerTests
{
    [TestMethod]
    public void DefaultAndSelectedRootsUseOneStableProductFolder()
    {
        string documents = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "documents");
        string selected = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "selected");

        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(documents, "VibeTableData")),
            ProductDataRootManager.DefaultDataRoot(documents));
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(selected, "VibeTableData")),
            ProductDataRootManager.DataRootForSelectedFolder(selected));
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(selected, "VibeTableData")),
            ProductDataRootManager.DataRootForSelectedFolder(
                Path.Combine(selected, "VibeTableData")));
    }

    [TestMethod]
    public void ProductDataCacheRuntimeAndLogsUseSeparateRoots()
    {
        string local = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "local");
        string program = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "program");

        string runtime = ProductDataRootManager.ResolveRuntimeRoot(local);
        string cache = ProductDataRootManager.ResolveCacheRoot(local);
        string logs = ProductDataRootManager.ResolveLogsRoot(program);

        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(local, "VibeTable", "runtime")),
            runtime);
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(local, "VibeTable", "cache")),
            cache);
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(program, "logs")),
            logs);
        Assert.AreNotEqual(runtime, cache);
        Assert.AreEqual(
            Path.Combine(logs, "backend.log"),
            ProductDataRootManager.ResolveSidecarLogPath(program));
        Assert.AreEqual(
            Path.Combine(logs, "pocketbase.log"),
            ProductDataRootManager.ResolvePocketBaseLogPath(program));
    }

    [TestMethod]
    public void BackendEnvironmentUsesExplicitRuntimeStateWithoutReplacingLocalAppData()
    {
        string runtime = Path.Combine(
            Path.GetTempPath(),
            "VibeTableRootChoice",
            "runtime");
        var environment = new Dictionary<string, string>
        {
            ["LOCALAPPDATA"] = "keep-system-local-app-data",
        };

        ProductDataRootManager.ConfigureProcessEnvironment(
            environment,
            runtime);

        Assert.AreEqual(
            "keep-system-local-app-data",
            environment["LOCALAPPDATA"]);
        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(runtime, "state")),
            environment["VIBETABLE_STATE_DIR"]);
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

    [TestMethod]
    public void TransactionalMigrationOmitsMachineLocalCachesAndLogs()
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
            foreach (string volatileDirectory in new[]
            {
                "logs",
                "webview2-user-data",
                "attachment-preview",
                "state",
            })
            {
                Directory.CreateDirectory(
                    Path.Combine(source, volatileDirectory));
                File.WriteAllText(
                    Path.Combine(source, volatileDirectory, "local.tmp"),
                    "do-not-sync");
            }

            ProductDataRootManager.MigrateDirectoryTransactional(
                source,
                target);

            Assert.IsTrue(File.Exists(
                Path.Combine(target, "pocketbase", "data.db")));
            Assert.IsFalse(Directory.Exists(Path.Combine(target, "logs")));
            Assert.IsFalse(Directory.Exists(
                Path.Combine(target, "webview2-user-data")));
            Assert.IsFalse(Directory.Exists(
                Path.Combine(target, "attachment-preview")));
            Assert.IsFalse(Directory.Exists(Path.Combine(target, "state")));
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
