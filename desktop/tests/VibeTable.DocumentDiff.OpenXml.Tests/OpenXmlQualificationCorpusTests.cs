using System.IO.Compression;
using System.Text.Json;
using System.Xml.Linq;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class OpenXmlQualificationCorpusTests
{
    private static readonly string[] RequiredCoverage =
    [
        "docx.cjk.single-character",
        "docx.cjk.sentence-insert",
        "docx.mixed-language",
        "docx.punctuation",
        "docx.paragraph-move",
        "docx.table.cell",
        "docx.table.row-column",
        "docx.header-footer",
        "docx.footnote",
        "docx.text-box",
        "docx.image",
        "docx.format.font",
        "docx.format.paragraph-indent",
        "docx.existing-revisions",
        "docx.corrupt",
        "docx.large",
        "xlsx.value",
        "xlsx.formula",
        "xlsx.style",
        "xlsx.worksheet-add-delete",
        "xlsx.merge",
        "xlsx.hidden-row-column",
        "xlsx.sparse-large",
        "xlsx.macro-container",
    ];

    [TestMethod]
    public void QualificationCorpus_CoversRequiredRiskClasses()
    {
        var manifest = LoadManifest();
        var covered = manifest.Pairs
            .SelectMany(item => item.Covers)
            .Concat(manifest.Artifacts.SelectMany(item => item.Covers))
            .ToHashSet(StringComparer.Ordinal);

        CollectionAssert.IsSubsetOf(RequiredCoverage, covered.ToArray());
    }

    [TestMethod]
    public void QualificationCorpus_UsesDeclaredOfficePackages()
    {
        var manifest = LoadManifest();
        foreach (var item in manifest.Pairs.SelectMany(pair => new[]
                 {
                     new CorpusFile(pair.Id, pair.Before, pair.Producer),
                     new CorpusFile(pair.Id, pair.After, pair.Producer),
                 }).Concat(manifest.Artifacts
                     .Where(item => item.ValidPackage)
                     .Select(item => new CorpusFile(item.Id, item.Path, item.Producer))))
        {
            var path = CorpusPath(item.Path);
            Assert.IsTrue(File.Exists(path), $"Corpus file is missing for {item.Id}: {item.Path}");
            using var archive = ZipFile.OpenRead(path);
            Assert.IsNotNull(archive.GetEntry("[Content_Types].xml"), item.Id);
            var application = ReadEntry(archive, "docProps/app.xml");
            StringAssert.Contains(application, item.Producer, StringComparison.OrdinalIgnoreCase);
            var properties = ReadEntry(archive, "docProps/core.xml");
            StringAssert.Contains(
                properties,
                "<dc:creator>VibeTable Qualification</dc:creator>");
            StringAssert.Contains(
                properties,
                "<cp:lastModifiedBy>VibeTable Qualification</cp:lastModifiedBy>");
            foreach (var entry in archive.Entries.Where(entry =>
                         entry.FullName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase)
                         || entry.FullName.EndsWith(".rels", StringComparison.OrdinalIgnoreCase)))
            {
                var content = ReadEntry(archive, entry.FullName);
                Assert.IsFalse(
                    content.Contains(":\\", StringComparison.Ordinal),
                    $"Local path metadata found in {item.Id}:{entry.FullName}");
                Assert.IsFalse(
                    content.Contains("absPath", StringComparison.Ordinal),
                    $"Office absolute-path metadata found in {item.Id}:{entry.FullName}");
                var xml = XDocument.Parse(content, LoadOptions.PreserveWhitespace);
                foreach (var identity in xml.Descendants()
                             .Where(element => element.Name.LocalName is
                                 "author" or "creator" or "lastModifiedBy")
                             .Select(element => element.Value)
                             .Concat(xml.Descendants().Attributes()
                                 .Where(attribute => attribute.Name.LocalName == "author")
                                 .Select(attribute => attribute.Value)))
                {
                    Assert.AreEqual("VibeTable Qualification", identity, entry.FullName);
                }
                foreach (var userId in xml.Descendants().Attributes()
                             .Where(attribute => attribute.Name.LocalName == "userId"))
                {
                    Assert.AreEqual("qualification", userId.Value, entry.FullName);
                }
                foreach (var providerId in xml.Descendants().Attributes()
                             .Where(attribute => attribute.Name.LocalName == "providerId"))
                {
                    Assert.AreEqual("VibeTable", providerId.Value, entry.FullName);
                }
            }
        }
    }

    [TestMethod]
    public async Task CompareAsync_QualificationPairsMatchExpectedOutcomes()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var manifest = LoadManifest();
        foreach (var pair in manifest.Pairs)
        {
            var beforePath = CorpusPath(pair.Before);
            var afterPath = CorpusPath(pair.After);
            var outcome = await engine.CompareAsync(
                new DocumentDiffRequest(
                    Content(beforePath, pair.MimeType),
                    Content(afterPath, pair.MimeType)),
                CancellationToken.None);

            Assert.AreEqual(
                Enum.Parse<DocumentDiffOutcomeKind>(pair.ExpectedOutcome),
                outcome.Kind,
                pair.Id);
        }
    }

    [TestMethod]
    public void QualificationCorpus_DocxPairsEncodeRequiredSemanticChanges()
    {
        using (var before = ZipFile.OpenRead(CorpusPath("docx/cjk-before.docx")))
        using (var after = ZipFile.OpenRead(CorpusPath("docx/cjk-after.docx")))
        {
            StringAssert.Contains(ReadEntry(before, "word/document.xml"), "合同状态为甲。");
            StringAssert.Contains(ReadEntry(after, "word/document.xml"), "合同状态为乙。");
        }

        using (var before = ZipFile.OpenRead(CorpusPath("docx/content-before.docx")))
        using (var after = ZipFile.OpenRead(CorpusPath("docx/content-after.docx")))
        {
            var beforeDocument = ReadXml(before, "word/document.xml");
            var afterDocument = ReadXml(after, "word/document.xml");
            var beforeText = WordText(beforeDocument);
            var afterText = WordText(afterDocument);
            Assert.IsFalse(beforeText.Contains("这是新增的中文整句。", StringComparison.Ordinal));
            StringAssert.Contains(afterText, "这是新增的中文整句。");
            StringAssert.Contains(beforeText, "VibeTable 版本 1.0。");
            StringAssert.Contains(afterText, "VibeTable 版本 2.0。");
            StringAssert.Contains(beforeText, "合同金额为 100 万元。");
            StringAssert.Contains(afterText, "合同金额为 120 万元！");

            var beforeParagraphs = WordParagraphs(beforeDocument);
            var afterParagraphs = WordParagraphs(afterDocument);
            Assert.IsTrue(
                beforeParagraphs.IndexOf("将被移动的段落。")
                < beforeParagraphs.IndexOf("保留段落。"));
            Assert.IsTrue(
                afterParagraphs.IndexOf("将被移动的段落。")
                > afterParagraphs.IndexOf("保留段落。"));

            Assert.AreEqual(2, Elements(beforeDocument, "tr").Count);
            Assert.AreEqual(3, Elements(afterDocument, "tr").Count);
            Assert.AreEqual(4, Elements(beforeDocument, "tc").Count);
            Assert.AreEqual(9, Elements(afterDocument, "tc").Count);
            StringAssert.Contains(beforeText, "软件100");
            StringAssert.Contains(afterText, "软件120CNY");

            StringAssert.Contains(PartText(before, "word/header"), "资格样本页眉（旧）");
            StringAssert.Contains(PartText(after, "word/header"), "资格样本页眉（新）");
            StringAssert.Contains(PartText(before, "word/footer"), "资格样本页脚（旧）");
            StringAssert.Contains(PartText(after, "word/footer"), "资格样本页脚（新）");
            StringAssert.Contains(WordText(ReadXml(before, "word/footnotes.xml")), "原脚注");
            StringAssert.Contains(WordText(ReadXml(after, "word/footnotes.xml")), "新脚注");
            StringAssert.Contains(beforeText, "文本框（旧）");
            StringAssert.Contains(afterText, "文本框（新）");
            Assert.IsTrue(Elements(beforeDocument, "txbxContent").Count > 0);
            Assert.IsTrue(Elements(afterDocument, "txbxContent").Count > 0);
            Assert.AreEqual(0, before.Entries.Count(IsWordMedia));
            Assert.AreEqual(1, after.Entries.Count(IsWordMedia));
        }

        using (var before = ZipFile.OpenRead(CorpusPath("docx/format-before.docx")))
        using (var after = ZipFile.OpenRead(CorpusPath("docx/format-after.docx")))
        {
            var beforeDocument = ReadXml(before, "word/document.xml");
            var afterDocument = ReadXml(after, "word/document.xml");
            Assert.AreEqual(WordText(beforeDocument), WordText(afterDocument));
            Assert.AreEqual(0, Elements(beforeDocument, "sz").Count);
            Assert.IsTrue(Elements(afterDocument, "sz").Any(element =>
                Attribute(element, "val") == "28"));
            Assert.AreEqual(0, Elements(beforeDocument, "ind").Count);
            Assert.IsTrue(Elements(afterDocument, "ind").Any(element =>
                Attribute(element, "left") == "720"));
        }

        using var largeBefore = ZipFile.OpenRead(CorpusPath("docx/large-before.docx"));
        using var largeAfter = ZipFile.OpenRead(CorpusPath("docx/large-after.docx"));
        var beforeLargeDocument = ReadXml(largeBefore, "word/document.xml");
        var afterLargeDocument = ReadXml(largeAfter, "word/document.xml");
        Assert.AreEqual(2_101, Elements(beforeLargeDocument, "p").Count);
        Assert.AreEqual(2_101, Elements(afterLargeDocument, "p").Count);
        Assert.IsFalse(WordText(beforeLargeDocument)
            .Contains("2100 段（已修改）", StringComparison.Ordinal));
        StringAssert.Contains(WordText(afterLargeDocument), "2100 段（已修改）");
    }

    [TestMethod]
    public void QualificationCorpus_XlsxPairsEncodeRequiredSemanticChanges()
    {
        using (var before = ZipFile.OpenRead(CorpusPath("xlsx/content-before.xlsx")))
        using (var after = ZipFile.OpenRead(CorpusPath("xlsx/content-after.xlsx")))
        {
            var beforeWorkbook = ReadXml(before, "xl/workbook.xml");
            var afterWorkbook = ReadXml(after, "xl/workbook.xml");
            CollectionAssert.Contains(SheetNames(beforeWorkbook), "待删除工作表");
            CollectionAssert.DoesNotContain(SheetNames(afterWorkbook), "待删除工作表");
            CollectionAssert.Contains(SheetNames(afterWorkbook), "新增工作表");

            var beforeSheet = ReadXml(before, "xl/worksheets/sheet2.xml");
            var afterSheet = ReadXml(after, "xl/worksheets/sheet2.xml");
            Assert.AreEqual("100", CellValue(beforeSheet, "B7"));
            Assert.AreEqual("120", CellValue(afterSheet, "B7"));
            Assert.AreEqual("B12*C12", CellFormula(beforeSheet, "D12"));
            Assert.AreEqual("B12*C12*(1-E12)", CellFormula(afterSheet, "D12"));
            Assert.AreEqual("A1:C1", Attribute(Elements(beforeSheet, "mergeCell").Single(), "ref"));
            Assert.AreEqual("A1:D1", Attribute(Elements(afterSheet, "mergeCell").Single(), "ref"));
            Assert.IsTrue(Elements(beforeSheet, "row").Any(element =>
                Attribute(element, "r") == "8" && Attribute(element, "hidden") == "1"));
            Assert.IsTrue(Elements(beforeSheet, "col").Any(element =>
                Attribute(element, "min") == "6" && Attribute(element, "hidden") == "1"));
            Assert.IsFalse(Elements(afterSheet, "row").Any(element =>
                OptionalAttribute(element, "hidden") == "1"));
            Assert.IsFalse(Elements(afterSheet, "col").Any(element =>
                OptionalAttribute(element, "hidden") == "1"));
            Assert.AreEqual("稀疏尾单元格", SharedStringValue(after, afterSheet, "Z10000"));
        }

        using (var before = ZipFile.OpenRead(CorpusPath("xlsx/format-before.xlsx")))
        using (var after = ZipFile.OpenRead(CorpusPath("xlsx/format-after.xlsx")))
        {
            var beforeSheet = ReadXml(before, "xl/worksheets/sheet1.xml");
            var afterSheet = ReadXml(after, "xl/worksheets/sheet1.xml");
            Assert.AreEqual("100", CellValue(beforeSheet, "B7"));
            Assert.AreEqual("100", CellValue(afterSheet, "B7"));
            var beforeStyles = ReadXml(before, "xl/styles.xml");
            var afterStyles = ReadXml(after, "xl/styles.xml");
            Assert.AreEqual("2", Attribute(CellFormat(beforeStyles, 2), "numFmtId"));
            Assert.AreEqual("10", Attribute(CellFormat(afterStyles, 2), "numFmtId"));
            Assert.IsFalse(Font(beforeStyles, 2).Elements().Any(element => element.Name.LocalName == "b"));
            Assert.IsTrue(Font(afterStyles, 2).Elements().Any(element => element.Name.LocalName == "b"));
            Assert.AreEqual("FFFFFFFF", FillColor(beforeStyles, 2));
            Assert.AreEqual("FFFFFF00", FillColor(afterStyles, 2));
        }

        using var macroWorkbook = ZipFile.OpenRead(CorpusPath("xlsx/macro-container.xlsm"));
        var macroSheet = ReadXml(macroWorkbook, "xl/macrosheets/sheet1.xml");
        CollectionAssert.AreEqual(
            new[] { "RETURN()" },
            Elements(macroSheet, "f").Select(element => element.Value).ToArray());
        Assert.IsFalse(macroWorkbook.Entries.Any(entry =>
            entry.FullName.EndsWith("vbaProject.bin", StringComparison.OrdinalIgnoreCase)));
        Assert.IsFalse(macroWorkbook.Entries.Any(entry =>
            entry.FullName.StartsWith("xl/externalLinks/", StringComparison.Ordinal)));
        Assert.IsFalse(Elements(ReadXml(macroWorkbook, "xl/workbook.xml"), "definedName")
            .Select(element => OptionalAttribute(element, "name"))
            .Any(name => name?.Contains("Auto_", StringComparison.OrdinalIgnoreCase) is true));
        foreach (var relationship in macroWorkbook.Entries.Where(entry =>
                     entry.FullName.EndsWith(".rels", StringComparison.OrdinalIgnoreCase)))
        {
            var content = ReadEntry(macroWorkbook, relationship.FullName);
            Assert.IsFalse(content.Contains("TargetMode=\"External\"", StringComparison.Ordinal));
            Assert.IsFalse(content.Contains("relationships/vbaProject", StringComparison.OrdinalIgnoreCase));
        }
    }

    [TestMethod]
    public void QualificationEvidence_MatchesRecordedClippitBoundaries()
    {
        using (var compared = ZipFile.OpenRead(CorpusPath("evidence/clippit-cjk-compared.docx")))
        {
            var document = ReadXml(compared, "word/document.xml");
            var revisions = RevisionElements(document);
            Assert.AreEqual(2, revisions.Count);
            Assert.AreEqual("合同状态为甲。", WordText(revisions.Single(element =>
                element.Name.LocalName == "del")));
            Assert.AreEqual("合同状态为乙。", WordText(revisions.Single(element =>
                element.Name.LocalName == "ins")));
        }

        using (var compared = ZipFile.OpenRead(CorpusPath("evidence/clippit-format-compared.docx")))
        {
            Assert.AreEqual(0, RevisionElements(ReadXml(compared, "word/document.xml")).Count);
        }

        using var source = ZipFile.OpenRead(CorpusPath("docx/existing-revisions.docx"));
        using var accepted = ZipFile.OpenRead(
            CorpusPath("evidence/clippit-existing-revisions-accepted.docx"));
        Assert.AreEqual(2, RevisionElements(ReadXml(source, "word/document.xml")).Count);
        Assert.AreEqual(0, RevisionElements(ReadXml(accepted, "word/document.xml")).Count);
    }

    [TestMethod]
    public void QualificationResults_RecordExactToolAndOfficeObservations()
    {
        using var results = JsonDocument.Parse(File.ReadAllText(CorpusPath("qualification-results.json")));
        var root = results.RootElement;
        Assert.AreEqual(1, root.GetProperty("schemaVersion").GetInt32());
        var tools = root.GetProperty("tools");
        Assert.AreEqual("0.9.0", tools.GetProperty("clippitCli").GetString());
        Assert.AreEqual("3.5.1", tools.GetProperty("clippitOpenXmlSdk").GetString());
        Assert.AreEqual("3.9.0", tools.GetProperty("clippitLibraryCandidate").GetString());
        Assert.AreEqual(2, Observation(root, "clippit-cjk-single-character", "revisions").GetInt32());
        Assert.AreEqual(0, Observation(root, "clippit-format-only", "revisions").GetInt32());
        Assert.AreEqual(
            0,
            Observation(root, "clippit-accept-existing-revisions", "outputRevisionElements")
                .GetInt32());
        Assert.AreEqual(15, Observation(root, "clippit-comprehensive", "revisions").GetInt32());
        Assert.AreEqual(14, Observation(root, "word-native-comprehensive", "revisions").GetInt32());
        Assert.AreEqual(2, Observation(root, "word-native-format-only", "revisions").GetInt32());
        Assert.AreEqual(
            "inconclusive",
            Observation(root, "wps-viewer-probe", "result").GetString());
        Assert.AreEqual(
            "not-qualified",
            Observation(root, "wps-native-provider-probe", "result").GetString());
        foreach (var scenario in root.GetProperty("scenarios").EnumerateArray())
        {
            if (scenario.TryGetProperty("evidence", out var evidence))
            {
                Assert.IsTrue(File.Exists(CorpusPath(evidence.GetString()!)), scenario.GetProperty("id").GetString());
            }
        }
    }

    [TestMethod]
    public async Task CompareAsync_CorruptQualificationArtifactReturnsInvalidContent()
    {
        const string docxMime =
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();

        var outcome = await engine.CompareAsync(
            new DocumentDiffRequest(
                Content(CorpusPath("docx/content-before.docx"), docxMime),
                Content(CorpusPath("docx/corrupt.docx"), docxMime)),
            CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.InvalidContent, outcome.Failure);
    }

    private static QualificationManifest LoadManifest()
    {
        var path = CorpusPath("manifest.json");
        Assert.IsTrue(File.Exists(path), $"Qualification manifest is missing: {path}");
        var manifest = JsonSerializer.Deserialize<QualificationManifest>(
            File.ReadAllText(path),
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
        return manifest ?? throw new AssertFailedException("Qualification manifest is empty.");
    }

    private static DocumentContentSource Content(string path, string mimeType)
    {
        var info = new FileInfo(path);
        return new DocumentContentSource(
            info.Name,
            mimeType,
            info.Length,
            _ => ValueTask.FromResult<Stream>(File.OpenRead(path)));
    }

    private static string CorpusPath(string relativePath)
    {
        return Path.Combine(
            AppContext.BaseDirectory,
            "Qualification",
            relativePath.Replace('/', Path.DirectorySeparatorChar));
    }

    private static string ReadEntry(ZipArchive archive, string name)
    {
        var entry = archive.GetEntry(name);
        Assert.IsNotNull(entry, name);
        using var reader = new StreamReader(entry.Open());
        return reader.ReadToEnd();
    }

    private static XDocument ReadXml(ZipArchive archive, string name)
    {
        return XDocument.Parse(ReadEntry(archive, name), LoadOptions.PreserveWhitespace);
    }

    private static List<XElement> Elements(XContainer container, string localName)
    {
        return container.Descendants()
            .Where(element => element.Name.LocalName == localName)
            .ToList();
    }

    private static string Attribute(XElement element, string localName)
    {
        return element.Attributes().Single(attribute => attribute.Name.LocalName == localName).Value;
    }

    private static string? OptionalAttribute(XElement element, string localName)
    {
        return element.Attributes()
            .SingleOrDefault(attribute => attribute.Name.LocalName == localName)?.Value;
    }

    private static string WordText(XContainer container)
    {
        return string.Concat(container.Descendants()
            .Where(element => element.Name.LocalName is "t" or "delText")
            .Select(element => element.Value));
    }

    private static List<string> WordParagraphs(XContainer container)
    {
        return Elements(container, "p")
            .Select(WordText)
            .Where(text => text.Length > 0)
            .ToList();
    }

    private static string PartText(ZipArchive archive, string prefix)
    {
        return string.Concat(archive.Entries
            .Where(entry => entry.FullName.StartsWith(prefix, StringComparison.Ordinal)
                && entry.FullName.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            .Select(entry => WordText(ReadXml(archive, entry.FullName))));
    }

    private static bool IsWordMedia(ZipArchiveEntry entry)
    {
        return entry.FullName.StartsWith("word/media/", StringComparison.Ordinal);
    }

    private static string[] SheetNames(XDocument workbook)
    {
        return Elements(workbook, "sheet")
            .Select(element => Attribute(element, "name"))
            .ToArray();
    }

    private static XElement Cell(XDocument worksheet, string reference)
    {
        return Elements(worksheet, "c").Single(element => Attribute(element, "r") == reference);
    }

    private static string CellValue(XDocument worksheet, string reference)
    {
        return Elements(Cell(worksheet, reference), "v").Single().Value;
    }

    private static string CellFormula(XDocument worksheet, string reference)
    {
        return Elements(Cell(worksheet, reference), "f").Single().Value;
    }

    private static string SharedStringValue(
        ZipArchive archive,
        XDocument worksheet,
        string reference)
    {
        var index = int.Parse(CellValue(worksheet, reference), System.Globalization.CultureInfo.InvariantCulture);
        var strings = Elements(ReadXml(archive, "xl/sharedStrings.xml"), "si");
        return WordText(strings[index]);
    }

    private static XElement CellFormat(XDocument styles, int index)
    {
        return Elements(styles, "cellXfs").Single().Elements()
            .Where(element => element.Name.LocalName == "xf")
            .ElementAt(index);
    }

    private static XElement Font(XDocument styles, int index)
    {
        return Elements(styles, "fonts").Single().Elements()
            .Where(element => element.Name.LocalName == "font")
            .ElementAt(index);
    }

    private static string FillColor(XDocument styles, int index)
    {
        var fill = Elements(styles, "fills").Single().Elements()
            .Where(element => element.Name.LocalName == "fill")
            .ElementAt(index);
        return Elements(fill, "fgColor").Select(element => Attribute(element, "rgb")).Single();
    }

    private static List<XElement> RevisionElements(XDocument document)
    {
        return document.Descendants().Where(element => element.Name.LocalName is
            "ins" or "del" or "moveFrom" or "moveTo").ToList();
    }

    private static JsonElement Observation(JsonElement root, string scenarioId, string name)
    {
        return root.GetProperty("scenarios").EnumerateArray()
            .Single(scenario => scenario.GetProperty("id").GetString() == scenarioId)
            .GetProperty("observations")
            .GetProperty(name);
    }

    private sealed record QualificationManifest(
        int SchemaVersion,
        IReadOnlyList<QualificationPair> Pairs,
        IReadOnlyList<QualificationArtifact> Artifacts);

    private sealed record QualificationPair(
        string Id,
        string Before,
        string After,
        string MimeType,
        string ExpectedOutcome,
        string Producer,
        IReadOnlyList<string> Covers);

    private sealed record QualificationArtifact(
        string Id,
        string Path,
        string Producer,
        bool ValidPackage,
        IReadOnlyList<string> Covers);

    private sealed record CorpusFile(string Id, string Path, string Producer);
}
