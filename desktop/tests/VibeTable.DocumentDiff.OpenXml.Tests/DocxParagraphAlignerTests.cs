namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxParagraphAlignerTests
{
    [TestMethod]
    public void InsertionDoesNotShiftAllSubsequentParagraphs()
    {
        var result = DocxParagraphAligner.Align(["第一段", "第二段"], ["新增", "第一段", "第二段"], 0);
        Assert.IsFalse(result.Truncated);
        CollectionAssert.AreEqual(new[]
        {
            new DocxParagraphSpan(0, 0, 0, 1, false),
            new DocxParagraphSpan(0, 2, 1, 2, true),
        }, result.Spans.ToArray());
    }

    [TestMethod]
    public void ReplacementRunsStayBetweenExactAnchorsWithoutGuessingCorrespondence()
    {
        var result = DocxParagraphAligner.Align(["开头", "100万元", "删除", "结尾"],
            ["开头", "120万元", "结尾"], 20);
        CollectionAssert.AreEqual(new[]
        {
            new DocxParagraphSpan(0, 1, 0, 1, true),
            new DocxParagraphSpan(1, 2, 1, 1, false),
            new DocxParagraphSpan(3, 1, 2, 1, true),
        }, result.Spans.ToArray());
    }

    [TestMethod]
    public void InternalAnchorsAndRepeatedParagraphTiesAreDeterministic()
    {
        var result = DocxParagraphAligner.Align(["A", "B"], ["B", "A"], 9);
        CollectionAssert.AreEqual(new[]
        {
            new DocxParagraphSpan(0, 1, 0, 0, false),
            new DocxParagraphSpan(1, 1, 0, 1, true),
            new DocxParagraphSpan(2, 0, 1, 1, false),
        }, result.Spans.ToArray());
    }

    [TestMethod]
    public void BudgetIncludesBordersAndTruncationRetainsBothUnmatchedRanges()
    {
        var limited = DocxParagraphAligner.Align(["a"], ["b"], 3);
        Assert.IsTrue(limited.Truncated);
        Assert.AreEqual(new DocxParagraphSpan(0, 1, 0, 1, false), limited.Spans.Single());
        Assert.IsFalse(DocxParagraphAligner.Align(["a"], ["b"], 4).Truncated);
        Assert.AreEqual(0, DocxParagraphAligner.Align([], [], 0).Spans.Count);
        Assert.ThrowsExactly<OperationCanceledException>(() =>
            DocxParagraphAligner.Align(["a"], ["a"], 0, new CancellationToken(true)));
    }

    [TestMethod]
    public void VisibleTextAndParagraphBudgetsAcceptBoundaryAndRejectExcess()
    {
        string text = new('中', OpenXmlExtractionLimits.MaxVisibleTextCharacters);
        Assert.IsFalse(DocxParagraphAligner.Align([text], [text], 0).Truncated);
        Assert.IsTrue(DocxParagraphAligner.Align([text + "文"], [text], 0).Truncated);
        string[] paragraphs = Enumerable.Repeat(string.Empty, 250_000).ToArray();
        Assert.IsFalse(DocxParagraphAligner.Align(paragraphs, [], 0).Truncated);
        Assert.IsTrue(DocxParagraphAligner.Align([], [.. paragraphs, string.Empty], 0).Truncated);
    }

    [TestMethod]
    public void CancellationDuringAlignmentStopsBeforeReturningRanges()
    {
        using var cancellation = new CancellationTokenSource();
        var before = new CancellingParagraphs(cancellation);
        string[] after = Enumerable.Repeat("different", before.Count).ToArray();
        Assert.ThrowsExactly<OperationCanceledException>(() =>
            DocxParagraphAligner.Align(before, after, 10_000, cancellation.Token));
    }

    private sealed class CancellingParagraphs(CancellationTokenSource cancellation) : IReadOnlyList<string>
    {
        private readonly string[] _items = Enumerable.Repeat("original", 80).ToArray();
        private int _reads;
        public int Count => _items.Length;
        public string this[int index]
        {
            get
            {
                if (++_reads == 20)
                    cancellation.Cancel();
                return _items[index];
            }
        }
        public IEnumerator<string> GetEnumerator() => ((IEnumerable<string>)_items).GetEnumerator();
        System.Collections.IEnumerator System.Collections.IEnumerable.GetEnumerator() => GetEnumerator();
    }
}
