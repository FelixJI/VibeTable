using VibeTable.Desktop.Services;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRuntimeServiceTests
{
    [TestMethod]
    public async Task StartAsync_AllowsSidecarReadyBeyondTheLegacyBoundary()
    {
        var time = new ManualTimeProvider();
        var order = new List<string>();
        var traces = new List<string>();
        var localData = new FakeLocalDataService(async token =>
        {
            order.Add("sidecar");
            time.Advance(TimeSpan.FromSeconds(31));
            await Task.Yield();
            token.ThrowIfCancellationRequested();
        });
        var sidecar = new FakePocketBaseSupervisor(
            () => order.Add("bind"),
            new PocketBaseStartupTimings(
                TimeSpan.FromMilliseconds(20),
                TimeSpan.FromSeconds(30.5),
                TimeSpan.FromMilliseconds(480),
                "health"));
        var backend = new FakeBackendSupervisor(async token =>
        {
            order.Add("backend");
            time.Advance(TimeSpan.FromSeconds(2));
            await Task.Yield();
            token.ThrowIfCancellationRequested();
        });
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            _ => Task.CompletedTask);
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("33333333-3333-4333-8333-333333333333"),
            11,
            new WorkspaceActivationPolicy(
                totalTimeout: TimeSpan.FromSeconds(105),
                sidecarTimeout: TimeSpan.FromSeconds(60),
                backendTimeout: TimeSpan.FromSeconds(30),
                verificationTimeout: TimeSpan.FromSeconds(10)),
            CancellationToken.None,
            time,
            traces.Add);

        await runtime.StartAsync(budget);
        WorkspaceActivationReport report = budget.Complete();

        CollectionAssert.AreEqual(
            new[] { "sidecar", "bind", "backend" },
            order);
        Assert.AreEqual(1, localData.StartCalls);
        Assert.AreEqual(1, backend.StartCalls);
        Assert.AreEqual(TimeSpan.FromSeconds(33), report.Elapsed);
        Assert.IsTrue(traces.Any(message =>
            message.StartsWith(
                "workspace.activation.sidecar.ready_record.completed ",
                StringComparison.Ordinal)));
    }

    [TestMethod]
    public async Task StartAsync_BackendConsumesTheSameAbsoluteDeadline()
    {
        var time = new ManualTimeProvider();
        var localData = new FakeLocalDataService(_ =>
        {
            time.Advance(TimeSpan.FromSeconds(50));
            return Task.CompletedTask;
        });
        var sidecar = new FakePocketBaseSupervisor(
            () => time.Advance(TimeSpan.FromSeconds(20)));
        var backend = new FakeBackendSupervisor(
            token => Task.Delay(Timeout.InfiniteTimeSpan, token));
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            _ => Task.CompletedTask);
        using var budget = WorkspaceActivationBudget.Begin(
            Guid.Parse("44444444-4444-4444-8444-444444444444"),
            13,
            new WorkspaceActivationPolicy(
                totalTimeout: TimeSpan.FromSeconds(125),
                sidecarTimeout: TimeSpan.FromSeconds(60),
                backendTimeout: TimeSpan.FromSeconds(60),
                verificationTimeout: TimeSpan.FromSeconds(5)),
            CancellationToken.None,
            time);

        Task start = runtime.StartAsync(budget);
        time.Advance(TimeSpan.FromSeconds(55));

        WorkspaceActivationTimeoutException error =
            await Assert.ThrowsExactlyAsync<WorkspaceActivationTimeoutException>(
                () => start);
        Assert.AreEqual(WorkspaceActivationStage.Backend, error.Stage);
        Assert.AreEqual(1, localData.StartCalls);
        Assert.AreEqual(1, backend.StartCalls);
        Assert.AreEqual(WorkspaceActivationOutcome.TimedOut, budget.Report.Outcome);
    }

    [TestMethod]
    public async Task RecoveryRefreshesClientBindingBeforePublishingReady()
    {
        var order = new List<string>();
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => order.Add("bind"));
        var backend = new FakeBackendSupervisor(
            _ =>
            {
                order.Add("backend");
                return Task.CompletedTask;
            },
            () => order.Add("stop"));
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            _ =>
            {
                order.Add("refresh");
                return Task.CompletedTask;
            });
        var recovered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int readyCount = 0;
        runtime.ClientReady += () =>
        {
            order.Add("ready");
            if (Interlocked.Increment(ref readyCount) == 2)
                recovered.TrySetResult();
        };
        using var budget = Budget();
        await runtime.StartAsync(budget);
        order.Clear();

        sidecar.RaiseReady();
        await recovered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        CollectionAssert.AreEqual(
            new[] { "stop", "bind", "refresh", "backend", "ready" },
            order);
    }

    [TestMethod]
    public async Task RecoveryRefreshFailureDoesNotPublishClientReady()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new FakeBackendSupervisor(_ => Task.CompletedTask);
        var expected = new InvalidOperationException("capability mismatch");
        var verified = new WorkspaceV2SidecarCapabilities(
            WorkspaceV2Json.ContractVersion,
            Guid.NewGuid().ToString("D"),
            1,
            1,
            Guid.NewGuid().ToString("D"),
            ["query.page", "replica.status"]);
        var snapshot = new WorkspaceCapabilitiesSnapshot();
        snapshot.PublishVerified(verified);
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            token => snapshot.RefreshAsync(
                _ => Task.FromException<WorkspaceV2SidecarCapabilities>(
                    expected),
                token));
        var failed = new TaskCompletionSource<Exception>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int readyCount = 0;
        runtime.ClientReady += () => Interlocked.Increment(ref readyCount);
        runtime.RecoveryFailed += error => failed.TrySetResult(error);
        using var budget = Budget();
        await runtime.StartAsync(budget);

        sidecar.RaiseReady();
        Exception actual = await failed.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreSame(expected, actual);
        Assert.AreSame(verified, snapshot.Current);
        Assert.AreEqual(1, readyCount);
        Assert.AreEqual(BackendState.Stopped, backend.State);
    }

    [TestMethod]
    public async Task ReadyDuringRecoveryQueuesTheReplacementGeneration()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new FakeBackendSupervisor(_ => Task.CompletedTask);
        var firstRefreshEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseFirstRefresh = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int refreshCalls = 0;
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            async _ =>
            {
                if (Interlocked.Increment(ref refreshCalls) != 1)
                    return;
                firstRefreshEntered.TrySetResult();
                await releaseFirstRefresh.Task;
                throw new IOException("replacement generation exited");
            });
        var recovered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int readyCount = 0;
        runtime.ClientReady += () =>
        {
            if (Interlocked.Increment(ref readyCount) == 2)
                recovered.TrySetResult();
        };
        using var budget = Budget();
        await runtime.StartAsync(budget);

        sidecar.RaiseReady();
        await firstRefreshEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
        sidecar.RaiseReady();
        releaseFirstRefresh.TrySetResult();
        await recovered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreEqual(2, refreshCalls);
        Assert.AreEqual(BackendState.Ready, backend.State);
    }

    [TestMethod]
    public async Task DisposeCancelsAndJoinsRecoveryBeforeReleasingDependencies()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new FakeBackendSupervisor(_ => Task.CompletedTask);
        var refreshEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var refreshCanceled = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            async token =>
            {
                refreshEntered.TrySetResult();
                try
                {
                    await Task.Delay(Timeout.InfiniteTimeSpan, token);
                }
                catch (OperationCanceledException)
                    when (token.IsCancellationRequested)
                {
                    refreshCanceled.TrySetResult();
                    throw;
                }
            });
        using var budget = Budget();
        await runtime.StartAsync(budget);
        sidecar.RaiseReady();
        await refreshEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Task dispose = runtime.DisposeAsync().AsTask();
        await refreshCanceled.Task.WaitAsync(TimeSpan.FromSeconds(5));
        await dispose.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreEqual(BackendState.Stopped, backend.State);
    }

    [TestMethod]
    public async Task StopCancelsAndJoinsRecoveryBeforeReturning()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new FakeBackendSupervisor(_ => Task.CompletedTask);
        var refreshEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var refreshCanceled = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            async token =>
            {
                refreshEntered.TrySetResult();
                try
                {
                    await Task.Delay(Timeout.InfiniteTimeSpan, token);
                }
                catch (OperationCanceledException)
                    when (token.IsCancellationRequested)
                {
                    refreshCanceled.TrySetResult();
                    throw;
                }
            });
        using var budget = Budget();
        await runtime.StartAsync(budget);
        sidecar.RaiseReady();
        await refreshEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Task stop = runtime.StopAsync(CancellationToken.None);
        await refreshCanceled.Task.WaitAsync(TimeSpan.FromSeconds(5));
        await stop.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreEqual(BackendState.Stopped, backend.State);
    }

    [TestMethod]
    public async Task StopIngressPreventsLateRecoveryReadyPublication()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new FakeBackendSupervisor(_ => Task.CompletedTask);
        var refreshEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseRefresh = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            async _ =>
            {
                refreshEntered.TrySetResult();
                await releaseRefresh.Task;
            });
        int readyCount = 0;
        runtime.ClientReady += () => Interlocked.Increment(ref readyCount);
        using var budget = Budget();
        await runtime.StartAsync(budget);
        sidecar.RaiseReady();
        await refreshEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Task stopIngress = runtime.StopIngressAsync(CancellationToken.None);
        releaseRefresh.TrySetResult();
        await stopIngress.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreEqual(1, readyCount);
        Assert.AreEqual(BackendState.Stopped, backend.State);
    }

    [TestMethod]
    public async Task StopIngressDuringBackendRestartPreventsLateReadyPublication()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var restartEntered = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseRestart = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        int starts = 0;
        var backend = new FakeBackendSupervisor(async _ =>
        {
            if (Interlocked.Increment(ref starts) != 2)
                return;
            restartEntered.TrySetResult();
            await releaseRestart.Task;
        });
        await using var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            _ => Task.CompletedTask);
        int readyCount = 0;
        runtime.ClientReady += () => Interlocked.Increment(ref readyCount);
        using var budget = Budget();
        await runtime.StartAsync(budget);
        sidecar.RaiseReady();
        await restartEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Task stopIngress = runtime.StopIngressAsync(CancellationToken.None);
        releaseRestart.TrySetResult();
        await stopIngress.WaitAsync(TimeSpan.FromSeconds(5));

        Assert.AreEqual(1, readyCount);
        Assert.AreEqual(BackendState.Stopped, backend.State);
    }

    [TestMethod]
    public async Task RecoveryObserverFailureDoesNotSkipDispose()
    {
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new FakeBackendSupervisor(_ => Task.CompletedTask);
        var recoveryReported = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var runtime = new ProductRuntimeService(
            localData,
            sidecar,
            backend,
            new Dictionary<string, string>(),
            _ => Task.FromException(new IOException("refresh failed")));
        runtime.RecoveryFailed += _ =>
        {
            recoveryReported.TrySetResult();
            throw new InvalidOperationException("observer failed");
        };
        using var budget = Budget();
        await runtime.StartAsync(budget);
        sidecar.RaiseReady();
        await recoveryReported.Task.WaitAsync(TimeSpan.FromSeconds(5));

        await runtime.DisposeAsync();

        Assert.IsTrue(localData.Disposed);
        Assert.IsTrue(backend.Disposed);
    }

    private static WorkspaceActivationBudget Budget()
        => WorkspaceActivationBudget.Begin(
            Guid.NewGuid(),
            1,
            new WorkspaceActivationPolicy(
                totalTimeout: TimeSpan.FromSeconds(30),
                sidecarTimeout: TimeSpan.FromSeconds(10),
                backendTimeout: TimeSpan.FromSeconds(10),
                verificationTimeout: TimeSpan.FromSeconds(10)),
            CancellationToken.None);

    private sealed class FakeLocalDataService(
        Func<CancellationToken, Task> start) : ILocalDataService
    {
        private bool _ready;

        public int StartCalls { get; private set; }
        public bool Disposed { get; private set; }

        public async Task StartAsync(CancellationToken cancellationToken)
        {
            StartCalls += 1;
            await start(cancellationToken);
            _ready = true;
        }

        public LocalDataStatus GetStatus() => new(
            _ready ? LocalDataState.Ready : LocalDataState.Stopped,
            _ready,
            false,
            null,
            null);

        public Task StopAsync(CancellationToken cancellationToken)
        {
            _ready = false;
            return Task.CompletedTask;
        }

        public bool OpenAdmin() => false;
        public ValueTask DisposeAsync()
        {
            Disposed = true;
            return ValueTask.CompletedTask;
        }
    }

    private sealed class FakePocketBaseSupervisor(
        Action configure,
        PocketBaseStartupTimings? timings = null) : IPocketBaseSupervisor
    {
        public event Action<object?, PocketBaseStatus>? StatusChanged;
        public PocketBaseStartupTimings? LastStartupTimings => timings;

        public void RaiseReady() => StatusChanged?.Invoke(
            this,
            new PocketBaseStatus(PocketBaseState.Ready, null, false, null, null));

        public Task StartAsync(CancellationToken cancellationToken) => Task.CompletedTask;

        public PocketBaseStatus GetStatus() =>
            new(PocketBaseState.Ready, null, false, null, null);

        public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public Uri? GetAdminUri() => null;
        public PocketBaseAdminContext? GetAdminContext() => null;

        public void ConfigureBackendEnvironment(IDictionary<string, string> environment)
            => configure();

        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    private sealed class FakeBackendSupervisor(
        Func<CancellationToken, Task> start,
        Action? stop = null) : IBackendSupervisor
    {
        public int StartCalls { get; private set; }
        public bool Disposed { get; private set; }
        public BackendState State { get; private set; } = BackendState.Stopped;

        public async Task StartAsync(CancellationToken cancellationToken)
        {
            StartCalls += 1;
            State = BackendState.Starting;
            await start(cancellationToken);
            State = BackendState.Ready;
        }

        public Task StopAsync(CancellationToken cancellationToken)
        {
            stop?.Invoke();
            State = BackendState.Stopped;
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            Disposed = true;
            return ValueTask.CompletedTask;
        }
    }
}
