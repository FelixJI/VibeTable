using System;
using System.IO;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

[TestClass]
public sealed class DirectusLaunchOptionsTests
{
    [TestMethod]
    public void ResolveForHost_ReturnsNullWhenLocalDirectusAbsent()
    {
        WithTemporaryDirectory(root =>
        {
            string host = Path.Combine(root, "publish");
            Directory.CreateDirectory(host);

            var options = DirectusLaunchOptions.ResolveForHost(host);

            Assert.IsNull(options);
        });
    }

    [TestMethod]
    public void ResolveForHost_PrefersPackagedLocalDirectus()
    {
        WithTemporaryDirectory(root =>
        {
            string host = Path.Combine(root, "publish");
            string localDirectus = Path.Combine(host, "local-directus");
            Directory.CreateDirectory(localDirectus);
            string backend = Path.Combine(host, "backend", "vibetable-backend.exe");
            Directory.CreateDirectory(Path.GetDirectoryName(backend)!);
            File.WriteAllText(backend, string.Empty);

            string stateRoot = Path.Combine(root, "state");
            var options = DirectusLaunchOptions.ResolveForHost(host, stateRoot);

            Assert.IsNotNull(options);
            // Packaged layout: per-user writable runtime dir + template copy.
            Assert.AreEqual(
                Path.Combine(stateRoot, "directus"),
                options!.LocalDirectusDirectory);
            Assert.AreEqual(localDirectus, options.TemplateDirectory);
            Assert.AreEqual(Path.GetFullPath(host), options.ResourceRoot);
        });
    }

    [TestMethod]
    public void ResolveForHost_UsesRepoLocalDirectusInDevelopment()
    {
        WithTemporaryDirectory(root =>
        {
            // Repo markers so the walk-up resolves the root.
            File.WriteAllText(Path.Combine(root, "pyproject.toml"), "[project]");
            Directory.CreateDirectory(Path.Combine(root, "backend"));
            // The local_directus dir lives under scripts/.
            Directory.CreateDirectory(Path.Combine(root, "scripts", "local_directus"));
            // Host sits several levels under the repo root, like a real build output.
            string host = Path.Combine(
                root, "desktop", "src", "VibeTable.Desktop", "bin", "Release");
            Directory.CreateDirectory(host);

            var options = DirectusLaunchOptions.ResolveForHost(host);

            Assert.IsNotNull(options);
            StringAssert.Contains(options!.LocalDirectusDirectory, "local_directus");
            Assert.IsNull(options.TemplateDirectory);
            Assert.AreEqual(Path.GetFullPath(root), options.ResourceRoot);
        });
    }

    [TestMethod]
    public void ResolveForHost_ReturnsNullWhenNoLocalDirectusResolvable()
    {
        WithTemporaryDirectory(root =>
        {
            // A host dir with no local-directus beside it and no repo root above.
            string host = Path.Combine(root, "publish");
            Directory.CreateDirectory(host);

            var options = DirectusLaunchOptions.ResolveForHost(host);

            Assert.IsNull(options);
        });
    }

    private static void WithTemporaryDirectory(Action<string> test)
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-directus-options-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            test(root);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }
}
