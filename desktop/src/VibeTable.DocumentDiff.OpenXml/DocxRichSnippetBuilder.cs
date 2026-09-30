using System.Globalization;
using System.Xml.Linq;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

internal static class DocxRichSnippetBuilder
{
    private static readonly XNamespace W =
        "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    public static DocumentDiffRichSnippet? Build(
        IReadOnlyList<DocxStyledText> runs,
        DocumentDiffRichRunRole role)
    {
        ArgumentNullException.ThrowIfNull(runs);
        if (!Enum.IsDefined(role))
            throw new ArgumentOutOfRangeException(nameof(role));

        var output = new List<DocumentDiffRichRun>();
        foreach (DocxStyledText run in runs)
        {
            ArgumentNullException.ThrowIfNull(run);
            ArgumentNullException.ThrowIfNull(run.Text);
            ValidateProperties(run.RunProperties);
            if (run.Text.Length == 0)
                continue;

            XElement? properties = run.RunProperties;
            output.Add(new DocumentDiffRichRun(
                run.Text,
                role,
                ReadOnOff(properties, "b"),
                ReadOnOff(properties, "i"),
                ReadUnderline(properties),
                ReadOnOff(properties, "strike"),
                ReadFontSize(properties),
                ReadTextAttribute(properties, "rFonts", "ascii", "hAnsi", "eastAsia", "cs"),
                ReadRgb(properties, "color"),
                ReadHighlight(properties),
                ReadTextAttribute(properties, "rStyle", "val")));
        }

        return output.Count == 0 ? null : new DocumentDiffRichSnippet(output);
    }

    private static void ValidateProperties(XElement? properties)
    {
        if (properties is not null
            && (properties.Name != W + "rPr" || properties.Descendants(W + "rPrChange").Any()))
        {
            throw new ArgumentException("Runs must have normalized w:rPr properties.", nameof(properties));
        }
    }

    private static bool? ReadOnOff(XElement? properties, string name)
    {
        XElement? element = properties?.Element(W + name);
        if (element is null)
            return null;

        string? value = (string?)element.Attribute(W + "val");
        if (value is null)
            return true;
        return value.ToLowerInvariant() switch
        {
            "true" or "on" or "1" => true,
            "false" or "off" or "0" => false,
            _ => null,
        };
    }

    private static bool? ReadUnderline(XElement? properties)
    {
        XElement? element = properties?.Element(W + "u");
        if (element is null)
            return null;

        string? value = (string?)element.Attribute(W + "val");
        if (value is null)
            return true;
        return value.ToLowerInvariant() switch
        {
            "none" => false,
            "single" or "words" or "double" or "thick" or "dotted" or "dottedheavy"
                or "dash" or "dashedheavy" or "dashlong" or "dashlongheavy" or "dotdash"
                or "dashdotheavy" or "dotdotdash" or "dashdotdotheavy" or "wavy"
                or "wavyheavy" or "wavydouble" => true,
            _ => null,
        };
    }

    private static double? ReadFontSize(XElement? properties)
    {
        string? value = ReadTextAttribute(properties, "sz", "val");
        if (!double.TryParse(value, NumberStyles.Float, CultureInfo.InvariantCulture, out double halfPoints)
            || !double.IsFinite(halfPoints) || halfPoints <= 0)
        {
            return null;
        }
        double points = halfPoints / 2;
        return double.IsFinite(points) && points > 0 ? points : null;
    }

    private static string? ReadRgb(XElement? properties, string elementName)
    {
        string? value = ReadTextAttribute(properties, elementName, "val");
        if (value is null || value.Length != 6 || value.Any(character => !Uri.IsHexDigit(character)))
            return null;
        return "#" + value.ToUpperInvariant();
    }

    private static string? ReadHighlight(XElement? properties)
    {
        string? value = ReadTextAttribute(properties, "highlight", "val");
        return value?.ToLowerInvariant() switch
        {
            "black" => "#000000",
            "blue" => "#0000FF",
            "cyan" => "#00FFFF",
            "green" => "#00FF00",
            "magenta" => "#FF00FF",
            "red" => "#FF0000",
            "yellow" => "#FFFF00",
            "white" => "#FFFFFF",
            "darkblue" => "#000080",
            "darkcyan" => "#008080",
            "darkgreen" => "#008000",
            "darkmagenta" => "#800080",
            "darkred" => "#800000",
            "darkyellow" => "#808000",
            "darkgray" => "#808080",
            "lightgray" => "#C0C0C0",
            _ => null,
        };
    }

    private static string? ReadTextAttribute(
        XElement? properties,
        string elementName,
        params string[] attributeNames)
    {
        XElement? element = properties?.Element(W + elementName);
        if (element is null)
            return null;

        foreach (string attributeName in attributeNames)
        {
            string? value = (string?)element.Attribute(W + attributeName);
            if (!string.IsNullOrWhiteSpace(value))
                return value;
        }
        return null;
    }
}
