using System.IO.Compression;
using System.Text;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxContentTypesTests
{
    [TestMethod]
    public async Task OverrideWinsAndDefaultsMatchExtensionsIgnoringCase()
    {
        using var reader = await Open("""
            <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
              <Default Extension="xml" ContentType="application/xml" />
              <Default Extension="PNG" ContentType="image/png" />
              <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml" />
            </Types>
            """,
            ("word/document.xml", "<document/>"), ("word/IMAGE.PNG", "image"));

        DocxContentTypes types = DocxContentTypes.Read(reader);

        Assert.AreEqual("application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml",
            types.Get("word/document.xml"));
        Assert.AreEqual("image/png", types.Get("word/IMAGE.PNG"));
    }

    [TestMethod]
    public async Task OverrideAliasesAreNormalizedBeforeLookup()
    {
        using var reader = await Open("""
            <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
              <Override PartName="/word/a%20b.xml" ContentType="application/test+xml" />
            </Types>
            """, ("word/a b.xml", "<document/>"));

        Assert.AreEqual("application/test+xml", DocxContentTypes.Read(reader).Get("word/a b.xml"));
    }

    [TestMethod]
    public async Task RedundantChildNamespaceDeclarationAndMimeParametersAreValid()
    {
        using var reader = await Open("""
            <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
              <Default xmlns="http://schemas.openxmlformats.org/package/2006/content-types" Extension="xml" ContentType="application/xml; charset=utf-8" />
            </Types>
            """, ("word/document.xml", "<document/>"));

        Assert.AreEqual("application/xml; charset=utf-8",
            DocxContentTypes.Read(reader).Get("word/document.xml"));
    }

    [TestMethod]
    public async Task MissingMappingAndUnsafeXmlAreRejectedAndCancellationPropagates()
    {
        using (DocxPackageReader reader = await Open("""
            <!DOCTYPE Types [<!ENTITY value "application/xml">]>
            <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
              <Default Extension="xml" ContentType="&value;" />
            </Types>
            """, ("word/document.xml", "<document/>")))
        {
            Assert.ThrowsExactly<System.Xml.XmlException>(() => DocxContentTypes.Read(reader));
        }

        using (DocxPackageReader reader = await Open("""
            <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
              <Default Extension="xml" ContentType="application/xml" />
            </Types>
            """, ("word/document.xml", "<document/>")))
        {
            DocxContentTypes types = DocxContentTypes.Read(reader);
            Assert.ThrowsExactly<InvalidDataException>(() => types.Get("word/missing.bin"));
            Assert.ThrowsExactly<OperationCanceledException>(() => DocxContentTypes.Read(reader,
                new CancellationToken(true)));
        }
    }

    [TestMethod]
    [DataRow("<Default Extension='xml' ContentType='application/xml'/><Default Extension='XML' ContentType='text/xml'/>")]
    [DataRow("<Override PartName='/word/a%20b.xml' ContentType='application/xml'/><Override PartName='/word/a b.xml' ContentType='text/xml'/>")]
    [DataRow("<Default Extension='xml' ContentType='not a content type'/>")]
    [DataRow("<Default Extension='xml' ContentType='application/xml' Unexpected='value'/>")]
    [DataRow("<Default Extension='xml' ContentType=' application/xml'/>")]
    [DataRow("<Override PartName='/word/../document.xml' ContentType='application/xml'/>")]
    [DataRow("<Override PartName='/word/%2e%2e/document.xml' ContentType='application/xml'/>")]
    public async Task AmbiguousOrInvalidMetadataFailsClosed(string entries)
    {
        using var reader = await Open($"""
            <Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">{entries}</Types>
            """, ("word/a b.xml", "<document/>"));

        Assert.ThrowsExactly<InvalidDataException>(() => DocxContentTypes.Read(reader));
    }

    [TestMethod]
    public async Task RealDocxBodyHasTheOfficeDocumentContentType()
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "content-after.docx");
        using var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource("content.docx", null,
            new FileInfo(path).Length, _ => ValueTask.FromResult<Stream>(File.OpenRead(path))));

        Assert.AreEqual("application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml",
            DocxContentTypes.Read(reader).Get("word/document.xml"));
    }

    private static Task<DocxPackageReader> Open(string contentTypes,
        params (string Name, string Text)[] parts)
    {
        var bytes = new MemoryStream();
        using (var zip = new ZipArchive(bytes, ZipArchiveMode.Create, leaveOpen: true))
        {
            foreach (var part in parts.Concat(new[]
                     {
                         ("[Content_Types].xml", contentTypes),
                         ("_rels/.rels", "<Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'/>")
                     }))
            {
                using Stream stream = zip.CreateEntry(part.Item1).Open();
                stream.Write(Encoding.UTF8.GetBytes(part.Item2));
            }
        }
        bytes.Position = 0;
        return DocxPackageReader.OpenAsync(new DocumentContentSource("test.docx", null, bytes.Length,
            _ => ValueTask.FromResult<Stream>(bytes)));
    }
}
