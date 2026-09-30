using System.Xml.Linq;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxRichSnippetBuilderTests
{
    private static readonly XNamespace W =
        "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    [TestMethod]
    public void Build_MapsExplicitRunProperties()
    {
        var properties = new XElement(W + "rPr",
            new XElement(W + "rFonts", new XAttribute(W + "ascii", "Aptos")),
            new XElement(W + "sz", new XAttribute(W + "val", "21")),
            new XElement(W + "b", new XAttribute(W + "val", "0")),
            new XElement(W + "i"),
            new XElement(W + "u", new XAttribute(W + "val", "single")),
            new XElement(W + "strike", new XAttribute(W + "val", "false")),
            new XElement(W + "color", new XAttribute(W + "val", "ff00aa")),
            new XElement(W + "highlight", new XAttribute(W + "val", "darkBlue")),
            new XElement(W + "rStyle", new XAttribute(W + "val", "Quote")));

        DocumentDiffRichSnippet snippet = DocxRichSnippetBuilder.Build(
            [new DocxStyledText("甲<b>乙</b>𠀀", properties)], DocumentDiffRichRunRole.Changed)!;

        DocumentDiffRichRun run = snippet.Runs.Single();
        Assert.AreEqual("甲<b>乙</b>𠀀", run.Text);
        Assert.AreEqual(DocumentDiffRichRunRole.Changed, run.Role);
        Assert.AreEqual(false, run.Bold);
        Assert.AreEqual(true, run.Italic);
        Assert.AreEqual(true, run.Underline);
        Assert.AreEqual(false, run.Strike);
        Assert.AreEqual(10.5, run.FontSizePt);
        Assert.AreEqual("Aptos", run.FontFamily);
        Assert.AreEqual("#FF00AA", run.Foreground);
        Assert.AreEqual("#000080", run.Background);
        Assert.AreEqual("Quote", run.StyleName);
    }

    [TestMethod]
    public void Build_DropsInvalidOptionalPropertiesAndKeepsText()
    {
        var properties = new XElement(W + "rPr",
            new XElement(W + "rFonts", new XAttribute(W + "ascii", " ")),
            new XElement(W + "sz", new XAttribute(W + "val", "-2")),
            new XElement(W + "b", new XAttribute(W + "val", "maybe")),
            new XElement(W + "u", new XAttribute(W + "val", "none")),
            new XElement(W + "color", new XAttribute(W + "val", "FF0000;url(x)")),
            new XElement(W + "highlight", new XAttribute(W + "val", "themeAccent1")),
            new XElement(W + "rStyle", new XAttribute(W + "val", " ")));

        DocumentDiffRichSnippet snippet = DocxRichSnippetBuilder.Build(
            [new DocxStyledText("<img src=x onerror=alert(1)>👩🏽‍💻", properties)],
            DocumentDiffRichRunRole.Inserted)!;

        DocumentDiffRichRun run = snippet.Runs.Single();
        Assert.AreEqual("<img src=x onerror=alert(1)>👩🏽‍💻", run.Text);
        Assert.IsNull(run.Bold);
        Assert.AreEqual(false, run.Underline);
        Assert.IsNull(run.FontSizePt);
        Assert.IsNull(run.FontFamily);
        Assert.IsNull(run.Foreground);
        Assert.IsNull(run.Background);
        Assert.IsNull(run.StyleName);
    }

    [TestMethod]
    public void Build_ReturnsNullForEmptyTextAndRejectsPropertyRevisions()
    {
        Assert.IsNull(DocxRichSnippetBuilder.Build(
            [new DocxStyledText("", new XElement(W + "rPr"))], DocumentDiffRichRunRole.Context));

        Assert.ThrowsExactly<ArgumentException>(() => DocxRichSnippetBuilder.Build(
            [new DocxStyledText("text", new XElement(W + "rPr", new XElement(W + "rPrChange")))],
            DocumentDiffRichRunRole.Context));
    }

    [TestMethod]
    public void Build_DropsHalfPointSizeThatUnderflowsInPoints()
    {
        DocumentDiffRichSnippet snippet = DocxRichSnippetBuilder.Build(
            [new DocxStyledText("text", new XElement(W + "rPr",
                new XElement(W + "sz", new XAttribute(W + "val", "5e-324"))))],
            DocumentDiffRichRunRole.Context)!;

        Assert.IsNull(snippet.Runs.Single().FontSizePt);
    }
}
