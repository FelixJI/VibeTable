using VibeTable.DocumentDiff.OpenXml;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxTextDifferTests
{
    [TestMethod]
    public void AmountReplacementIsOneLogicalChangeAndPreservesBothTexts()
    {
        const string before = "合同金额为100万元。";
        const string after = "合同金额为120万元。";
        DocxTextDiff result = DocxTextDiffer.Compare(before, after, 10_000);

        Assert.IsFalse(result.Truncated);
        DocxTextSegment change = result.Segments.Single(s => s.IsChange);
        Assert.AreEqual("100万元", change.Before);
        Assert.AreEqual("120万元", change.After);
        Assert.AreEqual(before, string.Concat(result.Segments.Select(s => s.Before)));
        Assert.AreEqual(after, string.Concat(result.Segments.Select(s => s.After)));
    }

    [TestMethod]
    [DataRow("甲", "乙")]
    [DataRow("𠀀", "𠀁")]
    [DataRow("👩🏽‍💻", "👩🏻‍💻")]
    public void SingleTextElementChangeDoesNotExpandOrDisappear(string oldElement, string newElement)
    {
        DocxTextDiff result = DocxTextDiffer.Compare(
            "正文" + oldElement + "完成。", "正文" + newElement + "完成。", 10_000);
        DocxTextSegment change = result.Segments.Single(s => s.IsChange);
        Assert.AreEqual(oldElement, change.Before);
        Assert.AreEqual(newElement, change.After);
        Assert.IsFalse(result.Truncated);
    }

    [TestMethod]
    public void UnchangedTextSeparatesTwoChanges()
    {
        DocxTextDiff result = DocxTextDiffer.Compare("甲旧乙旧丙", "甲新乙改丙", 10_000);
        DocxTextSegment[] changes = result.Segments.Where(s => s.IsChange).ToArray();
        CollectionAssert.AreEqual(new[] { "旧", "旧" }, changes.Select(s => s.Before).ToArray());
        CollectionAssert.AreEqual(new[] { "新", "改" }, changes.Select(s => s.After).ToArray());
        Assert.AreEqual("甲旧乙旧丙", string.Concat(result.Segments.Select(s => s.Before)));
        Assert.AreEqual("甲新乙改丙", string.Concat(result.Segments.Select(s => s.After)));
    }

    [TestMethod]
    public void BudgetExhaustionKeepsBothSidesButDoesNotClaimCompleteAlignment()
    {
        DocxTextDiff result = DocxTextDiffer.Compare("甲乙丙", "丁乙戊", 0);
        Assert.IsTrue(result.Truncated);
        Assert.AreEqual("甲乙丙", string.Concat(result.Segments.Select(s => s.Before)));
        Assert.AreEqual("丁乙戊", string.Concat(result.Segments.Select(s => s.After)));
    }

    [TestMethod]
    public void PureInsertionAndDeletionNeedNoMatrixBudget()
    {
        DocxTextDiff inserted = DocxTextDiffer.Compare("", "新增👩🏽‍💻", 0);
        DocxTextDiff deleted = DocxTextDiffer.Compare("删除𠀀", "", 0);
        Assert.IsFalse(inserted.Truncated);
        Assert.IsFalse(deleted.Truncated);
        Assert.AreEqual(new DocxTextSegment("", "新增👩🏽‍💻"), inserted.Segments.Single());
        Assert.AreEqual(new DocxTextSegment("删除𠀀", ""), deleted.Segments.Single());
    }

    [TestMethod]
    public void CancellationIsObservedEvenForIdenticalText()
    {
        using var cancellation = new CancellationTokenSource();
        cancellation.Cancel();
        Assert.ThrowsExactly<OperationCanceledException>(() =>
            DocxTextDiffer.Compare("相同", "相同", 100, cancellation.Token));
    }

    [TestMethod]
    public void MatrixBudgetIncludesBoundaryRowAndColumn()
    {
        Assert.IsTrue(DocxTextDiffer.Compare("甲旧乙", "甲新乙", 3).Truncated);
        DocxTextDiff exact = DocxTextDiffer.Compare("甲旧乙", "甲新乙", 4);
        Assert.IsFalse(exact.Truncated);
        CollectionAssert.AreEqual(new[]
        {
            new DocxTextSegment("甲", "甲"),
            new DocxTextSegment("旧", "新"),
            new DocxTextSegment("乙", "乙"),
        }, exact.Segments.ToArray());
    }

    [TestMethod]
    public void RepeatedTokensUseDeterministicDeletionFirstAlignment()
    {
        DocxTextDiff result = DocxTextDiffer.Compare("甲乙甲", "乙甲乙", 16);
        CollectionAssert.AreEqual(new[]
        {
            new DocxTextSegment("甲", ""),
            new DocxTextSegment("乙甲", "乙甲"),
            new DocxTextSegment("", "乙"),
        }, result.Segments.ToArray());
    }
}
