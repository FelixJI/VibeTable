using System.IO;
using VibeTable.Infrastructure;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Resolves the active product data root for this launch. Explicit test and
/// development overrides win over the portable/standard mode marker so QA
/// never touches real user data and migrations never run for redirected
/// roots.
/// </summary>
internal static class ProductDataRoot
{
    public static string Resolve(HostStartupOptions startup)
    {
        ArgumentNullException.ThrowIfNull(startup);
        if (startup.TestMode
            && !string.IsNullOrWhiteSpace(startup.ReadinessDir))
        {
            return Path.Combine(
                Path.GetFullPath(startup.ReadinessDir!),
                "local-data");
        }
        if (!startup.TestMode
            && !string.IsNullOrWhiteSpace(startup.DevelopmentDataRoot))
        {
            return Path.GetFullPath(startup.DevelopmentDataRoot!);
        }
        return LaunchPaths.ResolveProductDataRoot(
            AppContext.BaseDirectory,
            Environment.GetFolderPath(
                Environment.SpecialFolder.LocalApplicationData));
    }

    /// <summary>Unified log directory shared by desktop, sidecar, backend.</summary>
    public static string LogsDirectory(HostStartupOptions startup) =>
        Path.Combine(Resolve(startup), "logs");
}
