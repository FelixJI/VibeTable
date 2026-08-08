using System;
using System.Collections.Generic;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Parsed command-line options for host readiness diagnostics.
/// Unknown flags are ignored for forward compatibility.
/// </summary>
public sealed class HostStartupOptions
{
    public bool TestMode { get; set; }

    public string? ReadinessDir { get; set; }

    /// <summary>
    /// Test-mode-only directory containing fixed, named E2E control files.
    /// The host never treats its contents as commands or executable scripts.
    /// </summary>
    public string? E2eControlsDir { get; set; }

    /// <summary>
    /// Source-layout-only runtime root supplied by <c>scripts/dev.py</c>.
    /// The composition root rejects this option when the packaged sidecar is
    /// selected, so an installed build cannot redirect product data with it.
    /// </summary>
    public string? DevelopmentDataRoot { get; set; }

    /// <summary>
    /// Set when the host was launched by the Windows startup registry value
    /// (<see cref="WindowsStartupRegistration"/> appends <c>--autostart</c>).
    /// Drives startup-only behaviors: stale startup-value reconciliation and,
    /// when the user also minimizes to tray, a silent tray launch.
    /// </summary>
    public bool AutoStart { get; set; }

    public static HostStartupOptions Parse(IReadOnlyList<string>? args)
    {
        var options = new HostStartupOptions();
        if (args is null)
        {
            return options;
        }

        for (int index = 0; index < args.Count; index++)
        {
            switch (args[index])
            {
                case "--test-mode":
                    options.TestMode = true;
                    break;
                case "--readiness-dir" when index + 1 < args.Count:
                    options.ReadinessDir = args[++index];
                    break;
                case "--e2e-controls-dir" when index + 1 < args.Count:
                    options.E2eControlsDir = args[++index];
                    break;
                case "--dev-data-root" when index + 1 < args.Count:
                    options.DevelopmentDataRoot = args[++index];
                    break;
                case "--autostart":
                    options.AutoStart = true;
                    break;
            }
        }

        if (!options.TestMode)
        {
            options.E2eControlsDir = null;
        }
        return options;
    }

    public static HostStartupOptions Current()
    {
        string[] args = Environment.GetCommandLineArgs();
        return Parse(args.Length > 1 ? args[1..] : []);
    }
}
