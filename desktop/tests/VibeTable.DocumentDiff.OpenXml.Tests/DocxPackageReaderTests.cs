using System.IO.Compression;
using System.Text;
using System.Xml;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxPackageReaderTests
{
    [TestMethod]
    [DataRow("cjk-before.docx")]
    [DataRow("cjk-after.docx")]
    [DataRow("content-before.docx")]
    [DataRow("content-after.docx")]
    [DataRow("format-before.docx")]
    [DataRow("format-after.docx")]
    [DataRow("existing-revisions.docx")]
    [DataRow("large-before.docx")]
    [DataRow("large-after.docx")]
    public async Task RealCorpusXmlPartsFitTheSharedReadBudget(string name)
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", name);
        using var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource(name, null,
            new FileInfo(path).Length, _ => ValueTask.FromResult<Stream>(File.OpenRead(path))));
        foreach (string part in reader.PartNames.Where(part =>
                     part.EndsWith(".xml", StringComparison.OrdinalIgnoreCase) ||
                     part.EndsWith(".rels", StringComparison.OrdinalIgnoreCase)))
            Assert.IsNotNull(reader.ReadXmlPart(part).Root, $"{name}: {part}");
    }

    [TestMethod]
    public async Task CorruptCorpusFailsAtThePackageBoundary()
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "corrupt.docx");
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxPackageReader.OpenAsync(
            new DocumentContentSource("corrupt.docx", null, new FileInfo(path).Length,
                _ => ValueTask.FromResult<Stream>(File.OpenRead(path)))));
    }

    [TestMethod]
    public async Task ReadsRealDocxWithoutChangingSourceAndOwnsOnlyOpenedStream()
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "cjk-before.docx");
        byte[] original = File.ReadAllBytes(path);
        DateTime writeTime = File.GetLastWriteTimeUtc(path);
        using var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource("sample.docx", null,
            original.Length, _ => ValueTask.FromResult<Stream>(File.OpenRead(path))));
        Assert.IsTrue(reader.PartNames.Contains("word/document.xml"));
        Assert.IsNotNull(reader.ReadXmlPart("word/document.xml").Root);
        CollectionAssert.AreEqual(original, File.ReadAllBytes(path));
        Assert.AreEqual(writeTime, File.GetLastWriteTimeUtc(path));
    }

    [TestMethod]
    [DataRow("../outside.xml")]
    [DataRow("word/../outside.xml")]
    [DataRow("word\\outside.xml")]
    [DataRow("word/%2e%2e/outside.xml")]
    [DataRow("/outside.xml")]
    public async Task RejectsNonCanonicalPartPathsAndClosesSource(string name)
    {
        var stream = new MemoryStream(Package((name, "<p/>")), writable: false);
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxPackageReader.OpenAsync(Source(stream)));
        Assert.IsFalse(stream.CanRead);
    }

    [TestMethod]
    public async Task RejectsCaseAmbiguousPartsAndSymlinks()
    {
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxPackageReader.OpenAsync(Source(
            new MemoryStream(Package(("word/document.xml", "<p/>"), ("word/DOCUMENT.xml", "<p/>"))))));
        using var bytes = new MemoryStream();
        bytes.Write(Package(("word/document.xml", "<p/>")));
        using (var zip = new ZipArchive(bytes, ZipArchiveMode.Update, true))
            zip.GetEntry("word/document.xml")!.ExternalAttributes = unchecked((int)0xA0000000);
        bytes.Position = 0;
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxPackageReader.OpenAsync(Source(bytes)));
    }

    [TestMethod]
    public async Task DtdDepthAndNodeBudgetFailBeforeReturningDocument()
    {
        using var dtd = await DocxPackageReader.OpenAsync(Source(new MemoryStream(Package(
            ("word/document.xml", "<!DOCTYPE p [<!ENTITY x SYSTEM 'file:///never-read'>]><p>&x;</p>")))));
        Assert.ThrowsExactly<XmlException>(() => dtd.ReadXmlPart("word/document.xml"));
        string deep = string.Concat(Enumerable.Repeat("<p>", 67)) + string.Concat(Enumerable.Repeat("</p>", 67));
        using var depth = await DocxPackageReader.OpenAsync(Source(new MemoryStream(Package(("word/document.xml", deep)))));
        Assert.ThrowsExactly<DiffBudgetExceededException>(() => depth.ReadXmlPart("word/document.xml"));
        string many = "<p>" + string.Concat(Enumerable.Repeat("<r/>", 250_001)) + "</p>";
        using var nodes = await DocxPackageReader.OpenAsync(Source(new MemoryStream(Package(("word/document.xml", many)))));
        Assert.ThrowsExactly<DiffBudgetExceededException>(() => nodes.ReadXmlPart("word/document.xml"));
    }

    [TestMethod]
    public async Task MetadataMissingPartsCancellationAndDisposalAreExplicit()
    {
        var source = new MemoryStream(Package(("word/document.xml", "<p/>")));
        var reader = await DocxPackageReader.OpenAsync(Source(source));
        Assert.ThrowsExactly<InvalidDataException>(() => reader.ReadXmlPart("word/missing.xml"));
        Assert.ThrowsExactly<OperationCanceledException>(() => reader.ReadXmlPart("word/document.xml", new CancellationToken(true)));
        reader.Dispose();
        reader.Dispose();
        Assert.IsFalse(source.CanRead);
        Assert.ThrowsExactly<ObjectDisposedException>(() => reader.ReadXmlPart("word/document.xml"));
        using var empty = new MemoryStream();
        using (var zip = new ZipArchive(empty, ZipArchiveMode.Create, true)) { }
        empty.Position = 0;
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxPackageReader.OpenAsync(Source(empty)));
    }

    [TestMethod]
    public async Task XmlAttributesAndMultiplePartsShareTheDomBudget()
    {
        string xml = "<p>" + string.Concat(Enumerable.Repeat("<r a='1' b='2'/>", 50_000)) + "</p>";
        using var reader = await DocxPackageReader.OpenAsync(Source(new MemoryStream(Package(
            ("word/document.xml", xml), ("word/header1.xml", xml)))));
        Assert.IsNotNull(reader.ReadXmlPart("word/document.xml").Root);
        Assert.ThrowsExactly<DiffBudgetExceededException>(() => reader.ReadXmlPart("word/header1.xml"));
    }

    private static DocumentContentSource Source(Stream stream) => new("sample.docx", null, stream.Length,
        _ => ValueTask.FromResult(stream));

    [TestMethod]
    public async Task NonSeekableInputIsOwnedAndCopyFailuresPropagate()
    {
        var stream = new NonSeekableInput(Package(("word/document.xml", "<p>文字</p>")));
        using (var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource("sample.docx", null,
            null, _ => ValueTask.FromResult<Stream>(stream))))
            Assert.AreEqual("文字", reader.ReadXmlPart("word/document.xml").Root!.Value);
        Assert.IsFalse(stream.CanRead);
        var failing = new NonSeekableInput([], fail: true);
        await Assert.ThrowsExactlyAsync<IOException>(() => DocxPackageReader.OpenAsync(
            new DocumentContentSource("sample.docx", null, null, _ => ValueTask.FromResult<Stream>(failing))));
        Assert.IsFalse(failing.CanRead);
    }

    private sealed class NonSeekableInput(byte[] bytes, bool fail = false) : MemoryStream(bytes)
    {
        public override bool CanSeek => false;
        public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default) =>
            fail ? ValueTask.FromException<int>(new IOException("simulated read failure"))
                : base.ReadAsync(buffer, cancellationToken);
    }

    private static byte[] Package(params (string Name, string Text)[] entries)
    {
        using var bytes = new MemoryStream();
        using (var archive = new ZipArchive(bytes, ZipArchiveMode.Create, true))
        {
            foreach (var entry in entries.Concat(new[] { ("[Content_Types].xml", "<Types/>"), ("_rels/.rels", "<Relationships/>") }))
            {
                using Stream part = archive.CreateEntry(entry.Item1).Open();
                part.Write(Encoding.UTF8.GetBytes(entry.Item2));
            }
        }
        return bytes.ToArray();
    }
}
