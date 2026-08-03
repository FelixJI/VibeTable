using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Release gate backed by the packaged provider-support matrix and a real
/// durable write/rename probe. Cloud and user-marked-sync classification can
/// only be supplied explicitly by a trusted native source.
/// </summary>
public sealed class WorkspaceProviderPolicy
{
    private static readonly IReadOnlyDictionary<string, WorkspaceStorageKind>
        ProviderKinds = new Dictionary<string, WorkspaceStorageKind>(
            StringComparer.Ordinal)
        {
            ["fixed"] = WorkspaceStorageKind.Fixed,
            ["network"] = WorkspaceStorageKind.Network,
            ["registeredCloud"] = WorkspaceStorageKind.RegisteredCloud,
            ["userMarkedSync"] = WorkspaceStorageKind.UserMarkedSync,
            ["removable"] = WorkspaceStorageKind.Removable,
        };

    private readonly IReadOnlyDictionary<
        WorkspaceStorageKind,
        ProviderRule> _rules;
    private readonly Func<
        string,
        bool,
        IEnumerable<string>?,
        WorkspaceStorageObservation> _probe;

    private WorkspaceProviderPolicy(
        IReadOnlyDictionary<WorkspaceStorageKind, ProviderRule> rules,
        Func<
            string,
            bool,
            IEnumerable<string>?,
            WorkspaceStorageObservation>? probe = null)
    {
        _rules = rules;
        var storageProbe = new WorkspaceStorageProbe();
        _probe = probe ?? storageProbe.Probe;
    }

    public bool MirroredCreationEnabled => _rules.Any(rule =>
        rule.Key != WorkspaceStorageKind.Fixed && rule.Value.CreationEnabled);

    public static WorkspaceProviderPolicy Load(string baseDirectory)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(baseDirectory);
        string? path = FindPolicy(baseDirectory);
        if (path is null)
            throw new WorkspaceRegistryException(
                "workspace.provider_policy_missing",
                "The packaged provider support policy is missing.");
        using JsonDocument document = JsonDocument.Parse(
            File.ReadAllText(path));
        JsonElement root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object
            || root.GetProperty("contractVersion").GetString() != "2.0"
            || !root.TryGetProperty("providers", out JsonElement providers)
            || providers.ValueKind != JsonValueKind.Object)
        {
            throw InvalidPolicy();
        }

        var rules = new Dictionary<WorkspaceStorageKind, ProviderRule>();
        foreach ((string providerName, WorkspaceStorageKind kind)
                 in ProviderKinds)
        {
            if (!providers.TryGetProperty(
                    providerName,
                    out JsonElement provider)
                || provider.ValueKind != JsonValueKind.Object
                || !provider.TryGetProperty(
                    "creation",
                    out JsonElement creation)
                || creation.ValueKind != JsonValueKind.String
                || !provider.TryGetProperty(
                    "coordinationStrength",
                    out JsonElement coordination)
                || coordination.ValueKind != JsonValueKind.String)
            {
                throw InvalidPolicy();
            }
            WorkspaceCoordinationStrength expected = kind ==
                WorkspaceStorageKind.Fixed
                    ? WorkspaceCoordinationStrength.Strong
                    : WorkspaceCoordinationStrength.Advisory;
            string expectedName = expected ==
                WorkspaceCoordinationStrength.Strong
                    ? "strong"
                    : "advisory";
            if (coordination.GetString() != expectedName)
                throw InvalidPolicy();
            if (kind == WorkspaceStorageKind.Network
                && (!provider.TryGetProperty(
                        "protocol",
                        out JsonElement protocol)
                    || protocol.ValueKind != JsonValueKind.String
                    || protocol.GetString() != "smb"))
            {
                throw InvalidPolicy();
            }
            rules.Add(
                kind,
                new ProviderRule(
                    creation.GetString() == "enabled",
                    expected));
        }
        if (providers.EnumerateObject().Any(
                provider => !ProviderKinds.ContainsKey(provider.Name)))
            throw InvalidPolicy();
        return new WorkspaceProviderPolicy(rules);
    }

    internal static WorkspaceProviderPolicy CreateForTests(
        IReadOnlyDictionary<WorkspaceStorageKind, bool> enabled,
        Func<
            string,
            bool,
            IEnumerable<string>?,
            WorkspaceStorageObservation> probe)
        => new(
            Enum.GetValues<WorkspaceStorageKind>()
                .ToDictionary(
                    kind => kind,
                    kind => new ProviderRule(
                        enabled.TryGetValue(kind, out bool allowed) && allowed,
                        kind == WorkspaceStorageKind.Fixed
                            ? WorkspaceCoordinationStrength.Strong
                            : WorkspaceCoordinationStrength.Advisory)),
            probe);

    public WorkspaceStorageObservation ProbeAndEnsureSupported(
        string root,
        bool userMarkedSync = false,
        IEnumerable<string>? registeredCloudRoots = null)
        => ProbeAndEnsureSupportedCore(
            root,
            storageMode: null,
            userMarkedSync,
            registeredCloudRoots);

    public WorkspaceStorageObservation ProbeAndEnsureSupported(
        string root,
        WorkspaceStorageMode storageMode,
        bool userMarkedSync = false,
        IEnumerable<string>? registeredCloudRoots = null)
        => ProbeAndEnsureSupportedCore(
            root,
            storageMode,
            userMarkedSync,
            registeredCloudRoots);

    private WorkspaceStorageObservation ProbeAndEnsureSupportedCore(
        string root,
        WorkspaceStorageMode? storageMode,
        bool userMarkedSync,
        IEnumerable<string>? registeredCloudRoots)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(root);
        WorkspaceStorageObservation observation = _probe(
            Path.GetFullPath(root),
            userMarkedSync,
            registeredCloudRoots);
        if (storageMode == WorkspaceStorageMode.Direct
            && (observation.StorageKind != WorkspaceStorageKind.Fixed
                || observation.CoordinationStrength !=
                WorkspaceCoordinationStrength.Strong))
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_requires_mirrored",
                "This non-fixed location requires mirrored storage mode.");
        }
        if (observation.StorageKind == WorkspaceStorageKind.Network
            && observation.RemoteProtocol != WorkspaceRemoteProtocol.Smb)
        {
            throw new WorkspaceRegistryException(
                "workspace.network_protocol_unsupported",
                "Only SMB network locations are supported for mirrored workspaces.");
        }
        if (!_rules.TryGetValue(
                observation.StorageKind,
                out ProviderRule? rule)
            || !rule.CreationEnabled)
        {
            throw new WorkspaceRegistryException(
                "workspace.provider_blocked",
                "This directory type is not enabled for mirrored workspaces in this build.");
        }
        if (rule.CoordinationStrength != observation.CoordinationStrength)
            throw InvalidPolicy();
        return observation;
    }

    public WorkspaceStorageObservation ProbeCreateTargetAndEnsureSupported(
        string root,
        bool userMarkedSync = false)
        => ProbeCreateTargetAndEnsureSupportedCore(
            root,
            storageMode: null,
            userMarkedSync);

    public WorkspaceStorageObservation ProbeCreateTargetAndEnsureSupported(
        string root,
        WorkspaceStorageMode storageMode,
        bool userMarkedSync = false)
        => ProbeCreateTargetAndEnsureSupportedCore(
            root,
            storageMode,
            userMarkedSync);

    private WorkspaceStorageObservation ProbeCreateTargetAndEnsureSupportedCore(
        string root,
        WorkspaceStorageMode? storageMode,
        bool userMarkedSync)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(root);
        string fullPath = Path.GetFullPath(root);
        bool created = !Directory.Exists(fullPath);
        Directory.CreateDirectory(fullPath);
        try
        {
            return storageMode is null
                ? ProbeAndEnsureSupported(fullPath, userMarkedSync)
                : ProbeAndEnsureSupported(
                    fullPath,
                    storageMode.Value,
                    userMarkedSync);
        }
        catch
        {
            if (created)
                TryDeleteEmptyCreateTarget(fullPath);
            throw;
        }
    }

    private static void TryDeleteEmptyCreateTarget(string fullPath)
    {
        try
        {
            if (Directory.Exists(fullPath)
                && !Directory.EnumerateFileSystemEntries(fullPath).Any())
            {
                Directory.Delete(fullPath);
            }
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // Preserve the authoritative probe/policy failure. A concurrent
            // filesystem actor may touch the directory between enumeration
            // and deletion; rollback cleanup must never replace that error.
        }
    }

    private static WorkspaceRegistryException InvalidPolicy()
        => new(
            "workspace.provider_policy_invalid",
            "The packaged provider support policy is invalid.");

    private static string? FindPolicy(string baseDirectory)
    {
        string start = Path.GetFullPath(baseDirectory);
        for (DirectoryInfo? current = new(start);
             current is not null;
             current = current.Parent)
        {
            foreach (string relativeRoot in new[]
                     {
                         Path.Combine("resources", "contracts"),
                         "contracts",
                     })
            {
                string candidate = Path.Combine(
                    current.FullName,
                    relativeRoot,
                    "v2",
                    "provider-support.json");
                if (File.Exists(candidate))
                    return candidate;
            }
        }
        return null;
    }

    private sealed record ProviderRule(
        bool CreationEnabled,
        WorkspaceCoordinationStrength CoordinationStrength);
}
