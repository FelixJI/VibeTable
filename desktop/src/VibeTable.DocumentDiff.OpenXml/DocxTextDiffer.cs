using System.Text;

namespace VibeTable.DocumentDiff.OpenXml;

internal sealed record DocxTextSegment(string Before, string After)
{
    public bool IsChange => !string.Equals(Before, After, StringComparison.Ordinal);
}

internal sealed record DocxTextDiff(IReadOnlyList<DocxTextSegment> Segments, bool Truncated);

internal static class DocxTextDiffer
{
    public static DocxTextDiff Compare(
        string before, string after, int maxCells, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(before);
        ArgumentNullException.ThrowIfNull(after);
        ArgumentOutOfRangeException.ThrowIfNegative(maxCells);
        cancellationToken.ThrowIfCancellationRequested();
        if (before == after)
            return new DocxTextDiff(before.Length == 0 ? [] : [new(before, after)], false);
        if (before.Length > OpenXmlExtractionLimits.MaxVisibleTextCharacters ||
            after.Length > OpenXmlExtractionLimits.MaxVisibleTextCharacters)
            return new DocxTextDiff([new(before, after)], true);

        string[] left = Tokens(before);
        string[] right = Tokens(after);
        int prefix = 0;
        while (prefix < left.Length && prefix < right.Length && left[prefix] == right[prefix])
        {
            cancellationToken.ThrowIfCancellationRequested();
            prefix++;
        }
        int leftEnd = left.Length;
        int rightEnd = right.Length;
        while (leftEnd > prefix && rightEnd > prefix && left[leftEnd - 1] == right[rightEnd - 1])
        {
            cancellationToken.ThrowIfCancellationRequested();
            leftEnd--;
            rightEnd--;
        }

        var segments = new List<DocxTextSegment>();
        var oldText = new StringBuilder();
        var newText = new StringBuilder();
        bool? changed = null;
        for (int i = 0; i < prefix; i++)
            Emit(left[i], right[i]);
        int rows = leftEnd - prefix;
        int columns = rightEnd - prefix;
        bool truncated = rows > 0 && columns > 0 && (long)(rows + 1) * (columns + 1) > maxCells;
        if (truncated || rows == 0 || columns == 0)
        {
            for (int i = prefix; i < leftEnd; i++)
                Emit(left[i], string.Empty);
            for (int j = prefix; j < rightEnd; j++)
                Emit(string.Empty, right[j]);
        }
        else
        {
            // Retain the path only within the caller's explicit cell budget.
            var lcs = new int[rows + 1, columns + 1];
            for (int i = rows - 1; i >= 0; i--)
            {
                cancellationToken.ThrowIfCancellationRequested();
                for (int j = columns - 1; j >= 0; j--)
                    lcs[i, j] = left[prefix + i] == right[prefix + j]
                        ? lcs[i + 1, j + 1] + 1
                        : Math.Max(lcs[i + 1, j], lcs[i, j + 1]);
            }
            int x = 0;
            int y = 0;
            while (x < rows || y < columns)
            {
                if (x < rows && y < columns && left[prefix + x] == right[prefix + y])
                {
                    Emit(left[prefix + x++], right[prefix + y++]);
                }
                else if (x < rows && (y == columns || lcs[x + 1, y] >= lcs[x, y + 1]))
                {
                    Emit(left[prefix + x++], string.Empty);
                }
                else
                {
                    Emit(string.Empty, right[prefix + y++]);
                }
            }
        }
        for (int i = leftEnd; i < left.Length; i++)
            Emit(left[i], left[i]);
        Flush();
        return new DocxTextDiff(segments.AsReadOnly(), truncated);

        string[] Tokens(string text)
        {
            var tokens = new List<string>();
            foreach (string token in CjkDiffTokenizer.Tokenize(text))
            {
                cancellationToken.ThrowIfCancellationRequested();
                tokens.Add(token);
            }
            return tokens.ToArray();
        }

        void Emit(string oldPart, string newPart)
        {
            cancellationToken.ThrowIfCancellationRequested();
            bool isChange = oldPart != newPart;
            if (changed is not null && changed != isChange)
                Flush();
            changed = isChange;
            oldText.Append(oldPart);
            newText.Append(newPart);
        }

        void Flush()
        {
            if (oldText.Length == 0 && newText.Length == 0)
                return;
            segments.Add(new DocxTextSegment(oldText.ToString(), newText.ToString()));
            oldText.Clear();
            newText.Clear();
        }
    }
}
