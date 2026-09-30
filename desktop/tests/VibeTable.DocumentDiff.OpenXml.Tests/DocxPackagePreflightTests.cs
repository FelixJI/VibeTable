using System.IO.Compression;
using System.Text;
using System.Xml;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxPackagePreflightTests
{
    [TestMethod]
    [DataRow("content-after.docx")]
    [DataRow("existing-revisions.docx")]
    public async Task RealPackagesAreFullyReadableWithoutChangingTheirSources(string name)
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", name);
        byte[] original = File.ReadAllBytes(path);
        DateTime writeTime = File.GetLastWriteTimeUtc(path);
        var content = new DocumentContentSource(name, null, original.Length,
            _ => ValueTask.FromResult<Stream>(File.OpenRead(path)));

        await DocxPackagePreflight.ValidateAsync(content);

        CollectionAssert.AreEqual(original, File.ReadAllBytes(path));
        Assert.AreEqual(writeTime, File.GetLastWriteTimeUtc(path));
    }

    [TestMethod]
    public async Task ContentTypeIdentifiesExtensionlessXmlAndRejectsDtd()
    {
        using MemoryStream package = Package(archive => WriteText(archive, "word/opaque",
            "<!DOCTYPE p [<!ENTITY x SYSTEM 'file:///never-read'>]><p>&x;</p>"),
            contentTypes: ContentTypes("<Override PartName='/word/opaque' ContentType='application/xml' />"));

        await Assert.ThrowsExactlyAsync<XmlException>(() => DocxPackagePreflight.ValidateAsync(Source(package)));
    }

    [TestMethod]
    [DataRow("bin")]
    [DataRow("xml")]
    public async Task BinaryPartLargerThanXmlLimitIsAllowed(string extension)
    {
        string name = "word/media/payload." + extension;
        using MemoryStream package = Package(archive => WriteBytes(archive, name,
            17L * 1024 * 1024), contentTypes: ContentTypes(
                $"<Override PartName='/{name}' ContentType='application/octet-stream' />"));

        await DocxPackagePreflight.ValidateAsync(Source(package));
    }

    [TestMethod]
    public async Task OversizedBinaryPartAndExpandedPackageAreRejected()
    {
        using (MemoryStream oversizedPart = Package(archive => WriteBytes(archive, "word/media/payload.bin",
                   64L * 1024 * 1024 + 1)))
        {
            await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() =>
                DocxPackagePreflight.ValidateAsync(Source(oversizedPart)));
        }

        using MemoryStream oversizedPackage = Package(archive =>
        {
            for (int index = 0; index < 5; index++)
            {
                WriteBytes(archive, $"word/media/payload-{index}.bin", 52L * 1024 * 1024);
            }
        });
        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() =>
            DocxPackagePreflight.ValidateAsync(Source(oversizedPackage)));
    }

    [TestMethod]
    [DataRow(1, 17)]
    [DataRow(5, 14)]
    public async Task XmlMimeRetainsPartAndAggregateLimitsWithoutXmlSuffix(int count, int mebibytes)
    {
        string entries = string.Concat(Enumerable.Range(0, count).Select(index =>
            $"<Override PartName='/word/payload-{index}' ContentType='application/xml' />"));
        using MemoryStream package = Package(archive =>
        {
            for (int index = 0; index < count; index++)
            {
                using Stream entry = archive.CreateEntry($"word/payload-{index}", CompressionLevel.Fastest).Open();
                entry.Write("<p>"u8);
                byte[] spaces = new byte[1024 * 1024];
                Array.Fill(spaces, (byte)' ');
                for (int block = 0; block < mebibytes; block++)
                    entry.Write(spaces);
                entry.Write("</p>"u8);
            }
        }, contentTypes: ContentTypes(entries));

        await Assert.ThrowsExactlyAsync<DiffBudgetExceededException>(() =>
            DocxPackagePreflight.ValidateAsync(Source(package)));
    }

    [TestMethod]
    public async Task InvalidMetadataAndMissingInternalRelationshipTargetsAreRejected()
    {
        using (MemoryStream missingContentType = Package(archive =>
            WriteBytes(archive, "word/media/payload.dat", 1), contentTypes: ContentTypes()))
        {
            await Assert.ThrowsExactlyAsync<InvalidDataException>(() =>
                DocxPackagePreflight.ValidateAsync(Source(missingContentType)));
        }

        using MemoryStream missingTarget = Package(_ => { }, rootRelationships: Relationships(
            "<Relationship Id='document' Type='urn:document' Target='word/missing.xml' />"));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() =>
            DocxPackagePreflight.ValidateAsync(Source(missingTarget)));
    }

    [TestMethod]
    public async Task CancellationPropagatesBeforeOpeningThePackage()
    {
        using MemoryStream package = Package(_ => { });

        await Assert.ThrowsExactlyAsync<OperationCanceledException>(() =>
            DocxPackagePreflight.ValidateAsync(Source(package), new CancellationToken(canceled: true)));
    }

    [TestMethod]
    public async Task PartLengthChecksDistinguishEmptyReadsFromEof()
    {
        using var shortPart = new BudgetedEntryStream(new MemoryStream([1, 2]),
            new ExpandedByteBudget(10), partLimit: 10, expectedLength: 3);
        Assert.AreEqual(0, shortPart.Read(Span<byte>.Empty));
        byte[] buffer = new byte[8];
        Assert.AreEqual(2, shortPart.Read(buffer));
        Assert.AreEqual(0, await shortPart.ReadAsync(Memory<byte>.Empty));
        Assert.ThrowsExactly<InvalidDataException>(() => shortPart.Read(buffer));
        using var longPart = new BudgetedEntryStream(new MemoryStream([1, 2, 3]),
            new ExpandedByteBudget(10), partLimit: 10, expectedLength: 2);
        await Assert.ThrowsExactlyAsync<InvalidDataException>(async () =>
            Assert.AreEqual(3, await longPart.ReadAsync(buffer.AsMemory())));
    }

    [TestMethod]
    public void IndependentPartAndPackageLimitsAreChargedOnActualReadBytes()
    {
        var budget = new ExpandedByteBudget(4);
        byte[] buffer = new byte[4];
        using var first = new BudgetedEntryStream(new MemoryStream([1, 2, 3]), budget, 3, 3);
        Assert.AreEqual(3, first.Read(buffer, 0, buffer.Length));
        Assert.AreEqual(0, first.Read(buffer, 0, 0));
        Assert.AreEqual(0, first.Read(buffer, 0, buffer.Length));
        using var second = new BudgetedEntryStream(new MemoryStream([1, 2]), budget, 3, 2);
        Assert.ThrowsExactly<DiffBudgetExceededException>(() => second.Read(buffer));
        using var oversized = new BudgetedEntryStream(new MemoryStream([1, 2, 3]),
            new ExpandedByteBudget(10), partLimit: 2, expectedLength: 3);
        Assert.ThrowsExactly<DiffBudgetExceededException>(() => oversized.Read(buffer));
    }

    [TestMethod]
    public async Task WordXmlRelationshipCannotDisguiseItsTargetAsBinary()
    {
        using var package = Package(archive => WriteText(archive, "word/payload.bin", "<p/>"),
            rootRelationships: Relationships("<Relationship Id='main' " +
                "Type='http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument' " +
                "Target='word/payload.bin'/>"));
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => DocxPackagePreflight.ValidateAsync(Source(package)));
    }

    private static DocumentContentSource Source(MemoryStream package)
    {
        package.Position = 0;
        return new DocumentContentSource("test.docx", null, package.Length,
            _ => ValueTask.FromResult<Stream>(package));
    }

    private static MemoryStream Package(Action<ZipArchive> writeParts, string? contentTypes = null,
        string? rootRelationships = null)
    {
        var bytes = new MemoryStream();
        using (var archive = new ZipArchive(bytes, ZipArchiveMode.Create, leaveOpen: true))
        {
            WriteText(archive, "[Content_Types].xml", contentTypes ?? ContentTypes());
            WriteText(archive, "_rels/.rels", rootRelationships ?? Relationships());
            writeParts(archive);
        }
        bytes.Position = 0;
        return bytes;
    }

    private static string ContentTypes(string entries = "") => """
        <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
          <Default Extension="xml" ContentType="application/xml" />
          <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml" />
          <Default Extension="bin" ContentType="application/octet-stream" />
        """ + entries + "</Types>";

    private static string Relationships(string entries = "") =>
        "<Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'>" + entries +
        "</Relationships>";

    private static void WriteText(ZipArchive archive, string name, string text)
    {
        using Stream entry = archive.CreateEntry(name).Open();
        entry.Write(Encoding.UTF8.GetBytes(text));
    }

    private static void WriteBytes(ZipArchive archive, string name, long length)
    {
        using Stream entry = archive.CreateEntry(name, CompressionLevel.Fastest).Open();
        byte[] buffer = new byte[64 * 1024];
        while (length > 0)
        {
            int count = (int)Math.Min(buffer.Length, length);
            entry.Write(buffer, 0, count);
            length -= count;
        }
    }
}
