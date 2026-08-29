using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class UpdateActivationWorkspaceHealthGateTests
{
    [TestMethod]
    public async Task ConfirmAsync_ProbesMostRecentWorkspaceReadOnlyBeforeConfirming()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-");
        WorkspaceRegistryEntryV2 older = fixture.AddDirectWorkspace("Older");
        WorkspaceRegistryEntryV2 recent = fixture.AddDirectWorkspace("Recent");
        recent = fixture.Registry.Register(recent with
        {
            LastOpenedAt = DateTimeOffset.UtcNow,
        });
        const ulong epoch = 37;
        fixture.Session.Open = (workspaceId, mode, switching, _) =>
        {
            Assert.AreEqual(recent.WorkspaceId, workspaceId);
            Assert.AreEqual(WorkspaceOpenMode.ReadOnly, mode);
            Assert.IsFalse(switching);
            return Task.FromResult(OpenReadOnly(workspaceId, epoch));
        };
        var reader = new RecordingSchemaReader(4);
        var activation = new RecordingSettlement();
        bool readyReported = false;
        activation.BeforeHealthy = () => Assert.IsTrue(readyReported);
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromSeconds(1),
            _ => readyReported = true);

        UpdateWorkspaceHealthProbeReceipt receipt = await gate.ConfirmAsync(
            activation,
            CancellationToken.None);

        Assert.AreEqual(UpdateWorkspaceHealthProbeStatus.Healthy, receipt.Status);
        Assert.AreEqual(recent.WorkspaceId, receipt.WorkspaceId);
        Assert.AreEqual(epoch, receipt.SessionEpoch);
        Assert.AreEqual(4, receipt.TableCount);
        Assert.AreEqual(WorkspaceOpenMode.ReadOnly, fixture.Session.LastOpenMode);
        Assert.AreEqual(1, fixture.Session.CloseCount);
        Assert.AreEqual(OpenReadOnly(recent.WorkspaceId, epoch), reader.Session);
        Assert.AreEqual(1, activation.HealthyCount);
        Assert.AreEqual(0, activation.FailedCount);
        Assert.AreNotEqual(older.WorkspaceId, receipt.WorkspaceId);
    }

    [TestMethod]
    public async Task ConfirmAsync_WithoutRegisteredWorkspaceSkipsProbeAndConfirms()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-empty-");
        var reader = new RecordingSchemaReader(0);
        var activation = new RecordingSettlement();
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromSeconds(1));

        UpdateWorkspaceHealthProbeReceipt receipt = await gate.ConfirmAsync(
            activation,
            CancellationToken.None);

        Assert.AreEqual(
            UpdateWorkspaceHealthProbeStatus.SkippedNoRegisteredWorkspace,
            receipt.Status);
        Assert.IsNull(fixture.Session.LastOpen);
        Assert.IsNull(reader.Session);
        Assert.AreEqual(1, activation.HealthyCount);
        Assert.AreEqual(0, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_ExactUpdatedHealthHoldLeavesSettlementPending()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-hold-");
        string root = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-update-health-hold-{Guid.NewGuid():N}");
        string readiness = Path.Combine(root, "self-update-readiness");
        string controls = Path.Combine(root, "self-update-updated-controls");
        Directory.CreateDirectory(readiness);
        Directory.CreateDirectory(controls);
        string request = Path.Combine(
            controls,
            "self-update-health-timeout-hold.request");
        File.WriteAllText(request, string.Empty);
        try
        {
            HostStartupOptions startup = HostStartupOptions.Parse([
                "--self-update-smoke",
                "--test-mode",
                "--readiness-dir", readiness,
                "--e2e-controls-dir", controls,
            ]);
            var reader = new RecordingSchemaReader(0);
            var activation = new RecordingSettlement();
            var gate = new UpdateActivationWorkspaceHealthGate(
                fixture.Registry,
                fixture.Session,
                reader,
                TimeSpan.FromSeconds(1),
                startupOptions: startup);
            using var cancelled = new CancellationTokenSource();

            Task<UpdateWorkspaceHealthProbeReceipt> pending = gate.ConfirmAsync(
                activation,
                cancelled.Token);

            Assert.IsFalse(pending.IsCompleted);
            Assert.IsFalse(File.Exists(request));
            Assert.IsNull(fixture.Session.LastOpen);
            Assert.IsNull(reader.Session);
            Assert.AreEqual(0, activation.HealthyCount);
            Assert.AreEqual(0, activation.FailedCount);

            cancelled.Cancel();
            await Assert.ThrowsExactlyAsync<TaskCanceledException>(async () =>
                await pending);
            Assert.AreEqual(0, activation.HealthyCount);
            Assert.AreEqual(0, activation.FailedCount);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task ConfirmAsync_WhenSessionIsAlreadyOpenFailsWithoutTakingOwnership()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-busy-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Busy");
        fixture.Session.CurrentSession = OpenWritable(workspace.WorkspaceId, 11);
        var reader = new RecordingSchemaReader(0);
        var activation = new RecordingSettlement();
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromSeconds(1));

        UpdateWorkspaceHealthException error = await Assert.ThrowsExactlyAsync<
            UpdateWorkspaceHealthException>(() => gate.ConfirmAsync(
                activation,
                CancellationToken.None));

        Assert.AreEqual("update.workspace_probe_busy", error.Code);
        Assert.IsNull(fixture.Session.LastOpen);
        Assert.AreEqual(0, fixture.Session.CloseCount);
        Assert.IsNull(reader.Session);
        Assert.AreEqual(0, activation.HealthyCount);
        Assert.AreEqual(1, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_WhenSchemaProbeFailsClosesSessionAndFailsActivation()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-failure-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Failure");
        fixture.Session.Open = (workspaceId, _, _, _) =>
            Task.FromResult(OpenReadOnly(workspaceId, 19));
        var expected = new InvalidOperationException("schema unavailable");
        var reader = new RecordingSchemaReader(expected);
        var activation = new RecordingSettlement();
        bool failureReported = false;
        activation.BeforeFailed = () => Assert.IsTrue(failureReported);
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromSeconds(1),
            reportFailure: error =>
            {
                Assert.AreSame(expected, error);
                failureReported = true;
            });

        InvalidOperationException error = await Assert.ThrowsExactlyAsync<
            InvalidOperationException>(() => gate.ConfirmAsync(
                activation,
                CancellationToken.None));

        Assert.AreSame(expected, error);
        Assert.AreEqual(1, fixture.Session.CloseCount);
        Assert.AreEqual(0, activation.HealthyCount);
        Assert.AreEqual(1, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_WhenProbeAndCloseFailPreservesBothFailures()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-cleanup-failure-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("CleanupFailure");
        fixture.Session.Open = (workspaceId, _, _, _) =>
            Task.FromResult(OpenReadOnly(workspaceId, 21));
        var probeFailure = new InvalidOperationException("schema unavailable");
        var closeFailure = new IOException("runtime did not stop");
        fixture.Session.Close = (_, _) =>
            Task.FromException<WorkspaceSessionV2>(closeFailure);
        var activation = new RecordingSettlement();
        Exception? reportedFailure = null;
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            new RecordingSchemaReader(probeFailure),
            TimeSpan.FromSeconds(1),
            reportFailure: error => reportedFailure = error);

        AggregateException error = await Assert.ThrowsExactlyAsync<AggregateException>(
            () => gate.ConfirmAsync(activation, CancellationToken.None));

        CollectionAssert.AreEqual(
            new Exception[] { probeFailure, closeFailure },
            error.InnerExceptions);
        Assert.AreSame(error, reportedFailure);
        Assert.AreEqual(1, fixture.Session.CloseCount);
        Assert.AreEqual(0, activation.HealthyCount);
        Assert.AreEqual(1, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_RejectsWritableSessionBeforeReadingSchema()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-writable-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Writable");
        fixture.Session.Open = (workspaceId, _, _, _) =>
            Task.FromResult(OpenWritable(workspaceId, 23));
        var reader = new RecordingSchemaReader(0);
        var activation = new RecordingSettlement();
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromSeconds(1));

        UpdateWorkspaceHealthException error = await Assert.ThrowsExactlyAsync<
            UpdateWorkspaceHealthException>(() => gate.ConfirmAsync(
                activation,
                CancellationToken.None));

        Assert.AreEqual("update.workspace_probe_session_invalid", error.Code);
        Assert.IsNull(reader.Session);
        Assert.AreEqual(1, fixture.Session.CloseCount);
        Assert.AreEqual(1, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_WhenSchemaProbeTimesOutUsesStableErrorAndCloses()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-timeout-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Timeout");
        fixture.Session.Open = (workspaceId, _, _, _) =>
            Task.FromResult(OpenReadOnly(workspaceId, 29));
        var reader = new RecordingSchemaReader();
        var activation = new RecordingSettlement();
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromMilliseconds(20));

        UpdateWorkspaceHealthException error = await Assert.ThrowsExactlyAsync<
            UpdateWorkspaceHealthException>(() => gate.ConfirmAsync(
                activation,
                CancellationToken.None));

        Assert.AreEqual("update.workspace_probe_timeout", error.Code);
        Assert.AreEqual(1, fixture.Session.CloseCount);
        Assert.AreEqual(1, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_WhenCallerCancelsPreservesCancellationAndStillCloses()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-cancel-");
        WorkspaceRegistryEntryV2 workspace = fixture.AddDirectWorkspace("Cancel");
        fixture.Session.Open = (workspaceId, _, _, _) =>
            Task.FromResult(OpenReadOnly(workspaceId, 31));
        var reader = new RecordingSchemaReader();
        var activation = new RecordingSettlement();
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            reader,
            TimeSpan.FromSeconds(1));
        using var cancelled = new CancellationTokenSource(TimeSpan.FromMilliseconds(20));

        await Assert.ThrowsExactlyAsync<TaskCanceledException>(() =>
            gate.ConfirmAsync(activation, cancelled.Token));

        Assert.AreEqual(1, fixture.Session.CloseCount);
        Assert.IsFalse(fixture.Session.LastCloseTokenCanBeCanceled);
        Assert.AreEqual(1, activation.FailedCount);
    }

    [TestMethod]
    public async Task ConfirmAsync_WhenReadinessEvidenceCannotBeWrittenFailsActivation()
    {
        using var fixture = new WorkspaceRegistryTopologyTestContext(
            "vibetable-update-health-readiness-");
        var activation = new RecordingSettlement();
        var expected = new IOException("readiness unavailable");
        var gate = new UpdateActivationWorkspaceHealthGate(
            fixture.Registry,
            fixture.Session,
            new RecordingSchemaReader(0),
            TimeSpan.FromSeconds(1),
            reportReady: _ => throw expected);

        IOException error = await Assert.ThrowsExactlyAsync<IOException>(() =>
            gate.ConfirmAsync(activation, CancellationToken.None));

        Assert.AreSame(expected, error);
        Assert.AreEqual(0, activation.HealthyCount);
        Assert.AreEqual(1, activation.FailedCount);
    }

    private static WorkspaceSessionV2 OpenReadOnly(Guid workspaceId, ulong epoch) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = workspaceId,
        SessionEpoch = epoch,
        State = WorkspaceSessionState.OpenedReadOnly,
        OpenMode = WorkspaceOpenMode.ReadOnly,
        Writable = false,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };

    private static WorkspaceSessionV2 OpenWritable(Guid workspaceId, ulong epoch) => new()
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

    private sealed class RecordingSchemaReader : IUpdateWorkspaceSchemaReader
    {
        private readonly int _tableCount;
        private readonly Exception? _exception;
        private readonly bool _hang;

        public RecordingSchemaReader(int tableCount) => _tableCount = tableCount;
        public RecordingSchemaReader(Exception exception) => _exception = exception;
        public RecordingSchemaReader() => _hang = true;

        public WorkspaceSessionV2? Session { get; private set; }

        public Task<int> ReadTableCountAsync(
            WorkspaceSessionV2 expectedSession,
            CancellationToken cancellationToken)
        {
            Session = expectedSession;
            if (_exception is not null)
            {
                return Task.FromException<int>(_exception);
            }
            if (_hang)
            {
                return Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken)
                    .ContinueWith(
                        _ => 0,
                        cancellationToken,
                        TaskContinuationOptions.ExecuteSynchronously,
                        TaskScheduler.Default);
            }
            return Task.FromResult(_tableCount);
        }
    }

    private sealed class RecordingSettlement : IUpdateActivationSettlement
    {
        public int HealthyCount { get; private set; }
        public int FailedCount { get; private set; }
        public Action? BeforeHealthy { get; set; }
        public Action? BeforeFailed { get; set; }

        public Task CompleteHealthCheckAsync(
            UpdateActivationHealth health,
            CancellationToken cancellationToken)
        {
            if (health is UpdateActivationHealth.Healthy)
            {
                BeforeHealthy?.Invoke();
                HealthyCount++;
            }
            else
            {
                BeforeFailed?.Invoke();
                FailedCount++;
            }
            return Task.CompletedTask;
        }
    }
}
