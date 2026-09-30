namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxParagraphSpan(int BeforeStart, int BeforeCount,
    int AfterStart, int AfterCount, bool EqualText);
internal sealed record DocxParagraphAlignment(IReadOnlyList<DocxParagraphSpan> Spans, bool Truncated);

internal static class DocxParagraphAligner
{
    // Call separately for each aligned story/container. EqualText is an alignment
    // anchor, not evidence that run properties or other paragraph content match.
    // Unmatched ranges retain both sides for the structural/text differ; they do
    // not invent one-to-one replacements where correspondence is ambiguous.
    public static DocxParagraphAlignment Align(IReadOnlyList<string> before, IReadOnlyList<string> after,
        int maxCells, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(before);
        ArgumentNullException.ThrowIfNull(after);
        ArgumentOutOfRangeException.ThrowIfNegative(maxCells);
        cancellationToken.ThrowIfCancellationRequested();
        var spans = new List<DocxParagraphSpan>();
        if (!WithinTextBudget(before) || !WithinTextBudget(after))
        {
            Append(0, before.Count, 0, after.Count, false);
            return new(spans.AsReadOnly(), true);
        }
        int prefix = 0;
        while (prefix < before.Count && prefix < after.Count && before[prefix] == after[prefix])
        {
            cancellationToken.ThrowIfCancellationRequested();
            prefix++;
        }
        int leftEnd = before.Count;
        int rightEnd = after.Count;
        while (leftEnd > prefix && rightEnd > prefix && before[leftEnd - 1] == after[rightEnd - 1])
        {
            cancellationToken.ThrowIfCancellationRequested();
            leftEnd--;
            rightEnd--;
        }
        Append(0, prefix, 0, prefix, true);
        int rows = leftEnd - prefix;
        int columns = rightEnd - prefix;
        bool truncated = rows > 0 && columns > 0 && ((long)rows + 1) * ((long)columns + 1) > maxCells;
        if (truncated || rows == 0 || columns == 0)
            Append(prefix, rows, prefix, columns, false);
        else
        {
            var lcs = new int[rows + 1, columns + 1];
            for (int i = rows - 1; i >= 0; i--)
            {
                cancellationToken.ThrowIfCancellationRequested();
                for (int j = columns - 1; j >= 0; j--)
                    lcs[i, j] = before[prefix + i] == after[prefix + j]
                        ? 1 + lcs[i + 1, j + 1] : Math.Max(lcs[i + 1, j], lcs[i, j + 1]);
            }
            int x = 0;
            int y = 0;
            while (x < rows || y < columns)
            {
                cancellationToken.ThrowIfCancellationRequested();
                if (x < rows && y < columns && before[prefix + x] == after[prefix + y])
                    Append(prefix + x++, 1, prefix + y++, 1, true);
                else if (x < rows && (y == columns || lcs[x + 1, y] >= lcs[x, y + 1]))
                    Append(prefix + x++, 1, prefix + y, 0, false);
                else
                    Append(prefix + x, 0, prefix + y++, 1, false);
            }
        }
        Append(leftEnd, before.Count - leftEnd, rightEnd, after.Count - rightEnd, true);
        return new(spans.AsReadOnly(), truncated);

        bool WithinTextBudget(IReadOnlyList<string> paragraphs)
        {
            if (paragraphs.Count > 250_000)
                return false;
            long characters = 0;
            foreach (string text in paragraphs)
            {
                cancellationToken.ThrowIfCancellationRequested();
                characters += text.Length;
                if (characters > OpenXmlExtractionLimits.MaxVisibleTextCharacters)
                    return false;
            }
            return true;
        }

        void Append(int oldStart, int oldCount, int newStart, int newCount, bool equalText)
        {
            if (oldCount == 0 && newCount == 0)
                return;
            if (spans.Count > 0 && spans[^1] is var last && last.EqualText == equalText)
                spans[^1] = last with { BeforeCount = last.BeforeCount + oldCount, AfterCount = last.AfterCount + newCount };
            else
                spans.Add(new(oldStart, oldCount, newStart, newCount, equalText));
        }
    }
}
