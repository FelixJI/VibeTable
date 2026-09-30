using System.Xml;
using System.Xml.Linq;
using VibeTable.DocumentDiff.OpenXml;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxRevisionRunsTests
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    [TestMethod]
    [DataRow("前\r旧\r\n后", "前\r新\r\n后")]
    [DataRow("\t旧\n", "\t新\n")]
    public void SerializedPartRestoresBothTextsAndLeavesOutputOpen(string before, string after)
    {
        int id = 0;
        var runs = DocxRevisionRuns.Build(DocxTextDiffer.Compare(before, after, 10_000),
            [new(before, Properties("b"))], [new(after, Properties("i"))], ref id);
        using var output = new MemoryStream();
        DocxPartWriter.Write(output, new XElement(W + "p", runs));
        Assert.IsTrue(output.CanWrite);
        output.Position = 0;
        var parsed = XElement.Load(output, LoadOptions.PreserveWhitespace).Elements().ToArray();
        AssertStyled([new(before, Properties("b"))], Restore(parsed, accept: false));
        AssertStyled([new(after, Properties("i"))], Restore(parsed, accept: true));
    }

    [TestMethod]
    [DataRow("合同状态为甲。𠀀👩🏽‍💻", "合同状态为乙。𠀁👩🏻‍💻")]
    [DataRow("", " 新增𠀀👩🏽‍💻 ")]
    [DataRow(" 删除𠀀👩🏽‍💻 ", "")]
    public void UnicodeRevisionsRestoreBothTexts(string before, string after)
    {
        int id = 7;
        IReadOnlyList<XElement> result = DocxRevisionRuns.Build(
            DocxTextDiffer.Compare(before, after, 10_000), [new(before, null)], [new(after, null)], ref id);
        Assert.AreEqual(before, Text(Restore(result, accept: false)));
        Assert.AreEqual(after, Text(Restore(result, accept: true)));
        XElement[] revisions = result.Where(e => e.Name != W + "r").ToArray();
        Assert.IsGreaterThan(0, revisions.Length);
        CollectionAssert.AreEqual(Enumerable.Range(7, revisions.Length).ToArray(),
            revisions.Select(e => (int)e.Attribute(W + "id")!).ToArray());
        Assert.AreEqual(7 + revisions.Length, id);
        foreach (XElement text in result.SelectMany(e => e.Descendants())
            .Where(e => e.Name == W + "t" || e.Name == W + "delText"))
        {
            Assert.AreEqual("preserve", (string?)text.Attribute(XNamespace.Xml + "space"));
            XmlConvert.VerifyXmlChars(text.Value);
        }
        XElement.Parse(new XElement(W + "p", result).ToString());
    }

    [TestMethod]
    public void ChangedRunsKeepEachSidesPropertiesAndInputsRemainUntouched()
    {
        XElement bold = Properties("b");
        XElement italic = Properties("i");
        var before = new DocxStyledText[] { new("甲旧", bold), new("文尾", italic) };
        var after = new DocxStyledText[] { new("甲新", italic), new("字尾", bold) };
        int id = 0;
        IReadOnlyList<XElement> result = DocxRevisionRuns.Build(
            DocxTextDiffer.Compare("甲旧文尾", "甲新字尾", 10_000), before, after, ref id);

        AssertStyled(before, Restore(result, accept: false));
        AssertStyled(after, Restore(result, accept: true));
        XElement deleted = result.Single(e => e.Name == W + "del");
        CollectionAssert.AreEqual(new[] { "旧", "文" },
            deleted.Elements(W + "r").Select(e => e.Element(W + "delText")!.Value).ToArray());
        Assert.IsTrue(XNode.DeepEquals(Properties("b"), bold));
        Assert.IsTrue(XNode.DeepEquals(Properties("i"), italic));
        Assert.IsNull(bold.Parent);
        Assert.IsNull(italic.Parent);
    }

    [TestMethod]
    public void FormatOnlyChangesSplitAtBothBoundariesAndRestoreOldProperties()
    {
        var before = new DocxStyledText[] { new("甲乙", Properties("b")), new("丙", null) };
        var after = new DocxStyledText[] { new("甲", null), new("乙丙", Properties("i")) };
        int id = 20;
        IReadOnlyList<XElement> result = DocxRevisionRuns.Build(
            DocxTextDiffer.Compare("甲乙丙", "甲乙丙", 0), before, after, ref id);

        Assert.AreEqual(3, result.Count);
        AssertStyled(before, Restore(result, accept: false));
        AssertStyled(after, Restore(result, accept: true));
        Assert.AreEqual(23, id);
        foreach (XElement run in result)
        {
            XElement properties = run.Element(W + "rPr")!;
            Assert.AreEqual(W + "rPrChange", properties.Elements().Last().Name);
            Assert.IsNotNull(properties.Element(W + "rPrChange")!.Element(W + "rPr"));
        }
    }

    [TestMethod]
    public void SurrogatePairsSplitAcrossEquallyStyledRunsProduceValidXml()
    {
        var before = new DocxStyledText[]
        {
            new("前\uD840", Properties("b")), new("\uDC00👩", Properties("b")),
            new("🏽‍💻后", Properties("i")),
        };
        var after = new DocxStyledText[]
        {
            new("前\uD840", Properties("b")), new("\uDC01👩", Properties("b")),
            new("🏻‍💻后", Properties("i")),
        };
        int id = 0;
        IReadOnlyList<XElement> result = DocxRevisionRuns.Build(
            DocxTextDiffer.Compare("前𠀀👩🏽‍💻后", "前𠀁👩🏻‍💻后", 10_000), before, after, ref id);

        AssertStyled(before, Restore(result, accept: false));
        AssertStyled(after, Restore(result, accept: true));
        foreach (XElement text in result.SelectMany(e => e.Descendants())
            .Where(e => e.Name == W + "t" || e.Name == W + "delText"))
            XmlConvert.VerifyXmlChars(text.Value);
        XElement.Parse(new XElement(W + "p", result).ToString());
    }

    [TestMethod]
    public void TruncationMismatchAndUnconsumedTailAreRejectedWithoutAllocatingIds()
    {
        int id = 9;
        Assert.ThrowsExactly<ArgumentException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("旧", "新")], true), [new("旧", null)], [new("新", null)], ref id));
        Assert.ThrowsExactly<ArgumentException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("错", "新")], false), [new("旧", null)], [new("新", null)], ref id));
        Assert.ThrowsExactly<ArgumentException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("旧", "新")], false), [new("旧尾", null)], [new("新", null)], ref id));
        Assert.ThrowsExactly<ArgumentException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("旧", "新")], false), [new("旧", null)], [new("新尾", null)], ref id));
        Assert.AreEqual(9, id);
    }

    [TestMethod]
    public void InvalidSurrogatesAndExistingPropertyRevisionsAreRejected()
    {
        int id = 0;
        Assert.ThrowsExactly<XmlException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("𠀀", "𠀀")], false),
            [new("\uD840", Properties("b")), new("\uDC00", Properties("i"))],
            [new("𠀀", null)], ref id));
        Assert.ThrowsExactly<XmlException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("\uD840", ""), new("\uDC00", "𠀀")], false),
            [new("𠀀", null)], [new("𠀀", null)], ref id));
        Assert.ThrowsExactly<ArgumentException>(() => DocxRevisionRuns.Build(
            new DocxTextDiff([new("甲", "甲")], false),
            [new("甲", new XElement(W + "rPr", new XElement(W + "rPrChange")))],
            [new("甲", null)], ref id));
    }

    [TestMethod]
    public void EmptyAndEquivalentPropertiesDoNotCreateRevisions()
    {
        int id = 2;
        Assert.AreEqual(0, DocxRevisionRuns.Build(
            new DocxTextDiff([], false), [new("", null)], [], ref id).Count);
        IReadOnlyList<XElement> result = DocxRevisionRuns.Build(
            new DocxTextDiff([new("相同", "相同")], false),
            [new("相同", null)], [new("相同", new XElement(W + "rPr"))], ref id);
        Assert.AreEqual(2, id);
        Assert.AreEqual("相同", Text(Restore(result, accept: true)));
        Assert.IsFalse(result.SelectMany(e => e.Descendants(W + "rPrChange")).Any());
    }

    private static XElement Properties(string name) => new(W + "rPr", new XElement(W + name));

    private static IReadOnlyList<DocxStyledText> Restore(IReadOnlyList<XElement> elements, bool accept)
    {
        var restored = new List<DocxStyledText>();
        foreach (XElement element in elements)
        {
            if (element.Name == W + (accept ? "del" : "ins"))
                continue;
            foreach (XElement run in element.Name == W + "r" ? [element] : element.Elements(W + "r"))
            {
                XElement? properties = run.Element(W + "rPr") is { } source ? new XElement(source) : null;
                if (properties?.Element(W + "rPrChange") is { } change)
                {
                    if (accept)
                        change.Remove();
                    else
                        properties = new XElement(change.Element(W + "rPr")!);
                }
                restored.Add(new DocxStyledText(string.Concat(run.Elements()
                    .Where(e => e.Name == W + "t" || e.Name == W + "delText").Select(e => e.Value)), properties));
            }
        }
        return restored;
    }

    private static string Text(IReadOnlyList<DocxStyledText> runs) => string.Concat(runs.Select(r => r.Text));

    private static void AssertStyled(IReadOnlyList<DocxStyledText> expected, IReadOnlyList<DocxStyledText> actual)
    {
        Assert.AreEqual(Text(expected), Text(actual));
        // Compare formatting per UTF-16 code unit, independently of output run boundaries.
        XElement[] expectedProperties = expected.SelectMany(r => Enumerable.Repeat(
            r.RunProperties ?? new XElement(W + "rPr"), r.Text.Length)).ToArray();
        XElement[] actualProperties = actual.SelectMany(r => Enumerable.Repeat(
            r.RunProperties ?? new XElement(W + "rPr"), r.Text.Length)).ToArray();
        for (int i = 0; i < expectedProperties.Length; i++)
            Assert.IsTrue(XNode.DeepEquals(expectedProperties[i], actualProperties[i]), $"Style differs at {i}.");
    }
}
