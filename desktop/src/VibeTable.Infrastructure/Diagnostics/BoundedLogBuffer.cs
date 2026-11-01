using System.Text;

namespace VibeTable.Infrastructure.Diagnostics;

/// <summary>
/// Tail-bounded line buffer for in-memory diagnostic logs. Long-running
/// processes keep only the newest slice so the diagnostics bundle read stays
/// bounded; trimming always lands on a line boundary and drops the oldest
/// half so the cost amortizes across appends.
/// </summary>
internal static class BoundedLogBuffer
{
    /// <summary>128k chars ≈ 256 KB of UTF-16 tail retention.</summary>
    public const int MaximumCharacters = 128 * 1024;

    public static void AppendLine(StringBuilder buffer, string line)
    {
        buffer.AppendLine(line);
        if (buffer.Length <= MaximumCharacters)
        {
            return;
        }
        int cut = buffer.Length - MaximumCharacters / 2;
        while (cut < buffer.Length && buffer[cut] != '\n')
        {
            cut++;
        }
        buffer.Remove(0, Math.Min(cut + 1, buffer.Length));
    }
}
