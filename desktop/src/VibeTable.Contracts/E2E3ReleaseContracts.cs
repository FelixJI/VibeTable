using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// E2+E3 release/launcher/updater/production contracts. Mirrors
/// <c>backend.contracts.release</c> and <c>backend.contracts.production</c>.
/// </summary>

// --- E2 Release manifest ---

public sealed record ComponentHash(string Component, string Sha256);

public sealed record DirectusCompatibilityRange(
    string ApiRange,
    IReadOnlyList<string> RequiredCapabilities,
    string SchemaContract,
    string DashboardPanelManifestVersion,
    IReadOnlyList<string> AssetPresetKeys);

public sealed record ReleaseManifest(
    string ReleaseVersion,
    DirectusCompatibilityRange DirectusCompatibility,
    IReadOnlyList<ComponentHash> Components,
    string SbomPath,
    string Signature,
    string BuiltAt);

// --- E2 Launcher pointer ---

public sealed record LauncherPointer(
    string ActiveVersion,
    string VersionDirectory,
    string ManifestPath,
    string? PreviousVersion);

// --- E2 Updater ---

public static class UpdateStates
{
    public const string Idle = "idle";
    public const string Downloading = "downloading";
    public const string Verifying = "verifying";
    public const string Unpacking = "unpacking";
    public const string Swapping = "swapping";
    public const string Succeeded = "succeeded";
    public const string RollbackRequired = "rollback-required";
    public const string RolledBack = "rolled-back";
    public const string Failed = "failed";
}

public sealed record UpdateRequest(string TargetVersion, string ManifestUrl, bool Force);

public sealed record UpdateResult(string TargetVersion, string State, string? PreviousVersion, string? Error);

public sealed record RollbackResult(string RolledBackTo, string CurrentVersion, string Reason);

// --- E2 Compatibility preflight ---

public static class HealthStatuses
{
    public const string Compatible = "compatible";
    public const string Incompatible = "incompatible";
    public const string Offline = "offline";
    public const string Unknown = "unknown";
}

public sealed record CompatibilityReport(
    string Status,
    string? ServerVersion,
    IReadOnlyList<string> MissingCapabilities,
    bool SchemaContractMatch,
    string Message);

public sealed record HealthCheckResult(CompatibilityReport Compatible, string Timestamp);

// --- E3 Production bootstrap ---

public static class BootstrapStates
{
    public const string Planned = "planned";
    public const string Applying = "applying";
    public const string Applied = "applied";
    public const string Failed = "failed";
}

public sealed record ProductionBootstrapPlan(
    string DirectusVersion,
    string SchemaVersion,
    string CapabilityManifestPath,
    string BlueprintPath,
    IReadOnlyList<string> Extensions,
    IReadOnlyList<string> Flows,
    string State);

public sealed record BootstrapResult(
    string State,
    int CollectionsCreated,
    int PoliciesCreated,
    int RolesCreated,
    int ExtensionsDeployed,
    string? Error);

// --- E3 Acceptance ---

public sealed record AcceptanceCheck(string Category, string Name, bool Passed, string Evidence);

public sealed record AcceptanceReport(
    IReadOnlyList<AcceptanceCheck> Checks,
    bool P0Passed,
    bool P1Passed,
    bool DisabledFeaturesConfirmed,
    string Summary);

// --- E3 Operations + approval ---

public sealed record OperationsRunbook(
    IReadOnlyList<string> DeploymentSteps,
    string SchemaApplyCommand,
    string BackupStrategy,
    string RpoRto,
    IReadOnlyList<string> MonitoringEndpoints,
    string IncidentResponse);

public sealed record ReleaseApproval(
    string ReleaseVersion,
    bool Approved,
    string ApprovedAt,
    IReadOnlyList<string> Conditions,
    AcceptanceReport AcceptanceReport);
