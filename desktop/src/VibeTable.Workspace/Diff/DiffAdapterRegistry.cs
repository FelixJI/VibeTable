using System.IO;

namespace VibeTable.Workspace.Diff;

/// <summary>
/// Registry that selects the appropriate diff adapter for a file based on
/// its MIME type and extension. Falls back to <see cref="BinaryDiffAdapter"/>
/// for unknown formats.
/// </summary>
public sealed class DiffAdapterRegistry
{
    private readonly List<IDocumentDiffAdapter> _adapters;
    private readonly BinaryDiffAdapter _fallback = new();

    public DiffAdapterRegistry(IEnumerable<IDocumentDiffAdapter>? extraAdapters = null)
    {
        _adapters = [new TextDiffAdapter()];
        if (extraAdapters is not null)
            _adapters.AddRange(extraAdapters);
    }

    /// <summary>
    /// Select the best adapter for a file. Checks MIME type first, then
    /// extension. Falls back to binary hash comparison.
    /// </summary>
    public IDocumentDiffAdapter Select(string fileName, string? mimeType = null)
    {
        var ext = Path.GetExtension(fileName).ToLowerInvariant();

        // Check MIME type first.
        if (!string.IsNullOrEmpty(mimeType))
        {
            foreach (var adapter in _adapters)
            {
                if (adapter.SupportedMimeTypes.Contains(mimeType))
                    return adapter;
            }
        }

        // Check extension.
        foreach (var adapter in _adapters)
        {
            if (adapter.SupportedExtensions.Contains(ext))
                return adapter;
        }

        // Fallback: binary hash comparison.
        return _fallback;
    }

    /// <summary>
    /// Compute a diff between two files, selecting the adapter automatically.
    /// </summary>
    public DiffResult Diff(string pathA, string pathB, string? mimeType = null)
    {
        var fileName = Path.GetFileName(pathA);
        var adapter = Select(fileName, mimeType);
        return adapter.Diff(pathA, pathB);
    }
}
