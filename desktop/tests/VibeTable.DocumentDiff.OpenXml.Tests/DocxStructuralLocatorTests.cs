using System.Xml.Linq;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxStructuralLocatorTests
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    [TestMethod]
    public void ParagraphsKeepHeadingAndSectionAndDoNotAbsorbTextBoxes()
    {
        var heading = P("第二章");
        heading.AddFirst(new XElement(W + "pPr", new XElement(W + "pStyle", new XAttribute(W + "val", "Heading2"))));
        var end = P("本节末");
        end.AddFirst(new XElement(W + "pPr", new XElement(W + "sectPr")));
        var box = new XElement(W + "txbxContent", P("框内"));
        var root = new XElement(W + "body", heading, end, P("下节"), box);
        var index = DocxStructuralLocator.Index(root, DocumentDiffPart.Body, 10);
        Assert.AreEqual(3, index.Paragraphs.Count);
        CollectionAssert.AreEqual(new int?[] { 0, 0, 1 }, index.Paragraphs.Select(p => p.Location.SectionIndex).ToArray());
        Assert.AreEqual("第二章", index.Paragraphs[2].Location.NearestHeading);
        var textBox = DocxStructuralLocator.Index(box, DocumentDiffPart.TextBox, 10);
        Assert.AreEqual(1, textBox.Paragraphs.Count);
        Assert.IsNull(textBox.Paragraphs[0].Location.NearestHeading);
    }

    [TestMethod]
    public void TablesUseGridColumnsAndNestedTablesHaveIndependentRows()
    {
        var first = new XElement(W + "tc", new XElement(W + "tcPr",
            new XElement(W + "gridSpan", new XAttribute(W + "val", 2))), P("跨两列"));
        var nested = new XElement(W + "tbl", new XElement(W + "tr", new XElement(W + "tc", P("嵌套"))));
        var second = new XElement(W + "tc", P("外表"), nested);
        var root = new XElement(W + "body", new XElement(W + "tbl", new XElement(W + "tr",
            new XElement(W + "trPr", new XElement(W + "gridBefore", new XAttribute(W + "val", 1))), first, second)));
        var paragraphs = DocxStructuralLocator.Index(root, DocumentDiffPart.Body, 10).Paragraphs;
        CollectionAssert.AreEqual(new int?[] { 1, 3, 0 }, paragraphs.Select(p => p.Location.ColumnIndex).ToArray());
        CollectionAssert.AreEqual(new int?[] { 0, 0, 1 }, paragraphs.Select(p => p.Location.TableIndex).ToArray());
        Assert.IsTrue(paragraphs.All(p => p.Location.RowIndex == 0));
    }

    [TestMethod]
    public void HeaderPartIsIsolatedAndBudgetAndCancellationAreExplicit()
    {
        var root = new XElement(W + "hdr", P("一"), P("二"));
        var result = DocxStructuralLocator.Index(root, DocumentDiffPart.Header, 1, 4);
        Assert.IsTrue(result.Truncated);
        Assert.AreEqual(DocumentDiffPart.Header, result.Paragraphs[0].Location.Part);
        Assert.AreEqual(4, result.Paragraphs[0].Location.SectionIndex);
        Assert.IsFalse(DocxStructuralLocator.Index(root, DocumentDiffPart.Header, 2, 4).Truncated);
        Assert.ThrowsExactly<OperationCanceledException>(() => DocxStructuralLocator.Index(root,
            DocumentDiffPart.Header, 2, cancellationToken: new CancellationToken(true)));
    }

    [TestMethod]
    public void TextBoxHeadingDoesNotInheritOuterTableOrHeading()
    {
        var title = P("框标题");
        title.AddFirst(new XElement(W + "pPr", new XElement(W + "outlineLvl", new XAttribute(W + "val", 0))));
        var box = new XElement(W + "txbxContent", title, P("框正文"));
        var outer = new XElement(W + "tbl", new XElement(W + "tr", new XElement(W + "tc", box)));
        var result = DocxStructuralLocator.Index(box, DocumentDiffPart.TextBox, 10);
        Assert.AreEqual("框标题", result.Paragraphs[1].Location.NearestHeading);
        Assert.IsNull(result.Paragraphs[1].Location.TableIndex);
        Assert.IsNotNull(outer.Element(W + "tr"));
    }

    [TestMethod]
    [DataRow("0")]
    [DataRow("-1")]
    [DataRow("not-a-number")]
    public void InvalidGridSpanCannotProduceMisleadingCoordinates(string span)
    {
        var root = new XElement(W + "body", new XElement(W + "tbl", new XElement(W + "tr",
            new XElement(W + "tc", new XElement(W + "tcPr", new XElement(W + "gridSpan",
                new XAttribute(W + "val", span))), P("文字")))));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxStructuralLocator.Index(root, DocumentDiffPart.Body, 10));
    }

    [TestMethod]
    public void ExcessiveNestingFailsWithoutStackOverflow()
    {
        XElement root = P("深层");
        for (int i = 0; i < 66; i++)
            root = new XElement(W + "sdt", root);
        Assert.ThrowsExactly<InvalidDataException>(() => DocxStructuralLocator.Index(root, DocumentDiffPart.Body, 10));
    }

    [TestMethod]
    public void HeadingTextCannotBypassVisibleTextBudget()
    {
        var heading = P(new string('字', OpenXmlExtractionLimits.MaxVisibleTextCharacters + 1));
        heading.AddFirst(new XElement(W + "pPr", new XElement(W + "outlineLvl", new XAttribute(W + "val", 0))));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxStructuralLocator.Index(
            new XElement(W + "body", heading), DocumentDiffPart.Body, 10));
    }

    [TestMethod]
    public void HeadingDescendantsAreDepthCheckedDuringExtraction()
    {
        XElement nested = new(W + "t", "标题");
        for (int i = 0; i < 66; i++)
            nested = new XElement(W + "sdt", nested);
        var heading = new XElement(W + "p", new XElement(W + "pPr",
            new XElement(W + "outlineLvl", new XAttribute(W + "val", 0))), nested);
        Assert.ThrowsExactly<InvalidDataException>(() => DocxStructuralLocator.Index(
            new XElement(W + "body", heading), DocumentDiffPart.Body, 10));
    }

    private static XElement P(string text) => new(W + "p", new XElement(W + "r", new XElement(W + "t", text)));
}
