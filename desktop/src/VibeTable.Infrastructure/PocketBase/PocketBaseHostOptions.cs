using System;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text.Json;

namespace VibeTable.Infrastructure.PocketBase;

/// <summary>Resolves the installed/development sidecar without provider flags.</summary>
public static class PocketBaseHostOptions
{
    public static readonly TimeSpan PackagedStartupTimeout = TimeSpan.FromSeconds(60);

    public static PocketBaseLaunchOptions Resolve(
        string baseDirectory,
        string localAppData)
    {
        string root = Path.GetFullPath(baseDirectory);
        string executable = LaunchPaths.ResolveSidecarBinary(root)
            ?? throw new FileNotFoundException(
                "The packaged PocketBase sidecar is missing.");
        string dataRoot = LaunchPaths.ResolveDataRoot(localAppData);
        string dataDirectory = Path.Combine(dataRoot, "pocketbase");
        LaunchPaths.EnsureInstallAndDataAreSeparated(
            Path.GetDirectoryName(executable) ?? root,
            dataDirectory);
        IdentityFile identity = ReadIdentity(root);
        bool developmentMode = !IsPackaged(root, executable);
        return new PocketBaseLaunchOptions
        {
            ExecutablePath = executable,
            WorkingDirectory = Path.GetDirectoryName(executable),
            DataDirectory = dataDirectory,
            LogPath = Path.Combine(dataRoot, "logs", "pocketbase.log"),
            DevelopmentMode = developmentMode,
            StartupTimeout = developmentMode
                ? PocketBaseLaunchOptions.DefaultStartupTimeout
                : PackagedStartupTimeout,
            ExpectedIdentity = new PocketBaseExpectedIdentity(
                "vibetable.sidecar.ready.v1",
                identity.ContractVersion,
                identity.PocketBaseVersion,
                identity.SchemaVersion,
                identity.MigrationHash),
        };
    }

    /// <summary>
    /// Re-roots writable sidecar state for an explicitly isolated test or
    /// source-development session while preserving binary identity and
    /// lifecycle policy.
    /// </summary>
    public static PocketBaseLaunchOptions WithRuntimeDataRoot(
        PocketBaseLaunchOptions options,
        string runtimeDataRoot,
        string? logPath = null)
    {
        ArgumentNullException.ThrowIfNull(options);
        ArgumentException.ThrowIfNullOrWhiteSpace(runtimeDataRoot);
        string dataRoot = Path.GetFullPath(runtimeDataRoot);
        string dataDirectory = Path.Combine(dataRoot, "pocketbase");
        LaunchPaths.EnsureInstallAndDataAreSeparated(
            options.WorkingDirectory
                ?? Path.GetDirectoryName(options.ExecutablePath)
                ?? AppContext.BaseDirectory,
            dataDirectory);
        return new PocketBaseLaunchOptions
        {
            ExecutablePath = options.ExecutablePath,
            WorkingDirectory = options.WorkingDirectory,
            DataDirectory = dataDirectory,
            LogPath = logPath is null
                ? Path.Combine(dataRoot, "logs", "pocketbase.log")
                : Path.GetFullPath(logPath),
            DevelopmentMode = options.DevelopmentMode,
            StartupTimeout = options.StartupTimeout,
            StopTimeout = options.StopTimeout,
            HealthPollInterval = options.HealthPollInterval,
            CrashRestartLimit = options.CrashRestartLimit,
            CrashRestartInitialDelay = options.CrashRestartInitialDelay,
            CrashRestartMaximumDelay = options.CrashRestartMaximumDelay,
            ExpectedIdentity = options.ExpectedIdentity,
            Environment = new Dictionary<string, string>(
                options.Environment,
                StringComparer.Ordinal),
        };
    }

    private static IdentityFile ReadIdentity(string baseDirectory)
    {
        string packaged = Path.Combine(
            baseDirectory,
            "resources",
            "sidecar",
            "build-info.json");
        if (File.Exists(packaged))
        {
            IdentityFile? parsed = JsonSerializer.Deserialize<IdentityFile>(
                File.ReadAllText(packaged),
                new JsonSerializerOptions(JsonSerializerDefaults.Web));
            if (parsed is null)
            {
                throw new InvalidDataException(
                    "The PocketBase build identity is invalid.");
            }
            parsed.Validate();
            return parsed;
        }

        string? repositoryRoot = LaunchPaths.FindRepositoryRoot(baseDirectory);
        string manifest = Path.Combine(
            repositoryRoot
                ?? throw new FileNotFoundException(
                    "The PocketBase build identity is missing."),
            "sidecar",
            "migrations",
            "manifest.json");
        if (!File.Exists(manifest))
        {
            throw new FileNotFoundException(
                "The PocketBase migration manifest is missing.");
        }
        using JsonDocument manifestDocument = JsonDocument.Parse(
            File.ReadAllText(manifest));
        if (manifestDocument.RootElement.ValueKind != JsonValueKind.Object
            || !manifestDocument.RootElement.TryGetProperty(
                "schemaVersion",
                out JsonElement schemaVersionNode)
            || schemaVersionNode.ValueKind != JsonValueKind.Number
            || !TryValidateSchemaVersion(
                schemaVersionNode.GetRawText(),
                out string? schemaVersion)
            || schemaVersion is null)
        {
            throw new InvalidDataException(
                "The PocketBase migration schema version is invalid.");
        }
        var identity = new IdentityFile(
            ContractVersion: "v1",
            PocketBaseVersion: "0.40.1",
            SchemaVersion: schemaVersion,
            MigrationHash: Convert.ToHexString(
                SHA256.HashData(File.ReadAllBytes(manifest))).ToLowerInvariant());
        identity.Validate();
        return identity;
    }

    private static bool IsPackaged(string baseDirectory, string executable) =>
        executable.StartsWith(
            Path.GetFullPath(
                Path.Combine(baseDirectory, "resources", "sidecar"))
                + Path.DirectorySeparatorChar,
            StringComparison.OrdinalIgnoreCase);

    private static bool TryValidateSchemaVersion(
        string? value,
        out string? canonical)
    {
        canonical = null;
        if (value is null
            || !int.TryParse(
                value,
                NumberStyles.None,
                CultureInfo.InvariantCulture,
                out int parsed)
            || parsed < 1)
        {
            return false;
        }
        canonical = parsed.ToString(CultureInfo.InvariantCulture);
        return string.Equals(value, canonical, StringComparison.Ordinal);
    }

    private static bool IsValidMigrationHash(string? value)
        => value is { Length: 64 }
            && value.All(character =>
                character is >= '0' and <= '9'
                    or >= 'a' and <= 'f'
                    or >= 'A' and <= 'F');

    private sealed record IdentityFile(
        string ContractVersion,
        string PocketBaseVersion,
        string SchemaVersion,
        string MigrationHash)
    {
        internal void Validate()
        {
            if (string.IsNullOrWhiteSpace(ContractVersion)
                || string.IsNullOrWhiteSpace(PocketBaseVersion)
                || !TryValidateSchemaVersion(SchemaVersion, out _)
                || !IsValidMigrationHash(MigrationHash))
            {
                throw new InvalidDataException(
                    "The PocketBase build identity is incomplete.");
            }
        }
    }
}
