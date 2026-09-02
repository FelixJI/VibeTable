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
    public async Task CaptureCurrentGeneration_ReusesFixedReadyContextWithoutExposingSecret()
    {
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(process),
            new FakePocketBaseHealthProbe(isHealthy: true));

        await supervisor.StartAsync(CancellationToken.None);

        int readsBeforeCapture = process.HasExitedReadCount;
        process.ThrowOnHasExitedRead = true;
        PocketBaseGenerationContext first;
        PocketBaseGenerationContext second;
        try
        {
            first = supervisor.CaptureCurrentGeneration()!;
            second = supervisor.CaptureCurrentGeneration(first.GenerationId)!;
        }
        finally
        {
            process.ThrowOnHasExitedRead = false;
        }

        Assert.AreEqual(readsBeforeCapture, process.HasExitedReadCount);
        Assert.IsTrue(first.GenerationId > 0);
        Assert.AreSame(first, second);
        Assert.AreSame(first.AdminContext, supervisor.GetAdminContext());
        Assert.IsFalse(
            first.ToString().Contains(
                first.AdminContext.SessionSecret,
                StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task CaptureCurrentGeneration_RejectsRetiredGenerationAfterRestart()
    {
        var firstProcess = FakePocketBaseProcess.Ready(ReadyRecord());
        var secondProcess = FakePocketBaseProcess.Ready(ReadyRecord());
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(firstProcess, secondProcess),
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        PocketBaseGenerationContext first = supervisor.CaptureCurrentGeneration()!;

        await supervisor.StopAsync(CancellationToken.None);
        Assert.IsNull(supervisor.CaptureCurrentGeneration(first.GenerationId));
        await supervisor.StartAsync(CancellationToken.None);

        PocketBaseGenerationContext second = supervisor.CaptureCurrentGeneration()!;
        Assert.IsTrue(second.GenerationId > first.GenerationId);
        Assert.AreNotSame(first, second);
        Assert.AreNotSame(first.AdminContext, second.AdminContext);
        Assert.IsNull(supervisor.CaptureCurrentGeneration(first.GenerationId));
        Assert.AreSame(
            second,
            supervisor.CaptureCurrentGeneration(second.GenerationId));
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task CaptureCurrentGeneration_ConsumesIdForFailedProcessAttempt()
    {
        var firstProcess = FakePocketBaseProcess.Ready(ReadyRecord());
        var thirdProcess = FakePocketBaseProcess.Ready(ReadyRecord());
        int attempt = 0;
        var factory = new FakePocketBaseProcessFactory(_ =>
            Interlocked.Increment(ref attempt) switch
            {
                1 => firstProcess,
                2 => throw new IOException("spawn failed"),
                3 => thirdProcess,
                _ => throw new InvalidOperationException("Unexpected attempt."),
            });
        await using var supervisor = new PocketBaseSupervisor(
            Options(), factory, new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        PocketBaseGenerationContext first = supervisor.CaptureCurrentGeneration()!;
        await supervisor.StopAsync(CancellationToken.None);

        await Assert.ThrowsAsync<IOException>(
            () => supervisor.StartAsync(CancellationToken.None));
        await supervisor.StartAsync(CancellationToken.None);

        PocketBaseGenerationContext third = supervisor.CaptureCurrentGeneration()!;
        Assert.AreEqual(first.GenerationId + 2, third.GenerationId);
        Assert.AreEqual(3, factory.Requests.Count);
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
    public async Task StopAdmittedDuringStartup_PreventsReadyPublication()
    {
        var healthEntered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseHealth = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var published =
            new System.Collections.Concurrent.ConcurrentQueue<PocketBaseState>();
        var process = FakePocketBaseProcess.Ready(ReadyRecord());
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(process),
            new FakePocketBaseHealthProbe(
                getHealth: async _ =>
                {
                    healthEntered.TrySetResult();
                    await releaseHealth.Task;
                    return HealthyStatus();
                }));
        supervisor.StatusChanged += (_, status) => published.Enqueue(status.State);

        Task starting = supervisor.StartAsync(CancellationToken.None);
        Task? stopping = null;
        try
        {
            await healthEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
            stopping = supervisor.StopAsync(CancellationToken.None);
        }
        finally
        {
            releaseHealth.TrySetResult();
        }

        await Assert.ThrowsAsync<InvalidOperationException>(
            () => starting.WaitAsync(TimeSpan.FromSeconds(2)));
        Assert.IsNotNull(stopping);
        await stopping!.WaitAsync(TimeSpan.FromSeconds(2));
        CollectionAssert.DoesNotContain(published.ToArray(), PocketBaseState.Ready);
        Assert.IsTrue(process.Disposed);
        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task ConcurrentStartDuringStartup_WaitsThenRejectsWithoutRetirement()
    {
        var healthEntered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseHealth = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var unused = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, unused);
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            factory,
            new FakePocketBaseHealthProbe(getHealth: async _ =>
            {
                healthEntered.TrySetResult();
                await releaseHealth.Task;
                return HealthyStatus();
            }));

        Task firstStart = supervisor.StartAsync(CancellationToken.None);
        Task? secondStart = null;
        try
        {
            await healthEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
            secondStart = supervisor.StartAsync(CancellationToken.None);
            Assert.IsFalse(secondStart.IsCompleted);
        }
        finally
        {
            releaseHealth.TrySetResult();
        }

        await firstStart.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsNotNull(secondStart);
        await Assert.ThrowsAsync<InvalidOperationException>(
            () => secondStart!.WaitAsync(TimeSpan.FromSeconds(2)));
        Assert.AreEqual(1, factory.Requests.Count);
        Assert.AreEqual(PocketBaseState.Ready, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task CrashBeforePendingStartEntersFifo_IsReplacedByExplicitStart()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var replacement = FakePocketBaseProcess.Ready(ReadyRecord());
        var unused = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, replacement, unused);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        var admitted = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        supervisor.StartAdmissionBarrierForTests = async () =>
        {
            admitted.TrySetResult();
            await release.Task;
        };
        Task restarting = supervisor.StartAsync(CancellationToken.None);
        await admitted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        try
        {
            first.Crash(exitCode: 17);
            await supervisor.WaitForRecoveryQuiescenceAsync()
                .WaitAsync(TimeSpan.FromSeconds(2));
            Assert.AreEqual(1, factory.Requests.Count);
        }
        finally
        {
            release.TrySetResult();
        }
        await restarting.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual((2, PocketBaseState.Ready),
            (factory.Requests.Count, supervisor.GetStatus().State));
    }

    [TestMethod]
    public async Task StartCancellationAfterRecoveryRetirement_CompletesCleanup()
    {
        using var callerCts = new CancellationTokenSource();
        var first = FakePocketBaseProcess.ReadyWithBlockedDispose(ReadyRecord());
        var unused = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, unused);
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);

        first.Crash(exitCode: 17);
        Task? replacing = null;
        try
        {
            await first.DisposeEntered.WaitAsync(TimeSpan.FromSeconds(2));
            replacing = supervisor.StartAsync(callerCts.Token);
            callerCts.Cancel();
        }
        finally
        {
            first.ReleaseDispose();
        }

        Assert.IsNotNull(replacing);
        await Assert.ThrowsAsync<OperationCanceledException>(
            () => replacing!.WaitAsync(TimeSpan.FromSeconds(2)));
        await supervisor.StopAsync(CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(1, factory.Requests.Count);
        Assert.IsTrue(first.Disposed);
        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task RecoveryCancellationCallbackFailure_CompletesSharedDispose()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var recovering = FakePocketBaseProcess.Ready(
            ReadyRecord(),
            disposeFailure: new IOException("recovery process dispose failed"));
        var healthEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseHealth = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int healthCalls = 0;
        var health = new FakePocketBaseHealthProbe(
            getHealth: async cancellationToken =>
            {
                if (Interlocked.Increment(ref healthCalls) == 1)
                {
                    return HealthyStatus();
                }

                using CancellationTokenRegistration registration =
                    cancellationToken.Register(
                        () => throw new InvalidOperationException(
                            "recovery cancellation callback failed"));
                healthEntered.SetResult();
                await releaseHealth.Task;
                return HealthyStatus();
            });
        var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            new FakePocketBaseProcessFactory(first, recovering),
            health);
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            first.Crash(exitCode: 17);
            await healthEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));

            Task disposing = supervisor.DisposeAsync().AsTask();
            Task repeatedDispose = supervisor.DisposeAsync().AsTask();
            Task stopping = supervisor.StopAsync(CancellationToken.None);
            releaseHealth.SetResult();

            Assert.AreSame(disposing, repeatedDispose);
            AggregateException failure =
                await Assert.ThrowsAsync<AggregateException>(
                () => disposing.WaitAsync(TimeSpan.FromSeconds(2)));
            string errors = string.Join(
                '|',
                failure.Flatten().InnerExceptions
                    .Select(exception => exception.Message)
                    .Order());
            Assert.AreEqual(
                "recovery cancellation callback failed|recovery process dispose failed",
                errors);
            await Assert.ThrowsAsync<AggregateException>(
                () => stopping.WaitAsync(TimeSpan.FromSeconds(2)));
            Assert.IsTrue(first.Disposed);
            Assert.IsTrue(recovering.Disposed);
            Assert.IsTrue(health.Disposed);
        }
        finally
        {
            releaseHealth.TrySetResult();
            try
            {
                await supervisor.DisposeAsync().AsTask()
                    .WaitAsync(TimeSpan.FromSeconds(2));
            }
            catch (AggregateException)
            {
                // This test deliberately makes the shared owner fault.
            }
        }
    }

    [TestMethod]
    public async Task StoppingObserver_WaitingOnRepeatedDispose_SeesCompletedOwner()
    {
        var health = new FakePocketBaseHealthProbe(isHealthy: true);
        var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(
                FakePocketBaseProcess.Ready(ReadyRecord())),
            health);
        bool repeatedDisposeCompleted = false;
        supervisor.StatusChanged += (_, status) =>
        {
            if (status.State == PocketBaseState.Stopping)
            {
                repeatedDisposeCompleted = supervisor.DisposeAsync().AsTask()
                    .Wait(TimeSpan.FromMilliseconds(200));
            }
        };
        await supervisor.StartAsync(CancellationToken.None);

        await Task.Run(async () => await supervisor.DisposeAsync())
            .WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsTrue(repeatedDisposeCompleted);
        Assert.IsTrue(health.Disposed);
    }

    [TestMethod]
    public async Task SharedRecoveryFailure_IsReportedOnceByConcurrentDispose()
    {
        var process = FakePocketBaseProcess.ReadyWithBlockedDispose(
            ReadyRecord(),
            new IOException("recovery teardown failed"));
        var health = new FakePocketBaseHealthProbe(isHealthy: true);
        var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 1),
            new FakePocketBaseProcessFactory(process),
            health);
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            process.Crash(exitCode: 17);
            Task? firstStop = null;
            Task? secondStop = null;
            Task? disposing = null;
            Task? repeatedDispose = null;
            try
            {
                await process.DisposeEntered.WaitAsync(TimeSpan.FromSeconds(2));
                firstStop = supervisor.StopAsync(CancellationToken.None);
                secondStop = supervisor.StopAsync(CancellationToken.None);
                disposing = supervisor.DisposeAsync().AsTask();
                repeatedDispose = supervisor.DisposeAsync().AsTask();
            }
            finally
            {
                process.ReleaseDispose();
            }

            Assert.IsNotNull(firstStop);
            Assert.IsNotNull(secondStop);
            Assert.IsNotNull(disposing);
            Assert.IsNotNull(repeatedDispose);
            Assert.AreSame(disposing, repeatedDispose);
            IOException firstFailure = await Assert.ThrowsAsync<IOException>(
                () => firstStop.WaitAsync(TimeSpan.FromSeconds(2)));
            IOException secondFailure = await Assert.ThrowsAsync<IOException>(
                () => secondStop.WaitAsync(TimeSpan.FromSeconds(2)));
            IOException disposeFailure = await Assert.ThrowsAsync<IOException>(
                () => disposing!.WaitAsync(TimeSpan.FromSeconds(2)));
            Assert.AreSame(firstFailure, secondFailure);
            Assert.AreSame(firstFailure, disposeFailure);
            Assert.IsTrue(health.Disposed);
        }
        finally
        {
            process.ReleaseDispose();
            try
            {
                await supervisor.DisposeAsync().AsTask()
                    .WaitAsync(TimeSpan.FromSeconds(2));
            }
            catch (IOException)
            {
                // This test deliberately makes the shared teardown fail.
            }
        }
    }

    [TestMethod]
    public async Task StopDuringRecovery_StartAndSecondStop_PreserveQueueOrder()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var recovering = FakePocketBaseProcess.Ready(ReadyRecord());
        var replacement = FakePocketBaseProcess.Ready(ReadyRecord());
        var orphan = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(
            first,
            recovering,
            replacement,
            orphan);
        var healthEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseHealth = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int healthCalls = 0;
        await using var supervisor = new PocketBaseSupervisor(
            Options(
                crashRestartLimit: 1,
                crashRestartDelay: TimeSpan.FromMilliseconds(50)),
            factory,
            new FakePocketBaseHealthProbe(
                getHealth: async _ =>
                {
                    int call = Interlocked.Increment(ref healthCalls);
                    if (call != 2)
                    {
                        return HealthyStatus();
                    }
                    healthEntered.SetResult();
                    await releaseHealth.Task;
                    return null;
                }));
        var observed =
            new System.Collections.Concurrent.ConcurrentQueue<PocketBaseState>();
        int readyNotifications = 0;
        supervisor.StatusChanged += (_, status) =>
        {
            observed.Enqueue(status.State);
            if (status.State == PocketBaseState.Ready
                && Interlocked.Increment(ref readyNotifications) == 2)
            {
                replacement.Crash(exitCode: 29);
            }
        };
        await supervisor.StartAsync(CancellationToken.None);
        first.Crash(exitCode: 17);
        await healthEntered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        observed.Clear();

        Task firstStop = supervisor.StopAsync(CancellationToken.None);
        try
        {
            Task starting = supervisor.StartAsync(CancellationToken.None);
            Task secondStop = supervisor.StopAsync(CancellationToken.None);
            releaseHealth.SetResult();

            await Task.WhenAll(firstStop, starting, secondStop)
                .WaitAsync(TimeSpan.FromSeconds(2));
        }
        finally
        {
            releaseHealth.TrySetResult();
        }

        Assert.AreEqual(
            PocketBaseState.Stopping,
            observed.First(state => state is PocketBaseState.Stopping
                or PocketBaseState.Ready));
        Assert.AreEqual(3, factory.Requests.Count);
        Assert.IsTrue(replacement.Disposed);
        Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
    }

    [TestMethod]
    public async Task DisposeAdmittedDuringStartRetirement_PreventsReplacementRecovery()
    {
        var first = FakePocketBaseProcess.ReadyWithBlockedDispose(ReadyRecord());
        var replacement = FakePocketBaseProcess.Ready(ReadyRecord());
        var orphan = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, replacement, orphan);
        var health = new FakePocketBaseHealthProbe(isHealthy: true);
        var supervisor = new PocketBaseSupervisor(
            Options(
                crashRestartLimit: 2,
                crashRestartDelay: TimeSpan.FromMilliseconds(50)),
            factory,
            health);
        int readyNotifications = 0;
        supervisor.StatusChanged += (_, status) =>
        {
            if (status.State == PocketBaseState.Ready
                && Interlocked.Increment(ref readyNotifications) == 2)
            {
                replacement.Crash(exitCode: 29);
            }
        };
        try
        {
            await supervisor.StartAsync(CancellationToken.None);
            first.Crash(exitCode: 17);
            Task starting = supervisor.StartAsync(CancellationToken.None);
            Task? retiring = null;
            try
            {
                await first.DisposeEntered.WaitAsync(TimeSpan.FromSeconds(2));
                retiring = supervisor.DisposeAsync().AsTask();
            }
            finally
            {
                first.ReleaseDispose();
            }

            Assert.IsNotNull(retiring);
            await Assert.ThrowsAsync<ObjectDisposedException>(
                () => starting.WaitAsync(TimeSpan.FromSeconds(2)));
            await retiring!.WaitAsync(TimeSpan.FromSeconds(2));
            Assert.AreEqual(1, factory.Requests.Count);
            Assert.AreEqual(PocketBaseState.Stopped, supervisor.GetStatus().State);
            Assert.IsTrue(health.Disposed);
        }
        finally
        {
            first.ReleaseDispose();
            await supervisor.DisposeAsync().AsTask()
                .WaitAsync(TimeSpan.FromSeconds(2));
        }
    }

    [TestMethod]
    public async Task UnexpectedExit_RetriesFailedReplacementAndHonorsRestartCap()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var failed = FakePocketBaseProcess.Ready(ReadyRecord());
        var recovered = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, failed, recovered);
        var exhausted = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int healthCalls = 0;
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 2),
            factory,
            new FakePocketBaseHealthProbe(getHealth: _ =>
                Task.FromResult<PocketBaseHealthStatus?>(
                    Interlocked.Increment(ref healthCalls) == 2
                        ? HealthyStatus(migrationHash: "wrong-hash")
                        : HealthyStatus())));
        supervisor.StatusChanged += (_, status) =>
        {
            if (status.State == PocketBaseState.Faulted && status.ExitCode == 23)
                exhausted.TrySetResult();
        };
        await supervisor.StartAsync(CancellationToken.None);
        string firstSecret = factory.Requests[0]
            .Environment["VIBETABLE_SIDECAR_SESSION_SECRET"];

        first.Crash(exitCode: 17);
        await WaitUntilAsync(
            () => factory.Requests.Count == 3
                && supervisor.GetStatus().State == PocketBaseState.Ready);

        Assert.AreNotEqual(
            firstSecret,
            factory.Requests[2].Environment["VIBETABLE_SIDECAR_SESSION_SECRET"]);
        Assert.IsTrue(first.Disposed);
        Assert.IsTrue(failed.Disposed);

        recovered.Crash(exitCode: 23);
        await exhausted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await supervisor.WaitForRecoveryQuiescenceAsync()
            .WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(3, factory.Requests.Count);
        await supervisor.StopAsync(CancellationToken.None).WaitAsync(TimeSpan.FromSeconds(2));
    }

    [TestMethod]
    public async Task RecoveredGenerationCrashDuringReadyDelivery_HandsOffRecoveryOwner()
    {
        var first = FakePocketBaseProcess.Ready(ReadyRecord());
        var recovered = FakePocketBaseProcess.Ready(ReadyRecord());
        var third = FakePocketBaseProcess.Ready(ReadyRecord());
        var factory = new FakePocketBaseProcessFactory(first, recovered, third);
        var thirdReady = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int readyNotifications = 0;
        await using var supervisor = new PocketBaseSupervisor(
            Options(crashRestartLimit: 2),
            factory,
            new FakePocketBaseHealthProbe(isHealthy: true));
        supervisor.StatusChanged += (_, status) =>
        {
            if (status.State != PocketBaseState.Ready)
                return;
            int ready = Interlocked.Increment(ref readyNotifications);
            if (ready == 2)
                recovered.Crash(exitCode: 29);
            else if (ready == 3)
                thirdReady.TrySetResult();
        };
        await supervisor.StartAsync(CancellationToken.None);

        first.Crash(exitCode: 17);
        await thirdReady.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual(3, factory.Requests.Count);
        Assert.IsTrue(first.Disposed);
        Assert.IsTrue(recovered.Disposed);
        Assert.AreEqual(PocketBaseState.Ready, supervisor.GetStatus().State);
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

        Assert.Contains("[REDACTED]", supervisor.GetSanitizedLog());
        Assert.IsFalse(supervisor.GetSanitizedLog().Contains(
            secret,
            StringComparison.Ordinal));
    }

    [TestMethod]
    [DataRow(false, DisplayName = "Stop joins pumps")]
    [DataRow(true, DisplayName = "Dispose joins pumps")]
    public async Task Retirement_CancelsAndJoinsPumpsWithoutPromotingDiagnosticsFailures(
        bool dispose)
    {
        var stdout = new ControlledPumpTextReader(
            ReadyRecord(),
            failWithIOException: true);
        var stderr = new ControlledPumpTextReader();
        var process = FakePocketBaseProcess.WithReaders(stdout, stderr);
        await using var supervisor = new PocketBaseSupervisor(
            Options(),
            new FakePocketBaseProcessFactory(process),
            new FakePocketBaseHealthProbe(isHealthy: true));
        await supervisor.StartAsync(CancellationToken.None);
        Task retirement = dispose
            ? supervisor.DisposeAsync().AsTask()
            : supervisor.StopAsync(CancellationToken.None);

        try
        {
            await Task.WhenAll(
                    stdout.CancellationObserved,
                    stderr.CancellationObserved)
                .WaitAsync(TimeSpan.FromSeconds(2));
            Assert.IsFalse(retirement.IsCompleted);
        }
        finally
        {
            stdout.Release();
            stderr.Release();
        }

        await retirement.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsTrue(retirement.IsCompletedSuccessfully);
        Assert.IsTrue(process.Disposed);
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

    private sealed class FakePocketBaseHealthProbe : IPocketBaseHealthProbe, IDisposable
    {
        private readonly PocketBaseHealthStatus? _status;
        private readonly Exception? _exception;
        private readonly bool _shutdownAccepted;
        private readonly Action? _onShutdown;
        private readonly Action? _onHealth;
        private readonly Func<CancellationToken, Task<PocketBaseHealthStatus?>>?
            _getHealth;

        public FakePocketBaseHealthProbe(
            bool isHealthy = false,
            PocketBaseHealthStatus? status = null,
            Exception? exception = null,
            bool shutdownAccepted = false,
            Action? onShutdown = null,
            Action? onHealth = null,
            Func<CancellationToken, Task<PocketBaseHealthStatus?>>? getHealth = null)
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
            _getHealth = getHealth;
        }

        public List<(Uri Endpoint, string SessionSecret)> Requests { get; } = [];
        public List<(Uri Endpoint, string SessionSecret)> ShutdownRequests { get; } = [];
        public bool Disposed { get; private set; }

        public Task<PocketBaseHealthStatus?> GetHealthAsync(
            Uri endpoint,
            string sessionSecret,
            CancellationToken cancellationToken)
        {
            Requests.Add((endpoint, sessionSecret));
            _onHealth?.Invoke();
            if (_getHealth is not null)
            {
                return _getHealth(cancellationToken);
            }
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

        public void Dispose() => Disposed = true;
    }

    private sealed class FakePocketBaseProcess : IPocketBaseProcess
    {
        private readonly TextReader _stdout;
        private readonly TextReader _stderr;
        private readonly Exception? _disposeFailure;
        private readonly TaskCompletionSource _disposeEntered = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private int _exitCode;
        private TaskCompletionSource? _releaseDispose;

        private FakePocketBaseProcess(
            TextReader stdout,
            TextReader stderr,
            Exception? disposeFailure = null)
        {
            _stdout = stdout;
            _stderr = stderr;
            _disposeFailure = disposeFailure;
        }

        public static FakePocketBaseProcess Ready(
            string record,
            string stderr = "",
            Exception? disposeFailure = null)
            => new(
                new StringReader(record + Environment.NewLine),
                new StringReader(stderr + Environment.NewLine),
                disposeFailure);

        public static FakePocketBaseProcess ReadyWithLateStderr(
            string record,
            string lateStderr)
            => new(
                new StringReader(record + Environment.NewLine),
                new LateLineTextReader(lateStderr));

        public static FakePocketBaseProcess WithReaders(
            TextReader stdout,
            TextReader stderr)
            => new(stdout, stderr);

        public static FakePocketBaseProcess ReadyWithBlockedDispose(
            string record,
            Exception? disposeFailure = null)
        {
            var process = new FakePocketBaseProcess(
                new StringReader(record + Environment.NewLine),
                new StringReader(Environment.NewLine),
                disposeFailure);
            process._releaseDispose = new TaskCompletionSource(
                TaskCreationOptions.RunContinuationsAsynchronously);
            return process;
        }

        public int Id => 42;
        public TextReader StandardOutput => _stdout;
        public TextReader StandardError => _stderr;
        public bool HasExited
        {
            get
            {
                HasExitedReadCount++;
                if (ThrowOnHasExitedRead)
                    throw new InvalidOperationException("Unexpected HasExited read.");
                return field;
            }
            private set;
        }
        public bool ThrowOnHasExitedRead { get; set; }
        public int HasExitedReadCount { get; private set; }
        public int? ExitCode => HasExited ? _exitCode : null;
        public int KillProcessTreeCalls { get; private set; }
        public bool Disposed { get; private set; }
        public Task DisposeEntered => _disposeEntered.Task;
        public event EventHandler? Exited;

        public void ReleaseDispose() => _releaseDispose?.TrySetResult();

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

        public async ValueTask DisposeAsync()
        {
            Disposed = true;
            _disposeEntered.TrySetResult();
            if (_releaseDispose is not null)
            {
                await _releaseDispose.Task;
            }
            _stdout.Dispose();
            _stderr.Dispose();
            if (_disposeFailure is not null)
            {
                throw _disposeFailure;
            }
        }
    }

    private sealed class LateLineTextReader(string lateLine) : TextReader
    {
        private readonly TaskCompletionSource<string?> _line =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private int _readCount;

        public override ValueTask<string?> ReadLineAsync(
            CancellationToken cancellationToken)
            => Interlocked.Increment(ref _readCount) == 1
                ? new ValueTask<string?>(_line.Task)
                : ValueTask.FromResult<string?>(null);

        protected override void Dispose(bool disposing)
        {
            if (disposing)
            {
                _line.TrySetResult(lateLine);
            }
            base.Dispose(disposing);
        }
    }

    private sealed class ControlledPumpTextReader(
        string? firstLine = null,
        bool failWithIOException = false) : TextReader
    {
        private readonly TaskCompletionSource _cancellationObserved = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _release = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private int _readCount;

        internal Task CancellationObserved => _cancellationObserved.Task;

        internal void Release() => _release.TrySetResult();

        public override ValueTask<string?> ReadLineAsync(
            CancellationToken cancellationToken)
        {
            if (Interlocked.Increment(ref _readCount) == 1 && firstLine is not null)
                return ValueTask.FromResult<string?>(firstLine);
            return new ValueTask<string?>(WaitForRetirementAsync(cancellationToken));
        }

        private async Task<string?> WaitForRetirementAsync(
            CancellationToken cancellationToken)
        {
            try
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
                return null;
            }
            catch (OperationCanceledException)
                when (cancellationToken.IsCancellationRequested)
            {
                _cancellationObserved.TrySetResult();
                await _release.Task;
                if (failWithIOException)
                    throw new IOException("controlled pump failure");
                throw;
            }
        }
    }
}
