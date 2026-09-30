using System.Xml.Linq;
using VibeTable.DocumentDiff.OpenXml;
using DocumentFormat.OpenXml;
using DocumentFormat.OpenXml.Packaging;
using DocumentFormat.OpenXml.Validation;
using VibeTable.DocumentDiff.Worker;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.Worker.Tests;

[TestClass]
public sealed class DocxNormalizerTests
{
    [TestMethod]
    [DataRow("<w:del w:id='1' w:author='VibeTable'><w:r><w:delText>DELETED</w:delText></w:r></w:del>")]
    [DataRow("<w:ins w:id='2' w:author='VibeTable'><w:r><w:t>FINAL</w:t></w:r></w:ins>")]
    [DataRow("<w:pPr><w:pPrChange w:id='3' w:author='VibeTable'><w:pPr/></w:pPrChange></w:pPr>")]
    [DataRow("<w:r><w:rPr><w:rPrChange w:id='4' w:author='VibeTable'><w:rPr/></w:rPrChange></w:rPr><w:t>TEXT</w:t></w:r>")]
    [DataRow("<w:moveFrom w:id='5' w:author='VibeTable'><w:r><w:t>MOVED</w:t></w:r></w:moveFrom>")]
    [DataRow("<w:moveTo w:id='6' w:author='VibeTable'><w:r><w:t>MOVED</w:t></w:r></w:moveTo>")]
    public async Task UnsupportedCommentRevisionsCannotBeReportedAsFinalContent(string revision)
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "content-before.docx");
        byte[] original = File.ReadAllBytes(path);
        DateTime modified = File.GetLastWriteTimeUtc(path);
        using var source = new MemoryStream();
        source.Write(original);
        source.Position = 0;
        using (var package = WordprocessingDocument.Open(source, true))
        {
            WordprocessingCommentsPart comments = package.MainDocumentPart!.AddNewPart<WordprocessingCommentsPart>();
            using var xml = new MemoryStream(System.Text.Encoding.UTF8.GetBytes($"""
                <w:comments xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
                  <w:comment w:id="0" w:author="VibeTable"><w:p>
                    {revision}
                  </w:p></w:comment>
                </w:comments>
                """));
            comments.FeedData(xml);
            Assert.AreEqual(0, new OpenXmlValidator(FileFormatVersions.Microsoft365).Validate(package).Count());
        }
        byte[] inputBytes = source.ToArray();
        var input = new DocumentContentSource("comments.docx", null, inputBytes.Length,
            _ => ValueTask.FromResult<Stream>(new MemoryStream(inputBytes, writable: false)));
        using var derived = new MemoryStream();

        await Assert.ThrowsExactlyAsync<NotSupportedException>(() => DocxNormalizer.NormalizeAsync(input, derived));

        CollectionAssert.AreEqual(original, File.ReadAllBytes(path));
        Assert.AreEqual(modified, File.GetLastWriteTimeUtc(path));
    }

    [TestMethod]
    [DataRow("existing-revisions.docx")]
    [DataRow("content-before.docx")]
    [DataRow("content-after.docx")]
    public async Task ExistingRevisionsAreAcceptedOnlyInTheDerivedCopy(string name)
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", name);
        byte[] original = File.ReadAllBytes(path);
        DateTime modified = File.GetLastWriteTimeUtc(path);
        var input = new DocumentContentSource("existing.docx", null, original.Length,
            _ => ValueTask.FromResult<Stream>(File.OpenRead(path)));
        using var derived = new MemoryStream();

        await DocxNormalizer.NormalizeAsync(input, derived);

        Assert.IsTrue(derived.CanRead);
        using var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource("final.docx", null,
            derived.Length, _ => ValueTask.FromResult<Stream>(new OpenXmlReadSafety.NonOwningStream(derived))));
        XNamespace w = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
        XDocument document = reader.ReadXmlPart("word/document.xml");
        Assert.IsFalse(document.Descendants(w + "ins").Any());
        Assert.IsFalse(document.Descendants(w + "del").Any());
        if (name == "existing-revisions.docx")
            Assert.AreEqual("合同金额为 120 万元。", string.Concat(document.Descendants(w + "t").Select(element => element.Value)));
        derived.Position = 0;
        using var normalized = WordprocessingDocument.Open(derived, false);
        Assert.AreEqual(0, new OpenXmlValidator(FileFormatVersions.Microsoft365).Validate(normalized).Count());
        CollectionAssert.AreEqual(original, File.ReadAllBytes(path));
        Assert.AreEqual(modified, File.GetLastWriteTimeUtc(path));
    }

    [TestMethod]
    public async Task InvalidOutputAndPrecancelledRequestsDoNotOpenTheInput()
    {
        var input = new DocumentContentSource("input.docx", null, null,
            _ => throw new AssertFailedException("Input must not open."));
        using var occupied = new MemoryStream([1]);
        await Assert.ThrowsExactlyAsync<ArgumentException>(() => DocxNormalizer.NormalizeAsync(input, occupied));
        CollectionAssert.AreEqual(new byte[] { 1 }, occupied.ToArray());
        using var empty = new MemoryStream();
        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() =>
            DocxNormalizer.NormalizeAsync(input, empty, new CancellationToken(true)));
        Assert.AreEqual(0L, empty.Length);
        var oversized = new DocumentContentSource("large.docx", null, 64L * 1024 * 1024 + 1,
            _ => throw new AssertFailedException("Oversized input must not open."));
        await Assert.ThrowsExactlyAsync<OpenXmlReadSafety.DiffBudgetExceededException>(() =>
            DocxNormalizer.NormalizeAsync(oversized, empty));
    }

    [TestMethod]
    public async Task CorruptInputClosesOnlyItsReadStreamAndDoesNotReportSuccess()
    {
        var source = new MemoryStream([1, 2, 3]);
        using var derived = new MemoryStream();
        var input = new DocumentContentSource("invalid.docx", null, 3,
            _ => ValueTask.FromResult<Stream>(source));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxNormalizer.NormalizeAsync(input, derived));
        Assert.IsFalse(source.CanRead);
        Assert.IsTrue(derived.CanWrite);
    }

    [TestMethod]
    public async Task CancellationDuringInputCopyClosesInputAndPreservesOutputOwnership()
    {
        using var cancellation = new CancellationTokenSource();
        var source = new CancellingInput(cancellation);
        using var derived = new MemoryStream();
        var input = new DocumentContentSource("cancel.docx", null, null,
            _ => ValueTask.FromResult<Stream>(source));
        await Assert.ThrowsAsync<OperationCanceledException>(() =>
            DocxNormalizer.NormalizeAsync(input, derived, cancellation.Token));
        Assert.IsFalse(source.CanRead);
        Assert.IsTrue(derived.CanWrite);
    }

    [TestMethod]
    public async Task AliasedStreamsAreRejectedWithoutDisposingTheOutput()
    {
        using var derived = new MemoryStream();
        var input = new DocumentContentSource("alias.docx", null, null,
            _ => ValueTask.FromResult<Stream>(derived));
        await Assert.ThrowsExactlyAsync<ArgumentException>(() => DocxNormalizer.NormalizeAsync(input, derived));
        Assert.IsTrue(derived.CanRead);
        Assert.IsTrue(derived.CanWrite);
    }

    [TestMethod]
    public async Task SeekableInputIsReadFromTheBeginning()
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "existing-revisions.docx");
        var source = new MemoryStream(File.ReadAllBytes(path));
        source.Position = source.Length;
        var input = new DocumentContentSource("positioned.docx", null, source.Length,
            _ => ValueTask.FromResult<Stream>(source));
        using var derived = new MemoryStream();
        await DocxNormalizer.NormalizeAsync(input, derived);
        Assert.IsFalse(source.CanRead);
        Assert.IsTrue(derived.Length > 0);
    }

    private sealed class CancellingInput(CancellationTokenSource cancellation) : MemoryStream(new byte[] { 1, 2, 3 })
    {
        public override bool CanSeek => false;
        public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
        {
            int read = Read(buffer.Span);
            cancellation.Cancel();
            return ValueTask.FromResult(read);
        }
    }
}
