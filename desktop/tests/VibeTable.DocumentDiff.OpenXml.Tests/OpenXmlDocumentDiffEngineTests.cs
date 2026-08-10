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
    public async Task CompareAsync_XlsxVisibleCellChanged_ReturnsLineDetails()
    {
        IDocumentDiffEngine engine = new OpenXmlDocumentDiffEngine();
        var request = new DocumentDiffRequest(
            Content("before.xlsx", XlsxMime, Xlsx("before")),
            Content("after.xlsx", XlsxMime, Xlsx("after")));

        var outcome = await engine.CompareAsync(request, CancellationToken.None);

        Assert.AreEqual(DocumentDiffOutcomeKind.ChangedWithDetails, outcome.Kind);
        Assert.AreEqual(1, outcome.AddedLines);
        Assert.AreEqual(1, outcome.RemovedLines);
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
        long declaredLength)
    {
        return new DocumentContentSource(
            name,
            DocxMime,
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
