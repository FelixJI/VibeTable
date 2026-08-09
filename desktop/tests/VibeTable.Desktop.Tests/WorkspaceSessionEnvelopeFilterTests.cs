using System.Text.Json;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceSessionEnvelopeFilterTests
{
    [TestMethod]
    public async Task HostAdmissionAtomicallyReservesSequenceAndEpochLease()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);

        Assert.IsTrue(filter.TryCaptureHost(
            opened.WorkspaceId!.Value,
            opened.SessionEpoch,
            Guid.NewGuid(),
            out WorkspaceRequestEpochLease? firstLease));
        Assert.IsNotNull(firstLease);
        Assert.AreEqual((ulong)1, firstLease.Scope.Sequence);
        Assert.IsTrue(filter.TryCaptureHost(
            opened.WorkspaceId.Value,
            opened.SessionEpoch,
            Guid.NewGuid(),
            out WorkspaceRequestEpochLease? secondLease));
        Assert.IsNotNull(secondLease);
        Assert.AreEqual((ulong)2, secondLease.Scope.Sequence);
        firstLease.Dispose();
        secondLease.Dispose();
    }

    [TestMethod]
    public async Task SwitchDrainsInflightRequestBeforeProtectionSnapshot()
    {
        using var fixture = new SessionFixture(blockProtection: true);
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        fixture.Manager.SetRequestDrainHook(filter);
        WorkspaceWireScope scope = ScopeFor(opened, sequence: 3);

        Assert.IsTrue(filter.TryCapture(scope, out WorkspaceRequestEpochLease? lease));
        Assert.IsNotNull(lease);
        Assert.IsFalse(lease.CancellationToken.IsCancellationRequested);

        Task<WorkspaceSessionV2> switchTask = fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        await WaitUntilAsync(
            () => lease.CancellationToken.IsCancellationRequested);

        fixture.Protection.Release();
        Assert.IsFalse(fixture.Protection.Entered.IsCompleted);
        Assert.IsFalse(switchTask.IsCompleted);
        lease.Dispose();
        await fixture.Protection.Entered.WaitAsync(TimeSpan.FromSeconds(2));
        await switchTask;

        Assert.IsTrue(lease.CancellationToken.IsCancellationRequested);
        Assert.IsFalse(filter.IsCurrent(lease));
        Assert.IsFalse(filter.TryCapture(scope, out _));
    }

    [TestMethod]
    public async Task AcceptsBoundedOutOfOrderButRejectsDuplicateOrStaleSequence()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);

        Assert.IsTrue(filter.TryCapture(ScopeFor(opened, 5), out _));
        Assert.IsFalse(filter.TryCapture(ScopeFor(opened, 5), out _));
        Assert.IsTrue(filter.TryCapture(ScopeFor(opened, 4), out _));
        Assert.IsTrue(filter.TryCapture(ScopeFor(opened, 6), out _));
        Assert.IsTrue(filter.TryCapture(ScopeFor(opened, 1_048_578), out _));
        Assert.IsFalse(filter.TryCapture(ScopeFor(opened, 1), out _));
    }

    [TestMethod]
    public async Task DrainCancelsAndWaitsForEveryEpochLease()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        fixture.Manager.SetRequestDrainHook(filter);
        Assert.IsTrue(filter.TryCapture(
            ScopeFor(opened, 1),
            out WorkspaceRequestEpochLease? lease));
        Assert.IsNotNull(lease);

        Task<WorkspaceSessionV2> switching = fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        await WaitUntilAsync(
            () => lease.CancellationToken.IsCancellationRequested);
        Assert.IsFalse(switching.IsCompleted);

        lease.Dispose();
        WorkspaceSessionV2 result = await switching.WaitAsync(
            TimeSpan.FromSeconds(2));
        Assert.AreEqual(second.WorkspaceId, result.WorkspaceId);
    }

    [TestMethod]
    public async Task LifecycleSwitchDoesNotWaitOnItsOwnEnvelope()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        fixture.Manager.SetRequestDrainHook(filter);

        Assert.IsTrue(filter.TryAdmitLifecycleRequest(
            ScopeFor(opened, sequence: 1)));
        WorkspaceSessionV2 switched = await fixture.Manager.SwitchAsync(
                second.WorkspaceId,
                WorkspaceOpenMode.Writable)
            .WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(second.WorkspaceId, switched.WorkspaceId);
    }

    [TestMethod]
    public async Task LifecycleCloseWaitsForOtherInflightButNotItsOwnEnvelope()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        fixture.Manager.SetRequestDrainHook(filter);
        Assert.IsTrue(filter.TryCapture(
            ScopeFor(opened, sequence: 1),
            out WorkspaceRequestEpochLease? inflight));
        Assert.IsNotNull(inflight);
        Assert.IsTrue(filter.TryAdmitLifecycleRequest(
            ScopeFor(opened, sequence: 2)));

        Task<WorkspaceSessionV2> closing =
            fixture.Manager.CloseAsync("lifecycle-test");
        await WaitUntilAsync(
            () => inflight.CancellationToken.IsCancellationRequested);
        Assert.IsFalse(closing.IsCompleted);

        inflight.Dispose();
        WorkspaceSessionV2 closed = await closing.WaitAsync(
            TimeSpan.FromSeconds(2));
        Assert.AreEqual(WorkspaceSessionState.Closed, closed.State);
    }

    [TestMethod]
    public async Task OldResponseIsDroppedAfterWorkspaceSwitch()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        var transport = new ControlledQueryTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var dispatcher = CreateDispatcher(sink, filter);
        dispatcher.SetProductDataGateway(gateway);

        dispatcher.Dispatch(QueryRequest("old-response", ScopeFor(opened, 1)));
        await transport.WaitForWriteAsync();
        await fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        transport.CompleteResponse();
        await Task.Delay(150);

        Assert.IsFalse(sink.Replies.Any(
            reply => reply.RequestId == "old-response"));
    }

    [TestMethod]
    public async Task FailedSwitchCancelsOldLeaseAndAcceptsRolledBackEpoch()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        Assert.IsTrue(filter.TryCapture(
            ScopeFor(opened, 1),
            out WorkspaceRequestEpochLease? oldLease));
        fixture.RuntimeFactory.FailNextStartFor = second.WorkspaceId;

        WorkspaceSwitchException error =
            await Assert.ThrowsExactlyAsync<WorkspaceSwitchException>(
                () => fixture.Manager.SwitchAsync(
                    second.WorkspaceId,
                    WorkspaceOpenMode.Writable));

        Assert.IsNotNull(oldLease);
        Assert.IsTrue(oldLease.CancellationToken.IsCancellationRequested);
        Assert.AreEqual(first.WorkspaceId, error.RolledBackSession.WorkspaceId);
        Assert.IsTrue(
            error.RolledBackSession.SessionEpoch > opened.SessionEpoch);
        Assert.IsTrue(filter.TryCapture(
            ScopeFor(error.RolledBackSession, 2),
            out WorkspaceRequestEpochLease? rollbackLease));
        Assert.IsTrue(filter.IsCurrent(rollbackLease));
    }

    [TestMethod]
    public async Task RecoverableReadDoesNotRetryOnNewEpochGateway()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        await using var staleClient = new JsonRpcClient(
            new ControlledQueryTransport());
        using var staleGateway = new JsonRpcProductDataGateway(staleClient);
        var sink = new FakeWebReplySink();
        var dispatcher = CreateDispatcher(sink, filter);
        dispatcher.SetProductDataGateway(staleGateway);
        staleGateway.Dispose();

        dispatcher.Dispatch(QueryRequest("stale-retry", ScopeFor(opened, 1)));
        await Task.Delay(60);
        await fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        var replacementTransport = new ControlledQueryTransport();
        await using var replacementClient = new JsonRpcClient(
            replacementTransport);
        using var replacementGateway = new JsonRpcProductDataGateway(
            replacementClient);
        dispatcher.SetProductDataGateway(replacementGateway);
        await Task.Delay(200);

        Assert.AreEqual(0, replacementTransport.WriteCount);
        Assert.IsFalse(sink.Replies.Any(
            reply => reply.RequestId == "stale-retry"));
    }

    [TestMethod]
    public async Task ReadOnlySessionRejectsProductMutationBeforeGateway()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.ReadOnly);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        var transport = new ControlledQueryTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var dispatcher = CreateDispatcher(sink, filter);
        dispatcher.SetProductDataGateway(gateway);
        using var payload = JsonDocument.Parse(
            """{"tableId":"tbl_records","operations":[]}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "mutation.apply",
            "read-only-write",
            payload.RootElement.Clone(),
            string.Empty,
            ScopeFor(opened, 1)));

        FakeWebReplySink.Reply? failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        StringAssert.Contains(
            JsonSerializer.Serialize(failed.Payload),
            @"""code"":""WORKSPACE_READ_ONLY""");
        Assert.AreEqual(0, transport.WriteCount);
    }

    [TestMethod]
    public async Task DangerousProductMutationProtectsBeforeGatewayWrite()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        var transport = new ControlledQueryTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcProductDataGateway(client);
        var sink = new FakeWebReplySink();
        var dispatcher = CreateDispatcher(sink, filter);
        dispatcher.SetProductDataGateway(gateway);
        using var payload = JsonDocument.Parse(
            """{"grantId":"grant-1","collection":"records","token":"import-1"}""");

        dispatcher.Dispatch(new RoutedWebRequest(
            "data.applyImport",
            "protected-import",
            payload.RootElement.Clone(),
            string.Empty,
            ScopeFor(opened, 1)));

        await transport.WaitForWriteAsync();
        Assert.AreEqual(1, fixture.Protection.CallCount);
    }

    private static WorkspaceRequestDispatcher CreateDispatcher(
        FakeWebReplySink sink,
        WorkspaceSessionEnvelopeFilter filter)
        => new(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink,
            readRecoveryTimeout: TimeSpan.FromMilliseconds(750),
            sessionEnvelopeFilter: filter);

    private static async Task WaitUntilAsync(Func<bool> condition)
    {
        using var timeout = new CancellationTokenSource(
            TimeSpan.FromSeconds(2));
        while (!condition())
            await Task.Delay(10, timeout.Token);
    }

    private static RoutedWebRequest QueryRequest(
        string requestId,
        WorkspaceWireScope scope)
    {
        using var document = JsonDocument.Parse(
            """{"tableId":"tbl_records","query":{"filters":[],"sorts":[],"offset":0,"limit":100}}""");
        return new RoutedWebRequest(
            "query.page",
            requestId,
            document.RootElement.Clone(),
            string.Empty,
            scope);
    }

    private static WorkspaceWireScope ScopeFor(
        WorkspaceSessionV2 session,
        ulong sequence)
        => new()
        {
            Scope = "workspace",
            WorkspaceId = session.WorkspaceId!.Value,
            SessionEpoch = session.SessionEpoch,
            OperationId = Guid.NewGuid(),
            Sequence = sequence,
        };

    private sealed class ControlledQueryTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        private readonly TaskCompletionSource<string> _written =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private string? _requestId;

        public int WriteCount { get; private set; }

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            _requestId = request.RootElement.GetProperty("id").GetString();
            WriteCount++;
            _written.TrySetResult(_requestId!);
            return Task.CompletedTask;
        }

        public Task WaitForWriteAsync()
            => _written.Task.WaitAsync(TimeSpan.FromSeconds(2));

        public void CompleteResponse()
        {
            using var response = JsonDocument.Parse(
                $$"""
                {
                  "jsonrpc": "2.0",
                  "id": "{{_requestId}}",
                  "result": {
                    "rows": [],
                    "total": 0,
                    "snapshot": {"schemaRevision": "schema_0001"}
                  }
                }
                """);
            _incoming.Writer.TryWrite(response.RootElement.Clone());
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }

    private sealed class SessionFixture : IDisposable
    {
        public SessionFixture(bool blockProtection = false)
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-envelope-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Registry = new WorkspaceRegistry(Root);
            Protection = new BlockingProtectionHook(blockProtection);
            RuntimeFactory = new FakeRuntimeFactory();
            Manager = new WorkspaceSessionManager(
                Registry,
                RuntimeFactory,
                Protection);
        }

        public string Root { get; }
        public WorkspaceRegistry Registry { get; }
        public BlockingProtectionHook Protection { get; }
        public FakeRuntimeFactory RuntimeFactory { get; }
        public WorkspaceSessionManager Manager { get; }

        public WorkspaceRegistryEntryV2 AddWorkspace(
            string displayName,
            string folder)
        {
            var result = WorkspaceLayout.Create(
                Path.Combine(Root, folder),
                displayName,
                WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient);
            return Registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = result.Manifest.WorkspaceId,
                DisplayName = displayName,
                SelectedRoot = result.SelectedRoot,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
        }

        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            try
            {
                if (Directory.Exists(Root))
                    Directory.Delete(Root, recursive: true);
            }
            catch
            {
                // Best effort.
            }
        }
    }

    private sealed class BlockingProtectionHook(bool blocked)
        : IWorkspaceProtectionHook
    {
        private readonly TaskCompletionSource<bool> _entered =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource<bool> _release =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public Task Entered => _entered.Task;
        public int CallCount { get; private set; }

        public async Task ProtectAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
        {
            CallCount++;
            _entered.TrySetResult(true);
            if (blocked)
                await _release.Task.WaitAsync(cancellationToken);
        }

        public void Release() => _release.TrySetResult(true);
    }

    private sealed class FakeRuntimeFactory : IWorkspaceRuntimeFactory
    {
        public Guid? FailNextStartFor { get; set; }

        public IWorkspaceRuntime Create(
            WorkspaceRegistryEntryV2 workspace,
            ulong sessionEpoch)
        {
            bool fail = FailNextStartFor == workspace.WorkspaceId;
            if (fail)
                FailNextStartFor = null;
            return new FakeRuntime(workspace.WorkspaceId, sessionEpoch, fail);
        }
    }

    private sealed class FakeRuntime(
        Guid workspaceId,
        ulong sessionEpoch,
        bool failStart) : IWorkspaceRuntime
    {
        public Guid WorkspaceId { get; } = workspaceId;
        public ulong SessionEpoch { get; } = sessionEpoch;
        public Task StartAsync(
            WorkspaceOpenMode mode,
            CancellationToken cancellationToken)
            => failStart
                ? Task.FromException(
                    new InvalidOperationException("injected start failure"))
                : Task.CompletedTask;
        public Task VerifyAsync(CancellationToken cancellationToken)
            => Task.CompletedTask;
        public Task DrainAsync(CancellationToken cancellationToken)
            => Task.CompletedTask;
        public Task StopAsync(CancellationToken cancellationToken)
            => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
