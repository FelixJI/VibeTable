using System.Globalization;
using System.Text.RegularExpressions;

namespace VibeTable.Workspace.Domain;

/// <summary>
/// Strict UTC RFC3339 timestamp validation for workspace write boundaries.
/// </summary>
public static partial class UtcRfc3339Timestamp
{
    public static string Canonicalize(string value, string parameterName)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(value, parameterName);
        if (!UtcPattern().IsMatch(value)
            || !DateTimeOffset.TryParseExact(
                value,
                [
                    "yyyy-MM-dd'T'HH:mm:ss'Z'",
                    "yyyy-MM-dd'T'HH:mm:ss.FFFFFFF'Z'",
                    "yyyy-MM-dd'T'HH:mm:sszzz",
                    "yyyy-MM-dd'T'HH:mm:ss.FFFFFFFzzz",
                ],
                CultureInfo.InvariantCulture,
                DateTimeStyles.AssumeUniversal | DateTimeStyles.AdjustToUniversal,
                out DateTimeOffset parsed)
            || parsed.Offset != TimeSpan.Zero)
        {
            throw new ArgumentException(
                "timestamp must be an RFC3339 UTC value",
                parameterName);
        }

        return parsed.UtcDateTime.ToString(
            "yyyy-MM-dd'T'HH:mm:ss.FFFFFFF'Z'",
            CultureInfo.InvariantCulture);
    }

    [GeneratedRegex(
        "^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}(?:\\.\\d{1,7})?(?:Z|\\+00:00)$",
        RegexOptions.CultureInvariant)]
    private static partial Regex UtcPattern();
}
