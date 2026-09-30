using System.Net.Mime;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxHeaderFooterBinding(int SectionIndex, DocumentDiffPart Kind,
    string Variant, bool IsDisplayed, string PartName, XDocument Document);
internal sealed record DocxNotePart(DocumentDiffPart Kind, string PartName, XDocument Document);

internal sealed record DocxPartSelection(string MainPartName, XDocument MainDocument,
    int SectionCount, bool EvenAndOddHeaders, IReadOnlyList<DocxHeaderFooterBinding> HeadersFooters,
    IReadOnlyList<DocxNotePart> Notes)
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
    private static readonly XNamespace R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
    private const string Mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.";
    private const int MaxSections = 4096;

    // Inputs must already have existing revisions normalized. Physical parts are
    // loaded once; bindings retain section/variant identity without copying XML.
    public static DocxPartSelection Read(DocxPackageReader reader, CancellationToken cancellationToken = default)
    {
        cancellationToken.ThrowIfCancellationRequested();
        DocxContentTypes types = DocxContentTypes.Read(reader, cancellationToken);
        var rootRelationships = DocxRelationships.Read(reader, "/", cancellationToken);
        DocxRelationship[] mains = rootRelationships.Where(item => item.Type == R.NamespaceName + "/officeDocument").ToArray();
        if (mains.Length != 1)
            throw new InvalidDataException("DOCX must have exactly one main document relationship.");
        var documents = new Dictionary<string, XDocument>(StringComparer.OrdinalIgnoreCase);
        XDocument main = Load(mains[0], "document.main", "document");
        XElement[] bodies = main.Root!.Elements(W + "body").ToArray();
        if (bodies.Length != 1)
            throw new InvalidDataException("DOCX must have exactly one body.");
        XElement body = bodies[0];
        string mainName = mains[0].PartName!;
        var relationships = DocxRelationships.Read(reader, mainName, cancellationToken);
        var byId = relationships.ToDictionary(item => item.Id, StringComparer.Ordinal);
        DocxRelationship? settings = OptionalRelationship("settings");
        bool evenAndOdd = settings is not null && On(Load(settings, "settings", "settings").Root!.Element(W + "evenAndOddHeaders"));

        var notes = new List<DocxNotePart>();
        foreach (var (kind, name) in new[] { (DocumentDiffPart.Footnote, "footnotes"), (DocumentDiffPart.Endnote, "endnotes") })
        {
            DocxRelationship? relationship = OptionalRelationship(name);
            if (relationship is not null)
                notes.Add(new(kind, relationship.PartName!, Load(relationship, name, name)));
        }

        var sections = new List<XElement>();
        foreach (XElement element in body.Descendants())
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (element.Name != W + "sectPr" || element.Ancestors().Any(ancestor =>
                    ancestor.Name == W + "txbxContent" || ancestor.Name == W + "tbl"))
                continue;
            if (element.Parent == body || element.Parent?.Name == W + "pPr" && element.Parent.Parent?.Name == W + "p")
            {
                if (sections.Count == MaxSections)
                    throw new DiffBudgetExceededException();
                sections.Add(element);
            }
        }
        // The last section may omit sectPr entirely.
        if (sections.Count == 0 || sections[^1].Parent != body)
        {
            if (sections.Count == MaxSections)
                throw new DiffBudgetExceededException();
            sections.Add(new XElement(W + "sectPr"));
        }
        if (body.Elements(W + "sectPr").Skip(1).Any())
            throw new InvalidDataException("Duplicate final section properties.");

        var inherited = new Dictionary<(DocumentDiffPart Kind, string Variant), DocxRelationship>();
        var bindings = new List<DocxHeaderFooterBinding>();
        for (int sectionIndex = 0; sectionIndex < sections.Count; sectionIndex++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            XElement section = sections[sectionIndex];
            var declared = new HashSet<(DocumentDiffPart, string)>();
            foreach (XElement reference in section.Elements().Where(element =>
                         element.Name == W + "headerReference" || element.Name == W + "footerReference"))
            {
                bool header = reference.Name == W + "headerReference";
                DocumentDiffPart kind = header ? DocumentDiffPart.Header : DocumentDiffPart.Footer;
                string? variant = (string?)reference.Attribute(W + "type");
                string? id = (string?)reference.Attribute(R + "id");
                if (variant is not ("default" or "first" or "even") || id is null ||
                    !declared.Add((kind, variant)) || !byId.TryGetValue(id, out DocxRelationship? relationship) ||
                    relationship.Type != R.NamespaceName + (header ? "/header" : "/footer"))
                    throw new InvalidDataException("Invalid header/footer reference.");
                // Validate even a currently hidden variant; later sections can inherit it.
                Load(relationship, header ? "header" : "footer", header ? "hdr" : "ftr");
                inherited[(kind, variant)] = relationship;
            }
            foreach (var pair in inherited.OrderBy(item => item.Key.Kind).ThenBy(item => item.Key.Variant, StringComparer.Ordinal))
            {
                bool displayed = pair.Key.Variant == "default" ||
                    pair.Key.Variant == "first" && On(section.Element(W + "titlePg")) ||
                    pair.Key.Variant == "even" && evenAndOdd;
                bindings.Add(new(sectionIndex, pair.Key.Kind, pair.Key.Variant, displayed,
                    pair.Value.PartName!, documents[DocxRelationships.PartKey(pair.Value.PartName!)]));
            }
        }
        return new(mainName, main, sections.Count, evenAndOdd, bindings.AsReadOnly(), notes.AsReadOnly());

        DocxRelationship? OptionalRelationship(string name)
        {
            DocxRelationship[] matches = relationships.Where(item => item.Type == R.NamespaceName + "/" + name).ToArray();
            if (matches.Length > 1)
                throw new InvalidDataException("Duplicate singleton document relationship.");
            return matches.SingleOrDefault();
        }

        XDocument Load(DocxRelationship relationship, string contentKind, string rootName)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (relationship.IsExternal || relationship.PartName is null || relationship.Fragment is not null ||
                !string.Equals(new ContentType(types.Get(relationship.PartName)).MediaType,
                    Mime + contentKind + "+xml", StringComparison.OrdinalIgnoreCase))
                throw new InvalidDataException("Invalid DOCX part relationship or content type.");
            string key = DocxRelationships.PartKey(relationship.PartName);
            if (!documents.TryGetValue(key, out XDocument? document))
            {
                document = reader.ReadXmlPart(relationship.PartName, cancellationToken);
                documents.Add(key, document);
            }
            if (document.Root?.Name != W + rootName)
                throw new InvalidDataException("Unexpected DOCX part root.");
            return document;
        }
    }

    private static bool On(XElement? element) => element is not null &&
        ((string?)element.Attribute(W + "val") switch
        {
            null or "true" or "1" or "on" => true,
            "false" or "0" or "off" => false,
            _ => throw new InvalidDataException("Invalid on/off document setting."),
        });
}
