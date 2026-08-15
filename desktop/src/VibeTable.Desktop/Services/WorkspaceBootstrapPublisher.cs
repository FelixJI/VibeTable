using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

public interface IWorkspaceBootstrapPublisher
{
    void Post();
}

/// <summary>
/// Projects registry, session, capability and storage authority into the one
/// renderer bootstrap notification. Projection failures never invent state.
/// </summary>
public sealed class WorkspaceBootstrapPublisher(
    IWorkspaceProductReplySink reply,
    IWorkspaceProductHost host,
    IWorkspaceProductSessionPort session,
    WorkspaceRegistry workspaceRegistry,
    WorkspaceProviderPolicy providerPolicy,
    IWorkspaceStorageMeter storageMeter) : IWorkspaceBootstrapPublisher
{
    private readonly IWorkspaceProductReplySink _reply = reply
        ?? throw new ArgumentNullException(nameof(reply));
    private readonly IWorkspaceProductHost _host = host
        ?? throw new ArgumentNullException(nameof(host));
    private readonly IWorkspaceProductSessionPort _session = session
        ?? throw new ArgumentNullException(nameof(session));
    private readonly WorkspaceRegistry _workspaceRegistry = workspaceRegistry
        ?? throw new ArgumentNullException(nameof(workspaceRegistry));
    private readonly WorkspaceProviderPolicy _providerPolicy = providerPolicy
        ?? throw new ArgumentNullException(nameof(providerPolicy));
    private readonly IWorkspaceStorageMeter _storageMeter = storageMeter
        ?? throw new ArgumentNullException(nameof(storageMeter));

    public void Post()
    {
        if (!_host.IsRendererReady)
            return;
        IReadOnlyList<WorkspaceRegistryEntryV2> workspaces;
        try
        {
            workspaces = _workspaceRegistry.List();
        }
        catch (WorkspaceRegistryException)
        {
            workspaces = [];
        }
        WorkspaceV2SidecarCapabilities? sidecar =
            _session.CurrentCapabilities;
        HashSet<string> methods = sidecar?.RpcMethods.ToHashSet(
            StringComparer.Ordinal) ?? [];
        List<string> capabilities = BuildCapabilities(methods);
        WorkspaceSessionV2 currentSession = _session.CurrentSession;
        _reply.PostNotification(
            "workspace.v2.bootstrap",
            new
            {
                contractVersion = WorkspaceV2Json.ContractVersion,
                capabilities,
                workspaces = workspaces
                    .Select(WorkspaceProjection.RegistryEntry)
                    .ToArray(),
                session = ToSessionProjection(currentSession),
                snapshots = Array.Empty<object>(),
                storage = BuildStorageProjection(
                    _session.CurrentWorkspace,
                    capabilities.Contains(
                        "repository.settings.v2",
                        StringComparer.Ordinal)),
                retention = (object?)null,
                conflicts = Array.Empty<object>(),
                fileTrees = Array.Empty<object>(),
            });
    }

    private List<string> BuildCapabilities(HashSet<string> methods)
    {
        var capabilities = new List<string>
        {
            "workspace.session.v2",
            "snapshot.package.v2",
            "workspace.storage.relocate.v2",
            "workspace.storage.topology.v2",
            "workspace.storage.release-cache.v2",
        };
        if (_providerPolicy.MirroredCreationEnabled)
            capabilities.Add("workspace.storage.mirrored-create.v2");
        if (ContainsEvery(
                methods,
                "snapshot.request",
                "snapshot.list",
                "snapshot.inspect",
                "snapshot.update",
                "snapshot.previewRestore",
                "snapshot.applyRestore",
                "snapshot.previewExtract",
                "snapshot.applyExtract",
                "snapshot.export",
                "repository.verify"))
        {
            capabilities.Add("snapshot.timeline.v2");
            capabilities.Add("snapshot.open-as-new.v2");
        }
        if (ContainsEvery(
                methods,
                "history.query",
                "history.previewRestore",
                "history.applyRestore"))
            capabilities.Add("history.restore.v2");
        if (ContainsEvery(
                methods,
                "fileHistory.queryDocuments",
                "fileHistory.listPendingChanges",
                "fileHistory.import",
                "fileHistory.relink",
                "fileHistory.unlink",
                "fileHistory.readTree",
                "fileHistory.restore",
                "fileHistory.upgrade",
                "fileHistory.activateLeaf",
                "fileHistory.applyPendingChange"))
            capabilities.Add("fileHistory.tree.v2");
        if (_host.HasDocumentWorkspace &&
            ContainsEvery(
                methods,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod))
            capabilities.Add("document.diff.v1");
        if (ContainsEvery(
                methods,
                "retention.get",
                "retention.status",
                "retention.update",
                "retention.plan",
                "retention.apply"))
            capabilities.Add("retention.policy.v2");
        if (methods.Contains("repository.verify"))
            capabilities.Add("repository.settings.v2");
        if (ContainsEvery(
                methods,
                "repository.previewKeyRotation",
                "repository.applyKeyRotation"))
            capabilities.Add("repository.key-rotation.v2");
        if (ContainsEvery(
                methods,
                "conflict.list",
                "conflict.inspect",
                "conflict.preview",
                "conflict.apply"))
            capabilities.Add("conflict.center.v2");
        return capabilities;
    }

    private object ToStorageProjection(
        WorkspaceRegistryEntryV2 workspace)
    {
        string root = ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(root);
        WorkspaceStorageMeasurement measurement = _storageMeter.Measure(workspace);
        return new
        {
            location = workspace.SelectedRoot,
            activityRoot = root,
            mode = manifest.StorageMode == WorkspaceStorageMode.Direct
                ? "direct"
                : "mirrored",
            provider = WorkspaceProjection.ProviderName(workspace.StorageKind),
            health = workspace.LastKnownHealth switch
            {
                WorkspaceHealth.Healthy => "healthy",
                WorkspaceHealth.Offline => "offline",
                _ => "attention",
            },
            logicalSize = measurement.LogicalSize,
            physicalSize = measurement.PhysicalSize,
            reclaimableSize = 0L,
            encryption = manifest.EncryptionMode switch
            {
                WorkspaceEncryptionMode.None => "none",
                WorkspaceEncryptionMode.Convenient => "convenient",
                WorkspaceEncryptionMode.Protected => "protected",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(manifest.EncryptionMode)),
            },
            keyVersion = manifest.EncryptionMode
                == WorkspaceEncryptionMode.None ? 0 : 1,
            pendingSync = workspace.PendingSync,
            replicaVerified =
                manifest.StorageMode == WorkspaceStorageMode.Direct,
        };
    }

    private object? BuildStorageProjection(
        WorkspaceRegistryEntryV2? workspace,
        bool enabled) =>
        enabled && workspace is not null
            ? ToStorageProjection(workspace)
            : null;

    private static object ToSessionProjection(WorkspaceSessionV2 session)
        => new
        {
            contractVersion = session.ContractVersion,
            workspaceId = session.WorkspaceId?.ToString("D"),
            sessionEpoch = session.SessionEpoch,
            state = WorkspaceProjection.SessionStateName(session.State),
            openMode = session.OpenMode switch
            {
                WorkspaceOpenMode.ReadOnly => "readOnly",
                WorkspaceOpenMode.Writable => "writable",
                WorkspaceOpenMode.Provisional => "provisional",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(session.OpenMode)),
            },
            writable = session.Writable,
            provisional = session.Provisional,
            phase = session.Phase switch
            {
                WorkspaceSessionPhase.Idle => "idle",
                WorkspaceSessionPhase.Protecting => "protecting",
                WorkspaceSessionPhase.Draining => "draining",
                WorkspaceSessionPhase.Stopping => "stopping",
                WorkspaceSessionPhase.Starting => "starting",
                WorkspaceSessionPhase.Binding => "binding",
                WorkspaceSessionPhase.Verifying => "verifying",
                WorkspaceSessionPhase.RollingBack => "rollingBack",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(session.Phase)),
            },
            errorCode = session.ErrorCode,
        };

    private static bool ContainsEvery(
        HashSet<string> methods,
        params string[] required) => required.All(methods.Contains);
}

internal static class WorkspaceProjection
{
    public static object RegistryEntry(WorkspaceRegistryEntryV2 workspace) => new
    {
        contractVersion = workspace.ContractVersion,
        workspaceId = workspace.WorkspaceId.ToString("D"),
        displayName = workspace.DisplayName,
        selectedRoot = workspace.SelectedRoot,
        activityRoot = workspace.ActivityRoot,
        storageKind = ProviderName(workspace.StorageKind),
        coordinationStrength = workspace.CoordinationStrength
            == WorkspaceCoordinationStrength.Strong
                ? "strong"
                : "advisory",
        lastOpenedAt = workspace.LastOpenedAt,
        lastKnownHealth = workspace.LastKnownHealth switch
        {
            WorkspaceHealth.Healthy => "healthy",
            WorkspaceHealth.Offline => "offline",
            WorkspaceHealth.Degraded => "degraded",
            WorkspaceHealth.Corrupt => "corrupt",
            WorkspaceHealth.Unknown => "unknown",
            _ => throw new ArgumentOutOfRangeException(
                nameof(workspace.LastKnownHealth)),
        },
        lastSnapshotAt = workspace.LastSnapshotAt,
        lastSyncAt = workspace.LastSyncAt,
        pendingSync = workspace.PendingSync,
    };

    public static string ProviderName(WorkspaceStorageKind storageKind) =>
        storageKind switch
        {
            WorkspaceStorageKind.Fixed => "fixed",
            WorkspaceStorageKind.Network => "network",
            WorkspaceStorageKind.Removable => "removable",
            WorkspaceStorageKind.RegisteredCloud => "registeredCloud",
            WorkspaceStorageKind.UserMarkedSync => "userMarkedSync",
            _ => throw new ArgumentOutOfRangeException(nameof(storageKind)),
        };

    public static string SessionStateName(WorkspaceSessionState state) =>
        state switch
        {
            WorkspaceSessionState.Closed => "closed",
            WorkspaceSessionState.Opening => "opening",
            WorkspaceSessionState.OpenedReadOnly => "openedReadOnly",
            WorkspaceSessionState.OpenedWritable => "openedWritable",
            WorkspaceSessionState.OpenedProvisional => "openedProvisional",
            WorkspaceSessionState.Switching => "switching",
            WorkspaceSessionState.Failed => "failed",
            _ => throw new ArgumentOutOfRangeException(nameof(state)),
        };
}
