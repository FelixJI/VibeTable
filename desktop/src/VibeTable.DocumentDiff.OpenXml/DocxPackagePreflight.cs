using VibeTable.Workspace.Diff;

namespace VibeTable.DocumentDiff.OpenXml;

internal static class DocxPackagePreflight
{
    // Independent limits for non-XML payloads before a whole-document library
    // can expand them. XML retains its existing, stricter per-part/total limits.
    internal const long MaxBinaryPartBytes = 64L * 1024 * 1024;
    internal const long MaxExpandedPackageBytes = 256L * 1024 * 1024;

    public static async Task ValidateAsync(DocumentContentSource content, CancellationToken cancellationToken = default,
        Action<string, string>? validateElement = null)
    {
        using var reader = await DocxPackageReader.OpenAsync(content, cancellationToken).ConfigureAwait(false);
        reader.ValidateAllParts(cancellationToken, validateElement);
    }
}
