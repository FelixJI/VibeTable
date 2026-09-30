using System.Net.Mime;
using System.Xml.Linq;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed class DocxContentTypes
{
    private static readonly XNamespace Namespace =
        "http://schemas.openxmlformats.org/package/2006/content-types";
    private readonly Dictionary<string, string> _defaults;
    private readonly Dictionary<string, string> _overrides;

    private DocxContentTypes(Dictionary<string, string> defaults, Dictionary<string, string> overrides)
    {
        _defaults = defaults;
        _overrides = overrides;
    }

    public static DocxContentTypes Read(DocxPackageReader reader,
        CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(reader);
        cancellationToken.ThrowIfCancellationRequested();
        XElement? root = reader.ReadXmlPart("[Content_Types].xml", cancellationToken).Root;
        if (root?.Name != Namespace + "Types" || root.Attributes().Any(attribute =>
                !attribute.IsNamespaceDeclaration) || HasNonWhitespaceText(root))
            throw new InvalidDataException("Invalid content types root.");

        var defaults = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        var overrides = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        foreach (XElement element in root.Elements())
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (element.HasElements || HasNonWhitespaceText(element))
                throw new InvalidDataException("Invalid content type entry.");
            if (element.Name == Namespace + "Default")
            {
                RequireOnly(element, "Extension", "ContentType");
                string extension = Required(element, "Extension");
                if (!IsExtension(extension) || !defaults.TryAdd(extension, ValidContentType(element)))
                    throw new InvalidDataException("Invalid or duplicate default content type.");
            }
            else if (element.Name == Namespace + "Override")
            {
                RequireOnly(element, "PartName", "ContentType");
                string partName = Required(element, "PartName");
                if (!partName.StartsWith('/') || partName.StartsWith("//") || HasDotSegment(partName) ||
                    !overrides.TryAdd(DocxRelationships.PartKey(partName[1..]), ValidContentType(element)))
                    throw new InvalidDataException("Invalid or duplicate override content type.");
            }
            else
            {
                throw new InvalidDataException("Invalid content type entry.");
            }
        }
        return new DocxContentTypes(defaults, overrides);
    }

    public string Get(string partName)
    {
        string key = DocxRelationships.PartKey(partName);
        if (_overrides.TryGetValue(key, out string? overrideContentType))
            return overrideContentType;
        int dot = key.LastIndexOf('.');
        int slash = key.LastIndexOf('/');
        if (dot <= slash || !_defaults.TryGetValue(key[(dot + 1)..], out string? defaultContentType))
            throw new InvalidDataException("Package part has no content type.");
        return defaultContentType;
    }

    private static string Required(XElement element, string name) =>
        (string?)element.Attribute(name) is { Length: > 0 } value ? value :
            throw new InvalidDataException("Missing content type attribute.");

    private static void RequireOnly(XElement element, params string[] names)
    {
        if (element.Attributes().Any(attribute => !attribute.IsNamespaceDeclaration &&
                (attribute.Name.Namespace != XNamespace.None ||
                !names.Contains(attribute.Name.LocalName, StringComparer.Ordinal))))
            throw new InvalidDataException("Invalid content type attribute.");
    }

    private static bool IsExtension(string value) => value.All(character =>
        character is not '.' and not '/' and not '\\' and not ':' and not '?' and not '#' &&
        !char.IsControl(character) && !char.IsWhiteSpace(character));

    private static bool HasNonWhitespaceText(XElement element) => element.Nodes()
        .OfType<XText>().Any(text => !string.IsNullOrWhiteSpace(text.Value));

    private static bool HasDotSegment(string partName) => partName[1..].Split('/').Any(segment =>
    {
        string decoded = Uri.UnescapeDataString(segment);
        return decoded is "." or "..";
    });

    private static string ValidContentType(XElement element)
    {
        string value = Required(element, "ContentType");
        if (value != value.Trim())
            throw new InvalidDataException("Invalid MIME content type.");
        try
        {
            _ = new ContentType(value);
            return value;
        }
        catch (Exception exception) when (exception is ArgumentException or FormatException)
        {
            throw new InvalidDataException("Invalid MIME content type.", exception);
        }
    }
}
