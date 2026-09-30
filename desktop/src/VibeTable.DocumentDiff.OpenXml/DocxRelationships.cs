using System.IO.Packaging;
using System.Xml;
using System.Xml.Linq;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxRelationship(string Id, string Type, string RawTarget,
    bool IsExternal, string? PartName, string? Fragment);

internal static class DocxRelationships
{
    private static readonly XNamespace R = "http://schemas.openxmlformats.org/package/2006/relationships";

    public static IReadOnlyList<DocxRelationship> Read(DocxPackageReader reader, string sourcePart,
        CancellationToken cancellationToken = default)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Uri source = sourcePart == "/" ? new Uri("/", UriKind.Relative) :
            PackUriHelper.CreatePartUri(new Uri("/" + reader.ResolvePartName(sourcePart), UriKind.Relative));
        string relationshipsPart = PackUriHelper.GetRelationshipPartUri(source).OriginalString.TrimStart('/');
        if (!reader.ContainsPart(relationshipsPart))
            return [];
        XElement? root = reader.ReadXmlPart(relationshipsPart, cancellationToken).Root;
        return Parse(reader, source, root, cancellationToken);
    }

    internal static IReadOnlyList<DocxRelationship> Parse(DocxPackageReader reader, Uri source,
        XElement? root, CancellationToken cancellationToken)
    {
        if (root?.Name != R + "Relationships")
            throw new InvalidDataException("Invalid relationships root.");
        var result = new List<DocxRelationship>();
        var ids = new HashSet<string>(StringComparer.Ordinal);
        foreach (XElement element in root.Elements())
        {
            cancellationToken.ThrowIfCancellationRequested();
            string id = Required(element, "Id");
            string type = Required(element, "Type");
            string target = Required(element, "Target");
            string mode = (string?)element.Attribute("TargetMode") ?? "Internal";
            if (element.Name != R + "Relationship" || element.HasElements ||
                !ids.Add(id) || !Uri.TryCreate(type, UriKind.Absolute, out _) ||
                mode is not ("Internal" or "External"))
                throw new InvalidDataException("Invalid or duplicate relationship.");
            try { XmlConvert.VerifyNCName(id); }
            catch (XmlException exception) { throw new InvalidDataException("Invalid relationship ID.", exception); }
            if (mode == "External")
            {
                // External targets are opaque metadata: never resolve or open them.
                result.Add(new(id, type, target, true, null, null));
                continue;
            }
            int fragmentOffset = target.IndexOf('#');
            string path = fragmentOffset < 0 ? target : target[..fragmentOffset];
            string? fragment = fragmentOffset < 0 ? null : target[(fragmentOffset + 1)..];
            // ResolvePartUri drops authority, query and fragment; validate first.
            if (path.StartsWith("//", StringComparison.Ordinal) || path.Contains('\\') ||
                path.Contains(':') || path.Contains('?') || path.Any(char.IsControl) ||
                !ValidEscapes(target) || !Uri.TryCreate(path, UriKind.Relative, out Uri? relative))
                throw new InvalidDataException("Invalid internal relationship target.");
            try
            {
                Uri resolved = PackUriHelper.ResolvePartUri(source, relative);
                string part = reader.ResolvePartName(resolved.OriginalString.TrimStart('/'));
                result.Add(new(id, type, target, false, part, fragment));
            }
            catch (ArgumentException exception)
            {
                throw new InvalidDataException("Invalid internal relationship target.", exception);
            }
        }
        return result.AsReadOnly();
    }

    internal static string PartKey(string partName)
    {
        if (partName == "[Content_Types].xml")
            return partName;
        if (partName.Length == 0 || partName.StartsWith('/') || partName.Contains('?') ||
            partName.Contains('#') || partName.Contains('\\') || partName.Contains(':') ||
            partName.Any(char.IsControl) || !ValidEscapes(partName))
            throw new InvalidDataException("Invalid package part URI.");
        try
        {
            return PackUriHelper.GetNormalizedPartUri(PackUriHelper.CreatePartUri(
                new Uri("/" + partName, UriKind.Relative))).OriginalString;
        }
        catch (ArgumentException exception)
        {
            throw new InvalidDataException("Invalid package part URI.", exception);
        }
    }

    private static string Required(XElement element, string name) =>
        (string?)element.Attribute(name) is { Length: > 0 } value ? value :
            throw new InvalidDataException("Missing relationship attribute.");

    private static bool ValidEscapes(string value)
    {
        for (int i = 0; i < value.Length; i++)
        {
            if (value[i] != '%')
                continue;
            if (i + 2 >= value.Length || !Uri.IsHexDigit(value[i + 1]) || !Uri.IsHexDigit(value[i + 2]))
                return false;
            i += 2;
        }
        return true;
    }
}
