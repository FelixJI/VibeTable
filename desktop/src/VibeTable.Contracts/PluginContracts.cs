using System.Text.Json;
using System.Text.Json.Serialization;

namespace VibeTable.Contracts;

/// <summary>Wire contract identifiers shared by Web, WPF and Python.</summary>
public static class PluginContractVersions
{
    public const string Result = "vibetable.plugin-result.v1";
    public const string Event = "vibetable.plugin-event.v1";
    public const string Surface = "vibetable.plugin-surface.v1";
    public const string Theme = "vibetable.plugin-theme.v1";
}

public static class PluginRisk
{
    public const string Read = "read";
    public const string Write = "write";
    public const string Destructive = "destructive";
}

public static class PluginSurfaceEvents
{
    public const string Ready = "ready";
    public const string Close = "close";
    public const string Action = "action";
}

public static class PluginSurfaceMessages
{
    public const string ThemeChanged = "themeChanged";
}

public static class PluginThemeModes
{
    public const string Light = "light";
    public const string Dark = "dark";
}

public static class PluginDensityModes
{
    public const string Comfortable = "comfortable";
    public const string Compact = "compact";
}

public sealed record PluginSurfaceThemeSnapshot(
    string Contract,
    string Mode,
    string Locale,
    string Density,
    IReadOnlyDictionary<string, string> Variables);

public sealed record PluginSurfaceEvent(
    string Contract,
    string SurfaceToken,
    string Event,
    JsonElement Payload);

public sealed record PluginSurfaceHostMessage(
    string Contract,
    string SurfaceToken,
    string Event,
    PluginSurfaceThemeSnapshot Payload);

public sealed record PluginEventEnvelope(
    string Contract,
    string EventType,
    string ProjectKey,
    string EntityId,
    int Revision,
    JsonElement Snapshot);

// Closed Web/WPF/Python use-case parameters. There is deliberately no generic
// method-name or arbitrary RPC parameter contract.
public sealed record PluginCatalogListParams(string ProjectKey);
public sealed record PluginAuditListParams(string ProjectKey, string PluginId);
public sealed record PluginInspectInstallParams(
    string ProjectKey,
    string ProjectRevision,
    string SourceLocation);
public sealed record PluginCommitInstallParams(string PlanId, string ProjectRevision);
public sealed record PluginListExternalFlowCandidatesParams(
    string ProjectKey,
    string PluginId,
    string LogicalFlowId);
public sealed record PluginBindExternalFlowParams(
    string ProjectKey,
    string PluginId,
    string LogicalFlowId,
    string DirectusFlowUuid,
    bool AcceptsUnknownSideEffects);
public sealed record PluginSetEnabledParams(
    string ProjectKey,
    string PluginId,
    bool Enabled);
public sealed record PluginUpgradeParams(
    string ProjectKey,
    string PluginId,
    string PlanId,
    string ProjectRevision);
public sealed record PluginRollbackParams(string ProjectKey, string PluginId);
public sealed record PluginResolveDriftParams(
    string ProjectKey,
    string PluginId,
    string LogicalFlowId,
    string Strategy);
public sealed record PluginUninstallParams(
    string ProjectKey,
    string PluginId,
    bool CleanupPrivateSettings = false);
public sealed record PluginRuntimeCommandContext(
    string Contract,
    string ProjectKey,
    string? Collection,
    IReadOnlyList<JsonElement> SelectedKeys,
    JsonElement? QuerySnapshot,
    string Locale,
    string Theme,
    string Density,
    JsonElement User,
    string HostVersion);
public sealed record PluginDescribeActionParams(
    string ProjectKey,
    string PluginId,
    string ActionId,
    PluginRuntimeCommandContext Context);
public sealed record PluginStartActionParams(
    string ProjectKey,
    string PluginId,
    string ActionId,
    PluginRuntimeCommandContext Context,
    JsonElement Input);
public sealed record PluginResolveInteractionParams(
    string RunId,
    string InteractionId,
    string Decision);
public sealed record PluginResolveFileParams(string RequestId, string? SelectedPath);
public sealed record PluginTaskParams(string TaskId);
public sealed record PluginSurfaceAcceptance(bool Accepted);

// Python execution/interaction wire fixtures. These mirror the domain RPC
// contracts and remain separate from the smaller Web-facing presentation DTOs.
public sealed record PluginRuntimeMetric(string Label, JsonElement Value);

public sealed record PluginRuntimeResult(
    string Contract,
    string Status,
    string Summary,
    IReadOnlyList<PluginRuntimeMetric> Metrics,
    JsonElement? Table,
    IReadOnlyList<JsonElement> Artifacts,
    JsonElement? Refresh,
    IReadOnlyList<string> Warnings);

public sealed record PluginRuntimeConfirmationPreview(
    IReadOnlyList<IReadOnlyDictionary<string, JsonElement>> Summary,
    IReadOnlyList<IReadOnlyDictionary<string, JsonElement>> SampleRows,
    int AffectedCount,
    IReadOnlyList<string> Warnings);

public sealed record PluginRuntimePendingConfirmation(
    string InteractionId,
    string Risk,
    string Title,
    PluginRuntimeConfirmationPreview Preview,
    double ExpiresAt);

public sealed record PluginRuntimeProgress(
    int Current,
    int Total,
    string Message,
    bool Cancellable);

public sealed record PluginRuntimeInteractionSnapshot(
    string RunId,
    string ProjectKey,
    string PluginId,
    string ActionId,
    string Caller,
    PluginRuntimeProgress? Progress,
    PluginRuntimePendingConfirmation? PendingConfirmation,
    bool CancelRequested);

public sealed record PluginRuntimeFileRequest(
    string RequestId,
    string RunId,
    string ProjectKey,
    string PluginId,
    string ActionId,
    string Direction,
    IReadOnlyList<string> MediaTypes,
    string? SuggestedName,
    string? MediaType,
    double ExpiresAt);

public sealed record PluginRuntimeTaskSnapshot(
    string TaskId,
    string RunId,
    string PluginId,
    string PluginVersion,
    string ActionId,
    string ProjectKey,
    string? Collection,
    int TargetCount,
    string Risk,
    string State,
    bool CancelRequested,
    PluginRuntimeProgress? Progress,
    PluginRuntimeResult? Result,
    PluginRuntimeSafeError? Error);

public sealed record PluginRuntimeSafeError(
    string Contract,
    string Code,
    string Message,
    string Recoverability,
    string? PluginId,
    string? ActionId,
    string? RunId,
    IReadOnlyDictionary<string, JsonElement> Details,
    string? CauseId);

public sealed record PluginRuntimeInteractionResolveResult(
    string Status,
    string? Decision);

public sealed record PluginRuntimeAction(
    string ActionId,
    IReadOnlyDictionary<string, string> DisplayName,
    IReadOnlyDictionary<string, string> Description,
    string Mode,
    string Risk,
    string Invocation,
    IReadOnlyList<string> Placements,
    JsonElement Requires,
    string? EntryFlow,
    string? WorkerEntry,
    string? FormSchema,
    string? InputSchema,
    string? OutputSchema);

public sealed record PluginRuntimeManifest(
    [property: JsonPropertyName("$schema")] string Schema,
    string PluginId,
    string Version,
    IReadOnlyDictionary<string, string> DisplayName,
    IReadOnlyDictionary<string, string> Description,
    JsonElement Compatibility,
    JsonElement Permissions,
    IReadOnlyList<PluginRuntimeAction> Actions,
    IReadOnlyList<JsonElement> Flows,
    JsonElement Ui);

public sealed record PluginRuntimeFlowRequirement(
    string LogicalFlowId,
    string Ownership,
    string Trigger,
    string Risk,
    string ContractVersion,
    IReadOnlyList<string> RequiresOperations,
    JsonElement InputSchema,
    JsonElement OutputSchema,
    JsonElement? Definition);

public sealed record PluginRuntimeInstallPlan(
    string PlanId,
    string ProjectKey,
    string ProjectRevision,
    string SourceType,
    string SourceLocation,
    string PackageHash,
    PluginRuntimeManifest Manifest,
    IReadOnlyList<PluginRuntimeFlowRequirement> FlowRequirements,
    IReadOnlyDictionary<string, IReadOnlyDictionary<string, JsonElement>> Schemas);

public sealed record PluginRuntimeSnapshot(
    string ProjectKey,
    string PluginId,
    string Version,
    string PackageHash,
    string SourceType,
    string SourceLocation,
    PluginRuntimeManifest Manifest,
    IReadOnlyList<PluginRuntimeFlowRequirement> FlowRequirements,
    IReadOnlyList<PluginRuntimeFlowBindingSnapshot> FlowBindings,
    IReadOnlyDictionary<string, IReadOnlyDictionary<string, JsonElement>> Schemas,
    string Status,
    string? DisabledReason,
    int Revision,
    IReadOnlyList<string>? BlockingReasons = null,
    bool SourceChanged = false);

public sealed record PluginRuntimeExternalFlowCandidate(
    string DirectusFlowUuid,
    string Name,
    string TriggerType,
    string Status,
    IReadOnlyList<string> OperationKeys,
    bool Compatible,
    IReadOnlyList<string> Reasons);

public sealed record PluginRuntimeFlowBindingSnapshot(
    string ProjectKey,
    string PluginId,
    string LogicalFlowId,
    string Ownership,
    string DirectusFlowUuid,
    string? RollbackFlowUuid,
    string? RollbackContractVersion,
    string? RollbackDefinitionHash,
    string TriggerType,
    string ContractVersion,
    string? InstalledDefinitionHash,
    string ObservedDefinitionHash,
    int Revision,
    string Health,
    string DriftStatus,
    string? LastError);

public sealed record PluginRuntimeUninstallResult(
    int ManagedFlowsRemoved,
    int ExternalFlowsUnbound,
    bool Uninstalled,
    bool PrivateSettingsRetained,
    bool CleanupPending = false);

public sealed record PluginRuntimeAuditEvent(
    string EventId,
    string ProjectKey,
    string PluginId,
    string PluginVersion,
    string PackageHash,
    string EventType,
    string Outcome,
    string? ActionId,
    string? RunId,
    string Actor,
    string? Risk,
    string? TargetCollection,
    int? TargetCount,
    DateTimeOffset StartedAt,
    DateTimeOffset? FinishedAt,
    int? DurationMs,
    string? ErrorCode,
    IReadOnlyDictionary<string, JsonElement> Details);

public sealed record PluginRuntimeActionAvailability(
    bool Available,
    IReadOnlyList<string> Reasons);
