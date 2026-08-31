using System.Text.Json;
using System.Text.Json.Nodes;
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
    public async Task GoRouteHonorsEpochCancellationWithoutRendererReply()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        fixture.Manager.SetRequestDrainHook(filter);
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var cancelled = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var sidecar = new ControlledProductSidecarForwarder(async (call, token) =>
        {
            using CancellationTokenRegistration registration = token.Register(
                () => cancelled.TrySetResult());
            started.TrySetResult();
            await Task.Delay(Timeout.InfiniteTimeSpan, token);
            return new ProductSidecarSuccess(
                call.Wire.Clone(),
                JsonSerializer.SerializeToElement(new { rows = Array.Empty<object>() }));
        });
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(
            sink,
            GoQuerySelector(),
            sessionEnvelopeFilter: filter);
        controller.SetProductSidecarForwarder(sidecar);
        Task dispatch = controller.DispatchAsync(
            GoQueryRequest("go-cancel", ScopeFor(opened, 1)));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Task<WorkspaceSessionV2> switching = fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        await cancelled.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await dispatch.WaitAsync(TimeSpan.FromSeconds(2));
        await switching.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, sidecar.CallCount);
        Assert.IsFalse(sink.Replies.Any(
            reply => reply.RequestId == "go-cancel"));
    }

    [TestMethod]
    public async Task GoRouteDropsLateResultWhenForwarderIgnoresEpochCancellation()
    {
        using var fixture = new SessionFixture();
        WorkspaceRegistryEntryV2 first = fixture.AddWorkspace("一号", "One");
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("二号", "Two");
        WorkspaceSessionV2 opened = await fixture.Manager.OpenAsync(
            first.WorkspaceId,
            WorkspaceOpenMode.Writable);
        using var filter = new WorkspaceSessionEnvelopeFilter(fixture.Manager);
        fixture.Manager.SetRequestDrainHook(filter);
        var started = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var cancelled = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var response = new TaskCompletionSource<ProductSidecarForwardResult>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var sidecar = new ControlledProductSidecarForwarder(async (_, token) =>
        {
            using CancellationTokenRegistration registration = token.Register(
                () => cancelled.TrySetResult());
            started.TrySetResult();
            return await response.Task;
        });
        var sink = new FakeWebReplySink();
        var controller = new ProductDataRequestController(
            sink,
            GoQuerySelector(),
            sessionEnvelopeFilter: filter);
        controller.SetProductSidecarForwarder(sidecar);
        RoutedWebRequest request = GoQueryRequest(
            "go-late",
            ScopeFor(opened, 1));
        Task dispatch = controller.DispatchAsync(request);
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Task<WorkspaceSessionV2> switching = fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        await cancelled.Task.WaitAsync(TimeSpan.FromSeconds(2));
        response.SetResult(new ProductSidecarSuccess(
            request.Wire.Clone(),
            JsonSerializer.SerializeToElement(new { rows = Array.Empty<object>() })));
        await dispatch.WaitAsync(TimeSpan.FromSeconds(2));
        await switching.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(1, sidecar.CallCount);
        Assert.IsFalse(sink.Replies.Any(
            reply => reply.RequestId == "go-late"));
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

    [TestMethod]
    public async Task OrdinaryFieldPlanOwnsApplyAdmissionDespiteForgedConfirmation()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync();

        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.plan",
            "ordinary-plan",
            FieldPlanRequest("update", backupReceipt: "renderer-forged"),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "ordinary-apply",
            FieldApplyRequest(
                confirmations: ["backupReceipt"],
                protectionSnapshotId: "renderer-forged"),
            string.Empty,
            ScopeFor(fixture.Opened, 2)));

        Assert.AreEqual(0, fixture.Protection.CallCount);
        Assert.AreEqual(
            string.Empty,
            fixture.Transport.ParametersFor("field.change.plan")
                .GetProperty("backupReceipt").GetString());
        Assert.IsFalse(
            fixture.Transport.ParametersFor("field.change.apply")
                .TryGetProperty("protectionSnapshotId", out _));
    }

    [TestMethod]
    public async Task PurgePlanAndApplyInjectHostOwnedProtectionReceipts()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync();

        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.plan",
            "purge-plan",
            FieldPlanRequest("purge", backupReceipt: "renderer-forged"),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));
        Assert.AreEqual(1, fixture.Protection.CallCount);
        Assert.AreEqual(
            fixture.Protection.LastReceipt!.SnapshotId.ToString("D"),
            fixture.Transport.ParametersFor("field.change.plan")
                .GetProperty("backupReceipt").GetString());

        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "purge-apply",
            FieldApplyRequest(
                confirmations: ["backupReceipt", "fieldName"],
                protectionSnapshotId: "renderer-forged"),
            string.Empty,
            ScopeFor(fixture.Opened, 2)));

        Assert.AreEqual(2, fixture.Protection.CallCount);
        Assert.AreEqual(
            fixture.Protection.LastReceipt!.SnapshotId.ToString("D"),
            fixture.Transport.ParametersFor("field.change.apply")
                .GetProperty("protectionSnapshotId").GetString());
    }

    [TestMethod]
    public async Task UnknownFieldPlanApplyFailsClosedWithProtection()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync();

        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "unknown-apply",
            FieldApplyRequest(confirmations: []),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));

        Assert.AreEqual(1, fixture.Protection.CallCount);
        Assert.AreEqual(
            fixture.Protection.LastReceipt!.SnapshotId.ToString("D"),
            fixture.Transport.ParametersFor("field.change.apply")
                .GetProperty("protectionSnapshotId").GetString());
    }

    [TestMethod]
    public async Task GatewayReplacementDoesNotReuseOrdinaryFieldPlanAdmission()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync();
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.plan",
            "gateway-plan",
            FieldPlanRequest("update"),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));

        var replacementTransport = new FieldChangeTransport();
        await using var replacementClient = new JsonRpcClient(replacementTransport);
        using var replacementGateway = new JsonRpcProductDataGateway(replacementClient);
        fixture.Controller.SetGateway(replacementGateway);
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "gateway-apply",
            FieldApplyRequest(confirmations: []),
            string.Empty,
            ScopeFor(fixture.Opened, 2)));

        Assert.AreEqual(1, fixture.Protection.CallCount);
        Assert.IsTrue(replacementTransport.ParametersFor("field.change.apply")
            .TryGetProperty("protectionSnapshotId", out _));
    }

    [TestMethod]
    public async Task LatePlanResponseCannotRepopulateLedgerAfterGatewayReplacement()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync(holdPlanResponse: true);

        Task latePlan = fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.plan",
            "late-gateway-plan",
            FieldPlanRequest("update"),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));
        await fixture.Transport.PlanWritten;

        var replacementTransport = new FieldChangeTransport();
        await using var replacementClient = new JsonRpcClient(replacementTransport);
        using var replacementGateway = new JsonRpcProductDataGateway(replacementClient);
        fixture.Controller.SetGateway(replacementGateway);
        fixture.Transport.ReleasePlan();
        await latePlan;
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "late-gateway-apply",
            FieldApplyRequest(confirmations: []),
            string.Empty,
            ScopeFor(fixture.Opened, 2)));

        Assert.AreEqual(1, fixture.Protection.CallCount);
        Assert.IsTrue(replacementTransport.ParametersFor("field.change.apply")
            .TryGetProperty("protectionSnapshotId", out _));
    }

    [TestMethod]
    public async Task WorkspaceEpochDoesNotReuseOrdinaryFieldPlanAdmission()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync();
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("Other", "Other");
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.plan",
            "workspace-plan",
            FieldPlanRequest("update"),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));

        WorkspaceSessionV2 switched = await fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        int protectionCallsAfterSwitch = fixture.Protection.CallCount;
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "workspace-apply",
            FieldApplyRequest(confirmations: []),
            string.Empty,
            ScopeFor(switched, 1)));

        Assert.AreEqual(
            protectionCallsAfterSwitch + 1,
            fixture.Protection.CallCount);
    }

    [TestMethod]
    public async Task LatePlanResponseCannotAuthorizeApplyAfterWorkspaceEpochSwitch()
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync(holdPlanResponse: true);
        WorkspaceRegistryEntryV2 second = fixture.AddWorkspace("Other", "Other");

        Task latePlan = fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.plan",
            "late-workspace-plan",
            FieldPlanRequest("update"),
            string.Empty,
            ScopeFor(fixture.Opened, 1)));
        await fixture.Transport.PlanWritten;
        WorkspaceSessionV2 switched = await fixture.Manager.SwitchAsync(
            second.WorkspaceId,
            WorkspaceOpenMode.Writable);
        int protectionCallsAfterSwitch = fixture.Protection.CallCount;

        fixture.Transport.ReleasePlan();
        await latePlan;
        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            "field.change.apply",
            "late-workspace-apply",
            FieldApplyRequest(confirmations: []),
            string.Empty,
            ScopeFor(switched, 1)));

        Assert.AreEqual(
            protectionCallsAfterSwitch + 1,
            fixture.Protection.CallCount);
        Assert.IsTrue(fixture.Transport.ParametersFor("field.change.apply")
            .TryGetProperty("protectionSnapshotId", out _));
    }

    [TestMethod]
    [DataRow("invalid-action")]
    [DataRow("invalid-actor")]
    [DataRow("invalid-confirmations")]
    [DataRow("wrong-case-action")]
    [DataRow("duplicate-action")]
    [DataRow("duplicate-plan-id")]
    [DataRow("duplicate-backup-receipt")]
    public async Task MalformedFieldPayloadIsRejectedBeforeProtectionAndGateway(
        string malformedCase)
    {
        await using FieldProtectionHarness fixture =
            await FieldProtectionHarness.CreateAsync();
        (string requestType, JsonElement payload) = MalformedFieldPayload(malformedCase);

        await fixture.Controller.DispatchAsync(new RoutedWebRequest(
            requestType,
            "malformed-field-request",
            payload,
            string.Empty,
            ScopeFor(fixture.Opened, 1)));

        Assert.AreEqual(0, fixture.Protection.CallCount);
        Assert.AreEqual(0, fixture.Transport.WriteCount);
        FakeWebReplySink.Reply? failure = await fixture.Sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        StringAssert.Contains(
            JsonSerializer.Serialize(failure.Payload),
            @"""code"":""BAD_PAYLOAD""");
    }

    private static WorkspaceRequestDispatcher CreateDispatcher(
        FakeWebReplySink sink,
        WorkspaceSessionEnvelopeFilter filter)
        => new(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("local://configured"),
            sink,
            NoDatabaseOpenRoute.Instance,
            readRecoveryTimeout: TimeSpan.FromMilliseconds(750),
            sessionEnvelopeFilter: filter);

    private static JsonElement FieldPlanRequest(
        string action,
        string backupReceipt = "")
        => JsonSerializer.SerializeToElement(new
        {
            action,
            tableId = "tbl_records",
            fieldId = "fld_title",
            expectedSchemaRevision = "schema_1",
            expectedDataRevision = (long?)null,
            draft = (object?)null,
            actor = new { id = "tester", kind = "user" },
            conversionRule = "",
            confirmation = action == "purge" ? "Title" : "",
            backupReceipt,
        });

    private static JsonElement FieldApplyRequest(
        string[] confirmations,
        string? protectionSnapshotId = null)
    {
        var payload = new JsonObject
        {
            ["planId"] = FieldChangeTransport.PlanId,
            ["planHash"] = FieldChangeTransport.PlanHash,
            ["operationId"] = "operation-1",
            ["actor"] = new JsonObject
            {
                ["id"] = "tester",
                ["kind"] = "user",
            },
            ["confirmations"] = new JsonArray(
                confirmations.Select(value => JsonValue.Create(value)).ToArray()),
        };
        if (protectionSnapshotId is not null)
            payload["protectionSnapshotId"] = protectionSnapshotId;
        return JsonSerializer.SerializeToElement(payload);
    }

    private static (string Type, JsonElement Payload) MalformedFieldPayload(
        string malformedCase)
    {
        if (malformedCase == "invalid-confirmations")
        {
            JsonObject apply = JsonNode.Parse(
                FieldApplyRequest(confirmations: []).GetRawText())!
                .AsObject();
            apply["confirmations"] = new JsonArray(new JsonObject());
            return (
                "field.change.apply",
                JsonSerializer.SerializeToElement(apply));
        }
        if (malformedCase == "duplicate-plan-id")
        {
            string apply = FieldApplyRequest(confirmations: []).GetRawText()
                .Replace(
                    $"\"planId\":\"{FieldChangeTransport.PlanId}\"",
                    $"\"planId\":\"forged\",\"planId\":\"{FieldChangeTransport.PlanId}\"",
                    StringComparison.Ordinal);
            return ("field.change.apply", ParseClone(apply));
        }
        string planRaw = FieldPlanRequest("purge").GetRawText();
        if (malformedCase == "duplicate-action")
        {
            return (
                "field.change.plan",
                ParseClone(planRaw.Replace(
                    "\"action\":\"purge\"",
                    "\"action\":\"update\",\"action\":\"purge\"",
                    StringComparison.Ordinal)));
        }
        if (malformedCase == "duplicate-backup-receipt")
        {
            return (
                "field.change.plan",
                ParseClone(planRaw.Replace(
                    "\"backupReceipt\":\"\"",
                    "\"backupReceipt\":\"forged\",\"backupReceipt\":\"\"",
                    StringComparison.Ordinal)));
        }
        JsonObject plan = JsonNode.Parse(
            planRaw)!.AsObject();
        if (malformedCase == "invalid-action")
            plan["action"] = "destroy";
        else if (malformedCase == "invalid-actor")
            plan["actor"] = new JsonObject { ["id"] = "tester" };
        else if (malformedCase == "wrong-case-action")
        {
            plan.Remove("action");
            plan["Action"] = "purge";
        }
        else
            throw new ArgumentOutOfRangeException(nameof(malformedCase));
        return (
            "field.change.plan",
            JsonSerializer.SerializeToElement(plan));
    }

    private static JsonElement ParseClone(string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return document.RootElement.Clone();
    }

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

    private static RoutedWebRequest GoQueryRequest(
        string requestId,
        WorkspaceWireScope scope)
    {
        RoutedWebRequest request = QueryRequest(requestId, scope);
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = scope.Scope,
            workspaceId = scope.WorkspaceId,
            sessionEpoch = scope.SessionEpoch,
            operationId = scope.OperationId,
            sequence = scope.Sequence,
        });
        return request with { Wire = wire };
    }

    private static ProductRpcRouteSelector GoQuerySelector()
        => new(ProductRpcCapabilityManifest.CreateForTests(
            new ProductRpcCapability(
                "query.page",
                "workspace",
                "rendererPublic",
                "product.query.page",
                "goSidecar",
                "read")));

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

    private sealed class FieldProtectionHarness : IAsyncDisposable
    {
        private readonly SessionFixture _session;
        private readonly WorkspaceSessionEnvelopeFilter _filter;
        private readonly JsonRpcClient _client;
        private readonly JsonRpcProductDataGateway _gateway;

        private FieldProtectionHarness(
            SessionFixture session,
            WorkspaceSessionV2 opened,
            WorkspaceSessionEnvelopeFilter filter,
            FieldChangeTransport transport,
            JsonRpcClient client,
            JsonRpcProductDataGateway gateway,
            FakeWebReplySink sink,
            ProductDataRequestController controller)
        {
            _session = session;
            Opened = opened;
            _filter = filter;
            Transport = transport;
            _client = client;
            _gateway = gateway;
            Sink = sink;
            Controller = controller;
        }

        public WorkspaceSessionV2 Opened { get; }
        public FieldChangeTransport Transport { get; }
        public FakeWebReplySink Sink { get; }
        public ProductDataRequestController Controller { get; }
        public BlockingProtectionHook Protection => _session.Protection;
        public WorkspaceSessionManager Manager => _session.Manager;

        public static async Task<FieldProtectionHarness> CreateAsync(
            bool holdPlanResponse = false)
        {
            var session = new SessionFixture();
            WorkspaceRegistryEntryV2 first = session.AddWorkspace("Fields", "Fields");
            WorkspaceSessionV2 opened = await session.Manager.OpenAsync(
                first.WorkspaceId,
                WorkspaceOpenMode.Writable);
            var filter = new WorkspaceSessionEnvelopeFilter(session.Manager);
            var transport = new FieldChangeTransport(holdPlanResponse);
            var client = new JsonRpcClient(transport);
            var gateway = new JsonRpcProductDataGateway(client);
            var sink = new FakeWebReplySink();
            var controller = new ProductDataRequestController(
                sink,
                sessionEnvelopeFilter: filter);
            controller.SetGateway(gateway);
            return new FieldProtectionHarness(
                session,
                opened,
                filter,
                transport,
                client,
                gateway,
                sink,
                controller);
        }

        public WorkspaceRegistryEntryV2 AddWorkspace(
            string displayName,
            string folder)
            => _session.AddWorkspace(displayName, folder);

        public async ValueTask DisposeAsync()
        {
            _gateway.Dispose();
            await _client.DisposeAsync();
            _filter.Dispose();
            _session.Dispose();
        }
    }

    private sealed class FieldChangeTransport(bool holdPlanResponse = false)
        : IJsonLineTransport
    {
        public const string PlanId = "plan-field-protection";
        public const string PlanHash = "sha256:field-protection";
        private readonly Channel<JsonElement?> _incoming =
            Channel.CreateUnbounded<JsonElement?>();
        private readonly object _gate = new();
        private readonly List<(string Method, JsonElement Parameters)> _requests = [];
        private readonly TaskCompletionSource<bool> _planWritten =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource<bool> _releasePlan =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public Task PlanWritten => _planWritten.Task;

        public void ReleasePlan() => _releasePlan.TrySetResult(true);

        public int WriteCount
        {
            get
            {
                lock (_gate)
                    return _requests.Count;
            }
        }

        public JsonElement ParametersFor(string method)
        {
            lock (_gate)
                return _requests.Last(request => request.Method == method).Parameters;
        }

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public async Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            string id = request.RootElement.GetProperty("id").GetString()!;
            string method = request.RootElement.GetProperty("method").GetString()!;
            JsonElement parameters = request.RootElement.GetProperty("params").Clone();
            lock (_gate)
                _requests.Add((method, parameters));
            if (method == "field.change.plan")
            {
                _planWritten.TrySetResult(true);
                if (holdPlanResponse)
                    await _releasePlan.Task.ConfigureAwait(false);
            }
            JsonElement result = method switch
            {
                "field.change.plan" => PlanResult(parameters),
                "field.change.apply" => ApplyResult(parameters),
                _ => JsonSerializer.SerializeToElement(new { }),
            };
            JsonElement response = JsonSerializer.SerializeToElement(new
            {
                jsonrpc = "2.0",
                id,
                result,
            });
            _incoming.Writer.TryWrite(response);
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }

        private static JsonElement PlanResult(JsonElement parameters)
        {
            string action = parameters.GetProperty("action").GetString()!;
            string[] confirmations = action == "purge"
                ? ["backupReceipt", "fieldName"]
                : [];
            return JsonSerializer.SerializeToElement(new
            {
                contract = SchemaV2Contract.Name,
                planId = PlanId,
                planHash = PlanHash,
                expiresAt = "2026-08-20T12:00:00Z",
                intent = JsonSerializer.Deserialize<JsonElement>(
                    parameters.GetRawText()),
                before = (object?)null,
                after = (object?)null,
                classes = action == "purge" ? new[] { "danger" } : new[] { "display" },
                expectedSchemaRevision = "schema_1",
                expectedDataRevision = (long?)null,
                impact = new
                {
                    records = 0,
                    missing = 0,
                    ambiguous = 0,
                    failures = Array.Empty<object>(),
                    dependencies = Array.Empty<object>(),
                },
                steps = new[] { new { kind = "validate", details = new { } } },
                warnings = Array.Empty<object>(),
                errors = Array.Empty<object>(),
                confirmations,
                createsMigration = false,
                canApply = true,
            });
        }

        private static JsonElement ApplyResult(JsonElement parameters)
            => JsonSerializer.SerializeToElement(new
            {
                contract = SchemaV2Contract.Name,
                operationId = parameters.GetProperty("operationId").GetString(),
                planId = parameters.GetProperty("planId").GetString(),
                action = parameters.GetProperty("confirmations")
                    .EnumerateArray()
                    .Any(value => value.GetString() == "backupReceipt")
                        ? "purge"
                        : "update",
                tableId = "tbl_records",
                fieldId = "fld_title",
                schemaRevision = "schema_2",
                definition = (object?)null,
                migrationJobId = "",
            });
    }

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
        : IWorkspaceProtectionReceiptHook
    {
        private readonly TaskCompletionSource<bool> _entered =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource<bool> _release =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public Task Entered => _entered.Task;
        public int CallCount { get; private set; }
        public ProtectionSnapshotReceipt? LastReceipt { get; private set; }

        public async Task ProtectAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
        {
            _ = await CaptureCoreAsync(cancellationToken);
        }

        public Task<ProtectionSnapshotReceipt> CaptureAsync(
            Guid workspaceId,
            ulong sessionEpoch,
            string reason,
            CancellationToken cancellationToken)
            => CaptureCoreAsync(cancellationToken);

        private async Task<ProtectionSnapshotReceipt> CaptureCoreAsync(
            CancellationToken cancellationToken)
        {
            CallCount++;
            _entered.TrySetResult(true);
            if (blocked)
                await _release.Task.WaitAsync(cancellationToken);
            var receipt = new ProtectionSnapshotReceipt(
                new Guid(CallCount, 0, 0, new byte[8]),
                (ulong)CallCount);
            LastReceipt = receipt;
            return receipt;
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
            WorkspaceActivationBudget budget)
            => failStart
                ? Task.FromException(
                    new InvalidOperationException("injected start failure"))
                : Task.CompletedTask;
        public Task VerifyAsync(WorkspaceActivationBudget budget)
            => Task.CompletedTask;
        public Task DrainAsync(CancellationToken cancellationToken)
            => Task.CompletedTask;
        public Task StopAsync(CancellationToken cancellationToken)
            => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
