using System.Net;
using System.Text;
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

    [TestMethod]
    [DataRow(false, false)]
    [DataRow(true, false)]
    [DataRow(false, true)]
    public async Task RetiredForwardSettlesCallerOnce(bool completedAfterRetirement, bool refreshAfterSuccess)
    {
        using var fixture = new Fixture();
        using var retired = new CancellationTokenSource();
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var finish = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var scope = new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = Guid.NewGuid(),
            SessionEpoch = 7,
            OperationId = Guid.NewGuid(),
            Sequence = 1,
        };
        JsonElement wire = JsonSerializer.SerializeToElement(scope,
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
        string method = refreshAfterSuccess ? "replica.forceTakeover" : "snapshot.list";
        fixture.ReplicaStatus.Refresh = (_, _, token) =>
        {
            fixture.Session.LeaseCurrent = false;
            retired.Cancel();
            return Task.FromCanceled(token);
        };
        int completedLeases = 0;
        fixture.Session.Lease = new WorkspaceRequestEpochLease(
            scope, retired.Token, () => completedLeases++);
        fixture.Session.CurrentSession = OpenSession(scope.WorkspaceId, scope.SessionEpoch);
        fixture.Session.Capabilities = new WorkspaceV2SidecarCapabilities(
            "2.0", scope.WorkspaceId.ToString("D"), 7, 1, Guid.NewGuid().ToString("D"),
            [method]);
        using var handler = new ForwardHandler(async (_, token) =>
        {
            entered.TrySetResult();
            if (completedAfterRetirement) await finish.Task;
            else if (!refreshAfterSuccess) await Task.Delay(Timeout.InfiniteTimeSpan, token);
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(JsonSerializer.Serialize(new
                {
                    jsonrpc = "2.0",
                    id = "retired-forward",
                    wire,
                    result = new { snapshots = Array.Empty<object>() },
                }), Encoding.UTF8, "application/json"),
            };
        });
        using var gateway = new WorkspaceV2HttpGateway(() => new PocketBaseAdminContext(
            new Uri("http://127.0.0.1:8090/"), new Uri("http://127.0.0.1:8090/"),
            "X-VibeTable-Session", "test-secret"), handler);
        fixture.Session.Gateway = gateway;
        RoutedWebRequest request = Request(method, "retired-forward") with
        {
            Scope = scope,
            Wire = wire,
        };
        Task dispatch = fixture.Controller.DispatchAsync(request);
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        if (!refreshAfterSuccess)
        {
            fixture.Session.LeaseCurrent = false;
            if (!completedAfterRetirement) retired.Cancel();
            finish.TrySetResult();
        }
        await dispatch;

        Assert.AreEqual(1, completedLeases);
        JsonElement response = fixture.Reply.Responses.Single();
        Assert.AreEqual(refreshAfterSuccess, response.GetProperty("ok").GetBoolean());
        if (!refreshAfterSuccess)
        {
            Assert.AreEqual("workspace.session_stale", response.GetProperty("error").GetProperty("code").GetString());
            Assert.AreEqual(JsonValueKind.Null, response.GetProperty("result").ValueKind);
        }
        Assert.IsTrue(JsonElement.DeepEquals(wire, response.GetProperty("wire")));
        Assert.AreEqual("retired-forward", fixture.Reply.RequestIds.Single());
        Assert.AreEqual(0, fixture.Reply.Notifications.Count);
    }

    private sealed class ForwardHandler(
        Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> send) : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken) => send(request, cancellationToken);
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
        public WorkspaceV2HttpGateway? Gateway { get; set; }
        public WorkspaceV2HttpGateway? CurrentGateway => Gateway;
        public WorkspaceRequestEpochLease? Lease { get; set; }
        public bool LeaseCurrent { get; set; } = true;
        public WorkspaceV2SidecarCapabilities? Capabilities { get; set; }
        public WorkspaceV2SidecarCapabilities? CurrentCapabilities => Capabilities;
        public bool TryCapture(
            WorkspaceWireScope? scope,
            out WorkspaceRequestEpochLease? lease)
        {
            lease = Lease;
            return lease is not null;
        }

        public bool TryAdmitLifecycleRequest(WorkspaceWireScope? scope) => false;
        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => LeaseCurrent;
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
        public List<string?> RequestIds { get; } = [];
        public List<(string Type, JsonElement Payload)> Notifications { get; } = [];

        public void PostNotification(string type, object? payload) =>
            Notifications.Add((type, JsonSerializer.SerializeToElement(payload)));

        public void PostWorkspaceV2Response(
            string? requestId,
            object payload,
            JsonElement wire)
        {
            RequestIds.Add(requestId);
            Responses.Add(JsonSerializer.SerializeToElement(payload));
        }

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
        public Func<Guid, ulong, CancellationToken, Task> Refresh { get; set; } =
            (_, _, _) => Task.CompletedTask;

        public void Bind(WorkspaceSessionV2 session) => LastBound = session;

        public Task RefreshNowAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            CancellationToken cancellationToken) => Refresh(workspaceId, sessionEpoch, cancellationToken);

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
