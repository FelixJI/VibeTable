using System;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Runtime producer gate for writing the v1 autoDate role metadata.
/// Readers and diagnostics remain available when this switch is disabled.
/// </summary>
public sealed record AutoDateFeatureOptions(bool Enabled)
{
    public const string EnvironmentVariable = "VIBETABLE_AUTODATE_FIELDS_ENABLED";

    public static AutoDateFeatureOptions Disabled { get; } = new(false);
    public static AutoDateFeatureOptions EnabledForTests { get; } = new(true);

    public static AutoDateFeatureOptions FromEnvironment(
        Func<string, string?>? readVariable = null)
    {
        string? raw = (readVariable ?? Environment.GetEnvironmentVariable)(EnvironmentVariable);
        bool enabled = raw is null || raw.Trim().ToLowerInvariant() is
            "1" or "true" or "yes" or "on";
        return new AutoDateFeatureOptions(enabled);
    }
}
