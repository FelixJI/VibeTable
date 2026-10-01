using System.IO.Compression;
using System.Text;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxSemanticDiffTests
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
    private const string R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";

    [TestMethod]
    public async Task CjkCharactersAlignAcrossRunsAndKeepBothDirectStyles()
    {
        string prefix = new('中', 300);
        var before = P(Run(prefix + "合同100", "b"), Run("万元。𠀀👩🏽‍💻", "i"));
        var after = P(Run(prefix + "合同120", "b"), Run("万元。𠀁👩🏻‍💻", "i"));
        var details = await Compare(before, after);
        var changes = details.Changes.Where(c => c.Kind == DocumentDiffChangeKind.Replace).ToArray();
        Assert.AreEqual(2, changes.Length);
        Assert.AreEqual("100万元", Text(changes[0].Before));
        Assert.AreEqual("120万元", Text(changes[0].After));
        Assert.AreEqual(true, changes[0].Before!.Runs[0].Bold);
        Assert.AreEqual(true, changes[0].Before!.Runs[1].Italic);
        Assert.AreEqual("𠀀👩🏽‍💻", Text(changes[1].Before));
        Assert.AreEqual("𠀁👩🏻‍💻", Text(changes[1].After));
        Assert.AreEqual(4, details.Summary!.RawRevisionCount);
        Assert.IsFalse(details.Coverage.Truncated);
    }

    [TestMethod]
    public async Task RunSplitsAndPropertyOrderDoNotCreateChanges()
    {
        var oldProperties = new XElement(W + "rPr", new XElement(W + "b", new XAttribute(W + "val", "true")),
            new XElement(W + "i"), new XElement(W + "rFonts", new XAttribute(W + "ascii", "Arial"), new XAttribute(W + "eastAsia", "宋体")));
        var newProperties = new XElement(W + "rPr", new XElement(W + "rFonts", new XAttribute(W + "eastAsia", "宋体"), new XAttribute(W + "ascii", "Arial")),
            new XElement(W + "i", new XAttribute(W + "val", "on")), new XElement(W + "b", new XAttribute(W + "val", "1")));
        var details = await Compare(P(new XElement(W + "r", oldProperties, new XElement(W + "t", "甲𠀀👩🏽‍💻乙"))),
            P(new XElement(W + "r", newProperties, new XElement(W + "t", "甲𠀀")),
                new XElement(W + "r", new XElement(newProperties), new XElement(W + "t", "👩🏽‍💻乙"))));
        Assert.AreEqual(0, details.Changes.Count);
        CollectionAssert.Contains(details.Warnings.ToArray(), DocumentDiffWarning.PartialCoverage);
    }

    [TestMethod]
    public async Task RepeatedParagraphInsertionKeepsStableAnchors()
    {
        var details = await Compare(Body(P("甲"), P("重复"), P("重复"), P("乙")),
            Body(P("新增"), P("甲"), P("重复"), P("重复"), P("乙")));
        Assert.AreEqual(1, details.Changes.Count);
        Assert.AreEqual(DocumentDiffChangeKind.Insert, details.Changes[0].Kind);
        Assert.AreEqual("新增", Text(details.Changes[0].After));
    }

    [TestMethod]
    public async Task EqualTextStillChecksCharacterAndParagraphFormatting()
    {
        var before = P(Run("相同文字", "b"));
        before.AddFirst(new XElement(W + "pPr", new XElement(W + "ind", new XAttribute(W + "left", "0"))));
        var after = P(Run("相同文字", "i"));
        after.AddFirst(new XElement(W + "pPr", new XElement(W + "ind", new XAttribute(W + "left", "720")),
            new XElement(W + "jc", new XAttribute(W + "val", "center"))));
        var details = await Compare(before, after);
        Assert.AreEqual(2, details.Summary!.FormattingChanges);
        Assert.AreEqual(true, details.Changes[0].Before!.Runs[0].Bold);
        Assert.AreEqual(true, details.Changes[0].After!.Runs[0].Italic);
        StringAssert.Contains(Text(details.Changes[1].Before), "左：0 磅");
        StringAssert.Contains(Text(details.Changes[1].After), "左：36 磅");
        StringAssert.Contains(Text(details.Changes[1].After), "居中");
    }

    [TestMethod]
    public async Task ParagraphSpacingCharacterIndentAndFlagsHaveIndependentReadableValues()
    {
        var before = P("未改文字");
        before.AddFirst(new XElement(W + "pPr",
            new XElement(W + "spacing", new XAttribute(W + "line", "480")),
            new XElement(W + "ind", new XAttribute(W + "firstLineChars", "100")),
            new XElement(W + "keepNext", new XAttribute(W + "val", "false"))));
        var after = P("未改文字");
        after.AddFirst(new XElement(W + "pPr",
            new XElement(W + "spacing", new XAttribute(W + "line", "260"), new XAttribute(W + "lineRule", "exact"),
                new XAttribute(W + "beforeAutospacing", "true")),
            new XElement(W + "ind", new XAttribute(W + "firstLineChars", "200")),
            new XElement(W + "keepNext"), new XElement(W + "bidi")));
        var details = await Compare(before, after);
        Assert.AreEqual(1, details.Summary!.FormattingChanges);
        StringAssert.Contains(Text(details.Changes[0].Before), "行距：2 倍");
        StringAssert.Contains(Text(details.Changes[0].Before), "首行：1 字符");
        StringAssert.Contains(Text(details.Changes[0].Before), "与下段同页：否");
        StringAssert.Contains(Text(details.Changes[0].After), "行距：13 磅");
        StringAssert.Contains(Text(details.Changes[0].After), "首行：2 字符");
        StringAssert.Contains(Text(details.Changes[0].After), "与下段同页：是");
        StringAssert.Contains(Text(details.Changes[0].After), "行距规则：固定值");
    }

    [TestMethod]
    public async Task ParagraphPropertyOrderAndNumericSpellingAreEquivalent()
    {
        var before = P("段落");
        before.AddFirst(new XElement(W + "pPr", new XElement(W + "ind", new XAttribute(W + "left", "0720"), new XAttribute(W + "right", "0")),
            new XElement(W + "spacing", new XAttribute(W + "after", "120"), new XAttribute(W + "before", "0"))));
        var after = P("段落");
        after.AddFirst(new XElement(W + "pPr", new XElement(W + "spacing", new XAttribute(W + "before", "0"), new XAttribute(W + "after", "0120")),
            new XElement(W + "ind", new XAttribute(W + "right", "00"), new XAttribute(W + "left", "720"))));
        Assert.AreEqual(0, (await Compare(before, after)).Changes.Count);
    }

    [TestMethod]
    public async Task TableCellsNeverShareOneTextChange()
    {
        var before = Table(Cell(P("甲旧乙")), Cell(P("丙旧丁")));
        var after = Table(Cell(P("甲新乙")), Cell(P("丙新丁")));
        var details = await Compare(before, after);
        Assert.AreEqual(2, details.Changes.Count);
        CollectionAssert.AreEqual(new int?[] { 0, 1 }, details.Changes.Select(c => c.Location.ColumnIndex).ToArray());
        Assert.IsTrue(details.Changes.All(c => c.Location.TableIndex == 0 && Text(c.Before) == "旧" && Text(c.After) == "新"));
    }

    [TestMethod]
    public async Task EmptyParagraphAndEmptyTableShapeChangesAreVisible()
    {
        var before = Body(P("锚点"), new XElement(W + "tbl", new XElement(W + "tr", Cell(P("")))));
        var after = Body(P(""), P("锚点"), new XElement(W + "tbl", new XElement(W + "tr", Cell(P("")), Cell(P("")))));
        var details = await Compare(before, after);
        Assert.IsTrue(details.Changes.Any(c => c.Kind == DocumentDiffChangeKind.Other));
        Assert.AreEqual(1, details.Summary!.TableChanges);
        Assert.IsTrue(details.Changes.Single(c => c.Kind == DocumentDiffChangeKind.Table).After!.Runs[0].Text.Contains("单元格", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task GridSpanAndVerticalMergeAreStructuralChanges()
    {
        var oldCell = Cell(P("表内"));
        var newCell = Cell(P("表内"));
        newCell.AddFirst(new XElement(W + "tcPr", new XElement(W + "gridSpan", new XAttribute(W + "val", "2")), new XElement(W + "vMerge")));
        Assert.AreEqual(1, (await Compare(Table(oldCell), Table(newCell))).Summary!.TableChanges);
    }

    [TestMethod]
    public async Task HeaderAndFooterChangesKeepTheirOwnStoryAndSection()
    {
        var details = await Compare(P("正文"), P("正文"), "页眉旧", "页眉新", "页脚旧", "页脚新");
        Assert.AreEqual(2, details.Changes.Count);
        CollectionAssert.AreEquivalent(new[] { DocumentDiffPart.Header, DocumentDiffPart.Footer }, details.Changes.Select(c => c.Location.Part).ToArray());
        Assert.IsTrue(details.Changes.All(c => c.Location.SectionIndex == 0));
    }

    [TestMethod]
    public async Task FieldResultsAndTextBoxesAreNotAbsorbedIntoBodyText()
    {
        var before = P("正文");
        before.Add(new XElement(W + "fldSimple", new XAttribute(W + "instr", "PAGE"), Run("1")),
            new XElement(W + "r", new XElement(W + "drawing", new XElement(W + "txbxContent", P("框旧")))));
        var after = P("正文");
        after.Add(new XElement(W + "fldSimple", new XAttribute(W + "instr", "PAGE"), Run("2")),
            new XElement(W + "r", new XElement(W + "drawing", new XElement(W + "txbxContent", P("框新")))));
        var details = await Compare(before, after);
        Assert.AreEqual(0, details.Changes.Count);
        Assert.AreEqual(DocumentDiffCoverageStatus.NotCovered, details.Coverage.Areas.Single(a => a.Area == DocumentDiffCoverageArea.Fields).Status);
        Assert.AreEqual(DocumentDiffCoverageStatus.NotCovered, details.Coverage.Areas.Single(a => a.Area == DocumentDiffCoverageArea.TextBoxes).Status);
    }

    [TestMethod]
    public async Task LcsBudgetReturnsBoundedCoarseSnippetsWithTruncation()
    {
        string before = string.Concat(Enumerable.Repeat("甲👩🏽‍💻", 1500));
        string after = string.Concat(Enumerable.Repeat("乙👩🏻‍💻", 1500));
        var details = await Compare(P(before), P(after));
        Assert.IsTrue(details.Coverage.Truncated);
        CollectionAssert.Contains(details.Warnings.ToArray(), DocumentDiffWarning.ResultTruncated);
        foreach (var snippet in details.Changes.SelectMany(c => new[] { c.Before, c.After }).OfType<DocumentDiffRichSnippet>())
        {
            string text = Text(snippet);
            Assert.IsTrue(text.Length <= 2048);
            Assert.IsTrue(text.EndsWith("👩🏽‍💻", StringComparison.Ordinal) || text.EndsWith("👩🏻‍💻", StringComparison.Ordinal)
                || text.EndsWith("甲", StringComparison.Ordinal) || text.EndsWith("乙", StringComparison.Ordinal));
        }
    }

    [TestMethod]
    public async Task SingleOversizedGraphemeDoesNotCreateInvalidSnippet()
    {
        var details = await Compare(P(""), P("a" + new string('\u0301', 3000)));
        Assert.IsTrue(details.Coverage.Truncated);
        Assert.AreEqual(DocumentDiffChangeKind.Other, details.Changes.Single().Kind);
        Assert.IsNull(details.Changes.Single().After);
    }

    [TestMethod]
    public async Task OfficeFormatFixtureDetectsFontAndParagraphIndentIndependently()
    {
        string root = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx");
        var outcome = await DocxSemanticDiff.CompareAsync(new(SourceFile(Path.Combine(root, "format-before.docx")),
            SourceFile(Path.Combine(root, "format-after.docx"))), default);
        Assert.AreNotEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        var details = outcome.Details!;
        Assert.IsTrue(details.Summary!.FormattingChanges >= 2);
        Assert.IsTrue(details.Changes.Any(c => c.After?.Runs.Any(r => r.FontSizePt == 14) == true));
    }

    [TestMethod]
    public async Task HeadingContextIsBoundedWithoutSplittingEmoji()
    {
        string title = string.Concat(Enumerable.Repeat("题👩🏽‍💻", 80));
        XElement Heading() => new(W + "p", new XElement(W + "pPr", new XElement(W + "outlineLvl", new XAttribute(W + "val", "0"))), Run(title));
        var details = await Compare(Body(Heading(), P("甲旧乙")), Body(Heading(), P("甲新乙")));
        string heading = details.Changes.Single().Location.NearestHeading!;
        Assert.IsTrue(heading.Length <= 256);
        Assert.IsTrue(heading.EndsWith("题", StringComparison.Ordinal) || heading.EndsWith("👩🏽‍💻", StringComparison.Ordinal));
        Assert.IsTrue(details.Coverage.Truncated);
    }

    [TestMethod]
    public async Task TablePropertyOrderAndImplicitDefaultsAreEquivalent()
    {
        var oldCell = Cell(P("表内"));
        oldCell.AddFirst(new XElement(W + "tcPr", new XElement(W + "gridSpan", new XAttribute(W + "val", "01")), new XElement(W + "vMerge")));
        var newCell = Cell(P("表内"));
        newCell.AddFirst(new XElement(W + "tcPr", new XElement(W + "vMerge", new XAttribute(W + "val", "continue"))));
        Assert.AreEqual(0, (await Compare(Table(oldCell), Table(newCell))).Changes.Count);
    }

    [TestMethod]
    public async Task CancellationAndCorruptPackagesFailExplicitly()
    {
        var source = Package(P("文字"));
        Assert.AreEqual(DocumentDiffFailureKind.Cancelled,
            (await DocxSemanticDiff.CompareAsync(new(source, source), new CancellationToken(true))).Failure);
        var invalid = new DocumentContentSource("bad.docx", null, 3,
            _ => ValueTask.FromResult<Stream>(new MemoryStream([1, 2, 3])));
        Assert.AreEqual(DocumentDiffFailureKind.InvalidContent, (await DocxSemanticDiff.CompareAsync(new(invalid, source), default)).Failure);
    }

    private static async Task<DocumentDiffDetails> Compare(XElement before, XElement after,
        string? oldHeader = null, string? newHeader = null, string? oldFooter = null, string? newFooter = null)
    {
        var outcome = await DocxSemanticDiff.CompareAsync(new(Package(before, oldHeader, oldFooter), Package(after, newHeader, newFooter)), default);
        Assert.AreNotEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.IsNotNull(outcome.Details);
        return outcome.Details;
    }

    private static DocumentContentSource SourceFile(string path) => new("sample.docx", null, new FileInfo(path).Length,
        _ => ValueTask.FromResult<Stream>(File.OpenRead(path)));

    private static XElement Run(string text, string? format = null) => new(W + "r",
        format is null ? null : new XElement(W + "rPr", new XElement(W + format)), new XElement(W + "t", text));
    private static XElement P(string text) => P(Run(text));
    private static XElement P(params XElement[] runs) => new(W + "p", runs);
    private static XElement Body(params XElement[] blocks) => new(W + "body", blocks);
    private static XElement Cell(params XElement[] paragraphs) => new(W + "tc", paragraphs);
    private static XElement Table(params XElement[] cells) => new(W + "tbl", new XElement(W + "tr", cells));
    private static string Text(DocumentDiffRichSnippet? snippet) => snippet is null ? "" : string.Concat(snippet.Runs.Select(r => r.Text));

    private static DocumentContentSource Package(XElement content, string? header = null, string? footer = null)
    {
        const string mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.";
        using var output = new MemoryStream();
        using (var zip = new ZipArchive(output, ZipArchiveMode.Create, leaveOpen: true))
        {
            var body = content.Name == W + "body" ? new XElement(content) : Body(new XElement(content));
            var types = new StringBuilder("<Types xmlns='http://schemas.openxmlformats.org/package/2006/content-types'><Default Extension='rels' ContentType='application/vnd.openxmlformats-package.relationships+xml'/>");
            types.Append($"<Override PartName='/word/document.xml' ContentType='{mime}document.main+xml'/>");
            var relationships = new StringBuilder();
            var section = new XElement(W + "sectPr");
            foreach (var (text, kind, rootName) in new[] { (header, "header", "hdr"), (footer, "footer", "ftr") })
            {
                if (text is null) continue;
                types.Append($"<Override PartName='/word/{kind}.xml' ContentType='{mime}{kind}+xml'/>");
                relationships.Append($"<Relationship Id='{kind}' Type='{R}/{kind}' Target='{kind}.xml'/>");
                section.Add(new XElement(W + (kind + "Reference"), new XAttribute(W + "type", "default"), new XAttribute(XName.Get("id", R), kind)));
                Write("word/" + kind + ".xml", new XElement(W + rootName, P(text)).ToString(SaveOptions.DisableFormatting));
            }
            body.Add(section);
            Write("[Content_Types].xml", types.Append("</Types>").ToString());
            Write("_rels/.rels", Relationships($"<Relationship Id='main' Type='{R}/officeDocument' Target='word/document.xml'/>"));
            Write("word/_rels/document.xml.rels", Relationships(relationships.ToString()));
            Write("word/document.xml", new XElement(W + "document", body).ToString(SaveOptions.DisableFormatting));
            void Write(string name, string text)
            {
                using var stream = zip.CreateEntry(name).Open();
                stream.Write(Encoding.UTF8.GetBytes(text));
            }
        }
        byte[] bytes = output.ToArray();
        return new("sample.docx", null, bytes.Length, _ => ValueTask.FromResult<Stream>(new MemoryStream(bytes, writable: false)));
    }

    private static string Relationships(string entries) =>
        "<Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'>" + entries + "</Relationships>";
}
