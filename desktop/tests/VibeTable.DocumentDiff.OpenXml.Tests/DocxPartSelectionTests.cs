using System.IO.Compression;
using System.Text;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml.Tests;

[TestClass]
public sealed class DocxPartSelectionTests
{
    private const string W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
    private const string R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
    private const string Mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.";

    [TestMethod]
    public async Task ContentTypesNotFileSuffixDecideWhetherMainPartIsXml()
    {
        using var reader = await PackageAt("custom/main", "<w:p/>", "");
        Assert.AreEqual("custom/main", DocxPartSelection.Read(reader).MainPartName);
    }

    [TestMethod]
    public async Task SelectsNonstandardMainPathAndInheritedHeadersWithoutDuplicateReads()
    {
        string body = "<w:p><w:pPr><w:sectPr><w:headerReference w:type='default' r:id='h'/></w:sectPr></w:pPr></w:p>" +
            "<w:p><w:r><w:t>第二节</w:t></w:r></w:p><w:sectPr/>";
        using var reader = await Package(body, Relation("h", "header", "headers/one.xml"),
            ("custom/headers/one.xml", "header", "<w:hdr xmlns:w='" + W + "'><w:p/></w:hdr>"));
        DocxPartSelection selection = DocxPartSelection.Read(reader);
        Assert.AreEqual("custom/main.xml", selection.MainPartName);
        Assert.AreEqual(2, selection.SectionCount);
        Assert.AreEqual(2, selection.HeadersFooters.Count);
        CollectionAssert.AreEqual(new[] { 0, 1 }, selection.HeadersFooters.Select(item => item.SectionIndex).ToArray());
        Assert.IsTrue(selection.HeadersFooters.All(item => item.Kind == DocumentDiffPart.Header &&
            item.Variant == "default" && item.IsDisplayed));
        Assert.AreSame(selection.HeadersFooters[0].Document, selection.HeadersFooters[1].Document);
    }

    [TestMethod]
    public async Task FirstAndEvenVariantsRetainBindingsButTrackDisplaySettings()
    {
        string refs = "<w:headerReference w:type='first' r:id='h'/><w:footerReference w:type='even' r:id='f'/>";
        using var reader = await Package("<w:p><w:pPr><w:sectPr>" + refs + "</w:sectPr></w:pPr></w:p>" +
            "<w:sectPr><w:titlePg/></w:sectPr>",
            Relation("h", "header", "h.xml") + Relation("f", "footer", "f.xml") + Relation("s", "settings", "settings.xml"),
            ("custom/h.xml", "header", "<w:hdr xmlns:w='" + W + "'/>"),
            ("custom/f.xml", "footer", "<w:ftr xmlns:w='" + W + "'/>"),
            ("custom/settings.xml", "settings", "<w:settings xmlns:w='" + W + "'><w:evenAndOddHeaders/></w:settings>"));
        var bindings = DocxPartSelection.Read(reader).HeadersFooters;
        Assert.IsFalse(bindings.Single(item => item.SectionIndex == 0 && item.Variant == "first").IsDisplayed);
        Assert.IsTrue(bindings.Single(item => item.SectionIndex == 1 && item.Variant == "first").IsDisplayed);
        Assert.IsTrue(bindings.Where(item => item.Variant == "even").All(item => item.IsDisplayed));
    }

    [TestMethod]
    public async Task MissingEvenBindingStillRetainsTheBlankEvenPageSetting()
    {
        const string body = "<w:sectPr><w:headerReference w:type='default' r:id='h'/></w:sectPr>";
        var header = ("custom/h.xml", "header", $"<w:hdr xmlns:w='{W}'/>");
        using var normalReader = await Package(body, Relation("h", "header", "h.xml"), header);
        using var distinctReader = await Package(body,
            Relation("h", "header", "h.xml") + Relation("s", "settings", "settings.xml"), header,
            ("custom/settings.xml", "settings", $"<w:settings xmlns:w='{W}'><w:evenAndOddHeaders/></w:settings>"));
        var normal = DocxPartSelection.Read(normalReader);
        var distinct = DocxPartSelection.Read(distinctReader);
        Assert.IsFalse(normal.EvenAndOddHeaders);
        Assert.IsTrue(distinct.EvenAndOddHeaders);
        Assert.AreEqual(1, distinct.HeadersFooters.Count);
        Assert.AreEqual("default", distinct.HeadersFooters[0].Variant);
    }

    [TestMethod]
    [DataRow("header", "footer")]
    [DataRow("footer", "header")]
    public async Task RejectsReferenceWithWrongRelationshipKind(string referenceKind, string relationshipKind)
    {
        using var reader = await Package($"<w:sectPr><w:{referenceKind}Reference w:type='default' r:id='a'/></w:sectPr>",
            Relation("a", relationshipKind, "a.xml"),
            ("custom/a.xml", relationshipKind, $"<w:{(relationshipKind == "header" ? "hdr" : "ftr")} xmlns:w='{W}'/>"));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxPartSelection.Read(reader));
    }

    [TestMethod]
    [DataRow("<w:headerReference w:type='default'/>")]
    [DataRow("<w:headerReference w:type='other' r:id='h'/>")]
    [DataRow("<w:headerReference w:type='default' r:id='missing'/>")]
    [DataRow("<w:headerReference w:type='default' r:id='h'/><w:headerReference w:type='default' r:id='h'/>")]
    public async Task RejectsMissingOrAmbiguousSectionReferences(string references)
    {
        using var reader = await Package("<w:sectPr>" + references + "</w:sectPr>", Relation("h", "header", "h.xml"),
            ("custom/h.xml", "header", $"<w:hdr xmlns:w='{W}'/>"));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxPartSelection.Read(reader));
    }

    [TestMethod]
    public async Task ExternalHeaderAndWrongContentTypeCannotBecomeDocumentParts()
    {
        const string body = "<w:sectPr><w:headerReference w:type='default' r:id='h'/></w:sectPr>";
        using var external = await Package(body,
            $"<Relationship Id='h' Type='{R}/header' Target='file:///never-read' TargetMode='External'/>");
        Assert.ThrowsExactly<InvalidDataException>(() => DocxPartSelection.Read(external));
        using var wrongType = await Package(body, Relation("h", "header", "h.xml"),
            ("custom/h.xml", "footer", $"<w:hdr xmlns:w='{W}'/>"));
        Assert.ThrowsExactly<InvalidDataException>(() => DocxPartSelection.Read(wrongType));
    }

    [TestMethod]
    public async Task SectionBudgetAndCancellationRemainExplicit()
    {
        using var reader = await Package(string.Concat(Enumerable.Repeat(
            "<w:p><w:pPr><w:sectPr/></w:pPr></w:p>", 4096)), "");
        Assert.ThrowsExactly<OperationCanceledException>(() =>
            DocxPartSelection.Read(reader, new CancellationToken(true)));
        Assert.ThrowsExactly<OpenXmlReadSafety.DiffBudgetExceededException>(() => DocxPartSelection.Read(reader));
    }

    [TestMethod]
    public async Task ReadsRealCorpusBodyHeadersAndNotesAsSeparateParts()
    {
        string path = Path.Combine(AppContext.BaseDirectory, "Qualification", "docx", "content-after.docx");
        using var reader = await DocxPackageReader.OpenAsync(new DocumentContentSource("content.docx", null,
            new FileInfo(path).Length, _ => ValueTask.FromResult<Stream>(File.OpenRead(path))));
        var selection = DocxPartSelection.Read(reader);
        Assert.IsTrue(selection.HeadersFooters.Any(item => item.Kind == DocumentDiffPart.Header));
        Assert.IsTrue(selection.HeadersFooters.Any(item => item.Kind == DocumentDiffPart.Footer));
        Assert.IsTrue(selection.Notes.Any(item => item.Kind == DocumentDiffPart.Footnote));
        Assert.AreEqual("document", selection.MainDocument.Root!.Name.LocalName);
    }

    private static string Relation(string id, string kind, string target) =>
        $"<Relationship Id='{id}' Type='{R}/{kind}' Target='{target}'/>";

    private static Task<DocxPackageReader> Package(string body, string relationships,
        params (string Name, string Kind, string Xml)[] parts) =>
        PackageAt("custom/main.xml", body, relationships, parts);

    private static Task<DocxPackageReader> PackageAt(string mainName, string body, string relationships,
        params (string Name, string Kind, string Xml)[] parts)
    {
        var bytes = new MemoryStream();
        using (var zip = new ZipArchive(bytes, ZipArchiveMode.Create, true))
        {
            Write("[Content_Types].xml", "<Types xmlns='http://schemas.openxmlformats.org/package/2006/content-types'>" +
                "<Default Extension='rels' ContentType='application/vnd.openxmlformats-package.relationships+xml'/>" +
                $"<Override PartName='/{mainName}' ContentType='{Mime}document.main+xml'/>" +
                string.Concat(parts.Select(part => $"<Override PartName='/{part.Name}' ContentType='{Mime}{part.Kind}+xml'/>")) + "</Types>");
            Write("_rels/.rels", RelRoot(Relation("main", "officeDocument", mainName)));
            Write(mainName, $"<w:document xmlns:w='{W}' xmlns:r='{R}'><w:body>{body}</w:body></w:document>");
            Write("custom/_rels/" + mainName["custom/".Length..] + ".rels", RelRoot(relationships));
            foreach (var part in parts)
                Write(part.Name, part.Xml);
            void Write(string name, string text)
            {
                using var stream = zip.CreateEntry(name).Open();
                stream.Write(Encoding.UTF8.GetBytes(text));
            }
        }
        bytes.Position = 0;
        return DocxPackageReader.OpenAsync(new DocumentContentSource("test.docx", null, bytes.Length,
            _ => ValueTask.FromResult<Stream>(bytes)));
    }

    private static string RelRoot(string children) =>
        "<Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'>" + children + "</Relationships>";
}
