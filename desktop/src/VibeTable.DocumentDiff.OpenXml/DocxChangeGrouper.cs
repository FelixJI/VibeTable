using System.Xml.Linq;
using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxChangeGroups(
    IReadOnlyList<DocumentDiffChange> Changes, DocumentDiffSummary Summary, bool Truncated);

internal static class DocxChangeGrouper
{
    private static readonly XNamespace W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";

    // Consumes one paragraph produced by DocxRevisionRuns, not arbitrary vendor XML.
    // A call boundary is also a hard grouping boundary (part, paragraph and cell).
    public static DocxChangeGroups Group(IReadOnlyList<XElement> runs,
        DocumentDiffLocation location, int maxGroups, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(runs);
        ArgumentNullException.ThrowIfNull(location);
        ArgumentOutOfRangeException.ThrowIfNegative(maxGroups);
        cancellationToken.ThrowIfCancellationRequested();
        var changes = new List<DocumentDiffChange>();
        var before = new List<DocxStyledText>();
        var after = new List<DocxStyledText>();
        bool? formatting = null;
        bool truncated = false;
        int rawCount = 0;
        foreach (XElement element in runs)
        {
            cancellationToken.ThrowIfCancellationRequested();
            ArgumentNullException.ThrowIfNull(element);
            if (element.Name == W + "ins" || element.Name == W + "del")
            {
                Start(false);
                rawCount = checked(rawCount + 1);
                bool insertion = element.Name == W + "ins";
                if (!element.Elements().Any())
                    throw new InvalidDataException("A text revision cannot be empty.");
                foreach (XElement run in element.Elements())
                {
                    cancellationToken.ThrowIfCancellationRequested();
                    var styled = ReadRun(run, insertion ? "t" : "delText");
                    if (styled.RunProperties?.Element(W + "rPrChange") is not null)
                        throw new InvalidDataException("Text revisions must have normalized properties.");
                    (insertion ? after : before).Add(styled);
                }
            }
            else
            {
                DocxStyledText current = ReadRun(element, "t");
                if (current.RunProperties?.Element(W + "rPrChange") is { } revision)
                {
                    Start(true);
                    rawCount = checked(rawCount + 1);
                    XElement previous = revision.Element(W + "rPr")
                        ?? throw new InvalidDataException("Format revision lacks old properties.");
                    before.Add(new DocxStyledText(current.Text, new XElement(previous)));
                    revision.Remove();
                    after.Add(current);
                }
                else
                {
                    Flush();
                }
            }
        }
        Flush();
        int Count(DocumentDiffChangeKind kind) => changes.Count(change => change.Kind == kind);
        var summary = new DocumentDiffSummary(changes.Count, rawCount,
            Count(DocumentDiffChangeKind.Insert), Count(DocumentDiffChangeKind.Delete),
            Count(DocumentDiffChangeKind.Replace), 0, Count(DocumentDiffChangeKind.Format), 0, 0, 0);
        return new DocxChangeGroups(changes.AsReadOnly(), summary, truncated);

        void Start(bool isFormat)
        {
            if (formatting is not null && formatting != isFormat)
                Flush();
            formatting = isFormat;
        }

        void Flush()
        {
            if (formatting is null)
                return;
            cancellationToken.ThrowIfCancellationRequested();
            if (changes.Count == maxGroups)
            {
                truncated = true;
            }
            else
            {
                var kind = formatting.Value ? DocumentDiffChangeKind.Format
                    : before.Count == 0 ? DocumentDiffChangeKind.Insert
                    : after.Count == 0 ? DocumentDiffChangeKind.Delete : DocumentDiffChangeKind.Replace;
                changes.Add(new DocumentDiffChange(Guid.NewGuid(), kind, location,
                    DocxRichSnippetBuilder.Build(before, formatting.Value
                        ? DocumentDiffRichRunRole.Changed : DocumentDiffRichRunRole.Deleted),
                    DocxRichSnippetBuilder.Build(after, formatting.Value
                        ? DocumentDiffRichRunRole.Changed : DocumentDiffRichRunRole.Inserted),
                    DocumentDiffConfidence.Normalized));
            }
            before.Clear();
            after.Clear();
            formatting = null;
        }
    }

    private static DocxStyledText ReadRun(XElement run, string textName)
    {
        if (run.Name != W + "r" || run.Elements().Any(e => e.Name != W + "rPr" && e.Name != W + textName)
            || run.Elements(W + textName).Count() != 1 || run.Elements(W + "rPr").Count() > 1)
            throw new InvalidDataException("Expected a generated plain-text run.");
        string text = run.Element(W + textName)!.Value;
        if (text.Length == 0)
            throw new InvalidDataException("A generated run cannot be empty.");
        return new DocxStyledText(text, run.Element(W + "rPr") is { } properties ? new XElement(properties) : null);
    }
}
