using System;
using System.IO;
using Microsoft.VisualStudio.TestTools.UnitTesting;

namespace VibeTable.Infrastructure.Tests;

[TestClass]
public sealed class LaunchPathsTests
{
    [TestMethod]
    public void ProductDataAndBackupsLiveOutsideInstall()
    {
        string local = Path.Combine(Path.GetTempPath(), Guid.NewGuid().ToString("N"));
        string install = Path.Combine(Path.GetTempPath(), Guid.NewGuid().ToString("N"));

        string data = LaunchPaths.ResolveDataRoot(local);
        string backup = LaunchPaths.ResolveBackupRoot(local);
        LaunchPaths.EnsureInstallAndDataAreSeparated(install, data);

        StringAssert.EndsWith(data, Path.Combine("VibeTable", "data"));
        StringAssert.EndsWith(backup, Path.Combine("VibeTable", "backups"));
    }

    [TestMethod]
    public void NestedInstallAndDataAreRejected()
    {
        string install = Path.Combine(Path.GetTempPath(), Guid.NewGuid().ToString("N"));
        Assert.Throws<InvalidOperationException>(() =>
            LaunchPaths.EnsureInstallAndDataAreSeparated(
                install, Path.Combine(install, "data")));
    }

    [TestMethod]
    public void SourceLayoutPrefersFreshDevelopmentSidecarOverQaArtifact()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-launch-paths-" + Guid.NewGuid().ToString("N"));
        string baseDirectory = Path.Combine(
            root,
            "desktop",
            "src",
            "VibeTable.Desktop",
            "bin",
            "Release",
            "net10.0-windows");
        string name = OperatingSystem.IsWindows()
            ? "vibetable-pb.exe"
            : "vibetable-pb";
        string dev = Path.Combine(root, "build", "dev", name);
        string qa = Path.Combine(root, "build", "qa", name);
        Directory.CreateDirectory(baseDirectory);
        Directory.CreateDirectory(Path.Combine(root, "backend"));
        Directory.CreateDirectory(Path.GetDirectoryName(dev)!);
        Directory.CreateDirectory(Path.GetDirectoryName(qa)!);
        File.WriteAllText(Path.Combine(root, "pyproject.toml"), "");
        File.WriteAllText(dev, "dev");
        File.WriteAllText(qa, "qa");

        string? resolved = LaunchPaths.ResolveSidecarBinary(baseDirectory);

        Assert.AreEqual(dev, resolved);
    }

}
