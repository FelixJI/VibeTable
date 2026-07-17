using System;
using System.Collections.Generic;
using System.Linq;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Shared filter that distinguishes user-created Directus collections from
/// Directus system collections and VibeTable's own system collections.
/// Extracted verbatim from the former <c>TableManagementWindow.IsUserTable</c>
/// so both the web-bridge dispatcher and (transitively) the legacy window
/// share one implementation.
/// </summary>
public static class DirectusCollectionFilter
{
    public static bool IsUserTable(string? collection)
    {
        if (string.IsNullOrWhiteSpace(collection))
        {
            return false;
        }
        if (collection.StartsWith("directus_", StringComparison.Ordinal))
        {
            return false;
        }
        if (collection.StartsWith("vibetable_", StringComparison.Ordinal))
        {
            return false;
        }
        return true;
    }

    /// <summary>Filters a raw collection list to user tables, sorted case-insensitively.</summary>
    public static IReadOnlyList<string> FilterUserTables(IEnumerable<string> collections)
        => collections
            .Where(IsUserTable)
            .OrderBy(c => c, StringComparer.OrdinalIgnoreCase)
            .ToList();
}
