using System.IO.Compression;
using System.Text;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxRelationshipsTests
{
    [TestMethod]
    public async Task RealDocxRootAndBodyRelationshipsReachHeadersAndFootnotes()
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "content-after.docx");
        using var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource("content.docx", null,
            new FileInfo(path).Length, _ => ValueTask.FromResult<Stream>(File.OpenRead(path))));
        DocxRelationship main = DocxRelationships.Read(reader, "/").Single(relationship =>
            relationship.Type == "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument");
        var children = DocxRelationships.Read(reader, main.PartName!);
        foreach (string kind in new[] { "header", "footer", "footnotes" })
        {
            DocxRelationship child = children.First(relationship => relationship.Type.EndsWith("/" + kind,
                StringComparison.Ordinal));
            Assert.IsNotNull(reader.ReadXmlPart(child.PartName!).Root);
        }
        Assert.ThrowsExactly<OperationCanceledException>(() =>
            DocxRelationships.Read(reader, "/", new CancellationToken(true)));
    }

    [TestMethod]
    [DataRow("word/a%20b.xml", "word/a b.xml")]
    [DataRow("word/%E4%B8%AD.xml", "word/中.xml")]
    [DataRow("word/a%2520b.xml", "word/a%2520b.xml")]
    [DataRow("word/document.xml", "word/document.x%6dl")]
    public async Task EscapesResolveOnceToTheOriginalZipEntry(string target, string entry)
    {
        using var reader = await Open($"<Relationship Id='a' Type='urn:body' Target='{target}'/>",
            (entry, "<document/>"));
        string resolved = DocxRelationships.Read(reader, "/").Single().PartName!;
        Assert.AreEqual(entry, resolved);
        Assert.AreEqual("document", reader.ReadXmlPart(resolved).Root!.Name.LocalName);
    }

    [TestMethod]
    public async Task ResolvesRootAndRelativePartsAndPreservesFragment()
    {
        using var reader = await Open("<Relationship Id='body' Type='urn:body' Target='custom/main.xml'/>",
            ("custom/main.xml", "<document/>"),
            ("custom/_rels/main.xml.rels", Relationships(
                "<Relationship Id='header' Type='urn:header' Target='../headers/%73ection.xml#mark'/>")),
            ("headers/section.xml", "<header/>"));
        var root = DocxRelationships.Read(reader, "/");
        Assert.AreEqual("custom/main.xml", root.Single().PartName);
        var header = DocxRelationships.Read(reader, root.Single().PartName!).Single();
        Assert.AreEqual("headers/section.xml", header.PartName);
        Assert.AreEqual("mark", header.Fragment);
        Assert.AreEqual("../headers/%73ection.xml#mark", header.RawTarget);
        Assert.AreEqual(0, DocxRelationships.Read(reader, header.PartName!).Count);
    }

    [TestMethod]
    public async Task ExternalTargetsNeverBecomeReadablePackageParts()
    {
        using var reader = await Open(
            "<Relationship Id='a' Type='urn:link' Target='https://example.invalid/file' TargetMode='External'/>" +
            "<Relationship Id='b' Type='urn:link' Target='../outside.docx' TargetMode='External'/>" +
            "<Relationship Id='c' Type='urn:link' Target='file:///never-read' TargetMode='External'/>");
        foreach (var relationship in DocxRelationships.Read(reader, "/"))
        {
            Assert.IsTrue(relationship.IsExternal);
            Assert.IsNull(relationship.PartName);
        }
    }

    [TestMethod]
    [DataRow("//server/document.xml")]
    [DataRow("file:///document.xml")]
    [DataRow("word\\document.xml")]
    [DataRow("word/document.xml?query")]
    [DataRow("word/%GG.xml")]
    [DataRow("word/missing.xml")]
    public async Task RejectsInvalidOrMissingInternalTargets(string target)
    {
        using var reader = await Open($"<Relationship Id='a' Type='urn:body' Target='{target}'/>",
            ("word/document.xml", "<document/>"));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxRelationships.Read(reader, "/"));
    }

    [TestMethod]
    [DataRow("<Relationship Id='a' Type='urn:x' Target='word/document.xml' TargetMode='Other'/>")]
    [DataRow("<Relationship Id='a' Type='urn:x' Target='word/document.xml'/><Relationship Id='a' Type='urn:x' Target='word/document.xml'/>")]
    [DataRow("<Relationship Type='urn:x' Target='word/document.xml'/>")]
    [DataRow("<Relationship Id='a' Target='word/document.xml'/>")]
    public async Task RejectsAmbiguousRelationshipMetadata(string children)
    {
        using var reader = await Open(children, ("word/document.xml", "<document/>"));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxRelationships.Read(reader, "/"));
    }

    [TestMethod]
    public async Task EncodedAliasDuplicatePartsAreRejected()
    {
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Open("",
            ("word/styles.xml", "<styles/>"), ("word/%73tyles.xml", "<styles/>")));
    }

    [TestMethod]
    [DataRow("word/a.xml?ignored")]
    [DataRow("word/a.xml#ignored")]
    [DataRow("word/%GG.xml")]
    public async Task ZipPartNamesCannotDiscardUriSuffixes(string entry)
    {
        await Assert.ThrowsExactlyAsync<InvalidDataException>(() => Open("", (entry, "<document/>")));
    }

    private static string Relationships(string children) =>
        "<Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'>" +
        children + "</Relationships>";

    private static Task<DocxPackageReader> Open(string rootRelationships,
        params (string Name, string Text)[] parts)
    {
        var bytes = new MemoryStream();
        using (var zip = new ZipArchive(bytes, ZipArchiveMode.Create, true))
        {
            foreach (var part in parts.Concat(new[]
                     { ("[Content_Types].xml", "<Types/>"), ("_rels/.rels", Relationships(rootRelationships)) }))
            {
                using var stream = zip.CreateEntry(part.Item1).Open();
                stream.Write(Encoding.UTF8.GetBytes(part.Item2));
            }
        }
        bytes.Position = 0;
        return DocxPackageReader.OpenAsync(new DocumentContentSource("test.docx", null, bytes.Length,
            _ => ValueTask.FromResult<Stream>(bytes)));
    }
}
