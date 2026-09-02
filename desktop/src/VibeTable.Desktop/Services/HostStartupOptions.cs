using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Parsed command-line options for host readiness diagnostics.
/// Unknown flags are ignored for forward compatibility.
/// </summary>
public sealed class HostStartupOptions
{
    private const string SelfUpdateReadinessDirectoryName = "self-update-readiness";
    private const string SelfUpdateUpdatedControlsDirectoryName =
        "self-update-updated-controls";
    private const string SelfUpdateHealthTimeoutHoldRequestName =
        "self-update-health-timeout-hold.request";
    private const string SelfUpdateCrashRequestName = "self-update-crash.request";

    public bool TestMode { get; set; }

    private bool SelfUpdateSmoke { get; set; }

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

    /// <summary>
    /// Enables the real tray close/exit and auto-start visibility policies for
    /// packaged-host black-box tests. Ignored unless <see cref="TestMode"/> is
    /// also present so production launches cannot acquire a file control seam.
    /// </summary>
    public bool TestModeTrayLifecycle { get; set; }

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
                case "--self-update-smoke":
                    options.SelfUpdateSmoke = true;
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
                case "--test-mode-tray-lifecycle":
                    options.TestModeTrayLifecycle = true;
                    break;
            }
        }

        if (!options.TestMode)
        {
            options.E2eControlsDir = null;
            options.TestModeTrayLifecycle = false;
            options.SelfUpdateSmoke = false;
        }
        return options;
    }

    internal bool TryConsumeSelfUpdateHealthTimeoutHold(
        Action<string, string, string>? pathGuard = null,
        Action<string>? cleanupClaim = null) => TryConsumeSelfUpdateRequest(
            SelfUpdateHealthTimeoutHoldRequestName,
            pathGuard,
            cleanupClaim);

    internal void CrashSelfUpdateIfRequested(Action? crashAction = null)
    {
        if (!TryConsumeSelfUpdateRequest(SelfUpdateCrashRequestName))
        {
            return;
        }
        string evidence = Path.Combine(E2eControlsDir!, "self-update-crash.json");
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(evidence);
        using (Process process = Process.GetCurrentProcess())
        using (var stream = new FileStream(evidence, FileMode.CreateNew, FileAccess.Write))
        {
            JsonSerializer.Serialize(stream, new
            {
                action = "Environment.FailFast",
                processId = process.Id,
                startedAtUtc = process.StartTime.ToUniversalTime(),
            });
        }
        // This runs outside health failure handling: no settlement or graceful
        // host cleanup may disguise the missing health result from the watchdog.
        if (crashAction is null)
        {
            Environment.FailFast("VibeTable self-update crash smoke");
        }
        crashAction();
        throw new InvalidOperationException("Self-update crash action unexpectedly returned.");
    }

    private bool TryConsumeSelfUpdateRequest(
        string requestName,
        Action<string, string, string>? pathGuard = null,
        Action<string>? cleanupClaim = null)
    {
        if (!TestMode
            || !SelfUpdateSmoke
            || string.IsNullOrWhiteSpace(ReadinessDir)
            || string.IsNullOrWhiteSpace(E2eControlsDir))
        {
            return false;
        }
        try
        {
            var readiness = new DirectoryInfo(Path.GetFullPath(ReadinessDir));
            var controls = new DirectoryInfo(Path.GetFullPath(E2eControlsDir));
            if (!Directory.Exists(readiness.FullName)
                || !Directory.Exists(controls.FullName)
                || !string.Equals(
                    readiness.Name,
                    SelfUpdateReadinessDirectoryName,
                    StringComparison.OrdinalIgnoreCase)
                || !string.Equals(
                    controls.Name,
                    SelfUpdateUpdatedControlsDirectoryName,
                    StringComparison.OrdinalIgnoreCase)
                || readiness.Parent is null
                || controls.Parent is null
                || !string.Equals(
                    readiness.Parent.FullName,
                    controls.Parent.FullName,
                    StringComparison.OrdinalIgnoreCase))
            {
                return false;
            }
            string request = Path.Combine(
                controls.FullName,
                requestName);
            (pathGuard ?? RejectSelfUpdateRequestReparsePoints)(
                readiness.FullName,
                controls.FullName,
                request);
            string claimed = $"{request}.claimed-{Guid.NewGuid():N}";
            File.Move(request, claimed, overwrite: false);
            try
            {
                (cleanupClaim ?? File.Delete)(claimed);
            }
            catch (Exception exception) when (
                exception is IOException
                    or UnauthorizedAccessException
                    or ArgumentException
                    or NotSupportedException
                    or System.Security.SecurityException)
            {
                // Moving the one-shot request is the durable armed boundary.
                // A unique claim cannot block a later request, and cleanup is
                // best-effort once the smoke action has been armed.
            }
            return true;
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or ArgumentException
                or NotSupportedException
                or System.Security.SecurityException
                or ReleaseUpdateException)
        {
            return false;
        }
    }

    private static void RejectSelfUpdateRequestReparsePoints(
        string readiness,
        string controls,
        string request)
    {
        UpdateProcessCommand.RejectReparsePointChainsToVolumeRoot(
            readiness,
            controls,
            request);
    }

    public static HostStartupOptions Current()
    {
        string[] args = Environment.GetCommandLineArgs();
        return Parse(args.Length > 1 ? args[1..] : []);
    }
}
