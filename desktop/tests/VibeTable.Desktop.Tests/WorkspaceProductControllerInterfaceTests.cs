using System.Net;
using System.Net.Http;
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

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task PickerReentryCannotSendLaterWorkspaceSequenceBeforeExport(bool cancelPicker)
    {
        var picker = new NullPathPicker();
        using var fixture = new Fixture(picker);
        var scope = new WorkspaceWireScope
        {
            Scope = "workspace", WorkspaceId = Guid.NewGuid(), SessionEpoch = 7,
            OperationId = Guid.NewGuid(), Sequence = 100,
        };
        var options = new JsonSerializerOptions(JsonSerializerDefaults.Web);
        JsonElement wire = JsonSerializer.SerializeToElement(scope, options);
        JsonElement laterWire = JsonSerializer.SerializeToElement(
            scope with { OperationId = Guid.NewGuid(), Sequence = 101 }, options);
        fixture.Session.Lease = new WorkspaceRequestEpochLease(scope, CancellationToken.None, () => { });
        fixture.Session.CurrentSession = OpenSession(scope.WorkspaceId, scope.SessionEpoch);
        fixture.Session.Capabilities = new WorkspaceV2SidecarCapabilities(
            "2.0", scope.WorkspaceId.ToString("D"), 7, 1, Guid.NewGuid().ToString("D"), ["snapshot.export"]);
        ulong watermark = 0;
        var received = new List<ulong>();
        using var handler = new ForwardHandler(async (request, token) =>
        {
            using JsonDocument body = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
            JsonElement root = body.RootElement;
            JsonElement requestWire = root.GetProperty("wire");
            ulong sequence = requestWire.GetProperty("sequence").GetUInt64();
            received.Add(sequence);
            object payload = sequence <= watermark
                ? new { jsonrpc = "2.0", id = root.GetProperty("id").GetString(), wire = requestWire,
                    error = new { code = "workspace.sequence_stale", message = "stale", retryable = false } }
                : new { jsonrpc = "2.0", id = root.GetProperty("id").GetString(), wire = requestWire, result = new { } };
            watermark = Math.Max(watermark, sequence);
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(JsonSerializer.Serialize(payload), Encoding.UTF8, "application/json"),
            };
        });
        using var gateway = new WorkspaceV2HttpGateway(() => new PocketBaseAdminContext(
            new Uri("http://127.0.0.1:8090/"), new Uri("http://127.0.0.1:8090/"),
            "X-VibeTable-Session", "test-secret"), handler);
        fixture.Session.Gateway = gateway;
        Task<WorkspaceV2ForwardResult>? later = null;
        picker.SnapshotExport = () =>
        {
            later = gateway.ForwardAsync("host-query", "fileHistory.queryDocuments", laterWire,
                JsonSerializer.SerializeToElement(new { }), null, CancellationToken.None);
            return cancelPicker ? null : Path.Combine(Path.GetTempPath(), "ordered-export.vtsnapshot");
        };
        await fixture.Controller.DispatchAsync(Request("snapshot.export", "export",
            new { pathGrant = WorkspacePathGrantStore.SnapshotExportSentinel }) with { Scope = scope, Wire = wire });
        Assert.IsNotNull(later);
        Assert.IsNull((await later.WaitAsync(TimeSpan.FromSeconds(5))).Error);
        JsonElement response = fixture.Reply.Responses.Single();
        Assert.AreEqual(!cancelPicker, response.GetProperty("ok").GetBoolean(), response.GetRawText());
        if (cancelPicker)
            Assert.AreEqual("workspace.path_selection_cancelled",
                response.GetProperty("error").GetProperty("code").GetString());
        CollectionAssert.AreEqual(cancelPicker ? new ulong[] { 101 } : [100, 101], received);
    }
    [TestMethod]
    public async Task StartedRpcCancellationWaitsForTheActualExchangeToExit()
    {
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        using var handler = new ForwardHandler(async (request, _) =>
        {
            using JsonDocument body = JsonDocument.Parse(await request.Content!.ReadAsStringAsync());
            entered.TrySetResult();
            // Deliberately delay transport teardown after cancellation is requested.
            await release.Task;
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(JsonSerializer.Serialize(new
                {
                    jsonrpc = "2.0", id = "active", wire = body.RootElement.GetProperty("wire"), result = new { },
                }), Encoding.UTF8, "application/json"),
            };
        });
        using var gateway = new WorkspaceV2HttpGateway(() => new PocketBaseAdminContext(
            new Uri("http://127.0.0.1:8090/"), new Uri("http://127.0.0.1:8090/"),
            "X-VibeTable-Session", "test-secret"), handler);
        using var cancellation = new CancellationTokenSource();
        JsonElement wire = JsonSerializer.SerializeToElement(new { scope = "workspace", sequence = 1 });
        Task<WorkspaceV2ForwardResult> pending = gateway.ForwardAsync("active", "snapshot.list",
            wire, JsonSerializer.SerializeToElement(new { }), null, cancellation.Token);
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(5));
        cancellation.Cancel();
        try
        {
            Assert.IsFalse(pending.IsCompleted,
                "A started exchange must retain its caller's lease until transport teardown completes.");
        }
        finally
        {
            release.TrySetResult();
        }
        await Assert.ThrowsAsync<OperationCanceledException>(async () => await pending);
    }
    private sealed class ForwardHandler(
        Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> send) : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken) => send(request, cancellationToken);
    }

    [TestMethod]
    public async Task DispatchForwardsProvisionalReplicaForceTakeoverWithCurrentScope()
    {
        using var fixture = new Fixture();
        fixture.Session.CaptureCurrentSession = true;
        WorkspaceSessionV2 provisional = ProvisionalSession(Guid.NewGuid(), 42);
        WorkspaceWireScope scope = ScopeFor(provisional, sequence: 7);
        fixture.Session.CurrentSession = provisional;
        fixture.Session.Capabilities = Capabilities(
            provisional,
            "replica.forceTakeover");
        var handler = new RecordingHandler(request =>
        {
            Assert.AreEqual("/api/vibetable/v2/rpc", request.RequestUri!.AbsolutePath);
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            Assert.AreEqual("replica.forceTakeover", body.RootElement.GetProperty("method").GetString());
            Assert.AreEqual("force-1", body.RootElement.GetProperty("id").GetString());
            Assert.IsTrue(JsonElement.DeepEquals(
                RequestWire(scope),
                body.RootElement.GetProperty("wire")));
            Assert.AreEqual("provisional", body.RootElement
                .GetProperty("params").GetProperty("mode").GetString());
            return JsonResponse(
                "force-1",
                body.RootElement.GetProperty("wire"),
                "{\"fenceEpoch\":3,\"claimId\":\"22222222-2222-4222-8222-222222222222\",\"mode\":\"provisional\"}");
        });
        fixture.Session.Gateway = Gateway(handler);

        await fixture.Controller.DispatchAsync(Request(
            "replica.forceTakeover",
            "force-1",
            new { mode = "provisional" },
            scope));

        JsonElement response = fixture.Reply.Responses.Single();
        Assert.IsTrue(response.GetProperty("ok").GetBoolean());
        JsonElement result = response.GetProperty("result");
        Assert.AreEqual(3, result.GetProperty("fenceEpoch").GetInt32());
        Assert.AreEqual("22222222-2222-4222-8222-222222222222", result
            .GetProperty("claimId").GetString());
        Assert.AreEqual("provisional", result.GetProperty("mode").GetString());
    }

    [TestMethod]
    public async Task DispatchRelaysProvisionalConflictApplySidecarRejection()
    {
        using var fixture = new Fixture();
        fixture.Session.CaptureCurrentSession = true;
        WorkspaceSessionV2 provisional = ProvisionalSession(Guid.NewGuid(), 43);
        WorkspaceWireScope scope = ScopeFor(provisional, sequence: 8);
        fixture.Session.CurrentSession = provisional;
        fixture.Session.Capabilities = Capabilities(provisional, "conflict.apply");
        Guid planId = Guid.NewGuid();
        fixture.Session.Gateway = Gateway(new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            Assert.AreEqual("conflict.apply", body.RootElement.GetProperty("method").GetString());
            JsonElement parameters = body.RootElement.GetProperty("params");
            Assert.AreEqual(1, parameters.EnumerateObject().Count());
            Assert.AreEqual(planId.ToString("D"), parameters.GetProperty("planId").GetString());
            return new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(
                    "{\"jsonrpc\":\"2.0\",\"id\":\"conflict-1\",\"wire\":"
                    + body.RootElement.GetProperty("wire").GetRawText()
                    + ",\"error\":{\"code\":\"conflict.plan_stale\",\"message\":\"retry\",\"retryable\":false}}",
                    Encoding.UTF8,
                    "application/json"),
            };
        }));

        await fixture.Controller.DispatchAsync(Request(
            "conflict.apply", "conflict-1", new { planId = planId.ToString("D") }, scope));

        JsonElement response = fixture.Reply.Responses.Single();
        Assert.IsFalse(response.GetProperty("ok").GetBoolean());
        Assert.AreEqual("conflict.plan_stale", response.GetProperty("error")
            .GetProperty("code").GetString());
    }

    [TestMethod]
    [DataRow("replica.forceTakeover", "readOnly", "workspace.read_only")]
    [DataRow("replica.forceTakeover", "transition", "workspace.session_stale")]
    [DataRow("replica.forceTakeover", "stale", "workspace.session_stale")]
    [DataRow("retention.apply", "provisional", "workspace.read_only")]
    public async Task DispatchRetainsNarrowProvisionalAdmissionBoundaries(
        string method,
        string sessionKind,
        string expectedCode)
    {
        using var fixture = new Fixture();
        fixture.Session.CaptureCurrentSession = true;
        WorkspaceSessionV2 provisional = ProvisionalSession(Guid.NewGuid(), 44);
        fixture.Session.CurrentSession = sessionKind switch
        {
            "readOnly" => provisional with
            {
                State = WorkspaceSessionState.OpenedReadOnly,
                OpenMode = WorkspaceOpenMode.ReadOnly,
                Provisional = false,
            },
            "transition" => provisional with { Phase = WorkspaceSessionPhase.Verifying },
            _ => provisional,
        };
        WorkspaceWireScope scope = ScopeFor(fixture.Session.CurrentSession, 9);
        if (sessionKind == "stale")
            scope = scope with { SessionEpoch = scope.SessionEpoch + 1 };
        fixture.Session.Gateway = Gateway(new RecordingHandler(_ =>
            throw new AssertFailedException("request must not reach sidecar")));

        await fixture.Controller.DispatchAsync(Request(method, "reject-1", new { }, scope));

        JsonElement response = fixture.Reply.Responses.Single();
        Assert.IsFalse(response.GetProperty("ok").GetBoolean());
        Assert.AreEqual(expectedCode, response.GetProperty("error")
            .GetProperty("code").GetString());
    }

    private static RoutedWebRequest Request(
        string method,
        string requestId,
        object? parameters = null,
        WorkspaceWireScope? scope = null)
    {
        JsonElement payload = JsonSerializer.SerializeToElement(new
        {
            @params = parameters ?? new { },
        });
        JsonElement wire = scope is null
            ? JsonSerializer.SerializeToElement(new
            {
                operationId = Guid.NewGuid().ToString("D"),
            })
            : RequestWire(scope);
        return new RoutedWebRequest(
            "workspace.v2.request",
            requestId,
            payload,
            string.Empty,
            scope,
            Wire: wire,
            V2Method: method);
    }

    private static JsonElement RequestWire(WorkspaceWireScope scope) =>
        JsonSerializer.SerializeToElement(new
        {
            scope = scope.Scope,
            workspaceId = scope.WorkspaceId.ToString("D"),
            sessionEpoch = scope.SessionEpoch,
            operationId = scope.OperationId.ToString("D"),
            sequence = scope.Sequence,
        });

    private static WorkspaceWireScope ScopeFor(
        WorkspaceSessionV2 session,
        ulong sequence) => new()
        {
            Scope = "workspace",
            WorkspaceId = session.WorkspaceId!.Value,
            SessionEpoch = session.SessionEpoch,
            OperationId = Guid.NewGuid(),
            Sequence = sequence,
        };

    private static WorkspaceSessionV2 ProvisionalSession(
        Guid workspaceId,
        ulong epoch) => new()
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = workspaceId,
            SessionEpoch = epoch,
            State = WorkspaceSessionState.OpenedProvisional,
            OpenMode = WorkspaceOpenMode.Provisional,
            Writable = false,
            Provisional = true,
            Phase = WorkspaceSessionPhase.Idle,
            ErrorCode = null,
        };

    private static WorkspaceV2SidecarCapabilities Capabilities(
        WorkspaceSessionV2 session,
        params string[] methods) => new(
        WorkspaceV2Json.ContractVersion,
        session.WorkspaceId!.Value.ToString("D"),
        session.SessionEpoch,
        1,
        Guid.NewGuid().ToString("D"),
        methods);

    private static WorkspaceV2HttpGateway Gateway(HttpMessageHandler handler) => new(
        () => new PocketBaseAdminContext(
            new Uri("http://127.0.0.1:43125/api/vibetable/v1/admin/bootstrap"),
            new Uri("http://127.0.0.1:43125/"),
            "X-VibeTable-Session",
            "private-secret"),
        handler);

    private static HttpResponseMessage JsonResponse(
        string requestId,
        JsonElement wire,
        string result) => new(HttpStatusCode.OK)
        {
            Content = new StringContent(
                $"{{\"jsonrpc\":\"2.0\",\"id\":\"{requestId}\",\"wire\":"
                + wire.GetRawText()
                + $",\"result\":{result}}}",
                Encoding.UTF8,
                "application/json"),
        };

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

        public Fixture(IWorkspacePathPicker? picker = null)
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
                new WorkspacePathGrantStore(picker ?? new NullPathPicker()),
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
        public bool CaptureCurrentSession { get; set; }
        public bool TryCapture(
            WorkspaceWireScope? scope,
            out WorkspaceRequestEpochLease? lease)
        {
            if (!CaptureCurrentSession)
            {
                lease = Lease;
                return lease is not null;
            }
            lease = null;
            if (scope is null || scope.Scope != "workspace" ||
                CurrentSession.WorkspaceId != scope.WorkspaceId ||
                CurrentSession.SessionEpoch != scope.SessionEpoch ||
                CurrentSession.State is not (
                    WorkspaceSessionState.OpenedReadOnly or
                    WorkspaceSessionState.OpenedWritable or
                    WorkspaceSessionState.OpenedProvisional) ||
                CurrentSession.Phase != WorkspaceSessionPhase.Idle)
                return false;
            lease = new WorkspaceRequestEpochLease(
                scope,
                CancellationToken.None,
                () => { });
            return true;
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
        public Func<string?>? SnapshotExport { get; set; }
        public string? PickSnapshotExportTarget() => SnapshotExport?.Invoke();
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

    private sealed class RecordingHandler(
        Func<HttpRequestMessage, HttpResponseMessage> responder)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken) => Task.FromResult(responder(request));
    }
}
