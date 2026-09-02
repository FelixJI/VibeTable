using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductRuntimeServiceTests
{
    private static readonly TimeSpan TestTimeout = TimeSpan.FromSeconds(5);

    [TestMethod]
    [DataRow("capabilities"), DataRow("product"), DataRow("python"), DataRow("final")]
    public async Task NewReadySupersedesBlockedRecoveryWithoutLosingLatest(
        string blockedStage)
    {
        var firstEntered = NewSignal();
        var firstCancelled = NewSignal();
        var release = NewSignal();
        var coordinator = new FakeRecoveryCoordinator
        {
            BlockedStage = blockedStage,
            Entered = firstEntered,
            Cancelled = firstCancelled,
            Release = release,
        };
        await using RuntimeFixture fixture = await StartRuntimeAsync(coordinator);
        var ready = NewSignal();
        fixture.Backend.Starting = async (call, token) =>
        {
            if (call == 2)
                await coordinator.BlockAsync("python", 1, token);
        };
        int readyCount = 0;
        fixture.Runtime.ClientReady += () =>
        {
            Interlocked.Increment(ref readyCount);
            ready.TrySetResult();
        };

        coordinator.Current = Generation(1, 43101, "first-secret");
        fixture.Sidecar.RaiseReady();
        await firstEntered.Task.WaitAsync(TestTimeout);
        coordinator.Current = Generation(2, 43102, "second-secret");
        fixture.Sidecar.RaiseReady();

        if (blockedStage != "final")
            await firstCancelled.Task.WaitAsync(TestTimeout);
        release.TrySetResult();
        await ready.Task.WaitAsync(TestTimeout);

        Assert.AreEqual(1, Volatile.Read(ref readyCount));
        CollectionAssert.AreEqual(new long[] { 1, 2 }, coordinator.Prepared.ToArray());
        Assert.AreEqual(
            blockedStage is "python" or "final" ? 3 : 2,
            fixture.Backend.StartCalls);
        Dictionary<string, string> lastStart = fixture.Backend.StartEnvironments[^1];
        Assert.AreEqual("http://127.0.0.1:43102", lastStart["VIBETABLE_SIDECAR_URL"]);
        Assert.AreEqual("second-secret", lastStart["VIBETABLE_SIDECAR_SESSION_SECRET"]);
        Assert.AreEqual(3, fixture.Backend.StopCalls);
        Assert.AreEqual(coordinator.ExpectedClear, coordinator.ClearCalls);
    }

    [TestMethod]
    [DataRow("capabilities", false), DataRow("product", false), DataRow("python", false), DataRow("final", false)]
    [DataRow("capabilities", true), DataRow("product", true), DataRow("python", true), DataRow("final", true)]
    public async Task StopAndDisposeCancelAndJoinEveryRecoveryStage(
        string blockedStage,
        bool disposeFirst)
    {
        var entered = NewSignal();
        var cancelled = NewSignal();
        var release = NewSignal();
        var coordinator = new FakeRecoveryCoordinator
        {
            BlockedStage = blockedStage,
            Entered = entered,
            Cancelled = cancelled,
            Release = release,
        };
        await using RuntimeFixture fixture = await StartRuntimeAsync(coordinator);
        var teardownEntered = NewSignal();
        var releaseTeardown = NewSignal();
        fixture.Backend.Teardown = async (disposing, call) =>
        {
            if (disposing != disposeFirst || (!disposing && call != 3))
                return;
            teardownEntered.TrySetResult();
            await releaseTeardown.Task;
        };
        fixture.Backend.Starting = async (call, token) =>
        {
            if (call == 2)
                await coordinator.BlockAsync("python", 1, token);
        };
        int readyCount = 0;
        int failureCount = 0;
        fixture.Runtime.ClientReady += () => Interlocked.Increment(ref readyCount);
        fixture.Runtime.RecoveryFailed += _ => Interlocked.Increment(ref failureCount);
        coordinator.Current = Generation(1, 43101, "recovery-secret");
        fixture.Sidecar.RaiseReady();
        await entered.Task.WaitAsync(TestTimeout);

        Task firstOwner = disposeFirst
            ? fixture.Runtime.DisposeAsync().AsTask()
            : fixture.Runtime.StopAsync(CancellationToken.None);
        if (blockedStage != "final")
            await cancelled.Task.WaitAsync(TestTimeout);
        Assert.IsFalse(firstOwner.IsCompleted);
        release.TrySetResult();
        await teardownEntered.Task.WaitAsync(TestTimeout);
        Task secondOwner = disposeFirst
            ? fixture.Runtime.StopAsync(CancellationToken.None)
            : fixture.Runtime.DisposeAsync().AsTask();
        Assert.IsFalse(firstOwner.IsCompleted);
        Assert.IsFalse(secondOwner.IsCompleted);
        releaseTeardown.TrySetResult();
        await Task.WhenAll(firstOwner, secondOwner).WaitAsync(TestTimeout);

        Assert.AreEqual(0, Volatile.Read(ref readyCount));
        Assert.AreEqual(0, Volatile.Read(ref failureCount));
        Assert.AreEqual(1, fixture.Backend.DisposeCalls);
        Assert.AreEqual(1, fixture.LocalData.DisposeCalls);
        Assert.AreEqual(disposeFirst ? 2 : 3, fixture.Backend.StopCalls);
        Assert.AreEqual(coordinator.ExpectedClear, coordinator.ClearCalls);
    }

    [TestMethod]
    public async Task ClientReadyHandoffQueuesNextGenerationAndDeduplicatesReady()
    {
        var coordinator = new FakeRecoveryCoordinator();
        await using RuntimeFixture fixture = await StartRuntimeAsync(coordinator);
        var bothReady = NewSignal();
        int readyCount = 0;
        fixture.Runtime.ClientReady += () => throw new InvalidOperationException("observer");
        fixture.Runtime.ClientReady += () =>
        {
            int observed = Interlocked.Increment(ref readyCount);
            if (observed == 1)
            {
                coordinator.Current = Generation(2, 43102, "second-secret");
                fixture.Sidecar.RaiseReady();
                fixture.Sidecar.RaiseReady();
            }
            else if (observed == 2)
            {
                bothReady.TrySetResult();
            }
        };

        coordinator.Current = Generation(1, 43101, "first-secret");
        fixture.Sidecar.RaiseReady();
        fixture.Sidecar.RaiseReady();
        await bothReady.Task.WaitAsync(TestTimeout);

        Assert.AreEqual(2, Volatile.Read(ref readyCount));
        CollectionAssert.AreEqual(new long[] { 1, 2 }, coordinator.Prepared.ToArray());
    }

    [TestMethod]
    public async Task SupersededFailureIsSilentAndLatestFailureIsReportedOnce()
    {
        var firstEntered = NewSignal();
        var releaseFirst = NewSignal();
        var coordinator = new FakeRecoveryCoordinator();
        coordinator.GettingCapabilities = async (generation, _) =>
        {
            if (generation.GenerationId == 1)
            {
                firstEntered.TrySetResult();
                await releaseFirst.Task;
                throw new IOException("stale failure");
            }
            if (generation.GenerationId == 2)
                throw new InvalidOperationException("latest failure");
        };
        await using RuntimeFixture fixture = await StartRuntimeAsync(coordinator);
        var failed = NewSignal<Exception>();
        var retiredChecked = NewSignal();
        int failureCount = 0;
        fixture.Runtime.RecoveryFailed += _ => throw new InvalidOperationException("observer");
        fixture.Runtime.RecoveryFailed += exception =>
        {
            Interlocked.Increment(ref failureCount);
            failed.TrySetResult(exception);
        };

        coordinator.Current = Generation(1, 43101, "first-secret");
        fixture.Sidecar.RaiseReady();
        await firstEntered.Task.WaitAsync(TestTimeout);
        coordinator.Current = null;
        coordinator.CurrentChecked = retiredChecked;
        releaseFirst.TrySetResult();
        await retiredChecked.Task.WaitAsync(TestTimeout);
        coordinator.Current = Generation(2, 43102, "second-secret");
        fixture.Sidecar.RaiseReady();
        Exception error = await failed.Task.WaitAsync(TestTimeout);

        Assert.IsInstanceOfType<InvalidOperationException>(error);
        Assert.AreEqual(1, Volatile.Read(ref failureCount));
        var ready = NewSignal();
        fixture.Runtime.ClientReady += () => ready.TrySetResult();
        coordinator.Current = Generation(3, 43103, "third-secret");
        fixture.Sidecar.RaiseReady();
        await ready.Task.WaitAsync(TestTimeout);
        await fixture.Runtime.StopAsync(CancellationToken.None).WaitAsync(TestTimeout);
    }
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
            new Dictionary<string, string>());
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
            new Dictionary<string, string>());
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

    private sealed class FakeLocalDataService(
        Func<CancellationToken, Task> start) : ILocalDataService
    {
        private bool _ready;

        public int StartCalls { get; private set; }
        public int DisposeCalls { get; private set; }

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
            DisposeCalls++;
            return ValueTask.CompletedTask;
        }
    }

    private sealed class FakePocketBaseSupervisor(
        Action configure,
        PocketBaseStartupTimings? timings = null) : IPocketBaseSupervisor
    {
        public event Action<object?, PocketBaseStatus>? StatusChanged;
        public PocketBaseStartupTimings? LastStartupTimings => timings;

        public Task StartAsync(CancellationToken cancellationToken) => Task.CompletedTask;

        public PocketBaseStatus GetStatus() =>
            new(PocketBaseState.Ready, null, false, null, null);

        public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public Uri? GetAdminUri() => null;
        public PocketBaseAdminContext? GetAdminContext() => null;

        public void ConfigureBackendEnvironment(IDictionary<string, string> environment)
            => configure();

        internal void RaiseReady() => StatusChanged?.Invoke(
            this,
            new PocketBaseStatus(PocketBaseState.Ready, null, false, null, null));

        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    private sealed class RecordingBackendSupervisor(
        IDictionary<string, string> environment) : IBackendSupervisor
    {
        public int StartCalls { get; private set; }
        public int StopCalls { get; private set; }
        public int DisposeCalls { get; private set; }
        public List<Dictionary<string, string>> StartEnvironments { get; } = [];
        public Func<int, CancellationToken, Task> Starting { get; set; } =
            (_, _) => Task.CompletedTask;
        public Func<bool, int, Task> Teardown { get; set; } =
            (_, _) => Task.CompletedTask;
        public BackendState State { get; private set; } = BackendState.Stopped;

        public async Task StartAsync(CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            StartCalls++;
            StartEnvironments.Add(new Dictionary<string, string>(environment));
            State = BackendState.Starting;
            await Starting(StartCalls, cancellationToken);
            cancellationToken.ThrowIfCancellationRequested();
            State = BackendState.Ready;
        }

        public async Task StopAsync(CancellationToken cancellationToken)
        {
            StopCalls++;
            State = BackendState.Stopped;
            await Teardown(false, StopCalls);
        }

        public async ValueTask DisposeAsync()
        {
            DisposeCalls++;
            State = BackendState.Stopped;
            await Teardown(true, DisposeCalls);
        }
    }

    private sealed class FakeRecoveryCoordinator : IProductRuntimeRecoveryCoordinator
    {
        public ProductRuntimeSidecarGeneration? Current { get; set; }
        public List<long> Prepared { get; } = [];
        public int ClearCalls { get; set; }
        public int ExpectedClear => BlockedStage is "python" or "final" ? 1 : 0;
        public TaskCompletionSource? CurrentChecked { get; set; }
        public string? BlockedStage { get; init; }
        public TaskCompletionSource? Entered { get; init; }
        public TaskCompletionSource? Cancelled { get; init; }
        public TaskCompletionSource? Release { get; init; }
        public Func<
            ProductRuntimeSidecarGeneration,
            CancellationToken,
            Task> GettingCapabilities
        { get; set; } = (_, _) => Task.CompletedTask;
        public Func<ProductRuntimeSidecarGeneration, CancellationToken,
            Task<IProductRuntimeRecoveryCandidate?>> ReplacingProduct
        { get; set; } =
            (generation, _) => Task.FromResult<IProductRuntimeRecoveryCandidate?>(null);

        public ProductRuntimeSidecarGeneration? CaptureCurrentGeneration() => Current;

        public bool IsCurrent(ProductRuntimeSidecarGeneration generation)
        {
            CurrentChecked?.TrySetResult();
            return Current?.GenerationId == generation.GenerationId;
        }

        public async Task<ProductRuntimeRecoveryPreparation?> GetCapabilitiesAsync(
            ProductRuntimeSidecarGeneration generation,
            CancellationToken cancellationToken)
        {
            Prepared.Add(generation.GenerationId);
            await BlockAsync("capabilities", generation.GenerationId, cancellationToken);
            await GettingCapabilities(generation, cancellationToken);
            return token => ReplaceProductAsync(generation, token);
        }

        private async Task<IProductRuntimeRecoveryCandidate?> ReplaceProductAsync(
            ProductRuntimeSidecarGeneration generation,
            CancellationToken cancellationToken)
        {
            await BlockAsync("product", generation.GenerationId, cancellationToken);
            return await ReplacingProduct(generation, cancellationToken)
                ?? new FakeRecoveryCandidate(this, generation);
        }

        public async Task BlockAsync(
            string stage,
            long generationId,
            CancellationToken cancellationToken)
        {
            if (generationId != 1 || BlockedStage != stage)
                return;
            Entered!.TrySetResult();
            using CancellationTokenRegistration registration = cancellationToken.Register(
                () => Cancelled!.TrySetResult());
            await Release!.Task;
            cancellationToken.ThrowIfCancellationRequested();
        }

    }

    private sealed class FakeRecoveryCandidate(
        FakeRecoveryCoordinator coordinator,
        ProductRuntimeSidecarGeneration generation) : IProductRuntimeRecoveryCandidate
    {
        public ProductRuntimeSidecarGeneration Generation => generation;

        public bool TryCommit(Func<bool> action)
        {
            coordinator.BlockAsync("final", generation.GenerationId, CancellationToken.None)
                .GetAwaiter().GetResult();
            return coordinator.IsCurrent(generation) && action();
        }

        public void Clear() => coordinator.ClearCalls++;
    }

    private static ProductRuntimeSidecarGeneration Generation(
        long generationId,
        int port,
        string secret)
        => new(
            generationId,
            new PocketBaseAdminContext(
                new Uri(
                    $"http://127.0.0.1:{port}/api/vibetable/v1/admin/bootstrap"),
                new Uri($"http://127.0.0.1:{port}/"),
                "X-VibeTable-Session",
                secret));

    private static async Task<RuntimeFixture> StartRuntimeAsync(
        FakeRecoveryCoordinator coordinator)
    {
        var environment = new Dictionary<string, string>();
        var localData = new FakeLocalDataService(_ => Task.CompletedTask);
        var sidecar = new FakePocketBaseSupervisor(() => { });
        var backend = new RecordingBackendSupervisor(environment);
        var runtime = new ProductRuntimeService(
            localData, sidecar, backend, environment, coordinator);
        using WorkspaceActivationBudget budget = Budget();
        await runtime.StartAsync(budget);
        return new RuntimeFixture(runtime, backend, localData, sidecar);
    }

    private sealed record RuntimeFixture(
        ProductRuntimeService Runtime,
        RecordingBackendSupervisor Backend,
        FakeLocalDataService LocalData,
        FakePocketBaseSupervisor Sidecar) : IAsyncDisposable
    {
        public ValueTask DisposeAsync() => Runtime.DisposeAsync();
    }

    private static WorkspaceActivationBudget Budget()
        => WorkspaceActivationBudget.Begin(
            Guid.Parse("55555555-5555-4555-8555-555555555555"),
            17,
            new WorkspaceActivationPolicy(
                totalTimeout: TimeSpan.FromSeconds(30),
                sidecarTimeout: TimeSpan.FromSeconds(10),
                backendTimeout: TimeSpan.FromSeconds(10),
                verificationTimeout: TimeSpan.FromSeconds(10)),
            CancellationToken.None);

    private static TaskCompletionSource NewSignal() => new(
        TaskCreationOptions.RunContinuationsAsynchronously);

    private static TaskCompletionSource<T> NewSignal<T>() => new(
        TaskCreationOptions.RunContinuationsAsynchronously);

    private sealed class FakeBackendSupervisor(
        Func<CancellationToken, Task> start) : IBackendSupervisor
    {
        public int StartCalls { get; private set; }
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
            State = BackendState.Stopped;
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
