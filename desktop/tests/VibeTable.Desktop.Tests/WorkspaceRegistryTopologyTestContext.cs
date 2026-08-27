using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

internal sealed class WorkspaceRegistryTopologyTestContext : IDisposable
{
    public WorkspaceRegistryTopologyTestContext(string prefix)
    {
        Root = Path.Combine(
            Path.GetTempPath(),
            prefix + Guid.NewGuid().ToString("N"));
        ProductDataRoot = Path.Combine(Root, "product");
        Directory.CreateDirectory(Root);
        Registry = new WorkspaceRegistry(Root);
        Policy = WorkspaceProviderPolicy.CreateForTests(
            new Dictionary<WorkspaceStorageKind, bool>
            {
                [WorkspaceStorageKind.Fixed] = true,
            },
            (_, _, _) =>
            {
                ProbeCount++;
                return new WorkspaceStorageObservation(
                    WorkspaceStorageKind.Fixed,
                    WorkspaceCoordinationStrength.Strong,
                    1024,
                    false,
                    DateTimeOffset.UtcNow);
            });
        Picker = new RecordingWorkspacePathPicker();
        Session = new TopologySessionPort();
        Controller = new WorkspaceRegistryTopologyController(
            Session,
            Registry,
            Policy,
            new SuccessfulOnboardingPort(),
            new NullRecoveryUi(),
            new SuccessfulReplicaRecovery(),
            new WorkspacePathGrantStore(Picker),
            ProductDataRoot,
            Path.Combine(Root, "activity"));
    }

    public string Root { get; }
    public string ProductDataRoot { get; }
    public WorkspaceRegistry Registry { get; }
    public WorkspaceProviderPolicy Policy { get; }
    public RecordingWorkspacePathPicker Picker { get; }
    public TopologySessionPort Session { get; }
    public IWorkspaceRegistryTopologyController Controller { get; }
    public int ProbeCount { get; private set; }

    public Task<WorkspaceRegistryDispatchResult> DispatchAsync(
        string method,
        object parameters,
        Guid? operationId = null) =>
        Controller.DispatchAsync(
            method,
            JsonSerializer.SerializeToElement(parameters),
            operationId ?? Guid.NewGuid(),
            CancellationToken.None);

    public WorkspaceRegistryEntryV2 AddDirectWorkspace(string name)
    {
        WorkspaceLayoutResult layout = WorkspaceLayout.Create(
            Path.Combine(Root, name),
            name,
            WorkspaceStorageMode.Direct,
            WorkspaceEncryptionMode.None);
        DateTimeOffset now = DateTimeOffset.UtcNow;
        return Registry.Register(new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = layout.Manifest.WorkspaceId,
            DisplayName = name,
            SelectedRoot = layout.SelectedRoot,
            ActivityRoot = null,
            StorageKind = WorkspaceStorageKind.Fixed,
            CoordinationStrength = WorkspaceCoordinationStrength.Strong,
            LastOpenedAt = now.AddMinutes(-3),
            LastKnownHealth = WorkspaceHealth.Offline,
            LastSnapshotAt = now.AddMinutes(-2),
            LastSyncAt = now.AddMinutes(-1),
            PendingSync = true,
        });
    }

    public void Dispose()
    {
        try
        {
            if (Directory.Exists(Root))
                Directory.Delete(Root, recursive: true);
        }
        catch
        {
            // Best effort test cleanup.
        }
    }

    internal sealed class RecordingWorkspacePathPicker : IWorkspacePathPicker
    {
        public string? SelectedRoot { get; set; }
        public int WorkspacePickCount { get; private set; }

        public string? PickWorkspaceRoot()
        {
            WorkspacePickCount++;
            return SelectedRoot;
        }

        public string? PickSnapshotExportTarget() => null;
        public string? PickSnapshotImportSource() => null;
        public string? PickSnapshotExtractTarget() => null;
        public string? PickFileUpgradeSource() => null;
    }

    internal sealed class TopologySessionPort : IWorkspaceProductSessionPort
    {
        public WorkspaceSessionV2 CurrentSession { get; set; } = ClosedSession();
        public WorkspaceRegistryEntryV2? CurrentWorkspace { get; set; }
        public WorkspaceV2HttpGateway? CurrentGateway => null;
        public WorkspaceV2SidecarCapabilities? CurrentCapabilities { get; set; }
        public (Guid WorkspaceId, bool Switching)? LastOpen { get; private set; }
        public WorkspaceOpenMode? LastOpenMode { get; private set; }
        public int CloseCount { get; private set; }
        public bool LastCloseTokenCanBeCanceled { get; private set; }
        public Func<Guid, WorkspaceOpenMode, bool, CancellationToken,
            Task<WorkspaceSessionV2>> Open
        { get; set; } =
            (_, _, _, _) => throw new InvalidOperationException(
                "Open behavior was not configured.");
        public Func<string, CancellationToken, Task<WorkspaceSessionV2>>? Close
        { get; set; }

        public bool TryCapture(
            WorkspaceWireScope? scope,
            out WorkspaceRequestEpochLease? lease)
        {
            lease = null;
            return false;
        }

        public bool TryAdmitLifecycleRequest(WorkspaceWireScope? scope) => false;
        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => true;
        public ulong ReserveHostSequence(Guid workspaceId, ulong sessionEpoch) => 1;

        public Task<WorkspaceSessionV2> OpenAsync(
            Guid workspaceId,
            WorkspaceOpenMode mode,
            bool switching,
            CancellationToken cancellationToken)
        {
            LastOpen = (workspaceId, switching);
            LastOpenMode = mode;
            return Open(workspaceId, mode, switching, cancellationToken);
        }

        public Task<WorkspaceSessionV2> CloseAsync(
            string reason,
            CancellationToken cancellationToken)
        {
            CloseCount++;
            LastCloseTokenCanBeCanceled = cancellationToken.CanBeCanceled;
            return Close?.Invoke(reason, cancellationToken)
                ?? Task.FromResult(CurrentSession);
        }

        public Task<WorkspaceSessionV2> RestartAfterRestoreAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Task.FromResult(CurrentSession);

        public Task<WorkspaceSessionV2> RestartAfterHostMaintenanceAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Task.FromResult(CurrentSession);

        private static WorkspaceSessionV2 ClosedSession() => new()
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = null,
            SessionEpoch = 0,
            State = WorkspaceSessionState.Closed,
            OpenMode = WorkspaceOpenMode.ReadOnly,
            Writable = false,
            Provisional = false,
            Phase = WorkspaceSessionPhase.Idle,
            ErrorCode = null,
        };
    }

    private sealed class SuccessfulOnboardingPort : IWorkspaceRepositoryOnboardingPort
    {
        public Task<WorkspaceRepositoryInitialization> InitializeAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken) =>
            Task.FromResult(new WorkspaceRepositoryInitialization(
                workspace.WorkspaceId,
                WorkspaceLayout.ReadManifest(workspace.SelectedRoot).EncryptionMode,
                null));

        public Task UnlockAsync(
            WorkspaceRegistryEntryV2 workspace,
            string recoveryKey,
            CancellationToken cancellationToken) => Task.CompletedTask;
    }

    private sealed class SuccessfulReplicaRecovery : IWorkspaceReplicaRecoveryService
    {
        public Task<WorkspaceReplicaReceipt> InitializeAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken) => Task.FromResult(Receipt(workspace));

        public Task<WorkspaceReplicaReceipt> VerifyAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken) => Task.FromResult(Receipt(workspace));

        public Task<WorkspaceReplicaReceipt> RecoverAndPublishAsync(
            WorkspaceRegistryEntryV2 workspace,
            CancellationToken cancellationToken) => Task.FromResult(Receipt(workspace));

        public bool RequiresRecovery(WorkspaceRegistryEntryV2 workspace) => false;

        private static WorkspaceReplicaReceipt Receipt(
            WorkspaceRegistryEntryV2 workspace) => new(
                "test",
                workspace.WorkspaceId,
                Guid.NewGuid(),
                Guid.NewGuid(),
                1,
                "checkpoint",
                "sha256:" + new string('a', 64),
                DateTimeOffset.UtcNow,
                workspace.ActivityRoot);
    }

    private sealed class NullRecoveryUi : IWorkspaceRepositoryRecoveryUi
    {
        public void ConfirmRecoveryKey(string workspaceName, string recoveryKey) { }
        public string? PromptRecoveryKey(string workspaceName) => null;
    }
}
