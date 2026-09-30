using System.Globalization;
using System.Text.RegularExpressions;

namespace VibeTable.DocumentDiff.OpenXml;

internal static partial class CjkDiffTokenizer
{
    public static IEnumerable<string> Tokenize(string text)
    {
        ArgumentNullException.ThrowIfNull(text);
        int[] boundaries = StringInfo.ParseCombiningCharacters(text);
        for (int index = 0; index < boundaries.Length;)
        {
            int start = boundaries[index];
            int next = index + 1;
            Match match = WordOrNumber().Match(text, start);
            if (match.Success)
            {
                int end = start + match.Length;
                int boundary = end == text.Length
                    ? boundaries.Length
                    : Array.BinarySearch(boundaries, end);
                // A word/number must never split a keycap, combining mark or emoji sequence.
                // Consume the complete prefix instead of rescanning it at every character.
                if (boundary < 0)
                    boundary = ~boundary - 1;
                if (boundary > index)
                    next = boundary;
            }
            int limit = next == boundaries.Length ? text.Length : boundaries[next];
            yield return text[start..limit];
            index = next;
        }
    }

    [GeneratedRegex(@"\G(?:[0-9]{4}年[0-9]{1,2}月[0-9]{1,2}日|[0-9]{4}(?:-[0-9]{2}){2}|"
        + @"(?:[0-9]{1,3}(?:,[0-9]{3})+(?![0-9])|[0-9]+)(?:\.[0-9]+)?(?:万元|亿元|元|年|月|日|[%％])?|"
        + @"[A-Za-z\u00C0-\u00D6\u00D8-\u00F6\u00F8-\u024F][A-Za-z\u00C0-\u00D6\u00D8-\u00F6\u00F8-\u024F\p{M}]*)",
        RegexOptions.CultureInvariant, 1000)]
    private static partial Regex WordOrNumber();
}
