using System;

namespace VibeTable.Desktop.Services;

/// <summary>Runtime gate for the native dashboard surface.</summary>
public sealed record DashboardFeatureOptions(bool Enabled)
{
    public const string EnvironmentVariable = "VIBETABLE_DASHBOARDS_ENABLED";

    public static DashboardFeatureOptions Disabled { get; } = new(false);
    public static DashboardFeatureOptions EnabledForTests { get; } = new(true);

    public static DashboardFeatureOptions FromEnvironment(
        Func<string, string?>? readVariable = null)
    {
        string? raw = (readVariable ?? Environment.GetEnvironmentVariable)(EnvironmentVariable);
        bool enabled = raw is not null && raw.Trim().ToLowerInvariant() is
            "1" or "true" or "yes" or "on";
        return new DashboardFeatureOptions(enabled);
    }
}
