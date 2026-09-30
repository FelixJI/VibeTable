using System.Text;
using System.Xml;
using System.Xml.Linq;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxStyledText(string Text, XElement? RunProperties);

internal static class DocxRevisionRuns
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    public static IReadOnlyList<XElement> Build(
        DocxTextDiff diff,
        IReadOnlyList<DocxStyledText> before,
        IReadOnlyList<DocxStyledText> after,
        ref int nextRevisionId)
    {
        ArgumentNullException.ThrowIfNull(diff);
        ArgumentNullException.ThrowIfNull(before);
        ArgumentNullException.ThrowIfNull(after);
        ArgumentOutOfRangeException.ThrowIfNegative(nextRevisionId);
        if (diff.Truncated)
            throw new ArgumentException("Cannot publish a truncated alignment.", nameof(diff));

        var left = new RunCursor(Prepare(before));
        var right = new RunCursor(Prepare(after));
        ValidateAlignment(diff.Segments, left.Text, right.Text);
        int revisionId = nextRevisionId;
        var output = new List<XElement>();
        foreach (DocxTextSegment segment in diff.Segments)
        {
            if (segment.IsChange)
            {
                AddChange(left, segment.Before.Length, "del", "delText");
                AddChange(right, segment.After.Length, "ins", "t");
                continue;
            }

            int remaining = segment.Before.Length;
            while (remaining > 0)
            {
                int count = Math.Min(remaining, Math.Min(left.Remaining, right.Remaining));
                XElement? properties = CloneProperties(right.Properties);
                if (!SameProperties(left.Properties, right.Properties))
                {
                    properties ??= new XElement(W + "rPr");
                    properties.Add(Revision("rPrChange", ref revisionId,
                        CloneProperties(left.Properties) ?? new XElement(W + "rPr")));
                }
                output.Add(Run(left.Take(count), properties, "t"));
                right.Take(count);
                remaining -= count;
            }
        }
        nextRevisionId = revisionId;
        return output.AsReadOnly();

        void AddChange(RunCursor source, int remaining, string revision, string textElement)
        {
            if (remaining == 0)
                return;
            XElement wrapper = Revision(revision, ref revisionId);
            while (remaining > 0)
            {
                int count = Math.Min(remaining, source.Remaining);
                XElement? properties = CloneProperties(source.Properties);
                wrapper.Add(Run(source.Take(count), properties, textElement));
                remaining -= count;
            }
            output.Add(wrapper);
        }
    }

    private static IReadOnlyList<DocxStyledText> Prepare(IReadOnlyList<DocxStyledText> input)
    {
        var runs = new List<DocxStyledText>();
        var text = new StringBuilder();
        XElement? properties = null;
        int length = 0;
        foreach (DocxStyledText run in input)
        {
            ArgumentNullException.ThrowIfNull(run);
            ArgumentNullException.ThrowIfNull(run.Text);
            if (run.RunProperties is { } rPr &&
                (rPr.Name != W + "rPr" || rPr.Descendants(W + "rPrChange").Any()))
                throw new ArgumentException("Runs must have normalized w:rPr properties.", nameof(input));
            if (run.Text.Length == 0)
                continue;
            length = checked(length + run.Text.Length);
            if (length > OpenXmlExtractionLimits.MaxVisibleTextCharacters)
                throw new ArgumentException("Run text exceeds the document text budget.", nameof(input));
            if (text.Length > 0 && !SameProperties(properties, run.RunProperties))
                Flush();
            properties = run.RunProperties;
            text.Append(run.Text);
        }
        Flush();
        return runs;

        void Flush()
        {
            if (text.Length == 0)
                return;
            string value = text.ToString();
            // Adjacent equally styled runs may split a surrogate pair. Merge them before
            // validation; incompatible styles inside one scalar cannot be represented in XML.
            XmlConvert.VerifyXmlChars(value);
            runs.Add(new DocxStyledText(value, properties));
            text.Clear();
        }
    }

    private static void ValidateAlignment(
        IReadOnlyList<DocxTextSegment> segments, string before, string after)
    {
        int oldOffset = 0;
        int newOffset = 0;
        foreach (DocxTextSegment segment in segments)
        {
            Check(segment.Before, before, ref oldOffset);
            Check(segment.After, after, ref newOffset);
        }
        if (oldOffset != before.Length || newOffset != after.Length)
            throw new ArgumentException("Alignment does not consume all run text.", nameof(segments));

        static void Check(string part, string text, ref int offset)
        {
            XmlConvert.VerifyXmlChars(part);
            if (!text.AsSpan(offset).StartsWith(part.AsSpan(), StringComparison.Ordinal))
                throw new ArgumentException("Alignment does not match run text.", nameof(segments));
            offset += part.Length;
        }
    }

    private static bool SameProperties(XElement? left, XElement? right) =>
        XNode.DeepEquals(left ?? new XElement(W + "rPr"), right ?? new XElement(W + "rPr"));

    private static XElement? CloneProperties(XElement? properties) =>
        properties is null ? null : new XElement(properties);

    private static XElement Run(string text, XElement? properties, string textElement) =>
        new(W + "r", properties,
            new XElement(W + textElement, new XAttribute(XNamespace.Xml + "space", "preserve"), text));

    private static XElement Revision(string name, ref int id, params object[] content)
    {
        int current = id;
        id = checked(id + 1);
        return new XElement(W + name,
            new XAttribute(W + "id", current),
            new XAttribute(W + "author", "VibeTable"),
            new XAttribute(W + "date", "2000-01-01T00:00:00Z"), content);
    }

    private sealed class RunCursor(IReadOnlyList<DocxStyledText> runs)
    {
        private int _index;
        private int _offset;

        public string Text { get; } = string.Concat(runs.Select(run => run.Text));
        public int Remaining => runs[_index].Text.Length - _offset;
        public XElement? Properties => runs[_index].RunProperties;

        public string Take(int count)
        {
            string text = runs[_index].Text.Substring(_offset, count);
            _offset += count;
            if (_offset == runs[_index].Text.Length)
            {
                _index++;
                _offset = 0;
            }
            return text;
        }
    }
}
