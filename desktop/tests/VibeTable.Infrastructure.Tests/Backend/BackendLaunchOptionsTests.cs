using System;
using System.IO;
using VibeTable.Infrastructure.Backend;

namespace VibeTable.Infrastructure.Tests.Backend;

[TestClass]
[DoNotParallelize]
public sealed class BackendLaunchOptionsTests
{
    [TestMethod]
    public void ResolveForHost_PrefersPackagedBackend()
    {
        WithTemporaryDirectory(root =>
        {
            string host = Path.Combine(root, "publish");
            string backend = Path.Combine(
                host,
                "backend",
                "vibetable-backend.exe");
            Directory.CreateDirectory(Path.GetDirectoryName(backend)!);
            File.WriteAllText(backend, string.Empty);

            var options = BackendLaunchOptions.ResolveForHost(host);

            Assert.AreEqual(Path.GetFullPath(backend), options.Command);
            Assert.AreEqual(string.Empty, options.Arguments);
            Assert.AreEqual(Path.GetDirectoryName(backend), options.WorkingDirectory);
        });
    }

    [TestMethod]
    public void ResolveForHost_UsesRepositoryVenvInDevelopment()
    {
        WithTemporaryDirectory(root =>
        {
            File.WriteAllText(Path.Combine(root, "pyproject.toml"), "[project]");
            Directory.CreateDirectory(Path.Combine(root, "backend"));
            string python = Path.Combine(root, ".venv", "Scripts", "python.exe");
            Directory.CreateDirectory(Path.GetDirectoryName(python)!);
            File.WriteAllText(python, string.Empty);
            string host = Path.Combine(
                root, "desktop", "src", "VibeTable.Desktop", "bin", "Release");
            Directory.CreateDirectory(host);

            var options = BackendLaunchOptions.ResolveForHost(host);

            Assert.AreEqual(python, options.Command);
            Assert.AreEqual("-m backend", options.Arguments);
            Assert.AreEqual(root, options.WorkingDirectory);
        });
    }

    [TestMethod]
    public void ResolveForHost_PrefersLauncherPythonInDevelopment()
    {
        WithTemporaryDirectory(root =>
        {
            File.WriteAllText(Path.Combine(root, "pyproject.toml"), "[project]");
            Directory.CreateDirectory(Path.Combine(root, "backend"));
            string python = Path.Combine(root, "tools", "python.exe");
            Directory.CreateDirectory(Path.GetDirectoryName(python)!);
            File.WriteAllText(python, string.Empty);
            string host = Path.Combine(root, "desktop", "bin");
            Directory.CreateDirectory(host);

            string? original = Environment.GetEnvironmentVariable("VIBETABLE_PYTHON");
            try
            {
                Environment.SetEnvironmentVariable("VIBETABLE_PYTHON", python);
                var options = BackendLaunchOptions.ResolveForHost(host);
                Assert.AreEqual(Path.GetFullPath(python), options.Command);
                Assert.AreEqual("-m backend", options.Arguments);
                Assert.AreEqual(root, options.WorkingDirectory);
            }
            finally
            {
                Environment.SetEnvironmentVariable("VIBETABLE_PYTHON", original);
            }
        });
    }

    [TestMethod]
    public void ResolveForHost_FallsBackToUvAtRepositoryRoot()
    {
        WithTemporaryDirectory(root =>
        {
            File.WriteAllText(Path.Combine(root, "pyproject.toml"), "[project]");
            Directory.CreateDirectory(Path.Combine(root, "backend"));
            string host = Path.Combine(root, "desktop", "bin");
            Directory.CreateDirectory(host);

            var options = BackendLaunchOptions.ResolveForHost(host);

            Assert.AreEqual(BackendLaunchOptions.DefaultCommand, options.Command);
            Assert.AreEqual(BackendLaunchOptions.DefaultArguments, options.Arguments);
            Assert.AreEqual(root, options.WorkingDirectory);
        });
    }

    private static void WithTemporaryDirectory(Action<string> test)
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-backend-options-" + Guid.NewGuid().ToString("N"));
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
