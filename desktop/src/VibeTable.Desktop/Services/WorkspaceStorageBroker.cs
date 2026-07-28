using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Durable Desktop producer for storage changes. The released implementation
/// supports fixed-provider direct-root relocation. Topology conversion and
/// activity-cache release remain fail-closed until an independent Sidecar
/// reopen receipt can be verified.
/// </summary>
public sealed class WorkspaceStorageBroker
{
    private const int PlanFormatVersion = 1;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly WorkspaceRegistry _registry;
    private readonly WorkspaceSessionManager _sessions;
    private readonly WorkspaceProviderPolicy _providerPolicy;
    private readonly WorkspaceStorageManager _storage = new();
    private readonly string _plansRoot;

    public WorkspaceStorageBroker(
        WorkspaceRegistry registry,
        WorkspaceSessionManager sessions,
        WorkspaceProviderPolicy providerPolicy,
        string productDataRoot)
    {
        _registry = registry ?? throw new ArgumentNullException(nameof(registry));
        _sessions = sessions ?? throw new ArgumentNullException(nameof(sessions));
        _providerPolicy = providerPolicy
            ?? throw new ArgumentNullException(nameof(providerPolicy));
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
            if (action is "convertTopology" or "releaseActivityCache")
                throw VerificationUnavailable();
            if (action != "relocate")
                throw InvalidRequest();
            if (RequiredString(parameters, "targetMode") != "direct"
                || parameters.GetProperty("selectedRootGrant").ValueKind
                    != JsonValueKind.String
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
            if (plan.ExpiresAt <= DateTimeOffset.UtcNow
                || plan.Action != "relocate"
                || plan.CopyPlan.PlanId != plan.PlanId
                || plan.CopyPlan.WorkspaceId != plan.WorkspaceId)
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
            if (_sessions.Current.WorkspaceId == plan.WorkspaceId)
            {
                // Apply owns the normal protection/drain/stop transition. A
                // close failure aborts before any target copy is published.
                _ = await _sessions.CloseAsync(
                        "workspace-storage-relocate",
                        cancellationToken)
                    .ConfigureAwait(false);
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
            _storage.ApplyMove(plan.CopyPlan);
            WorkspaceRegistryEntryV2 updated = _registry.Relink(
                plan.WorkspaceId,
                plan.TargetSelectedRoot,
                activityRoot: null,
                targetStorage);
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
            mode = "direct",
            provider = workspace.StorageKind == WorkspaceStorageKind.Fixed
                ? "fixed"
                : throw new WorkspaceRegistryException(
                    "workspace.storage_plan_stale",
                    "The applied provider no longer matches the plan."),
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
            remoteVerified = true,
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

    private static WorkspaceRegistryException VerificationUnavailable()
        => new(
            "workspace.storage_verification_unavailable",
            "This storage action requires an independent Sidecar reopen verification receipt.");

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
        public required WorkspaceStoragePlan CopyPlan { get; init; }
        public required DateTimeOffset ExpiresAt { get; init; }
    }
}
