using System.IO;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for the G2.2 path safety invariants.
/// </summary>
[TestClass]
public sealed class PathGuardTests
{
    [TestMethod]
    public void ValidRelativePath_NormalizesSeparators()
    {
        var result = WorkspacePathGuard.ValidateRelativePath(@"folder/sub/file.docx");
        Assert.AreEqual("folder/sub/file.docx", result);
    }

    [TestMethod]
    public void ValidRelativePath_BackslashNormalized()
    {
        var result = WorkspacePathGuard.ValidateRelativePath(@"folder\sub\file.docx");
        Assert.AreEqual("folder/sub/file.docx", result);
    }

    [TestMethod]
    public void AbsolutePath_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(@"C:\Users\file.docx"));
    }

    [TestMethod]
    public void PathTraversal_Parent_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(@"../escape.docx"));
    }

    [TestMethod]
    public void PathTraversal_Nested_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(@"folder/../../escape.docx"));
    }

    [TestMethod]
    public void NTFS_Ads_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath("file.txt:Zone.Identifier"));
    }

    [TestMethod]
    public void DevicePath_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(@"\\.\COM1"));
    }

    [TestMethod]
    [DataRow("NUL")]
    [DataRow("nul.txt")]
    [DataRow("folder/COM1.docx")]
    [DataRow("folder/LPT9")]
    public void ReservedDeviceName_Rejected(string path)
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(path));
    }

    [TestMethod]
    [DataRow("folder/./file.docx")]
    [DataRow("folder./file.docx")]
    [DataRow("folder /file.docx")]
    public void AmbiguousWindowsSegment_Rejected(string path)
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(path));
    }

    [TestMethod]
    public void UncDevicePath_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(@"\\?\C:\file"));
    }

    [TestMethod]
    public void EmptyPath_Rejected()
    {
        Assert.Throws<InvalidOperationException>(
            () => WorkspacePathGuard.ValidateRelativePath(""));
    }

    [TestMethod]
    public void ShouldIgnore_BackupDirectory()
    {
        Assert.IsTrue(WorkspacePathGuard.ShouldIgnore(".backup"));
        Assert.IsTrue(WorkspacePathGuard.ShouldIgnore(".staging"));
        Assert.IsTrue(WorkspacePathGuard.ShouldIgnore(".git"));
    }

    [TestMethod]
    public void ShouldIgnore_SynologyConflict()
    {
        Assert.IsTrue(WorkspacePathGuard.ShouldIgnore("file (conflict John-PC 2024-01-15).docx"));
    }

    [TestMethod]
    public void ShouldIgnore_OfficeTempLock()
    {
        Assert.IsTrue(WorkspacePathGuard.ShouldIgnore("~$main.docx"));
    }

    [TestMethod]
    public void ShouldNotIgnore_NormalFile()
    {
        Assert.IsFalse(WorkspacePathGuard.ShouldIgnore("main.docx"));
        Assert.IsFalse(WorkspacePathGuard.ShouldIgnore("folder"));
    }

    [TestMethod]
    public void ResolveAndCheck_ValidPath_StaysInRoot()
    {
        var tmp = Path.Combine(Path.GetTempPath(), "vibetable-ws-test-" + Guid.NewGuid().ToString("N")[..8]);
        try
        {
            Directory.CreateDirectory(tmp);
            var resolved = WorkspacePathGuard.ResolveAndCheck(tmp, "sub/file.docx");
            Assert.IsTrue(resolved.StartsWith(tmp, StringComparison.OrdinalIgnoreCase));
        }
        finally
        {
            if (Directory.Exists(tmp))
                Directory.Delete(tmp, recursive: true);
        }
    }

    [TestMethod]
    public void ResolveAndCheck_Traversal_Rejected()
    {
        var tmp = Path.Combine(Path.GetTempPath(), "vibetable-ws-test-" + Guid.NewGuid().ToString("N")[..8]);
        try
        {
            Directory.CreateDirectory(tmp);
            Assert.Throws<InvalidOperationException>(
                () => WorkspacePathGuard.ResolveAndCheck(tmp, "../../../escape.docx"));
        }
        finally
        {
            if (Directory.Exists(tmp))
                Directory.Delete(tmp, recursive: true);
        }
    }

    [TestMethod]
    public void ResolveAndCheck_MissingRoot_Rejected()
    {
        var missing = Path.Combine(
            Path.GetTempPath(), "vibetable-ws-missing-" + Guid.NewGuid().ToString("N"));

        Assert.Throws<DirectoryNotFoundException>(
            () => WorkspacePathGuard.ResolveAndCheck(missing, "file.docx"));
    }
}
