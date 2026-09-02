using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductSidecarGatewayLifecycleTests
{
    private static readonly TimeSpan TestTimeout = TimeSpan.FromSeconds(2);

    [TestMethod]
    public async Task NewerAttemptSupersedesPendingCandidateAndLateCompletionCannotPublish()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var older = new ControlledGatewayCandidate(ignoreCancellation: true);
        var newer = new ControlledGatewayCandidate();
        ProductSidecarGenerationSnapshot snapshot = Snapshot();
        authority.SetCurrent(snapshot);
        var candidates = new Queue<ControlledGatewayCandidate>([older, newer]);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority,
            binding,
            _ => candidates.Dequeue());

        Task<bool> first = lifecycle.TryReplaceAsync(snapshot, CancellationToken.None);
        await older.HandshakeStarted.Task.WaitAsync(TestTimeout);
        Task<bool> second = lifecycle.TryReplaceAsync(snapshot, CancellationToken.None);
        await newer.HandshakeStarted.Task.WaitAsync(TestTimeout);
        Assert.IsNull(binding.Current);
        newer.CompleteHandshake();

        Assert.IsTrue(await second.WaitAsync(TestTimeout));
        Assert.AreSame(newer, binding.Current);

        older.CompleteHandshake();
        Assert.IsFalse(await first.WaitAsync(TestTimeout));
        authority.SetCurrent(snapshot);
        authority.SetCurrent(snapshot);
        Assert.AreSame(newer, binding.Current);
        Assert.AreEqual(1, older.DisposeCount);
        Assert.AreEqual(0, newer.DisposeCount);
    }

    [TestMethod]
    public async Task LateStaleAttemptDoesNotCancelCurrentPendingGeneration()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var currentCandidate = new ControlledGatewayCandidate();
        var staleCandidate = new ControlledGatewayCandidate();
        staleCandidate.CompleteHandshake();
        var candidates = new Queue<ControlledGatewayCandidate>(
            [currentCandidate, staleCandidate]);
        ProductSidecarGenerationSnapshot stale = Snapshot();
        ProductSidecarGenerationSnapshot current = Snapshot();
        authority.SetCurrent(current);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidates.Dequeue());
        Task<bool> currentReplacement = lifecycle.TryReplaceAsync(current, default);
        await currentCandidate.HandshakeStarted.Task.WaitAsync(TestTimeout);

        Assert.IsFalse(await lifecycle.TryReplaceAsync(stale, default)
            .WaitAsync(TestTimeout));
        currentCandidate.CompleteHandshake();

        Assert.IsTrue(await currentReplacement.WaitAsync(TestTimeout));
        Assert.AreSame(currentCandidate, binding.Current);
        Assert.AreEqual(0, currentCandidate.DisposeCount);
    }

    [TestMethod]
    public async Task CallerCancellationBeforePublishRejectsLateIgnoredHandshake()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var candidate = new ControlledGatewayCandidate(ignoreCancellation: true);
        ProductSidecarGenerationSnapshot snapshot = Snapshot();
        authority.SetCurrent(snapshot);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidate);
        using var cancellation = new CancellationTokenSource();
        Task<bool> replacement = lifecycle.TryReplaceAsync(
            snapshot, cancellation.Token);
        await candidate.HandshakeStarted.Task.WaitAsync(TestTimeout);

        cancellation.Cancel();
        candidate.CompleteHandshake();

        await Assert.ThrowsAsync<OperationCanceledException>(
            () => replacement.WaitAsync(TestTimeout));
        Assert.IsNull(binding.Current);
        Assert.AreEqual(1, candidate.DisposeCount);
    }

    [TestMethod]
    public async Task CurrentChangedClearsBoundAndCancelsStalePendingGeneration()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var bound = new ControlledGatewayCandidate();
        var pending = new ControlledGatewayCandidate();
        var candidates = new Queue<ControlledGatewayCandidate>([bound, pending]);
        ProductSidecarGenerationSnapshot first = Snapshot();
        ProductSidecarGenerationSnapshot next = Snapshot();
        authority.SetCurrent(first);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidates.Dequeue());
        Task<bool> initial = lifecycle.TryReplaceAsync(first, default);
        bound.CompleteHandshake();
        Assert.IsTrue(await initial.WaitAsync(TestTimeout));
        authority.SetCurrent(next, notify: false);
        Task<bool> replacement = lifecycle.TryReplaceAsync(next, default);
        await pending.HandshakeStarted.Task.WaitAsync(TestTimeout);

        authority.SetCurrent(null);

        Assert.IsFalse(await replacement.WaitAsync(TestTimeout));
        Assert.IsNull(binding.Current);
        Assert.AreEqual(1, bound.DisposeCount);
        Assert.AreEqual(1, pending.DisposeCount);
    }

    [TestMethod]
    public async Task ClearOnlyRetiresTheExpectedGeneration()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var firstCandidate = new ControlledGatewayCandidate();
        var secondCandidate = new ControlledGatewayCandidate();
        var candidates = new Queue<ControlledGatewayCandidate>(
            [firstCandidate, secondCandidate]);
        ProductSidecarGenerationSnapshot first = Snapshot();
        ProductSidecarGenerationSnapshot second = Snapshot();
        authority.SetCurrent(first);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority,
            binding,
            _ => candidates.Dequeue());
        Task<bool> firstReplacement = lifecycle.TryReplaceAsync(first, default);
        firstCandidate.CompleteHandshake();
        Assert.IsTrue(await firstReplacement.WaitAsync(TestTimeout));
        authority.SetCurrent(second);
        Task<bool> secondReplacement = lifecycle.TryReplaceAsync(second, default);
        secondCandidate.CompleteHandshake();
        Assert.IsTrue(await secondReplacement.WaitAsync(TestTimeout));

        Assert.IsFalse(lifecycle.Clear(first));
        Assert.AreSame(secondCandidate, binding.Current);
        Assert.AreEqual(0, secondCandidate.DisposeCount);

        Assert.IsTrue(lifecycle.Clear(second));
        Assert.IsNull(binding.Current);
        Assert.AreEqual(1, secondCandidate.DisposeCount);
    }

    [TestMethod]
    public void DispatcherBindingCompareAndSwapFailsClosed()
    {
        var tableGateway = new FakeTableRpcGateway();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(tableGateway),
            new FakeDatabasePicker(null),
            new FakeWebReplySink(),
            NoDatabaseOpenRoute.Instance);
        IProductSidecarForwarderBinding binding = dispatcher;
        var first = new ControlledGatewayCandidate();
        var unrelated = new ControlledGatewayCandidate();
        var replacement = new ControlledGatewayCandidate();
        dispatcher.SetProductSidecarForwarder(first);

        Assert.IsFalse(binding.TryReplace(unrelated, replacement));
        Assert.IsFalse(binding.Clear(unrelated));
        Assert.IsTrue(binding.TryReplace(first, replacement));
        Assert.IsTrue(binding.Clear(replacement));
    }

    [TestMethod]
    public async Task DisposeClearsPublishedGatewayCancelsPendingAndIsIdempotent()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var bound = new ControlledGatewayCandidate();
        var pending = new ControlledGatewayCandidate();
        var candidates = new Queue<ControlledGatewayCandidate>([bound, pending]);
        ProductSidecarGenerationSnapshot first = Snapshot();
        ProductSidecarGenerationSnapshot next = Snapshot();
        authority.SetCurrent(first);
        var lifecycle = new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidates.Dequeue());
        Task<bool> initial = lifecycle.TryReplaceAsync(first, default);
        bound.CompleteHandshake();
        Assert.IsTrue(await initial.WaitAsync(TestTimeout));
        authority.SetCurrent(next, notify: false);
        Task<bool> replacement = lifecycle.TryReplaceAsync(next, default);
        await pending.HandshakeStarted.Task.WaitAsync(TestTimeout);

        lifecycle.Dispose();
        lifecycle.Dispose();

        Assert.IsFalse(await replacement.WaitAsync(TestTimeout));
        Assert.IsNull(binding.Current);
        Assert.AreSame(bound, binding.LastClearExpected);
        Assert.AreEqual(1, binding.ClearCallCount);
        Assert.AreEqual(1, bound.DisposeCount);
        Assert.AreEqual(1, pending.DisposeCount);
    }

    [TestMethod]
    public async Task ClearCancelsOnlyTheMatchingPendingGeneration()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var firstCandidate = new ControlledGatewayCandidate();
        var newerCandidate = new ControlledGatewayCandidate();
        var candidates = new Queue<ControlledGatewayCandidate>(
            [firstCandidate, newerCandidate]);
        ProductSidecarGenerationSnapshot first = Snapshot();
        ProductSidecarGenerationSnapshot newer = Snapshot();
        authority.SetCurrent(first);
        using IProductSidecarGatewayLifecycle lifecycle =
            new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidates.Dequeue());
        Task<bool> replacement = lifecycle.TryReplaceAsync(first, default);
        await firstCandidate.HandshakeStarted.Task.WaitAsync(TestTimeout);

        var equivalent = new ProductSidecarGenerationSnapshot(
            first.RuntimeAuthority,
            first.SidecarGenerationId,
            first.Context,
            first.Identity,
            first.Registrations);
        Assert.IsFalse(lifecycle.Clear(equivalent));
        Assert.IsTrue(lifecycle.Clear(first));

        Assert.IsFalse(await replacement.WaitAsync(TestTimeout));
        Assert.IsNull(binding.Current);
        Assert.AreEqual(1, firstCandidate.DisposeCount);
        authority.SetCurrent(newer);
        Task<bool> next = lifecycle.TryReplaceAsync(newer, default);
        await newerCandidate.HandshakeStarted.Task.WaitAsync(TestTimeout);

        Assert.IsFalse(lifecycle.Clear(first));
        newerCandidate.CompleteHandshake();

        Assert.IsTrue(await next.WaitAsync(TestTimeout));
        Assert.AreSame(newerCandidate, binding.Current);
    }

    [TestMethod]
    public void SnapshotCacheCanonicalizesExactGenerationWithoutLeakingSecret()
    {
        var cache = new ProductSidecarGenerationSnapshotCache();
        var runtime = new object();
        var registrations = new[]
        {
            new ProductSidecarRegistration("query.page", "workspace"),
        };
        PocketBaseAdminContext context = Context("generation-secret");
        ProductSidecarIdentity identity = Identity();
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(
            () => cache.GetOrCreate(runtime, 0, context, identity, []));

        ProductSidecarGenerationSnapshot first = cache.GetOrCreate(
            runtime,
            41,
            context,
            identity,
            registrations);
        Assert.AreEqual(41, first.SidecarGenerationId);
        registrations[0] = new ProductSidecarRegistration(
            "query.changed",
            "global");
        ProductSidecarGenerationSnapshot same = cache.GetOrCreate(
            runtime,
            41,
            Context("generation-secret"),
            Identity(),
            [new ProductSidecarRegistration("query.page", "workspace")]);

        Assert.AreSame(first, same);
        Assert.AreEqual("query.page", first.Registrations.Single().Method);
        Assert.IsFalse(first.Registrations is ProductSidecarRegistration[]);
        Assert.IsFalse(first.ToString()!.Contains(
            "generation-secret",
            StringComparison.Ordinal));
        Assert.AreNotSame(
            first,
            cache.GetOrCreate(
                runtime,
                42,
                Context("generation-secret"),
                identity,
                [new ProductSidecarRegistration("query.page", "workspace")]));
        Assert.AreNotSame(
            first,
            cache.GetOrCreate(
                runtime,
                41,
                Context("next-secret"),
                identity,
                []));
        Assert.AreNotSame(
            first,
            new ProductSidecarGenerationSnapshotCache().GetOrCreate(
                new object(),
                41,
                context,
                identity,
                []));
        Assert.AreNotSame(
            first,
            new ProductSidecarGenerationSnapshotCache().GetOrCreate(
                runtime,
                41,
                context,
                Identity(sessionEpoch: 8),
                []));
    }

    [TestMethod]
    public async Task LateCandidateCannotPublishWhenGenerationRetiresAfterCurrentCheck()
    {
        var cache = new ProductSidecarGenerationSnapshotCache();
        var runtime = new object();
        PocketBaseAdminContext context = Context("fixed-secret");
        ProductSidecarIdentity identity = Identity();
        ProductSidecarRegistration[] registrations =
            [new("query.page", "workspace")];
        var currentCheckPassed = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseAdmission = new TaskCompletionSource(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var admissionGate = new object();
        int admissionAttempt = 0;
        bool retired = false;
        ProductSidecarGenerationSnapshot first = cache.GetOrCreate(
            runtime,
            71,
            context,
            identity,
            registrations,
            action =>
            {
                if (Interlocked.Increment(ref admissionAttempt) == 2)
                {
                    currentCheckPassed.TrySetResult();
                    releaseAdmission.Task.GetAwaiter().GetResult();
                }
                lock (admissionGate)
                    return !retired && action();
            });
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var candidate = new ControlledGatewayCandidate(ignoreCancellation: true);
        bool candidateConstructedInsideAdmission = false;
        authority.SetCurrent(first);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority,
            binding,
            _ =>
            {
                candidateConstructedInsideAdmission = Monitor.IsEntered(admissionGate);
                return candidate;
            });

        Task<bool> replacement = lifecycle.TryReplaceAsync(first, default);
        await candidate.HandshakeStarted.Task.WaitAsync(TestTimeout);
        ProductSidecarGenerationSnapshot second = cache.GetOrCreate(
            runtime,
            72,
            Context("fixed-secret"),
            Identity(),
            [new ProductSidecarRegistration("query.page", "workspace")]);
        candidate.CompleteHandshake();

        await currentCheckPassed.Task.WaitAsync(TestTimeout);
        lock (admissionGate)
            retired = true;
        authority.SetCurrent(second, notify: false);
        releaseAdmission.TrySetResult();

        Assert.IsFalse(await replacement.WaitAsync(TestTimeout));
        Assert.IsNull(binding.Current);
        Assert.AreEqual(0, binding.ClearCallCount);
        Assert.AreEqual(1, candidate.DisposeCount);
        Assert.IsTrue(candidateConstructedInsideAdmission);
    }

    [TestMethod]
    public async Task CandidateConstructionFailureDoesNotLeavePendingGeneration()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        ProductSidecarGenerationSnapshot snapshot = Snapshot();
        authority.SetCurrent(snapshot);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority,
            binding,
            _ => throw new InvalidOperationException(
                "product_sidecar.candidate_creation_failed"));

        InvalidOperationException error = await Assert.ThrowsExactlyAsync<
            InvalidOperationException>(
            () => lifecycle.TryReplaceAsync(snapshot, default).WaitAsync(TestTimeout));

        Assert.AreEqual(
            "product_sidecar.candidate_creation_failed",
            error.Message);
        Assert.IsFalse(lifecycle.Clear(snapshot));
        Assert.IsNull(binding.Current);
    }

    [TestMethod]
    public async Task FailedCanceledOrStaleCandidatesPreservePublishedBinding()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var current = new ControlledGatewayCandidate();
        var failed = new ControlledGatewayCandidate();
        var canceled = new ControlledGatewayCandidate();
        var stale = new ControlledGatewayCandidate();
        var candidates = new Queue<ControlledGatewayCandidate>(
            [current, failed, canceled, stale]);
        ProductSidecarGenerationSnapshot first = Snapshot();
        ProductSidecarGenerationSnapshot next = Snapshot();
        authority.SetCurrent(first);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidates.Dequeue());
        Task<bool> initial = lifecycle.TryReplaceAsync(first, default);
        current.CompleteHandshake();
        Assert.IsTrue(await initial.WaitAsync(TestTimeout));
        authority.SetCurrent(next, notify: false);

        Task<bool> failure = lifecycle.TryReplaceAsync(next, default);
        failed.FailHandshake(new InvalidOperationException("handshake failed"));
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(
            () => failure.WaitAsync(TestTimeout));
        Assert.AreSame(current, binding.Current);

        using var cancellation = new CancellationTokenSource();
        Task<bool> cancellationAttempt = lifecycle.TryReplaceAsync(
            next, cancellation.Token);
        await canceled.HandshakeStarted.Task.WaitAsync(TestTimeout);
        cancellation.Cancel();
        await Assert.ThrowsAsync<OperationCanceledException>(
            () => cancellationAttempt.WaitAsync(TestTimeout));
        Assert.AreSame(current, binding.Current);

        Task<bool> staleAttempt = lifecycle.TryReplaceAsync(next, default);
        authority.SetCurrent(null, notify: false);
        stale.CompleteHandshake();
        Assert.IsFalse(await staleAttempt.WaitAsync(TestTimeout));
        Assert.AreSame(current, binding.Current);
        Assert.AreEqual(1, failed.DisposeCount);
        Assert.AreEqual(1, canceled.DisposeCount);
        Assert.AreEqual(1, stale.DisposeCount);
    }

    [TestMethod]
    public async Task SuccessfulReplacementPublishesBeforeRetiringOldGateway()
    {
        var authority = new ControlledGenerationAuthority();
        var binding = new ControlledSidecarBinding();
        var replacement = new ControlledGatewayCandidate();
        var previous = new ControlledGatewayCandidate(
            onDispose: () =>
            {
                Assert.AreSame(replacement, binding.Current);
                throw new InvalidOperationException("retired dispose failed");
            });
        var candidates = new Queue<ControlledGatewayCandidate>(
            [previous, replacement]);
        ProductSidecarGenerationSnapshot first = Snapshot();
        ProductSidecarGenerationSnapshot second = Snapshot();
        authority.SetCurrent(first);
        using var lifecycle = new ProductSidecarGatewayLifecycle(
            authority, binding, _ => candidates.Dequeue());
        Task<bool> firstBinding = lifecycle.TryReplaceAsync(first, default);
        previous.CompleteHandshake();
        Assert.IsTrue(await firstBinding.WaitAsync(TestTimeout));
        authority.SetCurrent(second, notify: false);

        Task<bool> nextBinding = lifecycle.TryReplaceAsync(second, default);
        replacement.CompleteHandshake();

        Assert.IsTrue(await nextBinding.WaitAsync(TestTimeout));
        Assert.AreSame(replacement, binding.Current);
        Assert.AreEqual(1, previous.DisposeCount);
        Assert.AreEqual(0, replacement.DisposeCount);
    }

    private static ProductSidecarGenerationSnapshot Snapshot()
        => new(
            new object(),
            1,
            Context("private-secret"),
            Identity(),
            []);

    private static PocketBaseAdminContext Context(string secret)
        => new(
            new Uri("http://127.0.0.1:8090/_/"),
            new Uri("http://127.0.0.1:8090/"),
            "X-VibeTable-Session",
            secret);

    private static ProductSidecarIdentity Identity(ulong sessionEpoch = 7)
        => new(
            "11111111-1111-4111-8111-111111111111",
            sessionEpoch,
            3,
            "22222222-2222-4222-8222-222222222222");
}

internal sealed class ControlledGenerationAuthority : IProductSidecarGenerationAuthority
{
    private ProductSidecarGenerationSnapshot? _current;

    public event Action? CurrentChanged;

    internal void SetCurrent(
        ProductSidecarGenerationSnapshot? snapshot,
        bool notify = true)
    {
        _current = snapshot;
        if (notify)
            CurrentChanged?.Invoke();
    }

    public bool TryUseCurrent(
        ProductSidecarGenerationSnapshot snapshot,
        Func<bool> action)
        => ReferenceEquals(_current, snapshot)
            && snapshot.TryUseCurrent(action);
}

internal sealed class ControlledSidecarBinding : IProductSidecarForwarderBinding
{
    internal IProductSidecarRpcForwarder? Current { get; private set; }
    internal IProductSidecarRpcForwarder? LastClearExpected { get; private set; }
    internal int ClearCallCount { get; private set; }

    public bool TryReplace(
        IProductSidecarRpcForwarder? expected,
        IProductSidecarRpcForwarder replacement)
    {
        if (!ReferenceEquals(Current, expected))
            return false;
        Current = replacement;
        return true;
    }

    public bool Clear(IProductSidecarRpcForwarder expected)
    {
        LastClearExpected = expected;
        ClearCallCount++;
        if (!ReferenceEquals(Current, expected))
            return false;
        Current = null;
        return true;
    }
}

internal sealed class ControlledGatewayCandidate : IProductSidecarGatewayCandidate
{
    private readonly bool _ignoreCancellation;
    private readonly Action? _onDispose;
    private readonly TaskCompletionSource _handshake =
        new(TaskCreationOptions.RunContinuationsAsynchronously);

    internal TaskCompletionSource HandshakeStarted { get; } =
        new(TaskCreationOptions.RunContinuationsAsynchronously);
    internal int DisposeCount { get; private set; }

    internal ControlledGatewayCandidate(
        bool ignoreCancellation = false,
        Action? onDispose = null)
    {
        _ignoreCancellation = ignoreCancellation;
        _onDispose = onDispose;
    }

    public Task<ProductSidecarCapabilities> GetCapabilitiesAsync(
        CancellationToken cancellationToken)
    {
        HandshakeStarted.TrySetResult();
        return WaitForHandshakeAsync(cancellationToken);
    }

    public Task<ProductSidecarForwardResult> ForwardAsync(
        string requestId,
        string method,
        System.Text.Json.JsonElement wire,
        System.Text.Json.JsonElement parameters,
        CancellationToken cancellationToken)
        => throw new NotSupportedException();

    public void Dispose()
    {
        DisposeCount++;
        _onDispose?.Invoke();
    }

    internal void CompleteHandshake() => _handshake.TrySetResult();
    internal void FailHandshake(Exception exception) =>
        _handshake.TrySetException(exception);

    private async Task<ProductSidecarCapabilities> WaitForHandshakeAsync(
        CancellationToken cancellationToken)
    {
        if (_ignoreCancellation)
            await _handshake.Task;
        else
            await _handshake.Task.WaitAsync(cancellationToken);
        return new ProductSidecarCapabilities(
            "2.0",
            "11111111-1111-4111-8111-111111111111",
            7,
            3,
            "22222222-2222-4222-8222-222222222222",
            [],
            []);
    }
}
