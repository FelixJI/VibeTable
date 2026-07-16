using System.IO;
using VibeTable.Workspace.Diff;

namespace VibeTable.Workspace.Tests;

/// <summary>
/// Tests for the G5.1 diff adapters.
/// </summary>
[TestClass]
public sealed class DiffAdapterTests
{
    private static string MakeFile(string content, string ext = ".txt")
    {
        var path = Path.Combine(Path.GetTempPath(), "vibetable-diff-" + Guid.NewGuid().ToString("N")[..8] + ext);
        File.WriteAllText(path, content);
        return path;
    }

    [TestMethod]
    public void TextDiff_IdenticalFiles_Unchanged()
    {
        var a = MakeFile("line1\nline2\nline3");
        var b = MakeFile("line1\nline2\nline3");
        try
        {
            var adapter = new TextDiffAdapter();
            var result = adapter.Diff(a, b);
            Assert.AreEqual(DiffSummaryKind.Unchanged, result.Kind);
        }
        finally { Cleanup(a, b); }
    }

    [TestMethod]
    public void TextDiff_AddedLine_ReportsAddition()
    {
        var a = MakeFile("line1\nline2");
        var b = MakeFile("line1\nline2\nline3");
        try
        {
            var adapter = new TextDiffAdapter();
            var result = adapter.Diff(a, b);
            Assert.AreEqual(DiffSummaryKind.ChangedWithDetails, result.Kind);
            Assert.AreEqual(1, result.AddedLines);
            Assert.AreEqual(0, result.RemovedLines);
        }
        finally { Cleanup(a, b); }
    }

    [TestMethod]
    public void TextDiff_RemovedLine_ReportsRemoval()
    {
        var a = MakeFile("line1\nline2\nline3");
        var b = MakeFile("line1\nline3");
        try
        {
            var adapter = new TextDiffAdapter();
            var result = adapter.Diff(a, b);
            Assert.AreEqual(DiffSummaryKind.ChangedWithDetails, result.Kind);
            Assert.AreEqual(0, result.AddedLines);
            Assert.AreEqual(1, result.RemovedLines);
        }
        finally { Cleanup(a, b); }
    }

    [TestMethod]
    public void TextDiff_ModifiedLine_ReportsAddAndRemove()
    {
        var a = MakeFile("line1\nold\nline3");
        var b = MakeFile("line1\nnew\nline3");
        try
        {
            var adapter = new TextDiffAdapter();
            var result = adapter.Diff(a, b);
            Assert.IsTrue(result.AddedLines >= 1);
            Assert.IsTrue(result.RemovedLines >= 1);
        }
        finally { Cleanup(a, b); }
    }

    [TestMethod]
    public void BinaryDiff_IdenticalFiles_Unchanged()
    {
        var a = MakeFile("binary content");
        var b = MakeFile("binary content");
        try
        {
            var adapter = new BinaryDiffAdapter();
            var result = adapter.Diff(a, b);
            Assert.AreEqual(DiffSummaryKind.Unchanged, result.Kind);
        }
        finally { Cleanup(a, b); }
    }

    [TestMethod]
    public void BinaryDiff_DifferentFiles_Changed()
    {
        var a = MakeFile("content A");
        var b = MakeFile("content B");
        try
        {
            var adapter = new BinaryDiffAdapter();
            var result = adapter.Diff(a, b);
            Assert.AreEqual(DiffSummaryKind.Changed, result.Kind);
            Assert.IsTrue(result.Summary.Contains("changed"));
        }
        finally { Cleanup(a, b); }
    }

    [TestMethod]
    public void Registry_SelectsTextAdapter_ForTxt()
    {
        var registry = new DiffAdapterRegistry();
        var adapter = registry.Select("file.txt");
        Assert.IsInstanceOfType<TextDiffAdapter>(adapter);
    }

    [TestMethod]
    public void Registry_SelectsTextAdapter_ForCsv()
    {
        var registry = new DiffAdapterRegistry();
        var adapter = registry.Select("data.csv");
        Assert.IsInstanceOfType<TextDiffAdapter>(adapter);
    }

    [TestMethod]
    public void Registry_SelectsBinary_ForUnknownExtension()
    {
        var registry = new DiffAdapterRegistry();
        var adapter = registry.Select("file.dat");
        Assert.IsInstanceOfType<BinaryDiffAdapter>(adapter);
    }

    [TestMethod]
    public void Registry_SelectsByMimeType_OverExtension()
    {
        var registry = new DiffAdapterRegistry();
        // A .dat file but with text/plain MIME type should use text adapter.
        var adapter = registry.Select("file.dat", mimeType: "text/plain");
        Assert.IsInstanceOfType<TextDiffAdapter>(adapter);
    }

    [TestMethod]
    public void Registry_Diff_DelegatesToCorrectAdapter()
    {
        var a = MakeFile("a\nb\nc");
        var b = MakeFile("a\nb\nc\nd");
        try
        {
            var registry = new DiffAdapterRegistry();
            var result = registry.Diff(a, b, mimeType: "text/plain");
            Assert.AreEqual(DiffSummaryKind.ChangedWithDetails, result.Kind);
            Assert.AreEqual(1, result.AddedLines);
        }
        finally { Cleanup(a, b); }
    }

    private static void Cleanup(params string[] paths)
    {
        foreach (var p in paths)
        {
            try { if (File.Exists(p)) File.Delete(p); } catch { }
        }
    }
}
