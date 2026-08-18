using VibeTable.Desktop.Services;
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
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    private sealed class FakePocketBaseSupervisor(
        Action configure,
        PocketBaseStartupTimings? timings = null) : IPocketBaseSupervisor
    {
        public event Action<object?, PocketBaseStatus>? StatusChanged
        {
            add { }
            remove { }
        }
        public PocketBaseStartupTimings? LastStartupTimings => timings;

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
