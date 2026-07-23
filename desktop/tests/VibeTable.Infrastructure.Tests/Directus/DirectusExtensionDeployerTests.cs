using System;
using System.IO;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

[TestClass]
public sealed class DirectusExtensionDeployerTests
{
    [TestMethod]
    public void Deploy_StagesEveryManifestExtensionUsingItsPackageEntry()
    {
        WithTemporaryDirectories((resourceRoot, localDirectusDirectory) =>
        {
            WriteManifest(resourceRoot, """
                {
                  "formatVersion": 1,
                  "extensions": [
                    { "name": "vibetable-bulk-mutation", "type": "endpoint", "entry": "dist/index.js" },
                    { "name": "vibetable-plugin-bridge", "type": "bundle", "entry": "dist/api.js" },
                    { "name": "vibetable-lookup-query", "type": "endpoint", "entry": "dist/index.js" }
                  ]
                }
                """);
            WriteEndpoint(resourceRoot, "vibetable-bulk-mutation", "bulk");
            WriteBundle(resourceRoot, "vibetable-plugin-bridge", "bridge-api", "bridge-app");
            WriteEndpoint(resourceRoot, "vibetable-lookup-query", "lookup");

            DirectusExtensionDeployer.Deploy(resourceRoot, localDirectusDirectory);

            Assert.AreEqual(
                "bulk",
                File.ReadAllText(Path.Combine(
                    localDirectusDirectory, "extensions", "vibetable-bulk-mutation", "dist", "index.js")));
            Assert.AreEqual(
                "bridge-api",
                File.ReadAllText(Path.Combine(
                    localDirectusDirectory, "extensions", "vibetable-plugin-bridge", "dist", "api.js")));
            Assert.AreEqual(
                "bridge-app",
                File.ReadAllText(Path.Combine(
                    localDirectusDirectory, "extensions", "vibetable-plugin-bridge", "dist", "app.js")));
            Assert.AreEqual(
                "lookup",
                File.ReadAllText(Path.Combine(
                    localDirectusDirectory, "extensions", "vibetable-lookup-query", "dist", "index.js")));
            Assert.IsTrue(File.Exists(Path.Combine(
                localDirectusDirectory, "extensions", "vibetable-lookup-query", "package.json")));
        });
    }

    [TestMethod]
    public void Deploy_RejectsPackageEntryThatEscapesTheDistDirectory()
    {
        WithTemporaryDirectories((resourceRoot, localDirectusDirectory) =>
        {
            WriteManifest(resourceRoot, """
                {
                  "formatVersion": 1,
                  "extensions": [
                    { "name": "unsafe-extension", "type": "endpoint", "entry": "dist/../other/index.js" }
                  ]
                }
                """);
            WriteExtension(
                resourceRoot,
                "unsafe-extension",
                """{"directus:extension":{"type":"endpoint","path":"dist/../other/index.js"}}""",
                ("other/index.js", "unsafe"));

            InvalidOperationException error = Assert.Throws<InvalidOperationException>(
                () => DirectusExtensionDeployer.Deploy(resourceRoot, localDirectusDirectory));

            StringAssert.Contains(error.Message, "Unsafe Directus package entry");
        });
    }

    [TestMethod]
    public void Deploy_RejectsExtensionNameThatChangesTheDeploymentHierarchy()
    {
        WithTemporaryDirectories((resourceRoot, localDirectusDirectory) =>
        {
            WriteManifest(resourceRoot, """
                {
                  "formatVersion": 1,
                  "extensions": [
                    { "name": "nested/extension", "type": "endpoint", "entry": "dist/index.js" }
                  ]
                }
                """);
            WriteEndpoint(resourceRoot, "nested/extension", "unsafe");

            InvalidOperationException error = Assert.Throws<InvalidOperationException>(
                () => DirectusExtensionDeployer.Deploy(resourceRoot, localDirectusDirectory));

            StringAssert.Contains(error.Message, "Unsafe Directus extension name");
        });
    }

    [TestMethod]
    public void Deploy_ReportsTheExtensionAndEntryWhenABuildArtifactIsMissing()
    {
        WithTemporaryDirectories((resourceRoot, localDirectusDirectory) =>
        {
            WriteManifest(resourceRoot, """
                {
                  "formatVersion": 1,
                  "extensions": [
                    { "name": "vibetable-lookup-query", "type": "endpoint", "entry": "dist/index.js" }
                  ]
                }
                """);
            WriteExtension(
                resourceRoot,
                "vibetable-lookup-query",
                """{"directus:extension":{"type":"endpoint","path":"dist/index.js"}}""");

            InvalidOperationException error = Assert.Throws<InvalidOperationException>(
                () => DirectusExtensionDeployer.Deploy(resourceRoot, localDirectusDirectory));

            StringAssert.Contains(error.Message, "vibetable-lookup-query");
            StringAssert.Contains(error.Message, "dist/index.js");
            Assert.IsFalse(Directory.Exists(Path.Combine(
                localDirectusDirectory, "extensions", "vibetable-lookup-query")));
        });
    }

    [TestMethod]
    public void Deploy_ValidatesAllEntriesBeforeRefreshingTheRuntime()
    {
        WithTemporaryDirectories((resourceRoot, localDirectusDirectory) =>
        {
            WriteManifest(resourceRoot, """
                {
                  "formatVersion": 1,
                  "extensions": [
                    { "name": "first", "type": "endpoint", "entry": "dist/index.js" },
                    { "name": "missing", "type": "endpoint", "entry": "dist/index.js" }
                  ]
                }
                """);
            WriteEndpoint(resourceRoot, "first", "new-content");
            WriteExtension(
                resourceRoot,
                "missing",
                """{"directus:extension":{"type":"endpoint","path":"dist/index.js"}}""");

            string existing = Path.Combine(
                localDirectusDirectory, "extensions", "first", "dist", "index.js");
            Directory.CreateDirectory(Path.GetDirectoryName(existing)!);
            File.WriteAllText(existing, "old-content");

            Assert.Throws<InvalidOperationException>(
                () => DirectusExtensionDeployer.Deploy(resourceRoot, localDirectusDirectory));

            Assert.AreEqual("old-content", File.ReadAllText(existing));
        });
    }

    private static void WriteManifest(string resourceRoot, string json)
    {
        string extensionsRoot = Path.Combine(resourceRoot, "directus", "extensions");
        Directory.CreateDirectory(extensionsRoot);
        File.WriteAllText(Path.Combine(extensionsRoot, "manifest.json"), json);
    }

    private static void WriteEndpoint(string resourceRoot, string name, string content)
    {
        WriteExtension(
            resourceRoot,
            name,
            """{"directus:extension":{"type":"endpoint","path":"dist/index.js"}}""",
            ("dist/index.js", content));
    }

    private static void WriteBundle(
        string resourceRoot,
        string name,
        string apiContent,
        string appContent)
    {
        WriteExtension(
            resourceRoot,
            name,
            """{"directus:extension":{"type":"bundle","path":{"app":"dist/app.js","api":"dist/api.js"}}}""",
            ("dist/api.js", apiContent),
            ("dist/app.js", appContent));
    }

    private static void WriteExtension(
        string resourceRoot,
        string name,
        string packageJson,
        params (string RelativePath, string Content)[] artifacts)
    {
        string extensionRoot = Path.Combine(resourceRoot, "directus", "extensions", name);
        Directory.CreateDirectory(extensionRoot);
        File.WriteAllText(Path.Combine(extensionRoot, "package.json"), packageJson);
        foreach ((string relativePath, string content) in artifacts)
        {
            string path = Path.Combine(extensionRoot, relativePath.Replace('/', Path.DirectorySeparatorChar));
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            File.WriteAllText(path, content);
        }
    }

    private static void WithTemporaryDirectories(Action<string, string> body)
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-extension-deploy-" + Guid.NewGuid().ToString("N"));
        string resourceRoot = Path.Combine(root, "resources");
        string localDirectusDirectory = Path.Combine(root, "runtime");
        try
        {
            Directory.CreateDirectory(resourceRoot);
            Directory.CreateDirectory(localDirectusDirectory);
            body(resourceRoot, localDirectusDirectory);
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { /* best-effort test cleanup */ }
        }
    }
}
