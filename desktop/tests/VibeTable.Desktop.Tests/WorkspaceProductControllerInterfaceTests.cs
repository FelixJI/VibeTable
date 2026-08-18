using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceProductControllerInterfaceTests
{
    [TestMethod]
    public async Task DispatchReportsSuccessAndStableFailureThroughReplyInterface()
    {
        using var fixture = new Fixture();

        await fixture.Controller.DispatchAsync(Request("workspace.list", "list-1"));
        await fixture.Controller.DispatchAsync(Request("workspace.open", "open-1"));

        Assert.AreEqual(2, fixture.Reply.Responses.Count);
        Assert.IsTrue(fixture.Reply.Responses[0].GetProperty("ok").GetBoolean());
        Assert.IsFalse(fixture.Reply.Responses[1].GetProperty("ok").GetBoolean());
        Assert.AreEqual(
            "workspace.request_invalid",
            fixture.Reply.Responses[1]
                .GetProperty("error")
                .GetProperty("code")
                .GetString());
    }

    [TestMethod]
    public async Task DispatchPreservesTheStableWorkspaceActivationTimeoutCode()
    {
        using var fixture = new Fixture();
        Guid workspaceId = Guid.NewGuid();
        fixture.RegistryTopology.Open = (_, _, _, _) =>
            Task.FromException<WorkspaceSessionV2>(
                new WorkspaceActivationTimeoutException(
                    WorkspaceActivationStage.Sidecar,
                    TimeSpan.FromSeconds(60)));

        await fixture.Controller.DispatchAsync(Request(
            "workspace.open",
            "open-timeout",
            new
            {
                workspaceId = workspaceId.ToString("D"),
                openMode = "writable",
            }));

        JsonElement response = fixture.Reply.Responses.Single();
        Assert.IsFalse(response.GetProperty("ok").GetBoolean());
        Assert.AreEqual(
            WorkspaceActivationTimeoutException.ErrorCode,
            response.GetProperty("error").GetProperty("code").GetString());
    }

    [TestMethod]
    public async Task DispatchPreservesActivationTimeoutThroughSwitchRollback()
    {
        using var fixture = new Fixture();
        Guid workspaceId = Guid.NewGuid();
        var timeout = new WorkspaceActivationTimeoutException(
            WorkspaceActivationStage.Backend,
            TimeSpan.FromSeconds(90));
        fixture.RegistryTopology.Open = (_, _, _, _) =>
            Task.FromException<WorkspaceSessionV2>(
                new WorkspaceSwitchException(
                    "target activation failed",
                    timeout,
                    OpenSession(Guid.NewGuid(), 21)));

        await fixture.Controller.DispatchAsync(Request(
            "workspace.switch",
            "switch-timeout",
            new
            {
                targetWorkspaceId = workspaceId.ToString("D"),
                openMode = "writable",
            }));

        JsonElement response = fixture.Reply.Responses.Single();
        Assert.IsFalse(response.GetProperty("ok").GetBoolean());
        Assert.AreEqual(
            WorkspaceActivationTimeoutException.ErrorCode,
            response.GetProperty("error").GetProperty("code").GetString());
    }

    [TestMethod]
    public async Task OpenUsesOneSessionPortForSuccessFailureAndCancellation()
    {
        using var fixture = new Fixture();
        Guid workspaceId = Guid.NewGuid();
        WorkspaceSessionV2 opened = OpenSession(workspaceId, 19);
        fixture.RegistryTopology.Open = (_, _, _, _) => Task.FromResult(opened);

        WorkspaceSessionV2 result = await fixture.Controller.OpenAsync(
            workspaceId,
            WorkspaceOpenMode.Writable,
            switching: true,
            CancellationToken.None);

        Assert.AreSame(opened, result);
        Assert.AreEqual((workspaceId, true), fixture.RegistryTopology.LastOpen);

        fixture.RegistryTopology.Open = (_, _, _, _) =>
            Task.FromException<WorkspaceSessionV2>(new InvalidOperationException("failed"));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            fixture.Controller.OpenAsync(
                workspaceId,
                WorkspaceOpenMode.Writable,
                switching: false,
                CancellationToken.None));

        fixture.RegistryTopology.Open = (_, _, _, token) =>
            Task.FromCanceled<WorkspaceSessionV2>(token);
        using var cancelled = new CancellationTokenSource();
        cancelled.Cancel();
        await Assert.ThrowsExactlyAsync<TaskCanceledException>(() =>
            fixture.Controller.OpenAsync(
                workspaceId,
                WorkspaceOpenMode.Writable,
                switching: false,
                cancelled.Token));
    }

    [TestMethod]
    public void SessionChangedAndPostBootstrapPublishCurrentEpochThroughPublicInterface()
    {
        using var fixture = new Fixture();
        Guid workspaceId = Guid.NewGuid();
        fixture.Session.CurrentSession = OpenSession(workspaceId, 42);

        fixture.Controller.OnSessionChanged(
            new WorkspaceSessionChangedEventArgs(fixture.Session.CurrentSession, 7));

        Assert.AreEqual(1, fixture.Host.Scheduled);
        Assert.AreEqual(1, fixture.Host.OpenProductWorkspaceCalls);
        Assert.AreSame(
            fixture.Session.CurrentSession,
            fixture.ReplicaStatus.LastBound);
        Assert.AreEqual(1, fixture.Bootstrap.PostCount);

        fixture.Controller.PostBootstrap();
        Assert.AreEqual(2, fixture.Bootstrap.PostCount);
    }

    private static RoutedWebRequest Request(
        string method,
        string requestId,
        object? parameters = null)
    {
        JsonElement payload = JsonSerializer.SerializeToElement(new
        {
            @params = parameters ?? new { },
        });
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            operationId = Guid.NewGuid().ToString("D"),
        });
        return new RoutedWebRequest(
            "workspace.v2.request",
            requestId,
            payload,
            string.Empty,
            Wire: wire,
            V2Method: method);
    }

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

    private static WorkspaceSessionV2 OpenSession(Guid workspaceId, ulong epoch) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = workspaceId,
        SessionEpoch = epoch,
        State = WorkspaceSessionState.OpenedWritable,
        OpenMode = WorkspaceOpenMode.Writable,
        Writable = true,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };

    private sealed class Fixture : IDisposable
    {
        private readonly string _root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-workspace-product-" + Guid.NewGuid().ToString("N"));
        private readonly WorkspaceSessionManager _brokerSessions;

        public Fixture()
        {
            Directory.CreateDirectory(_root);
            var registry = new WorkspaceRegistry(_root);
            _brokerSessions = new WorkspaceSessionManager(
                registry,
                new UnusedRuntimeFactory());
            WorkspaceProviderPolicy policy = WorkspaceProviderPolicy.CreateForTests(
                new Dictionary<WorkspaceStorageKind, bool>
                {
                    [WorkspaceStorageKind.Fixed] = true,
                },
                (_, _, _) => throw new InvalidOperationException("unused probe"));
            var snapshots = new SnapshotPackageBroker(
                () => throw new InvalidOperationException("unused sidecar"),
                () => throw new InvalidOperationException("unused backend"),
                policy,
                registry,
                _brokerSessions,
                _root);
            var storage = new WorkspaceStorageBroker(
                registry,
                _brokerSessions,
                policy,
                _root);
            Session = new FakeSessionPort();
            Reply = new FakeReply();
            Host = new FakeHost();
            RegistryTopology = new FakeRegistryTopology();
            ReplicaStatus = new FakeReplicaStatus();
            Bootstrap = new FakeBootstrap();
            Controller = new WorkspaceProductController(
                Reply,
                Host,
                Session,
                RegistryTopology,
                ReplicaStatus,
                Bootstrap,
                new WorkspacePathGrantStore(new NullPathPicker()),
                snapshots,
                storage);
        }

        public WorkspaceProductController Controller { get; }
        public FakeSessionPort Session { get; }
        public FakeReply Reply { get; }
        public FakeHost Host { get; }
        public FakeRegistryTopology RegistryTopology { get; }
        public FakeReplicaStatus ReplicaStatus { get; }
        public FakeBootstrap Bootstrap { get; }

        public void Dispose()
        {
            Controller.DisposeAsync().AsTask().GetAwaiter().GetResult();
            _brokerSessions.DisposeAsync().AsTask().GetAwaiter().GetResult();
            try { Directory.Delete(_root, recursive: true); } catch { }
        }
    }

    private sealed class FakeSessionPort : IWorkspaceProductSessionPort
    {
        public WorkspaceSessionV2 CurrentSession { get; set; } = ClosedSession();
        public WorkspaceRegistryEntryV2? CurrentWorkspace { get; set; }
        public WorkspaceV2HttpGateway? CurrentGateway => null;
        public WorkspaceV2SidecarCapabilities? CurrentCapabilities => null;
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
            => throw new InvalidOperationException("controller must use topology port");

        public Task<WorkspaceSessionV2> CloseAsync(
            string reason,
            CancellationToken cancellationToken) => Task.FromResult(CurrentSession);

        public Task<WorkspaceSessionV2> RestartAfterRestoreAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Task.FromResult(CurrentSession);

        public Task<WorkspaceSessionV2> RestartAfterHostMaintenanceAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Task.FromResult(CurrentSession);
    }

    private sealed class FakeReply : IWorkspaceProductReplySink
    {
        public List<JsonElement> Responses { get; } = [];
        public List<(string Type, JsonElement Payload)> Notifications { get; } = [];

        public void PostNotification(string type, object? payload) =>
            Notifications.Add((type, JsonSerializer.SerializeToElement(payload)));

        public void PostWorkspaceV2Response(
            string? requestId,
            object payload,
            JsonElement wire) =>
            Responses.Add(JsonSerializer.SerializeToElement(payload));

        public void PostWorkspaceV2Event(object payload, JsonElement wire) =>
            Notifications.Add((
                "workspace.v2.event",
                JsonSerializer.SerializeToElement(payload)));
    }

    private sealed class FakeHost : IWorkspaceProductHost
    {
        public bool IsRendererReady => true;
        public bool IsClosing => false;
        public bool HasDocumentWorkspace => false;
        public int Scheduled { get; private set; }
        public int OpenProductWorkspaceCalls { get; private set; }

        public void Schedule(Action action)
        {
            Scheduled++;
            action();
        }

        public void OpenProductWorkspaceWhenReady() => OpenProductWorkspaceCalls++;
        public void WriteError(string message) => Assert.Fail(message);
    }

    private sealed class FakeRegistryTopology : IWorkspaceRegistryTopologyController
    {
        public (Guid WorkspaceId, bool Switching)? LastOpen { get; private set; }
        public Func<Guid, WorkspaceOpenMode, bool, CancellationToken,
            Task<WorkspaceSessionV2>> Open
        { get; set; } =
            (_, _, _, _) => throw new InvalidOperationException("open not configured");

        public Task<WorkspaceRegistryDispatchResult> DispatchAsync(
            string method,
            JsonElement parameters,
            Guid operationId,
            CancellationToken cancellationToken) =>
            Task.FromResult(new WorkspaceRegistryDispatchResult(new { }));

        public Task<WorkspaceSessionV2> OpenAsync(
            Guid workspaceId,
            WorkspaceOpenMode mode,
            bool switching,
            CancellationToken cancellationToken)
        {
            LastOpen = (workspaceId, switching);
            return Open(workspaceId, mode, switching, cancellationToken);
        }
    }

    private sealed class FakeReplicaStatus : IWorkspaceReplicaStatusController
    {
        public WorkspaceSessionV2? LastBound { get; private set; }

        public void Bind(WorkspaceSessionV2 session) => LastBound = session;

        public Task RefreshNowAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Task.CompletedTask;

        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    private sealed class FakeBootstrap : IWorkspaceBootstrapPublisher
    {
        public int PostCount { get; private set; }

        public void Post() => PostCount++;
    }

    private sealed class NullPathPicker : IWorkspacePathPicker
    {
        public string? PickWorkspaceRoot() => null;
        public string? PickSnapshotExportTarget() => null;
        public string? PickSnapshotImportSource() => null;
        public string? PickSnapshotExtractTarget() => null;
        public string? PickFileUpgradeSource() => null;
    }

    private sealed class UnusedRuntimeFactory : IWorkspaceRuntimeFactory
    {
        public IWorkspaceRuntime Create(
            WorkspaceRegistryEntryV2 workspace,
            ulong sessionEpoch) => throw new InvalidOperationException("unused runtime");
    }
}
