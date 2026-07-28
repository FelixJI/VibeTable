using System.IO;
using System.Diagnostics;
using System.Text.Json;
using System.Security.AccessControl;
using System.Security.Principal;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Device-global broker for package inspection/import. It creates an isolated
/// target workspace before starting a transient Sidecar, so the zero-workspace
/// shell never depends on a current workspace runtime. Native source paths are
/// retained only in this process and are rebound to the import operation.
/// </summary>
public sealed class SnapshotPackageBroker : IAsyncDisposable
{
    private const string OwnershipMarkerName =
        "snapshot-package-broker.json";
    private const string OwnershipKind =
        "vibetable.snapshot-package-target.v1";
    private const string TransferOwnershipKind =
        "vibetable.snapshot-package-transfer.v1";
    private static readonly TimeSpan OwnershipLeaseDuration =
        TimeSpan.FromMinutes(10);
    private static readonly TimeSpan OwnershipHeartbeatInterval =
        TimeSpan.FromMinutes(1);
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly Func<PocketBaseLaunchOptions> _sidecarOptions;
    private readonly Func<BackendLaunchOptions> _backendOptions;
    private readonly WorkspaceProviderPolicy _providerPolicy;
    private readonly WorkspaceRegistry _registry;
    private readonly WorkspaceSessionManager _sessions;
    private readonly string _managedWorkspaceRoot;
    private readonly string _transferRoot;
    private readonly Dictionary<Guid, PackagePlan> _plans = [];
    private bool _disposed;

    public SnapshotPackageBroker(
        Func<PocketBaseLaunchOptions> sidecarOptions,
        Func<BackendLaunchOptions> backendOptions,
        WorkspaceProviderPolicy providerPolicy,
        WorkspaceRegistry registry,
        WorkspaceSessionManager sessions,
        string productDataRoot)
    {
        _sidecarOptions = sidecarOptions
            ?? throw new ArgumentNullException(nameof(sidecarOptions));
        _backendOptions = backendOptions
            ?? throw new ArgumentNullException(nameof(backendOptions));
        _providerPolicy = providerPolicy
            ?? throw new ArgumentNullException(nameof(providerPolicy));
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _sessions = sessions ?? throw new ArgumentNullException(nameof(sessions));
        ArgumentException.ThrowIfNullOrWhiteSpace(productDataRoot);
        _managedWorkspaceRoot = Path.Combine(
            Path.GetFullPath(productDataRoot),
            "imported-workspaces");
        _transferRoot = Path.Combine(
            Path.GetFullPath(productDataRoot),
            "snapshot-transfers");
        CleanupOwnedOrphans();
        CleanupOwnedTransfers(_transferRoot);
    }

    public async Task<JsonElement> InspectAsync(
        string requestId,
        JsonElement wire,
        JsonElement parameters,
        WorkspaceSidecarPathGrant sourceGrant,
        CancellationToken cancellationToken)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(requestId);
        ArgumentNullException.ThrowIfNull(sourceGrant);
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        WorkspaceRegistryEntryV2? target = null;
        try
        {
            ThrowIfDisposed();
            PurgeExpiredPlans();
            RequireExactProperties(parameters, "pathGrant", "credential");
            target = await CreateTargetAsync(cancellationToken)
                .ConfigureAwait(false);
            WorkspaceV2ForwardResult forwarded =
                await RunWithOwnershipHeartbeatAsync(
                    target,
                    token => RunTransientAsync(
                        target,
                        (gateway, transientToken) => gateway.ForwardAsync(
                            requestId,
                            "snapshot.inspectPackage",
                            wire,
                            parameters,
                            sourceGrant,
                            transientToken),
                        token),
                    cancellationToken).ConfigureAwait(false);
            JsonElement result = RequireSuccessResult(forwarded);
            Guid planId = RequiredGuid(result, "planId");
            DateTimeOffset expiresAt = RequiredTimestamp(
                result,
                "expiresAt");
            if (expiresAt <= DateTimeOffset.UtcNow ||
                !HasExactProperties(
                    result,
                    "planId",
                    "trusted",
                    "workspaceId",
                    "sourceSnapshotId",
                    "snapshotCount",
                    "encrypted",
                    "verified",
                    "expiresAt")
                || !IsBoolean(result, "trusted")
                || !IsBoolean(result, "encrypted")
                || !IsBoolean(result, "verified")
                || !IsNonNegativeInteger(result, "snapshotCount")
                || !IsString(result, "workspaceId"))
                throw InvalidBrokerResponse();
            bool verified = result.GetProperty("verified").GetBoolean();
            Guid? sourceWorkspaceId = null;
            Guid? sourceSnapshotId = RequiredNullableGuid(
                result,
                "sourceSnapshotId");
            if (verified)
            {
                sourceWorkspaceId = RequiredGuid(result, "workspaceId");
                if (sourceSnapshotId is null ||
                    sourceWorkspaceId == target.WorkspaceId)
                    throw InvalidBrokerResponse();
            }
            else if (sourceSnapshotId is not null)
            {
                throw InvalidBrokerResponse();
            }
            _plans.Add(
                planId,
                new PackagePlan(
                    planId,
                    target,
                    expiresAt,
                    sourceWorkspaceId,
                    sourceSnapshotId));
            WriteOwnershipMarker(
                target.SelectedRoot,
                target.WorkspaceId,
                expiresAt);
            target = null;
            return result.Clone();
        }
        finally
        {
            if (target is not null)
                DeleteUnregisteredTarget(target);
            _gate.Release();
        }
    }

    public async Task<JsonElement> ImportAsync(
        string requestId,
        JsonElement wire,
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(requestId);
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            PurgeExpiredPlans();
            RequireExactProperties(
                parameters,
                "planId",
                "credential",
                "targetMode",
                "targetWorkspaceId");
            Guid planId = RequiredGuid(parameters, "planId");
            if (!_plans.TryGetValue(planId, out PackagePlan? plan))
                throw new WorkspaceRegistryException(
                    "snapshot.import_plan_stale",
                    "The inspected package plan is missing or expired.");
            if (RequiredString(parameters, "targetMode") != "newWorkspace"
                || parameters.GetProperty("targetWorkspaceId").ValueKind
                    != JsonValueKind.Null)
            {
                throw new WorkspaceRegistryException(
                    "snapshot.import_target_unsupported",
                    "This broker imports packages as a new workspace.");
            }
            Guid operationId = RequiredGuid(wire, "operationId");
            JsonElement rewritten = JsonSerializer.SerializeToElement(new
            {
                planId = planId.ToString("D"),
                credential = parameters.GetProperty("credential"),
                targetMode = "newWorkspace",
                targetWorkspaceId =
                    plan.Target.WorkspaceId.ToString("D"),
            });
            JsonElement result;
            try
            {
                result = await RunWithOwnershipHeartbeatAsync(
                        plan.Target,
                        token => ImportRestoreAndVerifyAsync(
                            requestId,
                            wire,
                            rewritten,
                            plan.Target,
                            operationId,
                            token),
                        cancellationToken)
                    .ConfigureAwait(false);
                Guid sourceWorkspaceId = RequiredGuid(
                    result,
                    "sourceWorkspaceId");
                Guid sourceSnapshotId = RequiredGuid(
                    result,
                    "sourceSnapshotId");
                if (sourceWorkspaceId == plan.Target.WorkspaceId ||
                    plan.SourceWorkspaceId is Guid inspectedWorkspaceId &&
                    inspectedWorkspaceId != sourceWorkspaceId ||
                    plan.SourceSnapshotId is Guid inspectedSnapshotId &&
                    inspectedSnapshotId != sourceSnapshotId)
                {
                    throw InvalidBrokerResponse();
                }
                WorkspaceManifestV2 manifest =
                    WorkspaceLayout.SetImportProvenance(
                        plan.Target.SelectedRoot,
                        sourceWorkspaceId,
                        sourceSnapshotId);
                if (manifest.WorkspaceId != plan.Target.WorkspaceId)
                    throw InvalidBrokerResponse();
            }
            catch
            {
                _plans.Remove(planId);
                DeleteUnregisteredTarget(plan.Target);
                throw;
            }
            _registry.Register(plan.Target);
            _plans.Remove(planId);
            DeleteOwnershipMarker(plan.Target.SelectedRoot);
            await OpenImportedWorkspaceAsync(
                    plan.Target,
                    cancellationToken)
                .ConfigureAwait(false);
            return result.Clone();
        }
        finally
        {
            _gate.Release();
        }
    }

    public async Task<SnapshotOpenAsNewPlan> StageOpenAsNewAsync(
        WorkspaceV2HttpGateway sourceGateway,
        string requestId,
        JsonElement sourceWire,
        JsonElement parameters,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(sourceGateway);
        ArgumentException.ThrowIfNullOrWhiteSpace(requestId);
        RequireExactProperties(parameters, "snapshotId");
        Guid snapshotId = RequiredGuid(parameters, "snapshotId");
        Guid sourceWorkspaceId = RequiredGuid(
            sourceWire,
            "workspaceId");
        Guid exportOperationId = RequiredGuid(
            sourceWire,
            "operationId");
        string transferDirectory = CreateSecureTransferDirectory();
        string packagePath = Path.Combine(
            transferDirectory,
            $"{snapshotId:D}.vtsnapshot");
        Guid stagedPlanId = Guid.Empty;
        try
        {
            string exportGrantId =
                $"host-path-grant://{Guid.NewGuid():D}";
            JsonElement exportParameters =
                JsonSerializer.SerializeToElement(new
                {
                    snapshotId = snapshotId.ToString("D"),
                    pathGrant = exportGrantId,
                    encryption = "none",
                    recipients = Array.Empty<string>(),
                    credential = (string?)null,
                });
            var exportGrant = new WorkspaceSidecarPathGrant(
                exportGrantId,
                "snapshot.export",
                exportOperationId,
                "snapshot-export",
                packagePath);
            JsonElement exported = RequireSuccessResult(
                await sourceGateway.ForwardAsync(
                        requestId + ":export",
                        "snapshot.export",
                        sourceWire,
                        exportParameters,
                        exportGrant,
                        cancellationToken)
                    .ConfigureAwait(false));
            if (!HasExactProperties(exported, "displayName", "sha256")
                || !IsString(exported, "displayName")
                || !IsString(exported, "sha256")
                || !File.Exists(packagePath))
            {
                throw InvalidBrokerResponse();
            }

            Guid inspectOperationId = Guid.NewGuid();
            JsonElement inspectWire = CreateGlobalWire(
                inspectOperationId,
                sequence: 1);
            string inspectGrantId =
                $"host-path-grant://{Guid.NewGuid():D}";
            JsonElement inspectParameters =
                JsonSerializer.SerializeToElement(new
                {
                    pathGrant = inspectGrantId,
                    credential = (string?)null,
                });
            var inspectGrant = new WorkspaceSidecarPathGrant(
                inspectGrantId,
                "snapshot.inspectPackage",
                inspectOperationId,
                "snapshot-import",
                packagePath);
            JsonElement inspected = await InspectAsync(
                    requestId + ":inspect",
                    inspectWire,
                    inspectParameters,
                    inspectGrant,
                    cancellationToken)
                .ConfigureAwait(false);
            if (!inspected.GetProperty("verified").GetBoolean()
                || inspected.GetProperty("encrypted").GetBoolean()
                || RequiredGuid(inspected, "workspaceId")
                    != sourceWorkspaceId
                || RequiredGuid(inspected, "sourceSnapshotId")
                    != snapshotId)
            {
                throw new WorkspaceRegistryException(
                    "snapshot.open_as_new_inspection_failed",
                    "The native snapshot transfer did not verify.");
            }
            Guid planId = RequiredGuid(inspected, "planId");
            stagedPlanId = planId;
            Guid targetWorkspaceId = await TargetWorkspaceIdAsync(
                    planId,
                    cancellationToken)
                .ConfigureAwait(false);
            return new SnapshotOpenAsNewPlan(
                planId,
                targetWorkspaceId,
                transferDirectory);
        }
        catch
        {
            if (stagedPlanId != Guid.Empty)
            {
                await AbandonPackagePlanAsync(stagedPlanId)
                    .ConfigureAwait(false);
            }
            DeleteOwnedTransfer(transferDirectory);
            throw;
        }
    }

    public async Task<WorkspaceSessionV2> CompleteOpenAsNewAsync(
        SnapshotOpenAsNewPlan plan,
        string requestId,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(plan);
        ArgumentException.ThrowIfNullOrWhiteSpace(requestId);
        try
        {
            Guid importOperationId = Guid.NewGuid();
            JsonElement wire = CreateGlobalWire(
                importOperationId,
                sequence: 2);
            JsonElement parameters =
                JsonSerializer.SerializeToElement(new
                {
                    planId = plan.PackagePlanId.ToString("D"),
                    credential = (string?)null,
                    targetMode = "newWorkspace",
                    targetWorkspaceId = (string?)null,
                });
            _ = await ImportAsync(
                    requestId + ":import",
                    wire,
                    parameters,
                    cancellationToken)
                .ConfigureAwait(false);
            WorkspaceSessionV2 current = _sessions.Current;
            if (current.WorkspaceId != plan.TargetWorkspaceId
                || !current.Writable)
            {
                current = _sessions.Current.WorkspaceId is null
                    ? await _sessions.OpenAsync(
                        plan.TargetWorkspaceId,
                        WorkspaceOpenMode.Writable,
                        cancellationToken).ConfigureAwait(false)
                    : await _sessions.SwitchAsync(
                        plan.TargetWorkspaceId,
                        WorkspaceOpenMode.Writable,
                        cancellationToken).ConfigureAwait(false);
            }
            return current;
        }
        catch
        {
            await AbandonPackagePlanAsync(plan.PackagePlanId)
                .ConfigureAwait(false);
            throw;
        }
        finally
        {
            DeleteOwnedTransfer(plan.TransferDirectory);
        }
    }

    public async ValueTask DisposeAsync()
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (_disposed)
                return;
            _disposed = true;
            foreach (PackagePlan plan in _plans.Values)
                DeleteUnregisteredTarget(plan.Target);
            _plans.Clear();
        }
        finally
        {
            _gate.Release();
            _gate.Dispose();
        }
    }

    private async Task<WorkspaceRegistryEntryV2> CreateTargetAsync(
        CancellationToken cancellationToken)
    {
        Guid workspaceId = Guid.NewGuid();
        string root = Path.Combine(
            _managedWorkspaceRoot,
            workspaceId.ToString("D"));
        string stagingRoot = Path.Combine(
            _managedWorkspaceRoot,
            ".creating-" + workspaceId.ToString("D"));
        WorkspaceStorageObservation storage =
            _providerPolicy.ProbeCreateTargetAndEnsureSupported(root);
        Directory.CreateDirectory(_managedWorkspaceRoot);
        WorkspaceLayoutResult layout;
        string creationJournal = CreationJournalPath(
            _managedWorkspaceRoot,
            workspaceId);
        try
        {
            WriteOwnershipRecord(
                creationJournal,
                workspaceId,
                DateTimeOffset.UtcNow.AddMinutes(30));
            layout = WorkspaceLayout.Create(
                stagingRoot,
                $"导入的工作区 {workspaceId.ToString("N")[..8]}",
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient,
                workspaceId: workspaceId);
            WriteOwnershipMarker(
                stagingRoot,
                workspaceId,
                DateTimeOffset.UtcNow.AddMinutes(30));
            Directory.Move(stagingRoot, root);
            File.Delete(creationJournal);
        }
        catch
        {
            DeleteBrokerRoot(stagingRoot, workspaceId);
            DeleteBrokerRoot(root, workspaceId);
            TryDeleteFile(creationJournal);
            throw;
        }
        var entry = new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = workspaceId,
            DisplayName = layout.Manifest.DisplayName,
            SelectedRoot = root,
            ActivityRoot = null,
            StorageKind = storage.StorageKind,
            CoordinationStrength = storage.CoordinationStrength,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = false,
        };
        try
        {
            await using var factory = new ProductionWorkspaceRuntimeFactory(
                _sidecarOptions,
                _backendOptions);
            var onboarding = new WorkspaceRepositoryOnboardingService(
                _sidecarOptions,
                factory.PrepareRepositoryOnboarding);
            WorkspaceRepositoryInitialization initialized =
                await RunWithOwnershipHeartbeatAsync(
                    entry,
                    token => onboarding.InitializeAsync(entry, token),
                    cancellationToken).ConfigureAwait(false);
            if (initialized.RecoveryKey is not null)
                throw new WorkspaceRegistryException(
                    "snapshot.import_target_invalid",
                    "A transient package target cannot expose recovery material.");
            return entry;
        }
        catch
        {
            DeleteUnregisteredTarget(entry);
            throw;
        }
    }

    private async Task<WorkspaceV2ForwardResult> RunTransientAsync(
        WorkspaceRegistryEntryV2 target,
        Func<
            WorkspaceV2HttpGateway,
            CancellationToken,
            Task<WorkspaceV2ForwardResult>> operation,
        CancellationToken cancellationToken)
    {
        await using var factory = new ProductionWorkspaceRuntimeFactory(
            _sidecarOptions,
            _backendOptions,
            [target]);
        ulong epoch = checked(factory.InitialSessionEpoch + 1);
        await using IWorkspaceRuntime runtime = factory.Create(target, epoch);
        await runtime.StartAsync(
            WorkspaceOpenMode.Writable,
            cancellationToken).ConfigureAwait(false);
        try
        {
            await runtime.VerifyAsync(cancellationToken).ConfigureAwait(false);
            WorkspaceV2HttpGateway gateway =
                factory.CurrentV2Gateway
                ?? throw new WorkspaceRegistryException(
                    "snapshot.package_runtime_unavailable",
                    "The transient package runtime is unavailable.");
            return await operation(gateway, cancellationToken)
                .ConfigureAwait(false);
        }
        finally
        {
            await runtime.StopAsync(CancellationToken.None)
                .ConfigureAwait(false);
        }
    }

    private async Task<T> RunWithOwnershipHeartbeatAsync<T>(
        WorkspaceRegistryEntryV2 target,
        Func<CancellationToken, Task<T>> operation,
        CancellationToken cancellationToken)
    {
        using Process process = Process.GetCurrentProcess();
        int ownerProcessId = process.Id;
        DateTimeOffset ownerStartedAt =
            process.StartTime.ToUniversalTime();
        if (!RenewOwnershipMarker(
                target.SelectedRoot,
                target.WorkspaceId,
                DateTimeOffset.UtcNow,
                OwnershipLeaseDuration,
                ownerProcessId,
                ownerStartedAt))
        {
            throw new WorkspaceRegistryException(
                "snapshot.package_ownership_lost",
                "The package target ownership lease could not be established.");
        }

        using var heartbeatStop = new CancellationTokenSource();
        using var operationStop =
            CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        Task heartbeat = MaintainOwnershipHeartbeatAsync(
            target,
            ownerProcessId,
            ownerStartedAt,
            heartbeatStop.Token);
        Task<T> work;
        try
        {
            work = operation(operationStop.Token)
                ?? throw new InvalidOperationException(
                    "The owned package operation returned no task.");
        }
        catch
        {
            heartbeatStop.Cancel();
            try
            {
                await heartbeat.ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                // Normal shutdown after synchronous operation setup failed.
            }
            throw;
        }
        Task completed = await Task.WhenAny(work, heartbeat)
            .ConfigureAwait(false);
        if (completed == heartbeat)
        {
            operationStop.Cancel();
            Exception? heartbeatFailure = null;
            try
            {
                await heartbeat.ConfigureAwait(false);
            }
            catch (Exception exception)
            {
                heartbeatFailure = exception;
            }
            try
            {
                _ = await work.ConfigureAwait(false);
            }
            catch when (heartbeatFailure is not null)
            {
                // The ownership failure is the primary safety boundary.
            }
            if (heartbeatFailure is not null)
                throw heartbeatFailure;
            return await work.ConfigureAwait(false);
        }

        heartbeatStop.Cancel();
        try
        {
            await heartbeat.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // Normal shutdown after the owned operation completed.
        }
        return await work.ConfigureAwait(false);
    }

    private static async Task MaintainOwnershipHeartbeatAsync(
        WorkspaceRegistryEntryV2 target,
        int ownerProcessId,
        DateTimeOffset ownerStartedAt,
        CancellationToken cancellationToken)
    {
        using var timer = new PeriodicTimer(OwnershipHeartbeatInterval);
        while (await timer.WaitForNextTickAsync(cancellationToken)
                   .ConfigureAwait(false))
        {
            if (!RenewOwnershipMarker(
                    target.SelectedRoot,
                    target.WorkspaceId,
                    DateTimeOffset.UtcNow,
                    OwnershipLeaseDuration,
                    ownerProcessId,
                    ownerStartedAt))
            {
                throw new WorkspaceRegistryException(
                    "snapshot.package_ownership_lost",
                    "The package target ownership lease could not be renewed.");
            }
        }
    }

    private async Task<JsonElement> ImportRestoreAndVerifyAsync(
        string requestId,
        JsonElement globalWire,
        JsonElement importParameters,
        WorkspaceRegistryEntryV2 target,
        Guid importOperationId,
        CancellationToken cancellationToken)
    {
        await using var factory = new ProductionWorkspaceRuntimeFactory(
            _sidecarOptions,
            _backendOptions,
            [target]);
        ulong importEpoch = checked(factory.InitialSessionEpoch + 1);
        Guid snapshotId;
        JsonElement importedResult = default;
        await using (IWorkspaceRuntime runtime =
                     factory.Create(target, importEpoch))
        {
            await runtime.StartAsync(
                WorkspaceOpenMode.Writable,
                cancellationToken).ConfigureAwait(false);
            try
            {
                await runtime.VerifyAsync(cancellationToken)
                    .ConfigureAwait(false);
                RequireRuntimeMethods(
                    factory,
                    "snapshot.import",
                    "snapshot.previewRestore",
                    "snapshot.applyRestore");
                WorkspaceV2HttpGateway gateway = RequireGateway(factory);
                JsonElement imported = RequireSuccessResult(
                    await gateway.ForwardAsync(
                            requestId,
                            "snapshot.import",
                            globalWire,
                            importParameters,
                            pathGrant: null,
                            cancellationToken)
                        .ConfigureAwait(false));
                if (!HasExactProperties(
                        imported,
                        "operationId",
                        "snapshotId",
                        "sourceWorkspaceId",
                        "sourceSnapshotId",
                        "state")
                    || RequiredGuid(imported, "operationId")
                        != importOperationId
                    || RequiredString(imported, "state")
                        != "restoreRequired")
                {
                    throw InvalidBrokerResponse();
                }
                snapshotId = RequiredGuid(imported, "snapshotId");
                Guid sourceWorkspaceId = RequiredGuid(
                    imported,
                    "sourceWorkspaceId");
                _ = RequiredGuid(imported, "sourceSnapshotId");
                if (sourceWorkspaceId == target.WorkspaceId)
                    throw InvalidBrokerResponse();
                importedResult = imported.Clone();

                Guid previewOperationId = Guid.NewGuid();
                JsonElement previewWire = CreateWorkspaceWire(
                    target.WorkspaceId,
                    importEpoch,
                    previewOperationId,
                    sequence: 1);
                JsonElement previewParameters =
                    JsonSerializer.SerializeToElement(new
                    {
                        snapshotId = snapshotId.ToString("D"),
                        targetMode = "currentWorkspace",
                    });
                JsonElement preview = RequireSuccessResult(
                    await gateway.ForwardAsync(
                            requestId + ":restore-preview",
                            "snapshot.previewRestore",
                            previewWire,
                            previewParameters,
                            pathGrant: null,
                            cancellationToken)
                        .ConfigureAwait(false));
                if (!HasExactProperties(
                        preview,
                        "planId",
                        "protectionRequired",
                        "changes")
                    || !IsBoolean(preview, "protectionRequired")
                    || preview.GetProperty("protectionRequired")
                        .ValueKind != JsonValueKind.True
                    || preview.GetProperty("changes").ValueKind
                        != JsonValueKind.Array)
                {
                    throw InvalidBrokerResponse();
                }
                Guid restorePlanId = RequiredGuid(preview, "planId");

                Guid restoreOperationId = Guid.NewGuid();
                JsonElement applyWire = CreateWorkspaceWire(
                    target.WorkspaceId,
                    importEpoch,
                    restoreOperationId,
                    sequence: 2);
                JsonElement applyParameters =
                    JsonSerializer.SerializeToElement(new
                    {
                        planId = restorePlanId.ToString("D"),
                        confirmed = true,
                    });
                JsonElement prepared = RequireSuccessResult(
                    await gateway.ForwardAsync(
                            requestId + ":restore-apply",
                            "snapshot.applyRestore",
                            applyWire,
                            applyParameters,
                            pathGrant: null,
                            cancellationToken)
                        .ConfigureAwait(false));
                if (!HasExactProperties(prepared, "operationId", "state")
                    || RequiredGuid(prepared, "operationId")
                        != restoreOperationId
                    || RequiredString(prepared, "state") != "prepared")
                {
                    throw InvalidBrokerResponse();
                }
            }
            finally
            {
                await runtime.StopAsync(CancellationToken.None)
                    .ConfigureAwait(false);
            }
        }

        ulong verifyEpoch = checked(importEpoch + 1);
        await using (IWorkspaceRuntime runtime =
                     factory.Create(target, verifyEpoch))
        {
            await runtime.StartAsync(
                WorkspaceOpenMode.Writable,
                cancellationToken).ConfigureAwait(false);
            try
            {
                // Startup atomically installs the pending restore, completes
                // its audit/recovery snapshot, and rolls it back on failure.
                await runtime.VerifyAsync(cancellationToken)
                    .ConfigureAwait(false);
                RequireRuntimeMethods(factory, "repository.verify");
                JsonElement verifyWire = CreateWorkspaceWire(
                    target.WorkspaceId,
                    verifyEpoch,
                    Guid.NewGuid(),
                    sequence: 3);
                JsonElement verified = RequireSuccessResult(
                    await RequireGateway(factory).ForwardAsync(
                            requestId + ":repository-verify",
                            "repository.verify",
                            verifyWire,
                            JsonSerializer.SerializeToElement(new { }),
                            pathGrant: null,
                            cancellationToken)
                        .ConfigureAwait(false));
                if (!HasExactProperties(
                        verified,
                        "state",
                        "snapshotCount",
                        "objectCount",
                        "corruptSnapshotIds")
                    || RequiredString(verified, "state") != "verified"
                    || !IsNonNegativeInteger(verified, "snapshotCount")
                    || !IsNonNegativeInteger(verified, "objectCount")
                    || verified.GetProperty("corruptSnapshotIds").ValueKind
                        != JsonValueKind.Array
                    || verified.GetProperty("corruptSnapshotIds")
                        .GetArrayLength() != 0)
                {
                    throw new WorkspaceRegistryException(
                        "snapshot.import_verification_failed",
                        "The imported workspace repository did not verify.");
                }
            }
            finally
            {
                await runtime.StopAsync(CancellationToken.None)
                    .ConfigureAwait(false);
            }
        }

        return importedResult;
    }

    private async Task OpenImportedWorkspaceAsync(
        WorkspaceRegistryEntryV2 target,
        CancellationToken cancellationToken)
    {
        try
        {
            if (_sessions.Current.WorkspaceId is null)
            {
                _ = await _sessions.OpenAsync(
                    target.WorkspaceId,
                    WorkspaceOpenMode.Writable,
                    cancellationToken).ConfigureAwait(false);
            }
            else
            {
                _ = await _sessions.SwitchAsync(
                    target.WorkspaceId,
                    WorkspaceOpenMode.Writable,
                    cancellationToken).ConfigureAwait(false);
            }
        }
        catch
        {
            // Restore and registration already crossed their durable
            // publication boundary. Keep the workspace discoverable so the
            // user can retry opening it from Workspace Center.
        }
    }

    private async Task<Guid> TargetWorkspaceIdAsync(
        Guid planId,
        CancellationToken cancellationToken)
    {
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            PurgeExpiredPlans();
            return _plans.TryGetValue(planId, out PackagePlan? plan)
                ? plan.Target.WorkspaceId
                : throw new WorkspaceRegistryException(
                    "snapshot.import_plan_stale",
                    "The inspected package plan is missing or expired.");
        }
        finally
        {
            _gate.Release();
        }
    }

    private async Task AbandonPackagePlanAsync(Guid planId)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (_plans.Remove(planId, out PackagePlan? plan))
                DeleteUnregisteredTarget(plan.Target);
        }
        finally
        {
            _gate.Release();
        }
    }

    private string CreateSecureTransferDirectory()
    {
        Directory.CreateDirectory(_transferRoot);
        Guid transferId = Guid.NewGuid();
        string directory = Path.Combine(
            _transferRoot,
            transferId.ToString("D"));
        Directory.CreateDirectory(directory);
        try
        {
            WindowsIdentity identity = WindowsIdentity.GetCurrent();
            SecurityIdentifier owner = identity.User
                ?? throw new WorkspaceRegistryException(
                    "snapshot.transfer_acl_failed",
                    "The current Windows user has no security identifier.");
            var security = new DirectorySecurity();
            security.SetOwner(owner);
            security.SetAccessRuleProtection(
                isProtected: true,
                preserveInheritance: false);
            security.AddAccessRule(new FileSystemAccessRule(
                owner,
                FileSystemRights.FullControl,
                InheritanceFlags.ContainerInherit
                    | InheritanceFlags.ObjectInherit,
                PropagationFlags.None,
                AccessControlType.Allow));
            new DirectoryInfo(directory).SetAccessControl(security);
            WriteOwnershipRecord(
                TransferMarkerPath(directory),
                transferId,
                DateTimeOffset.UtcNow.AddMinutes(30),
                kind: TransferOwnershipKind);
            return directory;
        }
        catch
        {
            try
            {
                Directory.Delete(directory, recursive: true);
            }
            catch
            {
                // Preserve the original ACL/setup failure.
            }
            throw;
        }
    }

    private void CleanupOwnedTransfers(string transferRoot)
    {
        if (!Directory.Exists(transferRoot))
            return;
        foreach (string candidate in Directory.EnumerateDirectories(
                     transferRoot,
                     "*",
                     SearchOption.TopDirectoryOnly))
        {
            try
            {
                var directory = new DirectoryInfo(candidate);
                if ((directory.Attributes & FileAttributes.ReparsePoint) != 0
                    || !Guid.TryParse(directory.Name, out Guid transferId)
                    || !TryReadOwnershipRecord(
                        TransferMarkerPath(candidate),
                        transferId,
                        out BrokerOwnershipMarker ownership,
                        TransferOwnershipKind)
                    || ShouldPreserveOwnership(
                        ownership,
                        DateTimeOffset.UtcNow,
                        ProbeOwnerLiveness))
                {
                    continue;
                }
                DeleteOwnedTransfer(candidate);
            }
            catch (Exception exception) when (
                exception is IOException
                    or UnauthorizedAccessException
                    or JsonException
                    or System.ComponentModel.Win32Exception)
            {
                // Ambiguous or inaccessible transfers are preserved.
            }
        }
    }

    private void DeleteOwnedTransfer(string directory)
    {
        try
        {
            string normalized = Path.GetFullPath(directory);
            string? parent = Directory.GetParent(normalized)?.FullName;
            if (!string.Equals(
                    parent,
                    Path.GetFullPath(_transferRoot),
                    StringComparison.OrdinalIgnoreCase)
                || !Guid.TryParse(
                    Path.GetFileName(normalized),
                    out Guid transferId)
                || !TryReadOwnershipRecord(
                    TransferMarkerPath(normalized),
                    transferId,
                    out _,
                    TransferOwnershipKind))
            {
                return;
            }
            DeleteTreeWithoutFollowingReparsePoints(normalized);
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or JsonException)
        {
            // Best-effort cleanup of a marker-proven transfer.
        }
    }

    private static void DeleteTreeWithoutFollowingReparsePoints(
        string directory)
    {
        var root = new DirectoryInfo(directory);
        if ((root.Attributes & FileAttributes.ReparsePoint) != 0)
            throw new IOException("Refusing to traverse a reparse point.");
        foreach (FileInfo file in root.EnumerateFiles())
        {
            if ((file.Attributes & FileAttributes.ReparsePoint) != 0)
                throw new IOException("Refusing to delete a reparse point.");
            file.Delete();
        }
        foreach (DirectoryInfo child in root.EnumerateDirectories())
            DeleteTreeWithoutFollowingReparsePoints(child.FullName);
        root.Delete();
    }

    private static string TransferMarkerPath(string directory)
        => Path.Combine(directory, "snapshot-transfer-owner.json");

    private static JsonElement CreateGlobalWire(
        Guid operationId,
        ulong sequence)
        => JsonSerializer.SerializeToElement(new
        {
            scope = "global",
            operationId = operationId.ToString("D"),
            sequence,
        });

    private static JsonElement CreateWorkspaceWire(
        Guid workspaceId,
        ulong sessionEpoch,
        Guid operationId,
        ulong sequence)
        => JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            operationId = operationId.ToString("D"),
            sequence,
            workspaceId = workspaceId.ToString("D"),
            sessionEpoch,
        });

    private static WorkspaceV2HttpGateway RequireGateway(
        ProductionWorkspaceRuntimeFactory factory)
        => factory.CurrentV2Gateway
           ?? throw new WorkspaceRegistryException(
               "snapshot.package_runtime_unavailable",
               "The transient package runtime is unavailable.");

    private static void RequireRuntimeMethods(
        ProductionWorkspaceRuntimeFactory factory,
        params string[] methods)
    {
        if (factory.CurrentCapabilities is null
            || methods.Any(method =>
                !factory.CurrentCapabilities.RpcMethods.Contains(
                    method,
                    StringComparer.Ordinal)))
        {
            throw new WorkspaceRegistryException(
                "workspace.capability_unavailable",
                "The transient runtime does not support the package restore workflow.");
        }
    }

    private void PurgeExpiredPlans()
    {
        DateTimeOffset now = DateTimeOffset.UtcNow;
        foreach (Guid planId in _plans
                     .Where(pair => pair.Value.ExpiresAt <= now)
                     .Select(pair => pair.Key)
                     .ToArray())
        {
            PackagePlan plan = _plans[planId];
            _plans.Remove(planId);
            DeleteUnregisteredTarget(plan.Target);
        }
    }

    private void CleanupOwnedOrphans()
    {
        IReadOnlySet<Guid> registered;
        try
        {
            registered = _registry.List()
                .Select(entry => entry.WorkspaceId)
                .ToHashSet();
        }
        catch (WorkspaceRegistryException)
        {
            // A corrupt registry makes ownership ambiguous. Fail closed and
            // preserve every root until the registry is repaired.
            return;
        }
        CleanupOwnedOrphans(_managedWorkspaceRoot, registered);
    }

    internal static void CleanupOwnedOrphans(
        string managedRoot,
        IReadOnlySet<Guid> registeredWorkspaceIds)
        => CleanupOwnedOrphans(
            managedRoot,
            registeredWorkspaceIds,
            DateTimeOffset.UtcNow,
            ProbeOwnerLiveness);

    internal static void CleanupOwnedOrphans(
        string managedRoot,
        IReadOnlySet<Guid> registeredWorkspaceIds,
        DateTimeOffset now,
        Func<int, DateTimeOffset, BrokerOwnerLiveness> probeOwnerLiveness)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(managedRoot);
        ArgumentNullException.ThrowIfNull(registeredWorkspaceIds);
        ArgumentNullException.ThrowIfNull(probeOwnerLiveness);
        if (!Directory.Exists(managedRoot))
            return;
        foreach (string journal in Directory.EnumerateFiles(
                     managedRoot,
                     ".creating-*.broker.json",
                     SearchOption.TopDirectoryOnly))
        {
            try
            {
                string fileName = Path.GetFileName(journal);
                string identityText = fileName[
                    ".creating-".Length
                    ..^".broker.json".Length];
                if (!Guid.TryParse(identityText, out Guid workspaceId)
                    || workspaceId == Guid.Empty
                    || registeredWorkspaceIds.Contains(workspaceId)
                    || !TryReadOwnershipRecord(
                        journal,
                        workspaceId,
                        out BrokerOwnershipMarker ownership,
                        now)
                    || ShouldPreserveOwnership(
                        ownership,
                        now,
                        probeOwnerLiveness))
                {
                    continue;
                }
                string staging = Path.Combine(
                    managedRoot,
                    ".creating-" + workspaceId.ToString("D"));
                DeleteBrokerRoot(staging, workspaceId);
                File.Delete(journal);
            }
            catch (Exception exception) when (
                exception is IOException
                    or UnauthorizedAccessException
                    or WorkspaceRegistryException
                    or JsonException)
            {
                // Preserve ambiguous journals and roots.
            }
        }
        foreach (string candidate in Directory.EnumerateDirectories(
                     managedRoot,
                     "*",
                     SearchOption.TopDirectoryOnly))
        {
            try
            {
                var directory = new DirectoryInfo(candidate);
                if ((directory.Attributes & FileAttributes.ReparsePoint) != 0)
                    continue;
                string name = directory.Name;
                bool staging = name.StartsWith(
                    ".creating-",
                    StringComparison.Ordinal);
                string identityText = staging
                    ? name[".creating-".Length..]
                    : name;
                if (!Guid.TryParse(identityText, out Guid workspaceId)
                    || workspaceId == Guid.Empty
                    || registeredWorkspaceIds.Contains(workspaceId))
                {
                    continue;
                }
                WorkspaceManifestV2 manifest =
                    WorkspaceLayout.ReadManifest(candidate);
                if (manifest.WorkspaceId != workspaceId)
                    continue;
                if (!TryReadOwnershipMarker(
                        candidate,
                        workspaceId,
                        out BrokerOwnershipMarker? ownership,
                        now)
                    || ShouldPreserveOwnership(
                        ownership,
                        now,
                        probeOwnerLiveness))
                {
                    continue;
                }
                WorkspaceLayout.DeleteWorkspaceRoot(
                    candidate,
                    workspaceId);
            }
            catch (Exception exception) when (
                exception is IOException
                    or UnauthorizedAccessException
                    or WorkspaceRegistryException
                    or JsonException)
            {
                // Cleanup is intentionally conservative: malformed markers,
                // unexpected identities, locked roots, and provider failures
                // are preserved for diagnosis.
            }
        }
    }

    internal static void WriteOwnershipMarker(
        string root,
        Guid workspaceId,
        DateTimeOffset expiresAt,
        int? ownerProcessId = null,
        DateTimeOffset? ownerStartedAt = null)
    {
        WriteOwnershipRecord(
            OwnershipMarkerPath(root),
            workspaceId,
            expiresAt,
            ownerProcessId,
            ownerStartedAt);
    }

    internal static bool RenewOwnershipMarker(
        string root,
        Guid workspaceId,
        DateTimeOffset renewedAt,
        TimeSpan leaseDuration,
        int ownerProcessId,
        DateTimeOffset ownerStartedAt)
    {
        if (workspaceId == Guid.Empty
            || leaseDuration <= TimeSpan.Zero
            || ownerProcessId <= 0)
        {
            return false;
        }
        string markerPath = OwnershipMarkerPath(root);
        try
        {
            if (!TryReadOwnershipRecord(
                    markerPath,
                    workspaceId,
                    out BrokerOwnershipMarker ownership,
                    renewedAt)
                || ownership.OwnerProcessId != ownerProcessId
                || ownership.OwnerStartedAt != ownerStartedAt)
            {
                return false;
            }
            DurableJsonFile.Write(
                markerPath,
                ownership with
                {
                    HeartbeatAt = renewedAt,
                    ExpiresAt = renewedAt.Add(leaseDuration),
                },
                WorkspaceV2Json.StrictOptions);
            return true;
        }
        catch (Exception exception) when (
            exception is IOException
                or UnauthorizedAccessException
                or JsonException
                or ArgumentOutOfRangeException)
        {
            return false;
        }
    }

    private static void WriteOwnershipRecord(
        string marker,
        Guid workspaceId,
        DateTimeOffset expiresAt,
        int? ownerProcessId = null,
        DateTimeOffset? ownerStartedAt = null,
        string kind = OwnershipKind)
    {
        using Process process = Process.GetCurrentProcess();
        DateTimeOffset now = DateTimeOffset.UtcNow;
        DurableJsonFile.Write(
            marker,
            new BrokerOwnershipMarker
            {
                Kind = kind,
                WorkspaceId = workspaceId,
                CreatedAt = now,
                HeartbeatAt = now,
                ExpiresAt = expiresAt,
                OwnerProcessId = ownerProcessId ?? process.Id,
                OwnerStartedAt = ownerStartedAt
                    ?? process.StartTime.ToUniversalTime(),
            },
            WorkspaceV2Json.StrictOptions);
    }

    private static bool TryReadOwnershipMarker(
        string root,
        Guid workspaceId,
        out BrokerOwnershipMarker ownership)
        => TryReadOwnershipRecord(
            OwnershipMarkerPath(root),
            workspaceId,
            out ownership);

    private static bool TryReadOwnershipMarker(
        string root,
        Guid workspaceId,
        out BrokerOwnershipMarker ownership,
        DateTimeOffset now)
        => TryReadOwnershipRecord(
            OwnershipMarkerPath(root),
            workspaceId,
            out ownership,
            now);

    private static bool TryReadOwnershipRecord(
        string marker,
        Guid workspaceId,
        out BrokerOwnershipMarker ownership,
        string expectedKind = OwnershipKind)
        => TryReadOwnershipRecord(
            marker,
            workspaceId,
            out ownership,
            DateTimeOffset.UtcNow,
            expectedKind);

    private static bool TryReadOwnershipRecord(
        string marker,
        Guid workspaceId,
        out BrokerOwnershipMarker ownership,
        DateTimeOffset now,
        string expectedKind = OwnershipKind)
    {
        ownership = null!;
        if (!File.Exists(marker))
            return false;
        BrokerOwnershipMarker? value =
            JsonSerializer.Deserialize<BrokerOwnershipMarker>(
                File.ReadAllText(marker),
                WorkspaceV2Json.StrictOptions);
        if (value is null
            || value.Kind != expectedKind
            || value.WorkspaceId != workspaceId
            || value.OwnerProcessId <= 0
            || value.CreatedAt > now.AddMinutes(1)
            || value.OwnerStartedAt > value.CreatedAt.AddMinutes(1)
            || value.HeartbeatAt is DateTimeOffset heartbeatAt
                && (heartbeatAt < value.CreatedAt
                    || heartbeatAt > now.AddMinutes(1)
                    || value.ExpiresAt <= heartbeatAt)
            || value.ExpiresAt <= value.CreatedAt)
        {
            return false;
        }
        ownership = value;
        return true;
    }

    private static bool ShouldPreserveOwnership(
        BrokerOwnershipMarker marker,
        DateTimeOffset now,
        Func<int, DateTimeOffset, BrokerOwnerLiveness> probeOwnerLiveness)
    {
        BrokerOwnerLiveness liveness = probeOwnerLiveness(
            marker.OwnerProcessId,
            marker.OwnerStartedAt);
        return liveness == BrokerOwnerLiveness.Alive
               || liveness == BrokerOwnerLiveness.Unknown
               && marker.ExpiresAt > now;
    }

    internal static BrokerOwnerLiveness ProbeOwnerLiveness(
        int ownerProcessId,
        DateTimeOffset ownerStartedAt)
    {
        try
        {
            using Process process =
                Process.GetProcessById(ownerProcessId);
            DateTimeOffset started = process.StartTime.ToUniversalTime();
            return started == ownerStartedAt
                ? BrokerOwnerLiveness.Alive
                : BrokerOwnerLiveness.Dead;
        }
        catch (ArgumentException)
        {
            return BrokerOwnerLiveness.Dead;
        }
        catch (InvalidOperationException)
        {
            return BrokerOwnerLiveness.Dead;
        }
        catch (System.ComponentModel.Win32Exception)
        {
            // Access denial cannot prove that the owner is alive or dead.
            return BrokerOwnerLiveness.Unknown;
        }
    }

    private static void DeleteOwnershipMarker(string root)
    {
        try
        {
            File.Delete(OwnershipMarkerPath(root));
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // The registry UUID is now authoritative. Leaving the marker is
            // safe because startup cleanup never deletes registered roots.
        }
    }

    private static string OwnershipMarkerPath(string root)
        => Path.Combine(
            WorkspaceLayout.Paths(root).Coordination,
            OwnershipMarkerName);

    private static string CreationJournalPath(
        string managedRoot,
        Guid workspaceId)
        => Path.Combine(
            managedRoot,
            $".creating-{workspaceId:D}.broker.json");

    private static void TryDeleteFile(string path)
    {
        try
        {
            File.Delete(path);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            // Best-effort removal of a broker-owned journal/marker.
        }
    }

    private static JsonElement RequireSuccessResult(
        WorkspaceV2ForwardResult forwarded)
    {
        if (forwarded.Error is not null)
            throw new WorkspaceRegistryException(
                forwarded.Error.Code,
                "The Sidecar rejected the snapshot package operation.");
        return forwarded.Result is JsonElement result
            && result.ValueKind == JsonValueKind.Object
                ? result
                : throw InvalidBrokerResponse();
    }

    private static void RequireExactProperties(
        JsonElement value,
        params string[] expected)
    {
        if (!HasExactProperties(value, expected))
            throw new WorkspaceRegistryException(
                "snapshot.request_invalid",
                "Snapshot package params are invalid.");
    }

    internal static bool HasExactProperties(
        JsonElement value,
        params string[] expected)
        => value.ValueKind == JsonValueKind.Object &&
           value.EnumerateObject()
               .Select(property => property.Name)
               .Order(StringComparer.Ordinal)
               .SequenceEqual(
                   expected.Order(StringComparer.Ordinal),
                   StringComparer.Ordinal);

    private static bool IsBoolean(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
           && property.ValueKind is JsonValueKind.True or JsonValueKind.False;

    private static bool IsNonNegativeInteger(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
           && property.ValueKind == JsonValueKind.Number
           && property.TryGetInt32(out int parsed)
           && parsed >= 0;

    private static bool IsString(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
           && property.ValueKind == JsonValueKind.String;

    private static Guid RequiredGuid(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
            && property.ValueKind == JsonValueKind.String
            && Guid.TryParse(property.GetString(), out Guid parsed)
            && parsed != Guid.Empty
                ? parsed
                : throw InvalidBrokerResponse();

    private static Guid? RequiredNullableGuid(
        JsonElement value,
        string name)
    {
        if (!value.TryGetProperty(name, out JsonElement property))
            throw InvalidBrokerResponse();
        if (property.ValueKind == JsonValueKind.Null)
            return null;
        if (property.ValueKind == JsonValueKind.String &&
            Guid.TryParse(property.GetString(), out Guid parsed) &&
            parsed != Guid.Empty)
            return parsed;
        throw InvalidBrokerResponse();
    }

    private static string RequiredString(JsonElement value, string name)
        => value.TryGetProperty(name, out JsonElement property)
            && property.ValueKind == JsonValueKind.String
            && !string.IsNullOrWhiteSpace(property.GetString())
                ? property.GetString()!
                : throw InvalidBrokerResponse();

    private static DateTimeOffset RequiredTimestamp(
        JsonElement value,
        string name)
        => DateTimeOffset.TryParse(
                RequiredString(value, name),
                System.Globalization.CultureInfo.InvariantCulture,
                System.Globalization.DateTimeStyles.RoundtripKind,
                out DateTimeOffset parsed)
            ? parsed
            : throw InvalidBrokerResponse();

    private static WorkspaceRegistryException InvalidBrokerResponse()
        => new(
            "snapshot.package_response_invalid",
            "The Sidecar returned an invalid snapshot package response.");

    private static void DeleteUnregisteredTarget(
        WorkspaceRegistryEntryV2 target)
        => DeleteBrokerRoot(target.SelectedRoot, target.WorkspaceId);

    private static void DeleteBrokerRoot(
        string root,
        Guid workspaceId)
    {
        try
        {
            WorkspaceLayout.DeleteWorkspaceRoot(
                root,
                workspaceId);
        }
        catch
        {
            // Preserve unexpected state. Cleanup never deletes a root whose
            // manifest no longer proves the broker-created UUID.
        }
    }

    private void ThrowIfDisposed()
        => ObjectDisposedException.ThrowIf(_disposed, this);

    private sealed record PackagePlan(
        Guid PlanId,
        WorkspaceRegistryEntryV2 Target,
        DateTimeOffset ExpiresAt,
        Guid? SourceWorkspaceId,
        Guid? SourceSnapshotId);

    private sealed record BrokerOwnershipMarker
    {
        public required string Kind { get; init; }
        public required Guid WorkspaceId { get; init; }
        public required DateTimeOffset CreatedAt { get; init; }
        public DateTimeOffset? HeartbeatAt { get; init; }
        public required DateTimeOffset ExpiresAt { get; init; }
        public required int OwnerProcessId { get; init; }
        public required DateTimeOffset OwnerStartedAt { get; init; }
    }

    internal enum BrokerOwnerLiveness
    {
        Alive,
        Dead,
        Unknown,
    }
}

public sealed record SnapshotOpenAsNewPlan(
    Guid PackagePlanId,
    Guid TargetWorkspaceId,
    string TransferDirectory);
