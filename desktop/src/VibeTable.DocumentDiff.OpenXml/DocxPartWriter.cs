using System.Text;
using System.Xml;
using System.Xml.Linq;

namespace VibeTable.DocumentDiff.OpenXml;

internal static class DocxPartWriter
{
    public static void Write(Stream output, XElement root)
    {
        ArgumentNullException.ThrowIfNull(output);
        ArgumentNullException.ThrowIfNull(root);
        using var writer = XmlWriter.Create(output, new XmlWriterSettings
        {
            Encoding = new UTF8Encoding(false),
            // XML readers normalize literal CR/CRLF. Character references preserve
            // the source text when the comparison part is parsed again.
            NewLineHandling = NewLineHandling.Entitize,
            CloseOutput = false,
        });
        root.Save(writer);
    }
}
