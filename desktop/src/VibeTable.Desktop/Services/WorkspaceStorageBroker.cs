using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Durable Desktop producer for relocation, topology conversion and safe
/// activity-cache release. Mirrored mutations remain fail-closed until an
/// independent Sidecar reopen-and-roots verification receipt is persisted.
/// </summary>
public sealed class WorkspaceStorageBroker
{
    private const int PlanFormatVersion = 2;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly WorkspaceRegistry _registry;
    private readonly WorkspaceSessionManager _sessions;
    private readonly WorkspaceProviderPolicy _providerPolicy;
    private readonly WorkspaceStorageManager _storage = new();
    private readonly IWorkspaceReplicaRecoveryService? _replicas;
    private readonly IWorkspaceStorageFailureInjector _failureInjector;
    private readonly string _plansRoot;

    public WorkspaceStorageBroker(
        WorkspaceRegistry registry,
        WorkspaceSessionManager sessions,
        WorkspaceProviderPolicy providerPolicy,
        string productDataRoot,
        IWorkspaceStorageFailureInjector? failureInjector = null,
        IWorkspaceReplicaRecoveryService? replicas = null)
    {
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _sessions = sessions ?? throw new ArgumentNullException(nameof(sessions));
        _providerPolicy = providerPolicy
            ?? throw new ArgumentNullException(nameof(providerPolicy));
        _replicas = replicas;
        _failureInjector = failureInjector
            ?? NoopWorkspaceStorageFailureInjector.Instance;
        ArgumentException.ThrowIfNullOrWhiteSpace(productDataRoot);
        _plansRoot = Path.Combine(
            Path.GetFullPath(productDataRoot),
            "workspace-storage-plans");
        PurgeExpiredPlans();
    }

    public async Task<JsonElement> PreviewAsync(
        JsonElement parameters,
        string? selectedRoot,
        CancellationToken cancellationToken)
    {
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            RequireExactProperties(
                parameters,
                "workspaceId",
                "action",
                "targetMode",
                "selectedRootGrant");
            Guid workspaceId = RequiredGuid(parameters, "workspaceId");
            string action = RequiredString(parameters, "action");
            JsonElement targetModeValue =
                parameters.GetProperty("targetMode");
            JsonElement selectedRootGrantValue =
                parameters.GetProperty("selectedRootGrant");
            if (action == "convertTopology")
            {
                if (_replicas is null)
                    throw ReplicaCapabilityUnavailable();
                WorkspaceRegistryEntryV2 topologyWorkspace =
                    RequiredWorkspace(workspaceId);
                string expectedTargetMode =
                    topologyWorkspace.ActivityRoot is null
                        ? "mirrored"
                        : "direct";
                if (targetModeValue.ValueKind != JsonValueKind.String
                    || targetModeValue.GetString() != expectedTargetMode
                    || selectedRootGrantValue.ValueKind
                        != JsonValueKind.String
                    || string.IsNullOrWhiteSpace(selectedRoot)
                    || string.IsNullOrWhiteSpace(
                        selectedRootGrantValue.GetString()))
                {
                    throw InvalidRequest();
                }
                string conversionTarget = Path.GetFullPath(selectedRoot);
                WorkspaceStorageObservation conversionStorage =
                    _providerPolicy.ProbeCreateTargetAndEnsureSupported(
                        conversionTarget);
                WorkspaceStoragePlan? conversionCopyPlan = null;
                string sourceMode = topologyWorkspace.ActivityRoot is null
                    ? "direct"
                    : "mirrored";
                string? targetActivityRoot;
                if (expectedTargetMode == "mirrored")
                {
                    targetActivityRoot = topologyWorkspace.SelectedRoot;
                }
                else
                {
                    if (conversionStorage.StorageKind !=
                            WorkspaceStorageKind.Fixed ||
                        conversionStorage.CoordinationStrength !=
                            WorkspaceCoordinationStrength.Strong)
                        throw new WorkspaceRegistryException(
                            "workspace.storage_conversion_unsupported",
                            "A direct workspace target must use fixed storage with strong coordination.");
                    targetActivityRoot = null;
                    // The activity root can still contain live SQLite handles
                    // during preview. Seal the exact copy inventory only after
                    // protection, drain and writer-fence acquisition.
                }
                DateTimeOffset expiresAt =
                    DateTimeOffset.UtcNow.AddMinutes(10);
                var conversion = new DurableStoragePlan
                {
                    FormatVersion = PlanFormatVersion,
                    PlanId = conversionCopyPlan?.PlanId ?? Guid.NewGuid(),
                    WorkspaceId = workspaceId,
                    DisplayName = topologyWorkspace.DisplayName,
                    Action = action,
                    SourceSelectedRoot = topologyWorkspace.SelectedRoot,
                    SourceActivityRoot = topologyWorkspace.ActivityRoot,
                    SourceMode = sourceMode,
                    TargetSelectedRoot = conversionTarget,
                    TargetActivityRoot = targetActivityRoot,
                    TargetMode = expectedTargetMode,
                    TargetStorageKind = conversionStorage.StorageKind,
                    TargetCoordinationStrength =
                        expectedTargetMode == "mirrored"
                            ? WorkspaceCoordinationStrength.Advisory
                            : conversionStorage.CoordinationStrength,
                    CopyPlan = conversionCopyPlan,
                    ReplicaReceipt = null,
                    Phase = "previewed",
                    ExpiresAt = expiresAt,
                };
                WritePlan(conversion);
                return PlanProjection(
                    conversion,
                    expectedTargetMode == "direct"
                        ? MainWindow.MeasureWorkspaceStorage(topologyWorkspace)
                            .PhysicalSize
                        : 0,
                    "Topology conversion is published only after independent replica verification.");
            }
            if (action == "releaseActivityCache")
            {
                if (_replicas is null)
                    throw ReplicaCapabilityUnavailable();
                if (targetModeValue.ValueKind != JsonValueKind.Null
                    || selectedRootGrantValue.ValueKind != JsonValueKind.Null)
                    throw InvalidRequest();
                WorkspaceRegistryEntryV2 cacheWorkspace =
                    RequiredWorkspace(workspaceId);
                if (cacheWorkspace.ActivityRoot is null)
                {
                    throw new WorkspaceRegistryException(
                        "workspace.release_cache_not_applicable",
                        "Only mirrored workspaces have a local activity cache.");
                }
                var release = new DurableStoragePlan
                {
                    FormatVersion = PlanFormatVersion,
                    PlanId = Guid.NewGuid(),
                    WorkspaceId = workspaceId,
                    DisplayName = cacheWorkspace.DisplayName,
                    Action = action,
                    SourceSelectedRoot = cacheWorkspace.SelectedRoot,
                    SourceActivityRoot = cacheWorkspace.ActivityRoot,
                    SourceMode = "mirrored",
                    TargetSelectedRoot = cacheWorkspace.SelectedRoot,
                    TargetActivityRoot = cacheWorkspace.ActivityRoot,
                    TargetMode = "mirrored",
                    TargetStorageKind = cacheWorkspace.StorageKind,
                    TargetCoordinationStrength =
                        WorkspaceCoordinationStrength.Advisory,
                    CopyPlan = null,
                    ReplicaReceipt = null,
                    Phase = "previewed",
                    ExpiresAt = DateTimeOffset.UtcNow.AddMinutes(5),
                };
                WritePlan(release);
                return PlanProjection(
                    release,
                    MainWindow.MeasureWorkspaceStorage(cacheWorkspace)
                        .PhysicalSize,
                    "The activity cache is deleted only after an authenticated independent replica reopen.");
            }
            if (action != "relocate")
                throw InvalidRequest();
            if (targetModeValue.ValueKind != JsonValueKind.String
                || targetModeValue.GetString() != "direct"
                || selectedRootGrantValue.ValueKind
                    != JsonValueKind.String
                || string.IsNullOrWhiteSpace(
                    selectedRootGrantValue.GetString())
                || string.IsNullOrWhiteSpace(selectedRoot))
            {
                throw InvalidRequest();
            }
            WorkspaceRegistryEntryV2 workspace =
                RequiredWorkspace(workspaceId);
            string runtimeRoot =
                ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
            WorkspaceManifestV2 manifest =
                WorkspaceLayout.ReadManifest(runtimeRoot);
            if (manifest.WorkspaceId != workspaceId
                || manifest.StorageMode != WorkspaceStorageMode.Direct
                || workspace.ActivityRoot is not null
                || workspace.StorageKind != WorkspaceStorageKind.Fixed
                || workspace.CoordinationStrength
                    != WorkspaceCoordinationStrength.Strong)
            {
                throw new WorkspaceRegistryException(
                    "workspace.storage_relocate_unsupported",
                    "Only direct workspaces on fixed storage can be relocated in this release.");
            }
            string target = Path.GetFullPath(selectedRoot);
            WorkspaceStorageObservation targetStorage =
                _providerPolicy.ProbeCreateTargetAndEnsureSupported(target);
            if (targetStorage.StorageKind != WorkspaceStorageKind.Fixed
                || targetStorage.CoordinationStrength
                    != WorkspaceCoordinationStrength.Strong)
            {
                throw new WorkspaceRegistryException(
                    "workspace.storage_relocate_unsupported",
                    "The relocation target must be fixed storage with strong coordination.");
            }
            WorkspaceStoragePlan copyPlan = _storage.PreviewMove(
                workspace.SelectedRoot,
                target);
            var durable = new DurableStoragePlan
            {
                FormatVersion = PlanFormatVersion,
                PlanId = copyPlan.PlanId,
                WorkspaceId = workspaceId,
                DisplayName = workspace.DisplayName,
                Action = action,
                SourceSelectedRoot = workspace.SelectedRoot,
                SourceActivityRoot = workspace.ActivityRoot,
                SourceMode = "direct",
                TargetSelectedRoot = target,
                TargetActivityRoot = null,
                TargetMode = "direct",
                TargetStorageKind = targetStorage.StorageKind,
                TargetCoordinationStrength =
                    targetStorage.CoordinationStrength,
                CopyPlan = copyPlan,
                ReplicaReceipt = null,
                Phase = "previewed",
                ExpiresAt = copyPlan.ExpiresAt,
            };
            WritePlan(durable);
            return JsonSerializer.SerializeToElement(new
            {
                planId = durable.PlanId.ToString("D"),
                workspaceId = workspaceId.ToString("D"),
                action,
                source = new
                {
                    selectedRoot = durable.SourceSelectedRoot,
                    activityRoot = durable.SourceActivityRoot,
                    mode = durable.SourceMode,
                },
                target = new
                {
                    selectedRoot = durable.TargetSelectedRoot,
                    activityRoot = durable.TargetActivityRoot,
                    mode = durable.TargetMode,
                },
                bytesToCopy = copyPlan.BytesToCopy,
                requiresClosedSession = true,
                warnings = new[]
                {
                    "The verified source copy is retained after relocation.",
                },
                expiresAt = durable.ExpiresAt,
                verificationReceiptId = (string?)null,
            });
        }
        finally
        {
            _gate.Release();
        }
    }

    public async Task<JsonElement> ApplyAsync(
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            RequireExactProperties(parameters, "planId", "confirmation");
            Guid planId = RequiredGuid(parameters, "planId");
            string confirmation = RequiredString(parameters, "confirmation");
            DurableStoragePlan plan = ReadPlan(planId);
            if (plan.Action == "convertTopology")
                return await ApplyConversionAsync(
                    plan,
                    confirmation,
                    cancellationToken).ConfigureAwait(false);
            if (plan.Action == "releaseActivityCache")
                return await ApplyReleaseAsync(
                    plan,
                    confirmation,
                    cancellationToken).ConfigureAwait(false);
            if (plan.ExpiresAt <= DateTimeOffset.UtcNow
                || plan.Action != "relocate"
                || plan.CopyPlan is null
                || plan.CopyPlan.WorkspaceId != plan.WorkspaceId
                || plan.Phase is not ("previewed" or "sealed"))
            {
                throw StalePlan();
            }
            if (!string.Equals(
                    confirmation,
                    plan.DisplayName,
                    StringComparison.Ordinal))
            {
                throw new WorkspaceRegistryException(
                    "workspace.storage_confirmation_mismatch",
                    "Type the workspace display name to apply this storage plan.");
            }
            WorkspaceRegistryEntryV2 current =
                RequiredWorkspace(plan.WorkspaceId);
            bool sourceStillRegistered = string.Equals(
                Path.GetFullPath(current.SelectedRoot),
                Path.GetFullPath(plan.SourceSelectedRoot),
                StringComparison.OrdinalIgnoreCase);
            bool targetAlreadyRegistered = string.Equals(
                Path.GetFullPath(current.SelectedRoot),
                Path.GetFullPath(plan.TargetSelectedRoot),
                StringComparison.OrdinalIgnoreCase);
            if (!sourceStillRegistered && !targetAlreadyRegistered)
                throw StalePlan();
            if (targetAlreadyRegistered)
            {
                WorkspaceManifestV2 targetManifest =
                    WorkspaceLayout.ReadManifest(plan.TargetSelectedRoot);
                if (targetManifest.WorkspaceId != plan.WorkspaceId)
                    throw StalePlan();
                TryDeletePlan(plan.PlanId);
                return JsonSerializer.SerializeToElement(new
                {
                    workspaceId = plan.WorkspaceId.ToString("D"),
                    status = "applied",
                    storage = BuildStorageProjection(current),
                });
            }

            WorkspaceStorageObservation targetStorage =
                _providerPolicy.ProbeCreateTargetAndEnsureSupported(
                    plan.TargetSelectedRoot);
            if (targetStorage.StorageKind != plan.TargetStorageKind
                || targetStorage.CoordinationStrength
                    != plan.TargetCoordinationStrength
                || targetStorage.StorageKind != WorkspaceStorageKind.Fixed)
            {
                throw StalePlan();
            }

            string runtimeRoot =
                ProductionWorkspaceRuntimeFactory.RuntimeRoot(current);
            await using WorkspaceStorageMaintenanceLease maintenance =
                WorkspaceStorageMaintenanceLease.Acquire(
                    runtimeRoot,
                    plan.WorkspaceId);
            if (_sessions.Current.WorkspaceId == plan.WorkspaceId)
            {
                // The maintenance intent is already visible to other Desktop
                // processes before protection/drain releases the writer lock.
                _ = await _sessions.CloseAsync(
                        "workspace-storage-relocate",
                        cancellationToken)
                    .ConfigureAwait(false);
            }
            await maintenance.AcquireWriterFenceAsync(
                    runtimeRoot,
                    cancellationToken)
                .ConfigureAwait(false);

            current = RequiredWorkspace(plan.WorkspaceId);
            if (!string.Equals(
                    Path.GetFullPath(current.SelectedRoot),
                    Path.GetFullPath(plan.SourceSelectedRoot),
                    StringComparison.OrdinalIgnoreCase))
                throw StalePlan();
            targetStorage =
                _providerPolicy.ProbeCreateTargetAndEnsureSupported(
                    plan.TargetSelectedRoot);
            if (targetStorage.StorageKind != plan.TargetStorageKind
                || targetStorage.CoordinationStrength
                    != plan.TargetCoordinationStrength
                || targetStorage.StorageKind != WorkspaceStorageKind.Fixed)
                throw StalePlan();
            if (plan.Phase == "previewed")
            {
                // Protection is allowed to create a snapshot. Seal the copy
                // fingerprint only after protection/drain/stop and while the
                // cross-process writer fence is held.
                WorkspaceStoragePlan sealedCopy = _storage.PreviewMove(
                    plan.SourceSelectedRoot,
                    plan.TargetSelectedRoot);
                plan = plan with
                {
                    CopyPlan = sealedCopy,
                    Phase = "sealed",
                };
                WritePlan(plan);
                _failureInjector.Checkpoint("after-seal");
            }
            _storage.ApplyMove(plan.CopyPlan);
            _failureInjector.Checkpoint("after-copy");
            WorkspaceStorageObservation publishStorage =
                _providerPolicy.ProbeAndEnsureSupported(
                    plan.TargetSelectedRoot);
            if (publishStorage.StorageKind != plan.TargetStorageKind
                || publishStorage.CoordinationStrength
                    != plan.TargetCoordinationStrength
                || publishStorage.StorageKind != WorkspaceStorageKind.Fixed)
                throw StalePlan();
            WorkspaceRegistryEntryV2 updated = _registry.Relink(
                plan.WorkspaceId,
                plan.TargetSelectedRoot,
                activityRoot: null,
                publishStorage);
            _failureInjector.Checkpoint("after-registry-publish");
            TryDeletePlan(plan.PlanId);
            return JsonSerializer.SerializeToElement(new
            {
                workspaceId = plan.WorkspaceId.ToString("D"),
                status = "applied",
                storage = BuildStorageProjection(updated),
            });
        }
        finally
        {
            _gate.Release();
        }
    }

    private async Task<JsonElement> ApplyConversionAsync(
        DurableStoragePlan plan,
        string confirmation,
        CancellationToken cancellationToken)
    {
        if (_replicas is null ||
            plan.ExpiresAt <= DateTimeOffset.UtcNow ||
            plan.SourceMode == plan.TargetMode)
            throw StalePlan();
        RequireConfirmation(confirmation, plan.DisplayName);
        WorkspaceRegistryEntryV2 current = RequiredWorkspace(plan.WorkspaceId);
        JsonElement? completed = CompletePublishedConversion(plan, current);
        if (completed is not null)
            return completed.Value;
        RequireSourceStillCurrent(plan, current);
        string runtimeRoot =
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(current);
        await using WorkspaceStorageMaintenanceLease maintenance =
            WorkspaceStorageMaintenanceLease.Acquire(
                runtimeRoot,
                plan.WorkspaceId);
        if (_sessions.Current.WorkspaceId == plan.WorkspaceId)
            _ = await _sessions.CloseAsync(
                    "workspace-storage-convert",
                    cancellationToken)
                .ConfigureAwait(false);
        await maintenance.AcquireWriterFenceAsync(
                runtimeRoot,
                cancellationToken)
            .ConfigureAwait(false);
        current = RequiredWorkspace(plan.WorkspaceId);
        if (plan.SourceMode == "direct" && plan.TargetMode == "mirrored")
        {
            if (current.ActivityRoot is not null ||
                !SamePath(current.SelectedRoot, plan.SourceSelectedRoot))
                throw StalePlan();
            bool topologyPublished = false;
            try
            {
                if (plan.Phase == "previewed")
                {
                    WorkspaceLayout.RewriteStorageMode(
                        current.SelectedRoot,
                        current.WorkspaceId,
                        WorkspaceStorageMode.Mirrored);
                    if (!Directory.Exists(plan.TargetSelectedRoot) ||
                        !Directory.EnumerateFileSystemEntries(
                            plan.TargetSelectedRoot).Any())
                    {
                        WorkspaceLayout.CreateReplicaRoot(
                            plan.TargetSelectedRoot,
                            current.SelectedRoot,
                            current.WorkspaceId);
                    }
                    else
                    {
                        WorkspaceManifestV2 prepared =
                            WorkspaceLayout.ReadManifest(
                                plan.TargetSelectedRoot);
                        if (prepared.WorkspaceId != current.WorkspaceId ||
                            prepared.StorageMode !=
                                WorkspaceStorageMode.Mirrored)
                            throw StalePlan();
                    }
                    plan = plan with
                    {
                        Phase = "replicaPrepared",
                        ReplicaReceipt = null,
                    };
                    WritePlan(plan);
                    _failureInjector.Checkpoint("after-replica-prepare");
                }
                else if (plan.Phase is not (
                             "replicaPrepared" or "replicaVerified"))
                {
                    throw StalePlan();
                }
                if (WorkspaceLayout.ReadManifest(current.SelectedRoot)
                        .StorageMode != WorkspaceStorageMode.Mirrored ||
                    WorkspaceLayout.ReadManifest(plan.TargetSelectedRoot)
                        .StorageMode != WorkspaceStorageMode.Mirrored)
                    throw StalePlan();
                var candidate = current with
                {
                    SelectedRoot = plan.TargetSelectedRoot,
                    ActivityRoot = current.SelectedRoot,
                    StorageKind = plan.TargetStorageKind,
                    CoordinationStrength =
                        WorkspaceCoordinationStrength.Advisory,
                    PendingSync = true,
                };
                WorkspaceReplicaReceipt receipt = plan.Phase == "replicaVerified"
                    ? await _replicas.VerifyAsync(
                        candidate,
                        cancellationToken).ConfigureAwait(false)
                    : await _replicas.InitializeAsync(
                        candidate,
                        cancellationToken).ConfigureAwait(false);
                plan = plan with
                {
                    Phase = "replicaVerified",
                    ReplicaReceipt = receipt,
                };
                WritePlan(plan);
                _failureInjector.Checkpoint("after-replica-verify");
                WorkspaceStorageObservation storage =
                    _providerPolicy.ProbeAndEnsureSupported(
                        plan.TargetSelectedRoot);
                WorkspaceRegistryEntryV2 updated = _registry.Relink(
                    plan.WorkspaceId,
                    plan.TargetSelectedRoot,
                    current.SelectedRoot,
                    storage with
                    {
                        CoordinationStrength =
                            WorkspaceCoordinationStrength.Advisory,
                    });
                topologyPublished = true;
                _failureInjector.Checkpoint(
                    "after-conversion-registry-publish");
                updated = _registry.UpdateHealth(
                    plan.WorkspaceId,
                    new WorkspaceHealthObservation(
                        WorkspaceHealth.Healthy,
                        PendingSync: false,
                        LastSnapshotAt: receipt.VerifiedAt,
                        LastSyncAt: receipt.VerifiedAt));
                TryDeletePlan(plan.PlanId);
                return AppliedProjection(updated);
            }
            catch
            {
                if (topologyPublished)
                    throw;
                WorkspaceLayout.RewriteStorageMode(
                    current.SelectedRoot,
                    current.WorkspaceId,
                    WorkspaceStorageMode.Direct);
                try
                {
                    WorkspaceLayout.DeleteWorkspaceRoot(
                        plan.TargetSelectedRoot,
                        current.WorkspaceId);
                }
                catch (Exception exception) when (
                    exception is IOException
                        or UnauthorizedAccessException
                        or WorkspaceRegistryException)
                {
                    // Preserve unexpected target state for diagnosis.
                }
                plan = plan with
                {
                    Phase = "previewed",
                    ReplicaReceipt = null,
                };
                WritePlan(plan);
                throw;
            }
        }
        if (plan.SourceMode == "mirrored" && plan.TargetMode == "direct")
        {
            if (current.ActivityRoot is null ||
                !SamePath(current.SelectedRoot, plan.SourceSelectedRoot))
                throw StalePlan();
            if (current.PendingSync)
                throw new WorkspaceRegistryException(
                    "workspace.storage_pending_sync",
                    "Synchronize the workspace before converting it to direct storage.");
            WorkspaceReplicaReceipt receipt =
                await _replicas.VerifyAsync(
                    current,
                    cancellationToken).ConfigureAwait(false);
            plan = plan with { ReplicaReceipt = receipt };
            WritePlan(plan);
            WorkspaceStoragePlan sealedCopy;
            if (plan.Phase == "previewed")
            {
                sealedCopy = _storage.PreviewMove(
                    current.ActivityRoot,
                    plan.TargetSelectedRoot);
                plan = plan with { CopyPlan = sealedCopy, Phase = "sealed" };
                WritePlan(plan);
                _failureInjector.Checkpoint("after-conversion-seal");
            }
            else
            {
                sealedCopy = plan.CopyPlan ?? throw StalePlan();
            }
            if (plan.Phase == "sealed")
            {
                _storage.ApplyMove(sealedCopy);
                plan = plan with { Phase = "copied" };
                WritePlan(plan);
                _failureInjector.Checkpoint("after-conversion-copy");
            }
            if (plan.Phase == "copied")
            {
                WorkspaceLayout.RewriteStorageMode(
                    plan.TargetSelectedRoot,
                    current.WorkspaceId,
                    WorkspaceStorageMode.Direct);
                plan = plan with { Phase = "modeUpdated" };
                WritePlan(plan);
                _failureInjector.Checkpoint("after-conversion-mode");
            }
            if (plan.Phase != "modeUpdated")
                throw StalePlan();
            WorkspaceStorageObservation storage =
                _providerPolicy.ProbeAndEnsureSupported(
                    plan.TargetSelectedRoot);
            WorkspaceRegistryEntryV2 updated = _registry.Relink(
                plan.WorkspaceId,
                plan.TargetSelectedRoot,
                activityRoot: null,
                storage);
            _failureInjector.Checkpoint("after-conversion-registry-publish");
            updated = _registry.UpdateHealth(
                plan.WorkspaceId,
                new WorkspaceHealthObservation(
                    WorkspaceHealth.Healthy,
                    PendingSync: false,
                    LastSnapshotAt: receipt.VerifiedAt,
                    LastSyncAt: receipt.VerifiedAt));
            TryDeletePlan(plan.PlanId);
            return AppliedProjection(updated);
        }
        throw StalePlan();
    }

    private async Task<JsonElement> ApplyReleaseAsync(
        DurableStoragePlan plan,
        string confirmation,
        CancellationToken cancellationToken)
    {
        if (_replicas is null ||
            plan.ExpiresAt <= DateTimeOffset.UtcNow)
            throw StalePlan();
        RequireConfirmation(confirmation, plan.DisplayName);
        WorkspaceRegistryEntryV2 current = RequiredWorkspace(plan.WorkspaceId);
        if (current.ActivityRoot is null ||
            !SamePath(current.SelectedRoot, plan.SourceSelectedRoot) ||
            !SamePath(current.ActivityRoot, plan.SourceActivityRoot!))
            throw StalePlan();
        if (!Directory.Exists(current.ActivityRoot))
        {
            WorkspaceReplicaReceipt completedReceipt =
                RequirePlanReceipt(plan);
            if (plan.Phase is not ("replicaVerified" or "cacheDeleted"))
                throw StalePlan();
            if (plan.Phase != "cacheDeleted")
            {
                plan = plan with { Phase = "cacheDeleted" };
                WritePlan(plan);
            }
            WorkspaceRegistryEntryV2 completed = _registry.UpdateHealth(
                plan.WorkspaceId,
                new WorkspaceHealthObservation(
                    WorkspaceHealth.Offline,
                    PendingSync: false,
                    LastSnapshotAt: completedReceipt.VerifiedAt,
                    LastSyncAt: completedReceipt.VerifiedAt));
            TryDeletePlan(plan.PlanId);
            return AppliedProjection(completed);
        }
        string runtimeRoot = current.ActivityRoot;
        await using WorkspaceStorageMaintenanceLease maintenance =
            WorkspaceStorageMaintenanceLease.Acquire(
                runtimeRoot,
                plan.WorkspaceId);
        bool closedActiveSession =
            _sessions.Current.WorkspaceId == plan.WorkspaceId;
        ulong? expectedMutationRevision = null;
        if (closedActiveSession)
        {
            _ = await _sessions.CloseAsync(
                    "workspace-storage-release-cache",
                    cancellationToken,
                    synchronizeReplica: true)
                .ConfigureAwait(false);
            expectedMutationRevision =
                _sessions.LastProtectionMutationRevision
                ?? throw new WorkspaceRegistryException(
                    "workspace.replica_high_watermark_missing",
                    "The synchronized protection high-watermark is unavailable.");
        }
        await maintenance.AcquireWriterFenceAsync(
                runtimeRoot,
                cancellationToken)
            .ConfigureAwait(false);
        current = RequiredWorkspace(plan.WorkspaceId);
        WorkspaceReplicaReceipt receipt;
        if (plan.Phase == "previewed")
        {
            receipt = await _replicas.VerifyAsync(
                    current,
                    cancellationToken).ConfigureAwait(false);
            plan = plan with
            {
                Phase = "replicaVerified",
                ReplicaReceipt = receipt,
            };
            WritePlan(plan);
            _failureInjector.Checkpoint("after-release-replica-verify");
        }
        else if (plan.Phase == "replicaVerified")
        {
            _ = RequirePlanReceipt(plan);
            receipt = await _replicas.VerifyAsync(
                    current,
                    cancellationToken).ConfigureAwait(false);
            plan = plan with { ReplicaReceipt = receipt };
            WritePlan(plan);
        }
        else
        {
            throw StalePlan();
        }
        if (receipt.MutationRevision <
                receipt.RequiredMutationRevision ||
            (expectedMutationRevision is ulong expected &&
             (receipt.RequiredMutationRevision < expected ||
              receipt.MutationRevision < expected)))
            throw new WorkspaceRegistryException(
                "workspace.release_cache_unsafe",
                "The verified replica checkpoint does not cover the local mutation high-watermark.");
        var context = new ReleaseActivityCacheContext(
            SessionClosed: _sessions.Current.WorkspaceId != plan.WorkspaceId,
            ReplicaComplete: true,
            HasPendingSync: false,
            ReplicaReopenVerified: true);
        WorkspaceStoragePlan release = _storage.PreviewReleaseActivityCache(
            current.ActivityRoot!,
            context);
        maintenance.ReleaseWriterFenceForDeletion();
        _storage.ApplyReleaseActivityCache(release, context);
        plan = plan with { Phase = "cacheDeleted" };
        WritePlan(plan);
        _failureInjector.Checkpoint("after-release-cache-delete");
        WorkspaceRegistryEntryV2 updated = _registry.UpdateHealth(
            plan.WorkspaceId,
            new WorkspaceHealthObservation(
                WorkspaceHealth.Offline,
                PendingSync: false,
                LastSnapshotAt: receipt.VerifiedAt,
                LastSyncAt: receipt.VerifiedAt));
        TryDeletePlan(plan.PlanId);
        return AppliedProjection(updated);
    }

    private JsonElement? CompletePublishedConversion(
        DurableStoragePlan plan,
        WorkspaceRegistryEntryV2 current)
    {
        if (!SamePath(current.SelectedRoot, plan.TargetSelectedRoot))
            return null;
        WorkspaceReplicaReceipt receipt = RequirePlanReceipt(plan);
        WorkspaceManifestV2 manifest =
            WorkspaceLayout.ReadManifest(plan.TargetSelectedRoot);
        if (manifest.WorkspaceId != plan.WorkspaceId ||
            !string.Equals(
                manifest.StorageMode.ToString(),
                plan.TargetMode,
                StringComparison.OrdinalIgnoreCase))
            throw StalePlan();
        if (plan.TargetMode == "mirrored" &&
            (current.ActivityRoot is null ||
             !SamePath(current.ActivityRoot, plan.SourceSelectedRoot) ||
             current.CoordinationStrength !=
                WorkspaceCoordinationStrength.Advisory))
            throw StalePlan();
        if (plan.TargetMode == "direct" && current.ActivityRoot is not null)
            throw StalePlan();
        WorkspaceRegistryEntryV2 updated = _registry.UpdateHealth(
            plan.WorkspaceId,
            new WorkspaceHealthObservation(
                WorkspaceHealth.Healthy,
                PendingSync: false,
                LastSnapshotAt: receipt.VerifiedAt,
                LastSyncAt: receipt.VerifiedAt));
        TryDeletePlan(plan.PlanId);
        return AppliedProjection(updated);
    }

    private static void RequireConfirmation(
        string confirmation,
        string displayName)
    {
        if (!string.Equals(
                confirmation,
                displayName,
                StringComparison.Ordinal))
            throw new WorkspaceRegistryException(
                "workspace.storage_confirmation_mismatch",
                "Type the workspace display name to apply this storage plan.");
    }

    private static void RequireSourceStillCurrent(
        DurableStoragePlan plan,
        WorkspaceRegistryEntryV2 current)
    {
        if (!SamePath(current.SelectedRoot, plan.SourceSelectedRoot) ||
            !string.Equals(
                NormalizeNullable(current.ActivityRoot),
                NormalizeNullable(plan.SourceActivityRoot),
                StringComparison.OrdinalIgnoreCase))
            throw StalePlan();
    }

    private static WorkspaceReplicaReceipt RequirePlanReceipt(
        DurableStoragePlan plan)
    {
        WorkspaceReplicaReceipt receipt =
            plan.ReplicaReceipt ?? throw StalePlan();
        if (receipt.WorkspaceId != plan.WorkspaceId ||
            receipt.ReplicaId == Guid.Empty ||
            receipt.SnapshotId == Guid.Empty ||
            receipt.CatalogRevision == 0 ||
            string.IsNullOrWhiteSpace(receipt.CheckpointId) ||
            !receipt.ReceiptHash.StartsWith(
                "sha256:",
                StringComparison.Ordinal) ||
            receipt.ReceiptHash.Length != 71 ||
            receipt.ReceiptHash.AsSpan(7).IndexOfAnyExcept(
                "0123456789abcdef") >= 0)
            throw StalePlan();
        return receipt;
    }

    private static JsonElement PlanProjection(
        DurableStoragePlan plan,
        long bytesToCopy,
        string warning)
        => JsonSerializer.SerializeToElement(new
        {
            planId = plan.PlanId.ToString("D"),
            workspaceId = plan.WorkspaceId.ToString("D"),
            action = plan.Action,
            source = new
            {
                selectedRoot = plan.SourceSelectedRoot,
                activityRoot = plan.SourceActivityRoot,
                mode = plan.SourceMode,
            },
            target = new
            {
                selectedRoot = plan.TargetSelectedRoot,
                activityRoot = plan.TargetActivityRoot,
                mode = plan.TargetMode,
            },
            bytesToCopy,
            requiresClosedSession = true,
            warnings = new[] { warning },
            expiresAt = plan.ExpiresAt,
            verificationReceiptId = (string?)null,
        });

    private static JsonElement AppliedProjection(
        WorkspaceRegistryEntryV2 updated)
        => JsonSerializer.SerializeToElement(new
        {
            workspaceId = updated.WorkspaceId.ToString("D"),
            status = "applied",
            storage = BuildStorageProjection(updated),
        });

    private static bool SamePath(string left, string right)
        => string.Equals(
            Path.GetFullPath(left),
            Path.GetFullPath(right),
            StringComparison.OrdinalIgnoreCase);

    private static string? NormalizeNullable(string? path)
        => string.IsNullOrWhiteSpace(path)
            ? null
            : Path.GetFullPath(path);

    private WorkspaceRegistryEntryV2 RequiredWorkspace(Guid workspaceId)
        => _registry.List().SingleOrDefault(
               entry => entry.WorkspaceId == workspaceId)
           ?? throw new WorkspaceRegistryException(
               "workspace.not_registered",
               "Workspace is not registered on this device.");

    private void WritePlan(DurableStoragePlan plan)
    {
        Directory.CreateDirectory(_plansRoot);
        DurableJsonFile.Write(
            PlanPath(plan.PlanId),
            plan,
            WorkspaceV2Json.StrictOptions);
    }

    private DurableStoragePlan ReadPlan(Guid planId)
    {
        string path = PlanPath(planId);
        if (!File.Exists(path))
            throw StalePlan();
        try
        {
            DurableStoragePlan plan =
                JsonSerializer.Deserialize<DurableStoragePlan>(
                    File.ReadAllText(path),
                    WorkspaceV2Json.StrictOptions)
                ?? throw StalePlan();
            if (plan.FormatVersion != PlanFormatVersion
                || plan.PlanId != planId)
            {
                throw StalePlan();
            }
            return plan;
        }
        catch (WorkspaceRegistryException)
        {
            throw;
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or JsonException)
        {
            throw new WorkspaceRegistryException(
                "workspace.storage_plan_corrupt",
                "The durable storage plan is corrupt.",
                exception);
        }
    }

    private void PurgeExpiredPlans()
    {
        if (!Directory.Exists(_plansRoot))
            return;
        foreach (string path in Directory.EnumerateFiles(
                     _plansRoot,
                     "*.json",
                     SearchOption.TopDirectoryOnly))
        {
            try
            {
                DurableStoragePlan? plan =
                    JsonSerializer.Deserialize<DurableStoragePlan>(
                        File.ReadAllText(path),
                        WorkspaceV2Json.StrictOptions);
                if (plan?.ExpiresAt <= DateTimeOffset.UtcNow)
                    File.Delete(path);
            }
            catch (Exception exception) when (
                exception is IOException
                    or UnauthorizedAccessException
                    or JsonException)
            {
                // Preserve malformed plans for diagnosis; they fail closed.
            }
        }
    }

    private void TryDeletePlan(Guid planId)
    {
        try
        {
            File.Delete(PlanPath(planId));
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // Apply is idempotent: a retained plan can safely finalize again.
        }
    }

    private string PlanPath(Guid planId)
        => Path.Combine(_plansRoot, planId.ToString("D") + ".json");

    private static object BuildStorageProjection(
        WorkspaceRegistryEntryV2 workspace)
    {
        WorkspaceManifestV2 manifest =
            WorkspaceLayout.ReadManifest(workspace.SelectedRoot);
        (long logicalSize, long physicalSize) =
            MainWindow.MeasureWorkspaceStorage(workspace);
        return new
        {
            location = workspace.SelectedRoot,
            activityRoot =
                ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace),
            mode = manifest.StorageMode == WorkspaceStorageMode.Direct
                ? "direct"
                : "mirrored",
            provider = workspace.StorageKind switch
            {
                WorkspaceStorageKind.Fixed => "fixed",
                WorkspaceStorageKind.Network => "network",
                WorkspaceStorageKind.Removable => "removable",
                WorkspaceStorageKind.RegisteredCloud => "registeredCloud",
                WorkspaceStorageKind.UserMarkedSync => "userMarkedSync",
                _ => throw new ArgumentOutOfRangeException(),
            },
            health = workspace.LastKnownHealth switch
            {
                WorkspaceHealth.Healthy => "healthy",
                WorkspaceHealth.Offline => "offline",
                _ => "attention",
            },
            logicalSize,
            physicalSize,
            reclaimableSize = 0L,
            encryption = manifest.EncryptionMode switch
            {
                WorkspaceEncryptionMode.None => "none",
                WorkspaceEncryptionMode.Convenient => "convenient",
                WorkspaceEncryptionMode.Protected => "protected",
                _ => throw new ArgumentOutOfRangeException(),
            },
            keyVersion = manifest.EncryptionMode
                == WorkspaceEncryptionMode.None ? 0 : 1,
            pendingSync = workspace.PendingSync,
            remoteVerified =
                manifest.StorageMode == WorkspaceStorageMode.Direct ||
                (!workspace.PendingSync &&
                 workspace.LastSyncAt is not null),
        };
    }

    private static void RequireExactProperties(
        JsonElement value,
        params string[] expected)
    {
        if (!SnapshotPackageBroker.HasExactProperties(value, expected))
            throw InvalidRequest();
    }

    private static Guid RequiredGuid(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
            && property.ValueKind == JsonValueKind.String
            && Guid.TryParse(property.GetString(), out Guid parsed)
            && parsed != Guid.Empty
                ? parsed
                : throw InvalidRequest();

    private static string RequiredString(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
            && property.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(property.GetString())
                ? property.GetString()!
                : throw InvalidRequest();

    private static WorkspaceRegistryException InvalidRequest()
        => new(
            "workspace.storage_request_invalid",
            "Storage request parameters are invalid.");

    private static WorkspaceRegistryException StalePlan()
        => new(
            "workspace.storage_plan_stale",
            "Storage plan is missing, expired, or no longer current.");

    private static WorkspaceRegistryException ReplicaCapabilityUnavailable()
        => new(
            "workspace.storage_replica_capability_unavailable",
            "A real remote replica with independent reopen-and-roots verification is not available on this device.");

    private sealed record DurableStoragePlan
    {
        public required int FormatVersion { get; init; }
        public required Guid PlanId { get; init; }
        public required Guid WorkspaceId { get; init; }
        public required string DisplayName { get; init; }
        public required string Action { get; init; }
        public required string SourceSelectedRoot { get; init; }
        public required string? SourceActivityRoot { get; init; }
        public required string SourceMode { get; init; }
        public required string TargetSelectedRoot { get; init; }
        public required string? TargetActivityRoot { get; init; }
        public required string TargetMode { get; init; }
        public required WorkspaceStorageKind TargetStorageKind { get; init; }
        public required WorkspaceCoordinationStrength
            TargetCoordinationStrength { get; init; }
        public required WorkspaceStoragePlan? CopyPlan { get; init; }
        public required WorkspaceReplicaReceipt? ReplicaReceipt { get; init; }
        public required string Phase { get; init; }
        public required DateTimeOffset ExpiresAt { get; init; }
    }
}

public interface IWorkspaceStorageFailureInjector
{
    void Checkpoint(string checkpoint);
}

internal sealed class NoopWorkspaceStorageFailureInjector :
    IWorkspaceStorageFailureInjector
{
    public static NoopWorkspaceStorageFailureInjector Instance { get; } =
        new();

    private NoopWorkspaceStorageFailureInjector()
    {
    }

    public void Checkpoint(string checkpoint)
    {
    }
}
