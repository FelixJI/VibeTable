using System;
using System.Collections.Generic;

namespace VibeTable.Infrastructure.PocketBase;

/// <summary>
/// Immutable launch policy for the private PocketBase sidecar.
/// </summary>
public sealed class PocketBaseLaunchOptions
{
    public static readonly TimeSpan DefaultStartupTimeout = TimeSpan.FromSeconds(30);
    public static readonly TimeSpan DefaultStopTimeout = TimeSpan.FromSeconds(10);
    public static readonly TimeSpan DefaultHealthPollInterval = TimeSpan.FromMilliseconds(100);
    public static readonly TimeSpan DefaultCrashRestartInitialDelay =
        TimeSpan.FromMilliseconds(250);
    public static readonly TimeSpan DefaultCrashRestartMaximumDelay =
        TimeSpan.FromSeconds(5);

    public string ExecutablePath { get; init; } = string.Empty;
    public string DataDirectory { get; init; } = string.Empty;
    public string? WorkingDirectory { get; init; }
    public string? LogPath { get; init; }
    public bool DevelopmentMode { get; init; }
    public TimeSpan StartupTimeout { get; init; } = DefaultStartupTimeout;
    public TimeSpan StopTimeout { get; init; } = DefaultStopTimeout;
    public TimeSpan HealthPollInterval { get; init; } = DefaultHealthPollInterval;
    public int CrashRestartLimit { get; init; } = 3;
    public TimeSpan CrashRestartInitialDelay { get; init; } =
        DefaultCrashRestartInitialDelay;
    public TimeSpan CrashRestartMaximumDelay { get; init; } =
        DefaultCrashRestartMaximumDelay;
    public PocketBaseExpectedIdentity? ExpectedIdentity { get; init; }
    public IDictionary<string, string> Environment { get; init; } =
        new Dictionary<string, string>(StringComparer.Ordinal);

    internal void Validate()
    {
        if (string.IsNullOrWhiteSpace(ExecutablePath))
        {
            throw new ArgumentException("PocketBase executable path must be non-empty.");
        }
        if (string.IsNullOrWhiteSpace(DataDirectory))
        {
            throw new ArgumentException("PocketBase data directory must be non-empty.");
        }
        if (StartupTimeout <= TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(
                nameof(StartupTimeout), "Startup timeout must be positive.");
        }
        if (StopTimeout <= TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(
                nameof(StopTimeout), "Stop timeout must be positive.");
        }
        if (HealthPollInterval < TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(
                nameof(HealthPollInterval), "Health poll interval cannot be negative.");
        }
        if (CrashRestartLimit < 0)
        {
            throw new ArgumentOutOfRangeException(
                nameof(CrashRestartLimit), "Crash restart limit cannot be negative.");
        }
        if (CrashRestartInitialDelay < TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(
                nameof(CrashRestartInitialDelay),
                "Crash restart initial delay cannot be negative.");
        }
        if (CrashRestartMaximumDelay < CrashRestartInitialDelay)
        {
            throw new ArgumentOutOfRangeException(
                nameof(CrashRestartMaximumDelay),
                "Crash restart maximum delay cannot be less than the initial delay.");
        }
        if (ExpectedIdentity is null)
        {
            throw new ArgumentException(
                "Expected PocketBase package identity must be supplied.");
        }
        ExpectedIdentity.Validate();
    }
}

/// <summary>
/// Package identity compiled into the desktop distribution. It is injected by
/// composition so the supervisor does not silently accept an arbitrary sidecar.
/// </summary>
public sealed record PocketBaseExpectedIdentity(
    string ReadyContract,
    string ContractVersion,
    string PocketBaseVersion,
    string SchemaVersion,
    string MigrationHash)
{
    internal void Validate()
    {
        if (string.IsNullOrWhiteSpace(ReadyContract)
            || string.IsNullOrWhiteSpace(ContractVersion)
            || string.IsNullOrWhiteSpace(PocketBaseVersion)
            || string.IsNullOrWhiteSpace(SchemaVersion)
            || string.IsNullOrWhiteSpace(MigrationHash))
        {
            throw new ArgumentException(
                "Expected PocketBase package identity fields must be non-empty.");
        }
    }
}
