using System.Xml.Linq;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxChangeGrouperTests
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    [TestMethod]
    public void AmountReplacementIsOneLogicalChangeAndTwoRawRevisions()
    {
        var result = Group("合同金额为100万元。", "合同金额为120万元。");
        Assert.AreEqual(1, result.Summary.TotalChangeGroups);
        Assert.AreEqual(1, result.Summary.Replacements);
        Assert.AreEqual(2, result.Summary.RawRevisionCount);
        var change = result.Changes.Single();
        Assert.AreEqual("100万元", Text(change.Before));
        Assert.AreEqual("120万元", Text(change.After));
        Assert.AreEqual(DocumentDiffRichRunRole.Deleted, change.Before!.Runs.Single().Role);
        Assert.AreEqual(DocumentDiffRichRunRole.Inserted, change.After!.Runs.Single().Role);
    }

    [TestMethod]
    public void ContextSeparatesChangesAndBudgetReportsOmittedGroups()
    {
        var full = Group("甲旧乙旧丙", "甲新乙新丙");
        Assert.AreEqual(2, full.Summary.TotalChangeGroups);
        Assert.AreEqual(4, full.Summary.RawRevisionCount);
        var limited = Group("甲旧乙旧丙", "甲新乙新丙", 1);
        Assert.AreEqual(1, limited.Changes.Count);
        Assert.AreEqual(4, limited.Summary.RawRevisionCount);
        Assert.IsTrue(limited.Truncated);
        Assert.IsFalse(full.Truncated);
    }

    [TestMethod]
    public void AdjacentFormattingRevisionsBecomeOneGroupWithBothStyles()
    {
        int id = 0;
        var before = new DocxStyledText[] { new("甲", Props("b")), new("乙", null) };
        var after = new DocxStyledText[] { new("甲", null), new("乙", Props("i")) };
        var runs = DocxRevisionRuns.Build(DocxTextDiffer.Compare("甲乙", "甲乙", 0), before, after, ref id);
        var location = new DocumentDiffLocation(DocumentDiffPart.Header, sectionIndex: 3, paragraphIndex: 2);
        var result = DocxChangeGrouper.Group(runs, location, 10);
        Assert.AreEqual(1, result.Summary.FormattingChanges);
        Assert.AreEqual(2, result.Summary.RawRevisionCount);
        var change = result.Changes.Single();
        Assert.AreEqual(location, change.Location);
        Assert.AreEqual(true, change.Before!.Runs[0].Bold);
        Assert.AreEqual(true, change.After!.Runs[1].Italic);
        Assert.IsTrue(change.Before.Runs.All(r => r.Role == DocumentDiffRichRunRole.Changed));
    }

    [TestMethod]
    public void PureInsertionDeletionAndIdenticalTextHaveConsistentCounts()
    {
        Assert.AreEqual(1, Group("", "新增𠀀").Summary.Insertions);
        Assert.AreEqual(1, Group("删除👩🏽‍💻", "").Summary.Deletions);
        Assert.AreEqual(0, Group("相同", "相同", 0).Summary.TotalChangeGroups);
        Assert.IsFalse(Group("相同", "相同", 0).Truncated);
        Assert.IsTrue(Group("旧", "新", 0).Truncated);
    }

    [TestMethod]
    public void SeparateCellCallsKeepTheirOwnLocationsAndDoNotMutateRevisionXml()
    {
        int id = 0;
        var runs = DocxRevisionRuns.Build(DocxTextDiffer.Compare("字", "字", 0),
            [new("字", Props("b"))], [new("字", Props("i"))], ref id);
        var snapshot = new XElement(W + "p", runs);
        var left = new DocumentDiffLocation(DocumentDiffPart.Body, paragraphIndex: 0,
            tableIndex: 0, rowIndex: 0, columnIndex: 0);
        var right = new DocumentDiffLocation(DocumentDiffPart.Body, paragraphIndex: 1,
            tableIndex: 0, rowIndex: 0, columnIndex: 1);
        var first = DocxChangeGrouper.Group(runs, left, 10);
        var second = DocxChangeGrouper.Group(runs, right, 10);
        Assert.AreEqual(1, first.Summary.TotalChangeGroups);
        Assert.AreEqual(1, second.Summary.TotalChangeGroups);
        Assert.AreEqual(0, first.Changes.Single().Location.ColumnIndex);
        Assert.AreEqual(1, second.Changes.Single().Location.ColumnIndex);
        Assert.IsTrue(XNode.DeepEquals(snapshot, new XElement(W + "p", runs)));
        Assert.AreEqual(first.Summary, second.Summary);
    }

    [TestMethod]
    public void CancellationAndNonGeneratedContentAreNotSilentlyIgnored()
    {
        var location = new DocumentDiffLocation(DocumentDiffPart.Body);
        Assert.ThrowsExactly<OperationCanceledException>(() => DocxChangeGrouper.Group([], location, 0,
            new CancellationToken(true)));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxChangeGrouper.Group(
            [new XElement(W + "hyperlink", new XElement(W + "r", new XElement(W + "t", "link")))], location, 0));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxChangeGrouper.Group(
            [new XElement(W + "ins")], location, 0));
    }

    [TestMethod]
    public void FormattingAndTextChangesFlushAtEachTypeBoundary()
    {
        int id = 0;
        var runs = DocxRevisionRuns.Build(DocxTextDiffer.Compare("甲旧乙", "甲新乙", 100),
            [new("甲旧乙", Props("b"))], [new("甲新乙", Props("i"))], ref id);
        var result = DocxChangeGrouper.Group(runs, new DocumentDiffLocation(DocumentDiffPart.Body), 10);
        CollectionAssert.AreEqual(new[] { DocumentDiffChangeKind.Format, DocumentDiffChangeKind.Replace,
            DocumentDiffChangeKind.Format }, result.Changes.Select(c => c.Kind).ToArray());
        Assert.AreEqual(3, result.Summary.TotalChangeGroups);
        Assert.AreEqual(4, result.Summary.RawRevisionCount);
        Assert.AreEqual("旧", Text(result.Changes[1].Before));
        Assert.AreEqual("新", Text(result.Changes[1].After));
    }

    private static DocxChangeGroups Group(string before, string after, int budget = 10)
    {
        int id = 0;
        var runs = DocxRevisionRuns.Build(DocxTextDiffer.Compare(before, after, 10_000),
            [new(before, null)], [new(after, null)], ref id);
        return DocxChangeGrouper.Group(runs, new DocumentDiffLocation(DocumentDiffPart.Body, paragraphIndex: 0), budget);
    }

    private static XElement Props(string name) => new(W + "rPr", new XElement(W + name));
    private static string? Text(DocumentDiffRichSnippet? snippet) => snippet is null ? null : string.Concat(snippet.Runs.Select(r => r.Text));
}
