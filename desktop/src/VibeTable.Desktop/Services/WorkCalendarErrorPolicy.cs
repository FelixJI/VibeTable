using System;

namespace VibeTable.Desktop.Services;

internal static class WorkCalendarErrorPolicy
{
    // Both HTTP decoding and renderer projection use this closed domain list.
    internal static bool Accepts(string code) =>
        !code.StartsWith("settings.calendar.", StringComparison.Ordinal)
        || code is "settings.calendar.invalid" or "settings.calendar.revision_conflict"
            or "settings.calendar.corrupt";
}
