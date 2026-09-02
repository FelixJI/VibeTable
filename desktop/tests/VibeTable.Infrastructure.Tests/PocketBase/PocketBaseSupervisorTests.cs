using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Infrastructure.Tests.PocketBase;

[TestClass]
public sealed class PocketBaseSupervisorTests
{
    [TestMethod]
    public async Task StartAsync_PreservesWorkspaceAuthorityEnvironment()
    {
        PocketBaseLaunchOptions options = Options();
        options.Environment["VIBETABLE_WORKSPACE_ID"] =
            "11111111-1111-4111-8111-111111111111";
        options.Environment["VIBETABLE_WORKSPACE_SESSION_EPOCH"] = "17";
        options.Environment["VIBETABLE_WORKSPACE_FENCE_EPOCH"] = "3";
        options.Environment["VIBETABLE_WORKSPACE_CLAIM_ID"] =
            "22222222-2222-4222-8222-222222222222";
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(process);
        await using var supervisor = new PocketBaseSupervisor(
            options,
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));

        await supervisor.StartAsync(CancellationToken.None);

        PocketBaseProcessStartRequest request = factory.Requests.Single();
        foreach ((string key, string value) in options.Environment)
            Assert.AreEqual(value, request.Environment[key]);
        Assert.AreEqual(
            64,
            request.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"].Length);
    }

    [TestMethod]
    public async Task StartAsync_UsesEnvironmentSecretAndValidatesStructuredHealth()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var processFactory = new FakePocketBaseProcessFactory(process);
        var health = new FakePocketBaseHealthProbe(isHealthy: true);
        await using var supervisor = new PocketBaseSupervisor(
            Options(developmentMode: true),
            processFactory,
            health);

        await supervisor.StartAsync(CancellationToken.None);

        var request = processFactory.Requests.Single();
        Assert.AreEqual(
            64,
            request.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"].Length);
        CollectionAssert.DoesNotContain(
            request.Arguments.ToArray(),
            request.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"]);
        Assert.AreEqual("--data-dir", request.Arguments[0]);
        Assert.AreEqual("pb_data", request.Arguments[1]);
        Assert.AreEqual(
            new Uri("http://127.0.0.1:43125/api/vibetable/v1/health"),
            health.Requests.Single().Endpoint);
        Assert.AreEqual(
            request.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"],
            health.Requests.Single().SessionSecret);
        Assert.AreEqual(PocketBaseState.Ready, supervisor.GetStatus().State);
        Assert.IsTrue(supervisor.GetStatus().AdminAvailable);
        PocketBaseStartupTimings timings = supervisor.LastStartupTimings!;
        Assert.IsNotNull(timings);
        Assert.AreEqual("health", timings.LastStage);
        Assert.IsTrue(timings.SpawnDuration >= TimeSpan.Zero);
        Assert.IsTrue(timings.ReadyRecordDuration >= TimeSpan.Zero);
        Assert.IsTrue(timings.HealthDuration >= TimeSpan.Zero);
        Assert.AreEqual(
            new Uri("http://127.0.0.1:43125/_/"),
            supervisor.GetAdminUri());
        PocketBaseAdminContext? admin = supervisor.GetAdminContext();
        Assert.IsNotNull(admin);
        Assert.AreEqual(
            new Uri("http://127.0.0.1:43125/api/vibetable/v1/admin/bootstrap"),
            admin.BootstrapUri);
        Assert.AreEqual(
            request.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"],
            admin.SessionSecret);
    }

    [TestMethod]
    public async Task ConfigureBackendEnvironment_CopiesPrivateSessionWithoutChangingStatus()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(process);
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        var environment = new Dictionary<string, string>();

        supervisor.ConfigureBackendEnvironment(environment);

        Assert.AreEqual(
            "http://127.0.0.1:43125",
            environment["VIBETABLE_SIDECAR_URL"]);
        Assert.AreEqual(
            factory.Requests.Single()
                .Environment["VIBETABLE_SIDECAR_SESSION_SECRET"],
            environment["VIBETABLE_SIDECAR_SESSION_SECRET"]);
        Assert.IsFalse(
            supervisor.GetStatus().ToString()!.Contains(
                environment["VIBETABLE_SIDECAR_SESSION_SECRET"],
                StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task ConfigureBackendEnvironment_RejectsBeforeReadyAndAfterStop()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(process),
            new FakePocketBaseHealthProbe(isHealthy: true));
        var environment = new Dictionary<string, string>();

        Assert.Throws<InvalidOperationException>(
            () => supervisor.ConfigureBackendEnvironment(environment));
        await supervisor.StartAsync(CancellationToken.None);
        await supervisor.StopAsync(CancellationToken.None);
        Assert.Throws<InvalidOperationException>(
            () => supervisor.ConfigureBackendEnvironment(environment));
        Assert.AreEqual(0, environment.Count);
    }

    [TestMethod]
    public async Task StopAsync_KillsTheProcessTreeAndIsIdempotent()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(process);
        await using var supervisor = new PocketBaseSupervisor(
            Options(), factory, new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);

        await supervisor.StopAsync(CancellationToken.None);
        await supervisor.StopAsync(CancellationToken.None);

        Assert.AreEqual(1, process.KillProcessTreeCalls);
        Assert.IsTrue(process.Disposed);
        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task StopAsync_RequestsAuthenticatedGracefulShutdownBeforeForcefulFallback()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var health = new FakePocketBaseHealthProbe(
            isHealthy: true,
            shutdownAccepted: true,
            onShutdown: process.ExitGracefully);
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(process),
            health);
        await supervisor.StartAsync(CancellationToken.None);
        string secret = health.Requests.Single().SessionSecret;

        await supervisor.StopAsync(CancellationToken.None);

        Assert.AreEqual(0, process.KillProcessTreeCalls);
        Assert.AreEqual(1, health.ShutdownRequests.Count);
        Assert.AreEqual(
            new Uri("http://127.0.0.1:43125/api/vibetable/v1/shutdown"),
            health.ShutdownRequests.Single().Endpoint);
        Assert.AreEqual(secret, health.ShutdownRequests.Single().SessionSecret);
        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task StopAsync_CallerCancellationStillCompletesCleanupAndAllowsStart()
    {
        using var callerCts = new CancellationTokenSource();
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var second = FakePocketBaseProcess.Ready(ReadyRecord());
        var health = new FakePocketBaseHealthProbe(
            isHealthy: true,
            shutdownAccepted: true,
            onShutdown: () =>
            {
                first.ExitGracefully();
                callerCts.Cancel();
            });
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(first, second),
            health);
        await supervisor.StartAsync(CancellationToken.None);

        await Assert.ThrowsAsync<OperationCanceledException>(
            () => supervisor.StopAsync(callerCts.Token));

        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
        Assert.IsTrue(first.Disposed);
        await supervisor.StartAsync(CancellationToken.None);
        Assert.AreEqual(PocketBaseState.Ready, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task UnexpectedExit_AutoRecoversWithFreshSecretAndHonorsRestartCap()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var second = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, second);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        string firstSecret = factory.Requests[0]
            .Environment["VIBETABLE_SIDECAR_SESSION_SECRET"];

        first.Crash(exitCode: 17);
        await WaitUntilAsync(
            () => factory.Requests.Count == 2
                && supervisor.GetStatus().State == PocketBaseState.Ready);

        Assert.AreNotEqual(
            firstSecret,
            factory.Requests[1].Environment["VIBETABLE_SIDECAR_SESSION_SECRET"]);
        Assert.IsTrue(first.Disposed);

        second.Crash(exitCode: 23);
        await Task.Delay(30);
        Assert.AreEqual(2, factory.Requests.Count);
        Assert.AreEqual(PocketBaseState.Faulted, supervisor.GetStatus().State);
        Assert.AreEqual(23, supervisor.GetStatus().ExitCode);
    }

    [TestMethod]
    public async Task RepeatedStartWhileReady_DoesNotDisableCrashRecovery()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var recovered = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, recovered);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);

        await Assert.ThrowsAsync<InvalidOperationException>(
            () => supervisor.StartAsync(CancellationToken.None));
        first.Crash(exitCode: 17);
        await WaitUntilAsync(
            () => factory.Requests.Count == 2
                && supervisor.GetStatus().State == PocketBaseState.Ready);

        Assert.AreEqual(2, factory.Requests.Count);
        Assert.IsTrue(first.Disposed);
    }

    [TestMethod]
    public async Task FaultGateKilledSidecarPublishesDegradedThenRestartsReady()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var second = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, second);
        var published = new System.Collections.Concurrent.ConcurrentQueue<
            PocketBaseStatus>();
        var recoveredPublished = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int readyEvents = 0;
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        supervisor.StatusChanged += (_, status) =>
        {
            published.Enqueue(status);
            if (status.State == PocketBaseState.Ready
                && Interlocked.Increment(ref readyEvents) == 2)
                recoveredPublished.TrySetResult();
        };
        await supervisor.StartAsync(CancellationToken.None);

        first.Crash(exitCode: 137);
        await WaitUntilAsync(
            () => factory.Requests.Count == 2
                && supervisor.GetStatus().State == PocketBaseState.Ready);
        await recoveredPublished.Task.WaitAsync(TimeSpan.FromSeconds(5));

        List<PocketBaseStatus> ordered = published.ToList();
        int degraded = ordered.FindIndex(status =>
            status.State == PocketBaseState.Faulted
            && status.ExitCode == 137
            && status.Error?.Contains(
                "exited unexpectedly",
                StringComparison.Ordinal) == true);
        int restarting = ordered.FindIndex(
            Math.Max(0, degraded + 1),
            status => status.State == PocketBaseState.Starting);
        int recovered = ordered.FindIndex(
            Math.Max(0, restarting + 1),
            status => status.State == PocketBaseState.Ready);
        Assert.IsTrue(
            degraded >= 0 && restarting > degraded && recovered > restarting,
            "Host-visible lifecycle must be degraded/faulted -> starting -> ready.");
        Assert.AreNotEqual(
            factory.Requests[0].Environment["VIBETABLE_SIDECAR_SESSION_SECRET"],
            factory.Requests[1].Environment["VIBETABLE_SIDECAR_SESSION_SECRET"],
            "Recovered generation must rotate the private session secret.");
    }

    [TestMethod]
    public async Task BlockedReplacementFaultObserverDoesNotBlockLifecycle()
    {
        var faultEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseFault = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int faults = 0;
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var failed = FakePocketBaseProcess.Ready(
            ReadyRecord(migrationHash: "wrong-hash"));
        var recovered = FakePocketBaseProcess.Ready(ReadyRecord());
        var explicitRestart = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(
            first,
            failed,
            recovered,
            explicitRestart);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 2),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        supervisor.StatusChanged += (_, status) =>
        {
            if (status.State != PocketBaseState.Faulted
                || Interlocked.Increment(ref faults) != 2)
                return;
            faultEntered.TrySetResult();
            releaseFault.Task.GetAwaiter().GetResult();
        };
        await supervisor.StartAsync(CancellationToken.None);

        Task? starting = null;
        try
        {
            first.Crash(17);
            await faultEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await WaitUntilAsync(() =>
                factory.Requests.Count == 3
                    && supervisor.GetStatus().State == PocketBaseState.Ready);
            await supervisor.StopAsync(CancellationToken.None)
                .WaitAsync(TimeSpan.FromSeconds(5));
            Assert.IsTrue(failed.Disposed);
            starting = supervisor.StartAsync(CancellationToken.None);
            await WaitUntilAsync(() =>
                factory.Requests.Count == 4
                    && supervisor.GetStatus().State == PocketBaseState.Ready);
            Assert.IsFalse(starting.IsCompleted,
                "Manual Start must await its queued Ready notification.");
            releaseFault.TrySetResult();
            await starting.WaitAsync(TimeSpan.FromSeconds(5));
        }
        finally
        {
            releaseFault.TrySetResult();
            if (starting is not null)
                await starting.WaitAsync(TimeSpan.FromSeconds(5));
        }
    }

    [TestMethod]
    public async Task FaultObserverCanSynchronouslyStartReplacement()
    {
        var restarted = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var replacement = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, replacement);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 0),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        supervisor.StatusChanged += (_, status) =>
        {
            if (status.State != PocketBaseState.Faulted)
                return;
            try
            {
                supervisor.StartAsync(CancellationToken.None)
                    .GetAwaiter().GetResult();
                restarted.TrySetResult();
            }
            catch (Exception exception)
            {
                restarted.TrySetException(exception);
            }
        };

        Task crashing = Task.Run(() => first.Crash(17));
        await Task.WhenAll(crashing, restarted.Task)
            .WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreEqual(2, factory.Requests.Count);
        Assert.AreEqual(PocketBaseState.Ready, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task QueuedStatusUsesCommittedObserversAndIsolatesFailures()
    {
        var faultEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseFault = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var retainedReady = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var retained = new System.Collections.Concurrent.ConcurrentQueue<
            PocketBaseState>();
        var late = new System.Collections.Concurrent.ConcurrentQueue<
            PocketBaseState>();
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var recovered = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, recovered);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);

        void BlockFault(object? _, PocketBaseStatus status)
        {
            if (status.State != PocketBaseState.Faulted)
                return;
            faultEntered.TrySetResult();
            releaseFault.Task.GetAwaiter().GetResult();
        }
        void ThrowingObserver(object? _, PocketBaseStatus status)
            => throw new InvalidOperationException(status.State.ToString());
        void RetainedObserver(object? _, PocketBaseStatus status)
        {
            retained.Enqueue(status.State);
            if (status.State == PocketBaseState.Ready)
                retainedReady.TrySetResult();
        }
        void LateObserver(object? _, PocketBaseStatus status)
            => late.Enqueue(status.State);

        supervisor.StatusChanged += BlockFault;
        supervisor.StatusChanged += ThrowingObserver;
        supervisor.StatusChanged += RetainedObserver;
        Task crashing = Task.Run(() => first.Crash(17));
        try
        {
            await faultEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await WaitUntilAsync(() =>
                factory.Requests.Count == 2
                    && supervisor.GetStatus().State == PocketBaseState.Ready);
            supervisor.StatusChanged -= RetainedObserver;
            supervisor.StatusChanged += LateObserver;
            releaseFault.TrySetResult();

            await crashing.WaitAsync(TimeSpan.FromSeconds(5));
            await retainedReady.Task.WaitAsync(TimeSpan.FromSeconds(5));
            CollectionAssert.AreEqual(
                new[]
                {
                    PocketBaseState.Faulted,
                    PocketBaseState.Starting,
                    PocketBaseState.Ready,
                },
                retained.ToArray());
            Assert.AreEqual(0, late.Count);
        }
        finally
        {
            releaseFault.TrySetResult();
            await crashing.WaitAsync(TimeSpan.FromSeconds(5));
        }
    }

    [TestMethod]
    public async Task ExplicitStop_CancelsPendingCrashRecovery()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var unused = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, unused);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartDelay: TimeSpan.FromMilliseconds(100)),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);

        first.Crash(17);
        await supervisor.StopAsync(CancellationToken.None);
        await Task.Delay(150);

        Assert.AreEqual(1, factory.Requests.Count);
        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task StartAsync_OfflineHealthFailureFaultsWithoutUsingNonLoopbackEndpoint()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var health = new FakePocketBaseHealthProbe(
            exception: new HttpRequestException("offline"));
        await using var supervisor = new PocketBaseSupervisor(
            Options(startupTimeout: TimeSpan.FromMilliseconds(20)),
            new FakePocketBaseProcessFactory(process),
            health);

        await Assert.ThrowsAsync<TimeoutException>(
            () => supervisor.StartAsync(CancellationToken.None));

        Assert.AreEqual(PocketBaseState.Faulted, supervisor.GetStatus().State);
        Assert.AreEqual("health", supervisor.LastStartupTimings!.LastStage);
        Assert.IsNotNull(supervisor.LastStartupTimings.HealthDuration);
        Assert.IsTrue(process.KillProcessTreeCalls > 0);
        Assert.IsTrue(health.Requests.All(
            request => request.Endpoint.Host == "127.0.0.1"));
    }

    [TestMethod]
    public async Task StartAsync_RetriesAnIndividualHealthProbeTimeoutWithinStartupBudget()
    {
        int attempts = 0;
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var health = new FakePocketBaseHealthProbe(
            isHealthy: true,
            onHealth: () =>
            {
                attempts++;
                if (attempts == 1)
                    throw new TaskCanceledException("individual health probe timed out");
            });
        await using var supervisor = new PocketBaseSupervisor(
            Options(startupTimeout: TimeSpan.FromSeconds(1)),
            new FakePocketBaseProcessFactory(process),
            health);

        await supervisor.StartAsync(CancellationToken.None);

        Assert.AreEqual(2, health.Requests.Count);
        Assert.AreEqual(PocketBaseState.Ready, supervisor.GetStatus().State);
        Assert.AreEqual(0, process.KillProcessTreeCalls);
    }

    [TestMethod]
    public async Task Diagnostics_RedactsASecretEmittedWhileProcessIsBeingDisposed()
    {
        var factory = new FakePocketBaseProcessFactory(request =>
            FakePocketBaseProcess.ReadyWithLateStderr(
                ReadyRecord(),
                $"late {request.Environment["VIBETABLE_SIDECAR_SESSION_SECRET"]}"));
        await using var supervisor = new PocketBaseSupervisor(
            Options(), factory, new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        string secret = factory.Requests.Single()
            .Environment["VIBETABLE_SIDECAR_SESSION_SECRET"];

        await supervisor.StopAsync(CancellationToken.None);
        await WaitUntilAsync(
            () => supervisor.GetSanitizedLog().Contains(
                "[REDACTED]",
                StringComparison.Ordinal));

        Assert.IsFalse(supervisor.GetSanitizedLog().Contains(
            secret,
            StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task StartAsync_RejectsAReadyRecordOutsideIpv4Loopback()
    {
        var health = new FakePocketBaseHealthProbe(isHealthy: true);
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(FakePocketBaseProcess.Ready(
                ReadyRecord(address: "0.0.0.0:43125"))),
            health);

        await Assert.ThrowsAsync<InvalidOperationException>(
            () => supervisor.StartAsync(CancellationToken.None));

        Assert.AreEqual(PocketBaseState.Faulted, supervisor.GetStatus().State);
        Assert.AreEqual(0, health.Requests.Count);
    }

    [TestMethod]
    public async Task StartAsync_RejectsReadinessIdentityMismatch()
    {
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(FakePocketBaseProcess.Ready(
                ReadyRecord(migrationHash: "wrong-hash"))),
            new FakePocketBaseHealthProbe(isHealthy: true));

        InvalidOperationException exception =
            await Assert.ThrowsAsync<InvalidOperationException>(
                () => supervisor.StartAsync(CancellationToken.None));

        StringAssert.Contains(exception.Message, "identity");
        Assert.AreEqual(PocketBaseState.Faulted, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task StartAsync_RejectsHealthIdentityMismatch()
    {
        var health = new FakePocketBaseHealthProbe(
            status: HealthyStatus(migrationHash: "wrong-hash"));
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(
                FakePocketBaseProcess.Ready(ReadyRecord())),
            health);

        InvalidOperationException exception =
            await Assert.ThrowsAsync<InvalidOperationException>(
                () => supervisor.StartAsync(CancellationToken.None));

        StringAssert.Contains(exception.Message, "identity");
        Assert.AreEqual(PocketBaseState.Faulted, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task ExitAtReadyBoundary_NeverPublishesReady()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        var published = new List<PocketBaseState>();
        var health = new FakePocketBaseHealthProbe(
            isHealthy: true,
            onHealth: () => process.Crash(31));
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(process),
            health);
        supervisor.StatusChanged += (_, status) => published.Add(status.State);

        await Assert.ThrowsAsync<InvalidOperationException>(
            () => supervisor.StartAsync(CancellationToken.None));

        CollectionAssert.DoesNotContain(published, PocketBaseState.Ready);
        Assert.AreEqual(PocketBaseState.Faulted, supervisor.GetStatus().State);
    }

    private static PocketBaseLaunchOptions Options(
        TimeSpan? startupTimeout = null,
        int crashRestartLimit = 3,
        bool developmentMode = false,
        TimeSpan? crashRestartDelay = null) => new()
    {
        ExecutablePath = "vibetable-pb.exe",
        DataDirectory = "pb_data",
        DevelopmentMode = developmentMode,
        StartupTimeout = startupTimeout ?? TimeSpan.FromSeconds(1),
        StopTimeout = TimeSpan.FromSeconds(1),
        HealthPollInterval = TimeSpan.FromMilliseconds(1),
        CrashRestartLimit = crashRestartLimit,
        CrashRestartInitialDelay =
            crashRestartDelay ?? TimeSpan.FromMilliseconds(1),
        CrashRestartMaximumDelay =
            crashRestartDelay ?? TimeSpan.FromMilliseconds(2),
        ExpectedIdentity = ExpectedIdentity(),
    };

    private static PocketBaseExpectedIdentity ExpectedIdentity() => new(
        ReadyContract: "vibetable.sidecar.ready.v1",
        ContractVersion: "v1",
        PocketBaseVersion: "0.40.1",
        SchemaVersion: "1",
        MigrationHash: "migration-hash");

    private static PocketBaseHealthStatus HealthyStatus(
        string migrationHash = "migration-hash") => new(
        Status: "ok",
        PocketBase: "ok",
        SchemaReady: true,
        StorageWritable: true,
        Build: new PocketBaseBuildIdentity(
            ContractVersion: "v1",
            PocketBaseVersion: "0.40.1",
            SchemaVersion: "1",
            MigrationHash: migrationHash));

    private static string ReadyRecord(
        string address = "127.0.0.1:43125",
        string migrationHash = "migration-hash") =>
        System.Text.Json.JsonSerializer.Serialize(new
        {
            contract = "vibetable.sidecar.ready.v1",
            @event = "sidecar.ready",
            address,
            pid = 42,
            build = new
            {
                version = "0.1.0-dev",
                commit = "unknown",
                buildTime = "unknown",
                contractVersion = "v1",
                pocketBaseVersion = "0.40.1",
                celVersion = "0.31.0",
                schemaVersion = "1",
                migrationHash,
            },
        });

    private static async Task WaitUntilAsync(Func<bool> condition)
    {
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(2));
        while (!condition())
        {
            await Task.Delay(5, timeout.Token);
        }
    }

    private sealed class FakePocketBaseProcessFactory : IPocketBaseProcessFactory
    {
        private readonly Queue<IPocketBaseProcess> _processes;
        private readonly Func<PocketBaseProcessStartRequest, IPocketBaseProcess>? _factory;

        public FakePocketBaseProcessFactory(params IPocketBaseProcess[] processes)
        {
            _processes = new Queue<IPocketBaseProcess>(processes);
        }

        public FakePocketBaseProcessFactory(
            Func<PocketBaseProcessStartRequest, IPocketBaseProcess> factory)
        {
            _processes = [];
            _factory = factory;
        }

        public List<PocketBaseProcessStartRequest> Requests { get; } = [];

        public IPocketBaseProcess Start(PocketBaseProcessStartRequest request)
        {
            Requests.Add(request);
            return _factory?.Invoke(request) ?? _processes.Dequeue();
        }
    }

    private sealed class FakePocketBaseHealthProbe : IPocketBaseHealthProbe
    {
        private readonly PocketBaseHealthStatus? _status;
        private readonly Exception? _exception;
        private readonly bool _shutdownAccepted;
        private readonly Action? _onShutdown;
        private readonly Action? _onHealth;

        public FakePocketBaseHealthProbe(
            bool isHealthy = false,
            PocketBaseHealthStatus? status = null,
            Exception? exception = null,
            bool shutdownAccepted = false,
            Action? onShutdown = null,
            Action? onHealth = null)
        {
            _status = status ?? (isHealthy
                ? HealthyStatus()
                : new PocketBaseHealthStatus(
                    "starting",
                    "ok",
                    true,
                    true,
                    HealthyStatus().Build));
            _exception = exception;
            _shutdownAccepted = shutdownAccepted;
            _onShutdown = onShutdown;
            _onHealth = onHealth;
        }

        public List<(Uri Endpoint, string SessionSecret)> Requests { get; } = [];
        public List<(Uri Endpoint, string SessionSecret)> ShutdownRequests { get; } = [];

        public Task<PocketBaseHealthStatus?> GetHealthAsync(
            Uri endpoint,
            string sessionSecret,
            CancellationToken cancellationToken)
        {
            Requests.Add((endpoint, sessionSecret));
            _onHealth?.Invoke();
            return _exception is null
                ? Task.FromResult(_status)
                : Task.FromException<PocketBaseHealthStatus?>(_exception);
        }

        public Task<bool> RequestShutdownAsync(
            Uri endpoint,
            string sessionSecret,
            CancellationToken cancellationToken)
        {
            ShutdownRequests.Add((endpoint, sessionSecret));
            _onShutdown?.Invoke();
            return Task.FromResult(_shutdownAccepted);
        }
    }

    private sealed class FakePocketBaseProcess : IPocketBaseProcess
    {
        private readonly TextReader _stdout;
        private readonly TextReader _stderr;
        private int _exitCode;

        private FakePocketBaseProcess(TextReader stdout, TextReader stderr)
        {
            _stdout = stdout;
            _stderr = stderr;
        }

        public static FakePocketBaseProcess Ready(string record, string stderr = "")
            => new(
                new StringReader(record + Environment.NewLine),
                new StringReader(stderr + Environment.NewLine));

        public static FakePocketBaseProcess ReadyWithLateStderr(
            string record,
            string lateStderr)
            => new(
                new StringReader(record + Environment.NewLine),
                new LateLineTextReader(lateStderr));

        public int Id => 42;
        public TextReader StandardOutput => _stdout;
        public TextReader StandardError => _stderr;
        public bool HasExited { get; private set; }
        public int? ExitCode => HasExited ? _exitCode : null;
        public int KillProcessTreeCalls { get; private set; }
        public bool Disposed { get; private set; }
        public event EventHandler? Exited;

        public void KillProcessTree()
        {
            KillProcessTreeCalls++;
            HasExited = true;
            Exited?.Invoke(this, EventArgs.Empty);
        }

        public void Crash(int exitCode)
        {
            _exitCode = exitCode;
            HasExited = true;
            Exited?.Invoke(this, EventArgs.Empty);
        }

        public void ExitGracefully()
        {
            _exitCode = 0;
            HasExited = true;
            Exited?.Invoke(this, EventArgs.Empty);
        }

        public Task WaitForExitAsync(CancellationToken cancellationToken)
            => Task.CompletedTask;

        public ValueTask DisposeAsync()
        {
            Disposed = true;
            _stdout.Dispose();
            _stderr.Dispose();
            return ValueTask.CompletedTask;
        }
    }

    private sealed class LateLineTextReader(string lateLine) : TextReader
    {
        private readonly TaskCompletionSource<string?> _line =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _readCount;

        public override Task<string?> ReadLineAsync()
            => Interlocked.Increment(ref _readCount) == 1
                ? _line.Task
                : Task.FromResult<string?>(null);

        protected override void Dispose(bool disposing)
        {
            if (disposing)
            {
                _line.TrySetResult(lateLine);
            }
            base.Dispose(disposing);
        }
    }
}
