using System.IO.Compression;
using System.Text;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class OpenXmlDocumentDiffEngineTests
{
    private const string DocxMime =
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
    private const string XlsxMime =
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
    private const string PptxMime =
        "application/vnd.openxmlformats-officedocument.presentationml.presentation";

    [TestMethod]
    public async Task CompareAsync_DocxVisibleTextChanged_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.docx", DocxMime, Docx("before")),
            Content("after.docx", DocxMime, Docx("after")));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(1, outcome.AddedLines);
        Assert.AreEqual(1, outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_XlsxRoutesToDeepProviderWithPreciseOracle()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        string before = "<c r=\"A1\"><f>1+1</f></c><c r=\"B1\" s=\"1\"><v>1</v></c>";
        string after = "<c r=\"A1\"><f>1+1</f><v>2</v></c><c r=\"B1\" s=\"1\"><v>1</v></c>";
        var request = new DocumentDiffRequest(
            Content("before.xlsx", XlsxMime, XlsxSemanticDiffTests.Package(
                [new XlsxSemanticDiffTests.Sheet("数据", before)],
                XlsxSemanticDiffTests.StyleSheet("<font><name val=\"X\"/></font><font><name val=\"A\"/></font>", "<xf/><xf fontId=\"1\"/>"))),
            Content("after.xlsx", XlsxMime, XlsxSemanticDiffTests.Package(
                [new XlsxSemanticDiffTests.Sheet("数据", after)],
                XlsxSemanticDiffTests.StyleSheet("<font><name val=\"X\"/></font><font><name val=\"B\"/></font>", "<xf/><xf fontId=\"1\"/>"))));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(2, outcome.AddedLines);
        Assert.AreEqual(2, outcome.RemovedLines);
        Assert.IsNotNull(outcome.Details);
        Assert.AreEqual(DocumentDiffFormat.Xlsx, outcome.Details.Format);
        XlsxSemanticDiffTests.Oracle(outcome.Details,
            "Replace|数据|A1|cache: missing|cache: number: 2",
            "Format|数据|B1|style: font=font()[name(val=1:A)[]]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=|style: font=font()[name(val=1:B)[]]; fill=fill()[]; border=border()[]; numFmt=builtin:0; alignment=");
    }

    [TestMethod]
    public async Task CompareAsync_ExternalXlsxRelationshipIsUnsupportedWithoutFollowingIt()
    {
        byte[] bytes = XlsxSemanticDiffTests.Package([new XlsxSemanticDiffTests.Sheet("数据")],
            extra: [("xl/_rels/extra.xml.rels", "<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\"><Relationship Id=\"external\" Type=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink\" Target=\"https://invalid.example\" TargetMode=\"External\"/></Relationships>")]);
        var outcome = await new OpenXmlDocumentDiffEngine().CompareAsync(new(
            Content("before.xlsx", XlsxMime, bytes), Content("after.xlsx", XlsxMime, bytes)), CancellationToken.None);
        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Unsupported, outcome.Failure);
        Assert.IsNull(outcome.Details);
    }

    [TestMethod]
    public async Task CompareAsync_PptxVisibleTextChanged_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.pptx", PptxMime, Pptx("before")),
            Content("after.pptx", PptxMime, Pptx("after")));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(1, outcome.AddedLines);
        Assert.AreEqual(1, outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_IdenticalPackageBytes_ReturnsIdentical()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var package = Docx("same");
        var request = new DocumentDiffRequest(
            Content("before.docx", DocxMime, package),
            Content("after.docx", DocxMime, package));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, outcome.Kind);
    }

    [TestMethod]
    public async Task CompareAsync_PackageBytesChangedButVisibleTextSame_ReturnsChanged()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.docx", DocxMime, Docx("same", "before")),
            Content("after.docx", DocxMime, Docx("same", "after")));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Changed, outcome.Kind);
        Assert.IsNull(outcome.AddedLines);
        Assert.IsNull(outcome.RemovedLines);
    }

    [TestMethod]
    public async Task CompareAsync_CorruptOpenXmlPackage_ReturnsInvalidContentFailure()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.docx", DocxMime, [0x01, 0x02]),
            Content("after.docx", DocxMime, [0x03, 0x04]));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.InvalidContent, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_DifferentOpenXmlFormats_ReturnsUnsupportedFailure()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.docx", DocxMime, Docx("before")),
            Content("after.xlsx", XlsxMime, Xlsx("after")));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Unsupported, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_NonSeekablePackageExceedsNamedBudget_DegradesToChanged()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        byte[] before = Docx("same", "before");
        byte[] after = Docx("same", "after");
        var request = new DocumentDiffRequest(
            NonSeekableContent(
                "before.docx",
                before,
                OpenXmlExtractionLimits.MaxNonSeekablePackageBytes + 1),
            NonSeekableContent(
                "after.docx",
                after,
                OpenXmlExtractionLimits.MaxNonSeekablePackageBytes + 1));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Changed, outcome.Kind);
    }

    [TestMethod]
    public async Task CompareAsync_VisibleTextExceedsNamedBudget_DegradesToChanged()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        string oversized = new('x', OpenXmlExtractionLimits.MaxVisibleTextCharacters + 1);
        var request = new DocumentDiffRequest(
            Content("before.docx", DocxMime, Docx(oversized, "before")),
            Content("after.docx", DocxMime, Docx(oversized, "after")));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Changed, outcome.Kind);
    }

    [TestMethod]
    public async Task CompareAsync_IdenticalMalformedXlsxPackage_FailsClosedInsteadOfBinaryIdentical()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        // ZIP with sheet parts but no OPC skeleton: byte-identical, yet not a valid workbook.
        byte[] malformed = Xlsx("shallow");
        var request = new DocumentDiffRequest(
            Content("before.xlsx", XlsxMime, malformed),
            Content("after.xlsx", XlsxMime, malformed));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.InvalidContent, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_IdenticalValidXlsxPackages_ReturnIdenticalWithDeepCoverage()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        byte[] workbook = XlsxSemanticDiffTests.Package(
            [new XlsxSemanticDiffTests.Sheet("数据", "<c r=\"A1\"><v>1</v></c>")]);
        var request = new DocumentDiffRequest(
            Content("before.xlsx", XlsxMime, workbook),
            Content("after.xlsx", XlsxMime, workbook));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Identical, outcome.Kind);
        Assert.IsNotNull(outcome.Details);
        Assert.AreEqual(DocumentDiffFormat.Xlsx, outcome.Details.Format);
    }

    [TestMethod]
    public async Task CompareAsync_XlsxBudgetExceeded_FailsUnsupportedInsteadOfShallowChanged()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        byte[] workbook = XlsxSemanticDiffTests.Package(
            [new XlsxSemanticDiffTests.Sheet("预算", "<c r=\"A1\"><v>1</v></c>")]);
        var request = new DocumentDiffRequest(
            NonSeekableContent(
                "before.xlsx",
                workbook,
                OpenXmlExtractionLimits.MaxNonSeekablePackageBytes + 1,
                XlsxMime),
            NonSeekableContent(
                "after.xlsx",
                workbook,
                OpenXmlExtractionLimits.MaxNonSeekablePackageBytes + 1,
                XlsxMime));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Unsupported, outcome.Failure);
    }

    [TestMethod]
    public async Task CompareAsync_XlsxCancelledToken_ReturnsCancelledFailure()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        byte[] workbook = XlsxSemanticDiffTests.Package(
            [new XlsxSemanticDiffTests.Sheet("数据", "<c r=\"A1\"><v>1</v></c>")]);
        using var cancelled = new CancellationTokenSource();
        cancelled.Cancel();

        var outcome = await engine.CompareAsync(
            new DocumentDiffRequest(
                Content("before.xlsx", XlsxMime, workbook),
                Content("after.xlsx", XlsxMime, workbook)),
            cancelled.Token);

        Assert.AreEqual(DocumentDiffOutcomeKind.Failure, outcome.Kind);
        Assert.AreEqual(DocumentDiffFailureKind.Cancelled, outcome.Failure);
    }

    private static DocumentContentSource Content(string name, string mimeType, byte[] bytes)
    {
        return new DocumentContentSource(
            name,
            mimeType,
            bytes.Length,
            _ => ValueTask.FromResult<Stream>(new MemoryStream(bytes, writable: false)));
    }

    private static DocumentContentSource NonSeekableContent(
        string name,
        byte[] bytes,
        long declaredLength,
        string? mimeType = null)
    {
        return new DocumentContentSource(
            name,
            mimeType ?? DocxMime,
            declaredLength,
            _ => ValueTask.FromResult<Stream>(new NonSeekableStream(bytes)));
    }

    private static byte[] Docx(string text, string? packageMarker = null)
    {
        using var package = new MemoryStream();
        using (var archive = new ZipArchive(package, ZipArchiveMode.Create, leaveOpen: true))
        {
            WriteEntry(
                archive,
                "word/document.xml",
                $"<w:document xmlns:w=\"http://schemas.openxmlformats.org/wordprocessingml/2006/main\">"
                + $"<w:body><w:p><w:r><w:t>{text}</w:t></w:r></w:p></w:body></w:document>");
            if (packageMarker is not null)
            {
                WriteEntry(archive, "docProps/custom.xml", $"<marker>{packageMarker}</marker>");
            }
        }

        return package.ToArray();
    }

    private static byte[] Xlsx(string text)
    {
        const string spreadsheetNamespace =
            "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
        using var package = new MemoryStream();
        using (var archive = new ZipArchive(package, ZipArchiveMode.Create, leaveOpen: true))
        {
            WriteEntry(
                archive,
                "xl/sharedStrings.xml",
                $"<sst xmlns=\"{spreadsheetNamespace}\"><si><t>{text}</t></si></sst>");
            WriteEntry(
                archive,
                "xl/worksheets/sheet1.xml",
                $"<worksheet xmlns=\"{spreadsheetNamespace}\"><sheetData><row>"
                + "<c r=\"A1\" t=\"s\"><v>0</v></c></row></sheetData></worksheet>");
        }

        return package.ToArray();
    }

    private static void WriteEntry(ZipArchive archive, string name, string xml)
    {
        var entry = archive.CreateEntry(name);
        using var writer = new StreamWriter(entry.Open(), new UTF8Encoding(false));
        writer.Write(xml);
    }

    private static byte[] Pptx(string text)
    {
        using var package = new MemoryStream();
        using (var archive = new ZipArchive(package, ZipArchiveMode.Create, leaveOpen: true))
        {
            WriteEntry(
                archive,
                "ppt/slides/slide1.xml",
                "<p:sld xmlns:p=\"http://schemas.openxmlformats.org/presentationml/2006/main\" "
                + "xmlns:a=\"http://schemas.openxmlformats.org/drawingml/2006/main\">"
                + $"<p:cSld><a:p><a:r><a:t>{text}</a:t></a:r></a:p></p:cSld></p:sld>");
        }

        return package.ToArray();
    }

    private sealed class NonSeekableStream(byte[] bytes)
        : MemoryStream(bytes, writable: false)
    {
        public override bool CanSeek => false;

        public override long Seek(long offset, SeekOrigin loc)
            => throw new NotSupportedException();

        public override long Position
        {
            get => throw new NotSupportedException();
            set => throw new NotSupportedException();
        }
    }
}
