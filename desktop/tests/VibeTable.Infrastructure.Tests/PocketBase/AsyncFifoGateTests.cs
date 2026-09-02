using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Infrastructure.Tests.PocketBase;

[TestClass]
public sealed class AsyncFifoGateTests
{
    [TestMethod]
    public async Task EnterAsync_GrantsWaitersInAdmissionOrder()
    {
        var gate = new AsyncFifoGate();
        using AsyncFifoGate.Lease first =
            await gate.EnterAsync(CancellationToken.None);
        Task<AsyncFifoGate.Lease> second =
            gate.EnterAsync(CancellationToken.None).AsTask();
        Task<AsyncFifoGate.Lease> third =
            gate.EnterAsync(CancellationToken.None).AsTask();

        Assert.IsFalse(second.IsCompleted);
        Assert.IsFalse(third.IsCompleted);
        first.Dispose();

        using AsyncFifoGate.Lease secondLease =
            await second.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(third.IsCompleted);
        secondLease.Dispose();

        using AsyncFifoGate.Lease thirdLease =
            await third.WaitAsync(TimeSpan.FromSeconds(2));
    }

    [TestMethod]
    public async Task CanceledWaiter_DoesNotLetSuccessorOvertakeCurrentLease()
    {
        var gate = new AsyncFifoGate();
        using var cancellation = new CancellationTokenSource();
        using AsyncFifoGate.Lease first =
            await gate.EnterAsync(CancellationToken.None);
        Task<AsyncFifoGate.Lease> canceled =
            gate.EnterAsync(cancellation.Token).AsTask();
        Task<AsyncFifoGate.Lease> successor =
            gate.EnterAsync(CancellationToken.None).AsTask();

        cancellation.Cancel();
        await Assert.ThrowsAsync<OperationCanceledException>(
            () => canceled.WaitAsync(TimeSpan.FromSeconds(2)));
        Assert.IsFalse(successor.IsCompleted);

        first.Dispose();
        using AsyncFifoGate.Lease successorLease =
            await successor.WaitAsync(TimeSpan.FromSeconds(2));
    }
}
