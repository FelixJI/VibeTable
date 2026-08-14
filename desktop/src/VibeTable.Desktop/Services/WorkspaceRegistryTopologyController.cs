using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public sealed record WorkspaceRegistryDispatchResult(
    object Result,
    bool BootstrapChanged = false);

public interface IWorkspaceRegistryTopologyController
{
    Task<WorkspaceRegistryDispatchResult> DispatchAsync(
        string method,
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken);

    Task<WorkspaceSessionV2> OpenAsync(
        Guid workspaceId,
        WorkspaceOpenMode mode,
        bool switching,
        CancellationToken cancellationToken);
}

public interface IWorkspaceRepositoryOnboardingPort
{
    Task<WorkspaceRepositoryInitialization> InitializeAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken);

    Task UnlockAsync(
        WorkspaceRegistryEntryV2 workspace,
        string recoveryKey,
        CancellationToken cancellationToken);
}

public sealed class WorkspaceRepositoryOnboardingPort(
    WorkspaceRepositoryOnboardingService onboarding) :
    IWorkspaceRepositoryOnboardingPort
{
    private readonly WorkspaceRepositoryOnboardingService _onboarding =
        onboarding ?? throw new ArgumentNullException(nameof(onboarding));

    public Task<WorkspaceRepositoryInitialization> InitializeAsync(
        WorkspaceRegistryEntryV2 workspace,
        CancellationToken cancellationToken) =>
        _onboarding.InitializeAsync(workspace, cancellationToken);

    public Task UnlockAsync(
        WorkspaceRegistryEntryV2 workspace,
        string recoveryKey,
        CancellationToken cancellationToken) =>
        _onboarding.UnlockAsync(workspace, recoveryKey, cancellationToken);
}

/// <summary>
/// Owns registry lifecycle, storage-topology invariants, native location
/// grants and repository/replica provisioning behind Dispatch/Open.
/// </summary>
public sealed class WorkspaceRegistryTopologyController :
    IWorkspaceRegistryTopologyController
{
    private readonly IWorkspaceProductSessionPort _session;
    private readonly WorkspaceRegistry _registry;
    private readonly WorkspaceProviderPolicy _providerPolicy;
    private readonly IWorkspaceRepositoryOnboardingPort _onboarding;
    private readonly IWorkspaceRepositoryRecoveryUi _recoveryUi;
    private readonly IWorkspaceReplicaRecoveryService _replicas;
    private readonly WorkspacePathGrantStore _pathGrants;
    private readonly string _productDataRoot;
    private readonly string _activityRootBase;
    private readonly Dictionary<Guid, WorkspaceDeletePlan> _deletePlans = [];

    public WorkspaceRegistryTopologyController(
        IWorkspaceProductSessionPort session,
        WorkspaceRegistry registry,
        WorkspaceProviderPolicy providerPolicy,
        IWorkspaceRepositoryOnboardingPort onboarding,
        IWorkspaceRepositoryRecoveryUi recoveryUi,
        IWorkspaceReplicaRecoveryService replicas,
        WorkspacePathGrantStore pathGrants,
        string productDataRoot,
        string activityRootBase)
    {
        _session = session ?? throw new ArgumentNullException(nameof(session));
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _providerPolicy = providerPolicy
            ?? throw new ArgumentNullException(nameof(providerPolicy));
        _onboarding = onboarding
            ?? throw new ArgumentNullException(nameof(onboarding));
        _recoveryUi = recoveryUi
            ?? throw new ArgumentNullException(nameof(recoveryUi));
        _replicas = replicas ?? throw new ArgumentNullException(nameof(replicas));
        _pathGrants = pathGrants
            ?? throw new ArgumentNullException(nameof(pathGrants));
        ArgumentException.ThrowIfNullOrWhiteSpace(productDataRoot);
        ArgumentException.ThrowIfNullOrWhiteSpace(activityRootBase);
        _productDataRoot = Path.GetFullPath(productDataRoot);
        _activityRootBase = Path.GetFullPath(activityRootBase);
    }

    public async Task<WorkspaceRegistryDispatchResult> DispatchAsync(
        string method,
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        return method switch
        {
            "workspace.list" => new WorkspaceRegistryDispatchResult(new
            {
                workspaces = _registry.List()
                    .Select(WorkspaceProjection.RegistryEntry)
                    .ToArray(),
            }),
            "workspace.create" => new WorkspaceRegistryDispatchResult(
                await CreateAsync(parameters, operationId, cancellationToken)),
            "workspace.register" => new WorkspaceRegistryDispatchResult(
                await RegisterAsync(parameters, operationId, cancellationToken)),
            "workspace.relink" => new WorkspaceRegistryDispatchResult(
                await RelinkAsync(parameters, operationId, cancellationToken),
                BootstrapChanged: true),
            "workspace.remove" => new WorkspaceRegistryDispatchResult(
                Remove(parameters)),
            "workspace.planDelete" => new WorkspaceRegistryDispatchResult(
                PlanDelete(parameters)),
            "workspace.applyDelete" => new WorkspaceRegistryDispatchResult(
                ApplyDelete(parameters)),
            _ => throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                $"Unsupported registry operation '{method}'."),
        };
    }

    public async Task<WorkspaceSessionV2> OpenAsync(
        Guid workspaceId,
        WorkspaceOpenMode mode,
        bool switching,
        CancellationToken cancellationToken)
    {
        try
        {
            return await _session.OpenAsync(
                workspaceId,
                mode,
                switching,
                cancellationToken);
        }
        catch (Exception exception) when (RequiresRecovery(exception))
        {
            WorkspaceRegistryEntryV2 workspace = _registry.List()
                .Single(entry => entry.WorkspaceId == workspaceId);
            string? recoveryKey = _recoveryUi.PromptRecoveryKey(
                workspace.DisplayName);
            if (string.IsNullOrWhiteSpace(recoveryKey))
                throw new WorkspaceRegistryException(
                    "repository.recovery_cancelled",
                    "Workspace recovery was cancelled.");
            await _onboarding.UnlockAsync(
                workspace,
                recoveryKey,
                cancellationToken);
            return await _session.OpenAsync(
                workspaceId,
                mode,
                switching,
                cancellationToken);
        }
    }

    private async Task<object> CreateAsync(
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        Guid workspaceId = Guid.NewGuid();
        string selectedRoot = ResolveCreateRoot(
            parameters,
            operationId,
            workspaceId);
        bool userMarkedSync = ReadRequiredBoolean(parameters, "userMarkedSync");
        string displayName = ReadString(parameters, "displayName")?.Trim()
            ?? string.Empty;
        if (displayName.Length is < 1 or > 120)
            throw Invalid("Workspace display name is invalid.");
        WorkspaceStorageMode storageMode =
            ReadString(parameters, "storageMode") switch
            {
                "direct" => WorkspaceStorageMode.Direct,
                "mirrored" => WorkspaceStorageMode.Mirrored,
                _ => throw Invalid("Workspace storage mode is invalid."),
            };
        WorkspaceStorageObservation selectedStorage =
            _providerPolicy.ProbeCreateTargetAndEnsureSupported(
                selectedRoot,
                storageMode,
                userMarkedSync);
        WorkspaceEncryptionMode encryptionMode =
            ReadString(parameters, "encryptionMode") switch
            {
                "none" => WorkspaceEncryptionMode.None,
                "convenient" => WorkspaceEncryptionMode.Convenient,
                "protected" => WorkspaceEncryptionMode.Protected,
                _ => throw Invalid("Workspace encryption mode is invalid."),
            };
        string? activityRoot = storageMode == WorkspaceStorageMode.Mirrored
            ? ManagedActivityRoot(workspaceId)
            : null;
        if (activityRoot is not null)
            _ = _providerPolicy.ProbeCreateTargetAndEnsureSupported(
                activityRoot,
                WorkspaceStorageMode.Direct);
        WorkspaceLayoutResult layout = WorkspaceLayout.Create(
            selectedRoot,
            displayName,
            storageMode,
            encryptionMode,
            activityRoot,
            workspaceId);
        WorkspaceRegistryEntryV2 entry = Entry(
            layout.Manifest,
            layout.SelectedRoot,
            activityRoot,
            selectedStorage,
            pendingSync: false);
        try
        {
            WorkspaceRepositoryInitialization repository =
                await _onboarding.InitializeAsync(entry, cancellationToken);
            if (repository.RecoveryKey is not null)
                _recoveryUi.ConfirmRecoveryKey(
                    entry.DisplayName,
                    repository.RecoveryKey);
            if (storageMode == WorkspaceStorageMode.Mirrored)
            {
                WorkspaceReplicaReceipt receipt =
                    await _replicas.InitializeAsync(entry, cancellationToken);
                entry = WithReceipt(entry, receipt);
            }
            _registry.Register(entry);
        }
        catch
        {
            Rollback(layout);
            throw;
        }
        return new
        {
            workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
            status = "created",
        };
    }

    private async Task<object> RegisterAsync(
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        string selectedRoot = ConsumeRoot(
            "workspace.register",
            parameters,
            operationId);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(selectedRoot);
        WorkspaceStorageObservation selectedStorage =
            _providerPolicy.ProbeAndEnsureSupported(
                selectedRoot,
                manifest.StorageMode);
        string? activityRoot = manifest.StorageMode == WorkspaceStorageMode.Mirrored
            ? ManagedActivityRoot(manifest.WorkspaceId)
            : null;
        if (activityRoot is not null)
            _ = _providerPolicy.ProbeCreateTargetAndEnsureSupported(
                activityRoot,
                WorkspaceStorageMode.Direct);
        WorkspaceRegistryEntryV2 entry = Entry(
            manifest,
            selectedRoot,
            activityRoot,
            selectedStorage,
            pendingSync: manifest.StorageMode == WorkspaceStorageMode.Mirrored);
        if (manifest.StorageMode == WorkspaceStorageMode.Mirrored)
        {
            WorkspaceReplicaReceipt receipt =
                await _replicas.RecoverAndPublishAsync(entry, cancellationToken);
            entry = WithReceipt(entry, receipt);
        }
        _registry.Register(entry);
        return new
        {
            workspaceId = manifest.WorkspaceId.ToString("D"),
            status = "registered",
        };
    }

    private async Task<object> RelinkAsync(
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        Guid workspaceId = ReadRequiredGuid(parameters, "workspaceId");
        RequireClosed(workspaceId, "Close the workspace before changing its location.");
        string selectedRoot = ConsumeRoot(
            "workspace.relink",
            parameters,
            operationId);
        WorkspaceRegistryEntryV2 current = Find(workspaceId);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(selectedRoot);
        EnsureIdentityAndTopology(current, manifest, workspaceId);
        WorkspaceStorageObservation selectedStorage =
            _providerPolicy.ProbeAndEnsureSupported(
                selectedRoot,
                manifest.StorageMode);
        if (manifest.StorageMode == WorkspaceStorageMode.Mirrored)
        {
            string activityRoot = string.IsNullOrWhiteSpace(current.ActivityRoot)
                ? ManagedActivityRoot(workspaceId)
                : current.ActivityRoot;
            WorkspaceRegistryEntryV2 candidate = current with
            {
                SelectedRoot = Path.GetFullPath(selectedRoot),
                ActivityRoot = activityRoot,
                StorageKind = selectedStorage.StorageKind,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                PendingSync = true,
            };
            WorkspaceReplicaReceipt receipt = _replicas.RequiresRecovery(candidate)
                ? await _replicas.RecoverAndPublishAsync(candidate, cancellationToken)
                : await _replicas.VerifyAsync(candidate, cancellationToken);
            _registry.Relink(
                workspaceId,
                selectedRoot,
                activityRoot,
                selectedStorage with
                {
                    CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                });
            _registry.UpdateHealth(
                workspaceId,
                new WorkspaceHealthObservation(
                    WorkspaceHealth.Healthy,
                    PendingSync: false,
                    LastSnapshotAt: receipt.VerifiedAt,
                    LastSyncAt: receipt.VerifiedAt));
        }
        else
        {
            _registry.Relink(
                workspaceId,
                selectedRoot,
                activityRoot: null,
                selectedStorage);
        }
        return new
        {
            workspaceId = workspaceId.ToString("D"),
            status = "relinked",
        };
    }

    private object Remove(JsonElement parameters)
    {
        Guid workspaceId = ReadRequiredGuid(parameters, "workspaceId");
        RequireClosed(workspaceId, "Close the workspace before removing it from this device.");
        _registry.Unregister(workspaceId);
        return new { workspaceId = workspaceId.ToString("D"), status = "removed" };
    }

    private object PlanDelete(JsonElement parameters)
    {
        Guid workspaceId = ReadRequiredGuid(parameters, "workspaceId");
        RequireClosed(workspaceId, "Close the workspace before deleting it.");
        WorkspaceDeletePlan plan = _registry.PlanPermanentDelete(workspaceId);
        _deletePlans[plan.PlanId] = plan;
        return new
        {
            planId = plan.PlanId.ToString("D"),
            displayName = plan.DisplayName,
            requiresTypedName = true,
        };
    }

    private object ApplyDelete(JsonElement parameters)
    {
        Guid planId = ReadRequiredGuid(parameters, "planId");
        string confirmation = ReadString(parameters, "confirmation") ?? string.Empty;
        if (!_deletePlans.Remove(planId, out WorkspaceDeletePlan? plan))
            throw new WorkspaceRegistryException(
                "workspace.delete_plan_stale",
                "Workspace delete plan is missing or expired.");
        _registry.ApplyPermanentDelete(plan, confirmation);
        return new
        {
            workspaceId = plan.WorkspaceId.ToString("D"),
            status = "deleted",
        };
    }

    private string ResolveCreateRoot(
        JsonElement parameters,
        Guid operationId,
        Guid workspaceId)
    {
        if (!SnapshotPackageBroker.HasExactProperties(
                parameters,
                "displayName",
                "locationPolicy",
                "selectedRootGrant",
                "storageMode",
                "encryptionMode",
                "userMarkedSync"))
            throw Invalid("Workspace create params contain missing or unknown fields.");
        if (!parameters.TryGetProperty(
                "selectedRootGrant",
                out JsonElement selectedRootGrant))
            throw Invalid("Missing selectedRootGrant.");
        string? locationPolicy = ReadString(parameters, "locationPolicy");
        bool userMarkedSync = ReadRequiredBoolean(parameters, "userMarkedSync");
        if (locationPolicy == "managedDefault" && userMarkedSync)
            throw Invalid("The managed default location cannot be marked as sync-managed.");
        if (locationPolicy == "managedDefault" &&
            ReadString(parameters, "storageMode") == "mirrored")
            throw Invalid("The managed default location requires direct storage mode.");
        return locationPolicy switch
        {
            "managedDefault" when selectedRootGrant.ValueKind == JsonValueKind.Null =>
                Path.Combine(
                    _productDataRoot,
                    "workspaces",
                    workspaceId.ToString("D")),
            "managedDefault" => throw Invalid(
                "The managed default location must not include a path grant."),
            "other" => ConsumeRoot("workspace.create", parameters, operationId),
            _ => throw Invalid("Workspace locationPolicy is invalid."),
        };
    }

    private string ConsumeRoot(
        string method,
        JsonElement parameters,
        Guid operationId)
    {
        JsonElement materialized = _pathGrants.MaterializeSentinels(
            method,
            operationId,
            parameters);
        string grant = ReadString(materialized, "selectedRootGrant")
            ?? throw Invalid(
                method == "workspace.create"
                    ? "The other location requires a selectedRootGrant."
                    : "Missing selectedRootGrant.");
        return _pathGrants.Consume(
            grant,
            method,
            operationId,
            "workspace-root");
    }

    private WorkspaceRegistryEntryV2 Find(Guid workspaceId) =>
        _registry.List().SingleOrDefault(entry => entry.WorkspaceId == workspaceId)
        ?? throw new WorkspaceRegistryException(
            "workspace.not_registered",
            "Workspace is not registered on this device.");

    private void RequireClosed(Guid workspaceId, string message)
    {
        if (_session.CurrentSession.WorkspaceId == workspaceId)
            throw new WorkspaceRegistryException("workspace.session_open", message);
    }

    private string ManagedActivityRoot(Guid workspaceId) => Path.Combine(
        _activityRootBase,
        workspaceId.ToString("D"));

    private static WorkspaceRegistryEntryV2 Entry(
        WorkspaceManifestV2 manifest,
        string selectedRoot,
        string? activityRoot,
        WorkspaceStorageObservation selectedStorage,
        bool pendingSync) => new()
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = manifest.WorkspaceId,
            DisplayName = manifest.DisplayName,
            SelectedRoot = Path.GetFullPath(selectedRoot),
            ActivityRoot = activityRoot,
            StorageKind = selectedStorage.StorageKind,
            CoordinationStrength = manifest.StorageMode == WorkspaceStorageMode.Mirrored
                ? WorkspaceCoordinationStrength.Advisory
                : selectedStorage.CoordinationStrength,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = pendingSync,
        };

    private static WorkspaceRegistryEntryV2 WithReceipt(
        WorkspaceRegistryEntryV2 entry,
        WorkspaceReplicaReceipt receipt) => entry with
        {
            LastSnapshotAt = receipt.VerifiedAt,
            LastSyncAt = receipt.VerifiedAt,
            PendingSync = false,
        };

    private static void EnsureIdentityAndTopology(
        WorkspaceRegistryEntryV2 current,
        WorkspaceManifestV2 manifest,
        Guid workspaceId)
    {
        if (manifest.WorkspaceId != workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Selected path contains a different workspace UUID.");
        WorkspaceStorageMode registeredMode =
            string.IsNullOrWhiteSpace(current.ActivityRoot)
                ? WorkspaceStorageMode.Direct
                : WorkspaceStorageMode.Mirrored;
        if (manifest.StorageMode != registeredMode)
            throw new WorkspaceRegistryException(
                "workspace.storage_topology_mismatch",
                "Relinking cannot change storage topology; use a storage conversion plan.");
    }

    private static void Rollback(WorkspaceLayoutResult layout)
    {
        foreach (string root in new[] { layout.ActivityRoot, layout.SelectedRoot }
                     .Distinct(StringComparer.OrdinalIgnoreCase))
        {
            try
            {
                WorkspaceLayout.DeleteWorkspaceRoot(root, layout.Manifest.WorkspaceId);
            }
            catch (Exception exception) when (
                exception is IOException or
                    UnauthorizedAccessException or
                    WorkspaceRegistryException)
            {
                // Registration was never published; preserve unexpected state
                // only when safe rollback cannot prove ownership.
            }
        }
    }

    private static bool RequiresRecovery(Exception exception)
    {
        for (Exception? current = exception;
             current is not null;
             current = current.InnerException)
        {
            if (current is WorkspaceRegistryException registry &&
                registry.Code == "repository.key_missing")
                return true;
            if (current.Message.Contains(
                    "repository.key_missing",
                    StringComparison.Ordinal))
                return true;
            if (current is AggregateException aggregate &&
                aggregate.InnerExceptions.Any(RequiresRecovery))
                return true;
        }
        return false;
    }

    private static Guid ReadRequiredGuid(JsonElement value, string name)
    {
        string? raw = ReadString(value, name);
        return Guid.TryParse(raw, out Guid parsed) && parsed != Guid.Empty
            ? parsed
            : throw Invalid($"Missing or invalid '{name}'.");
    }

    private static bool ReadRequiredBoolean(JsonElement value, string name)
    {
        if (value.ValueKind != JsonValueKind.Object ||
            !value.TryGetProperty(name, out JsonElement item) ||
            item.ValueKind is not (JsonValueKind.True or JsonValueKind.False))
            throw Invalid($"Missing or invalid '{name}'.");
        return item.GetBoolean();
    }

    private static string? ReadString(JsonElement value, string name) =>
        value.ValueKind == JsonValueKind.Object &&
        value.TryGetProperty(name, out JsonElement item) &&
        item.ValueKind == JsonValueKind.String
            ? item.GetString()
            : null;

    private static WorkspaceRegistryException Invalid(string message) =>
        new("workspace.request_invalid", message);
}
