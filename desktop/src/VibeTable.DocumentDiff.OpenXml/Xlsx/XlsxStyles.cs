using System.Globalization;
using System.Xml.Linq;
using static VibeTable.DocumentDiff.OpenXml.OpenXmlReadSafety;

namespace VibeTable.DocumentDiff.OpenXml;

internal static partial class XlsxSemanticDiff
{
    private static string[] ReadStyles(XElement root)
    {
        if (root.Name != S + "styleSheet") throw new InvalidDataException("Invalid styles root.");
        string[] fonts = Definitions(root, "fonts", "font");
        string[] fills = Definitions(root, "fills", "fill");
        string[] borders = Definitions(root, "borders", "border");
        var formats = new Dictionary<uint, string>();
        XElement? nums = Only(root, "numFmts");
        if (nums is not null)
            foreach (var num in nums.Elements())
            {
                uint id = UInt(Required(num, "numFmtId"));
                if (num.Name != S + "numFmt" || id < 164 ||
                    !formats.TryAdd(id, Required(num, "formatCode")))
                    throw new InvalidDataException("Invalid custom number format.");
            }
        XElement? xfs = Only(root, "cellXfs");
        if (xfs is null || !xfs.HasElements) throw new InvalidDataException("Missing cell styles.");
        var styles = new List<string>();
        foreach (var xf in xfs.Elements())
        {
            if (styles.Count == MaxCells) throw new DiffBudgetExceededException();
            if (xf.Name != S + "xf") throw new InvalidDataException("Invalid cell style.");
            uint num = UInt((string?)xf.Attribute("numFmtId") ?? "0");
            string numFormat = num < 164 ? "builtin:" + num.ToString(CultureInfo.InvariantCulture) :
                formats.GetValueOrDefault(num) ?? throw new InvalidDataException("Missing number format.");
            foreach (string flag in new[] { "applyNumberFormat", "applyFont", "applyFill", "applyBorder",
                "applyAlignment", "applyProtection", "pivotButton", "quotePrefix" })
                if (xf.Attribute(flag) is { } attribute) Boolean(attribute.Value);
            string font = Component(xf, "fontId", fonts);
            string fill = Component(xf, "fillId", fills);
            string border = Component(xf, "borderId", borders);
            XElement? alignment = Only(xf, "alignment");
            if (alignment is not null)
            {
                foreach (string flag in new[] { "wrapText", "shrinkToFit", "justifyLastLine" })
                    if (alignment.Attribute(flag) is { } attribute) Boolean(attribute.Value);
                foreach (string number in new[] { "textRotation", "indent", "relativeIndent", "readingOrder" })
                    if (alignment.Attribute(number) is { } attribute) UInt(attribute.Value);
            }
            string style = "font=" + font + "; fill=" + fill + "; border=" + border +
                "; numFmt=" + numFormat + "; alignment=" + (alignment is null ? "" : Canonical(alignment));
            styles.Add(style);
        }
        return styles.ToArray();
    }

    private static string Component(XElement xf, string attribute, string[] definitions)
    {
        int index = Index((string?)xf.Attribute(attribute) ?? "0", definitions.Length - 1, zero: true);
        return definitions[index];
    }

    private static string[] Definitions(XElement root, string container, string item)
    {
        XElement? parent = Only(root, container);
        if (parent is null || !parent.HasElements) throw new InvalidDataException("Missing style definitions.");
        var result = new List<string>();
        foreach (var element in parent.Elements())
        {
            if (result.Count == MaxCells) throw new DiffBudgetExceededException();
            if (element.Name != S + item) throw new InvalidDataException("Invalid style definition.");
            result.Add(Canonical(element));
        }
        return result.ToArray();
    }

    private static string Canonical(XElement element)
    {
        if (element.Name.Namespace != S) throw new InvalidDataException("Invalid style namespace.");
        string name = element.Name.LocalName;
        bool toggle = name is "b" or "i" or "strike" or "outline" or "shadow" or "condense" or "extend";
        if (toggle)
        {
            bool value = Boolean((string?)element.Attribute("val") ?? "1");
            return value ? name + "=true" : "";
        }
        if (name == "u")
        {
            string underline = (string?)element.Attribute("val") ?? "single";
            return underline == "none" ? "" : "u=" + underline;
        }
        var attributes = element.Attributes().Where(a => !a.IsNamespaceDeclaration)
            .OrderBy(a => a.Name.ToString(), StringComparer.Ordinal)
            .Select(a =>
            {
                string value = a.Value;
                if (a.Name.LocalName is "wrapText" or "shrinkToFit" or "justifyLastLine")
                {
                    if (!Boolean(value)) return "";
                    value = "true";
                }
                if (a.Name.LocalName == "rgb")
                {
                    if (value.Length != 8 || value.Any(c => !Uri.IsHexDigit(c)))
                        throw new InvalidDataException("Invalid style color.");
                    value = value.ToUpperInvariant();
                }
                if (name == "sz" && a.Name.LocalName == "val") value = Number(value);
                // Numeric style attributes compare by canonical number, not raw spelling:
                // "07"/"7" or "0.50"/"0.5" must not report a formatting change. Non-numeric
                // values fail closed through Number; alignment ranges stay UInt-validated.
                if (a.Name.LocalName is "textRotation" or "indent" or "relativeIndent" or
                    "readingOrder" or "tint")
                    value = Number(value);
                return a.Name.LocalName + "=" + value.Length.ToString(CultureInfo.InvariantCulture) + ":" + value;
            }).Where(a => a.Length != 0);
        // Font/fill/border properties are keyed elements; ZIP ordering and definition indices are not semantics.
        string[] children = element.Elements().Select(Canonical).Where(v => v.Length != 0)
            .Order(StringComparer.Ordinal).ToArray();
        return name + "(" + string.Join(",", attributes) + ")[" + string.Join(",", children) + "]";
    }
}
