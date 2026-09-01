using System.Diagnostics;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class TrustedSidecarProcessRunnerTests
{
    [TestMethod]
    public async Task CancellationDoesNotReturnUntilKilledHelperHasExited()
    {
        var process = new DelayedExitProcess();
        var runner = new TrustedSidecarProcessRunner(_ => process);
        using var cancellation = new CancellationTokenSource();

        Task<TrustedSidecarProcessResult> run = runner.RunAsync(
            new ProcessStartInfo(),
            standardInput: null,
            cancellation.Token);
        await process.WaitStarted.WaitAsync(TimeSpan.FromSeconds(2));

        cancellation.Cancel();
        await process.KillObserved.WaitAsync(TimeSpan.FromSeconds(2));

        process.CompleteExit();
        await Assert.ThrowsAsync<OperationCanceledException>(() => run);
        Assert.AreEqual(1, process.WaitCallCount);
        Assert.IsFalse(process.ReadWasCancelable);
    }

    [TestMethod]
    public async Task CancellationWhileWritingInputUsesTheSameExitCleanup()
    {
        var process = new DelayedExitProcess(writePending: true);
        var runner = new TrustedSidecarProcessRunner(_ => process);
        using var cancellation = new CancellationTokenSource();

        Task<TrustedSidecarProcessResult> run = runner.RunAsync(
            new ProcessStartInfo(),
            "trusted input",
            cancellation.Token);
        await process.WriteStarted.WaitAsync(TimeSpan.FromSeconds(2));

        cancellation.Cancel();
        await process.KillObserved.WaitAsync(TimeSpan.FromSeconds(2));

        process.CompleteExit();
        await Assert.ThrowsAsync<OperationCanceledException>(() => run);
        Assert.AreEqual(1, process.WaitCallCount);
    }

    [TestMethod]
    public async Task OutputFailureWaitsForExitAndPreservesTheOriginalError()
    {
        var original = new IOException("stdout failed");
        var process = new DelayedExitProcess(
            outputPending: true,
            killFailure: new InvalidOperationException("kill failed"),
            cleanupWaitFailure: new InvalidOperationException("wait failed"),
            disposeFailure: new InvalidOperationException("dispose failed"));
        var runner = new TrustedSidecarProcessRunner(_ => process);

        Task<TrustedSidecarProcessResult> run = runner.RunAsync(
            new ProcessStartInfo(),
            standardInput: null,
            CancellationToken.None);
        await process.OutputStarted.WaitAsync(TimeSpan.FromSeconds(2));
        process.FailOutput(original);
        await process.KillObserved.WaitAsync(TimeSpan.FromSeconds(2));

        process.CompleteExit();
        IOException error = await Assert.ThrowsExactlyAsync<IOException>(() => run);
        Assert.AreSame(original, error);
        Assert.AreEqual(1, process.WaitCallCount);
    }

    [TestMethod]
    public async Task TerminationWaitsForPendingExitAfterKill()
    {
        var process = new DelayedExitProcess();
        Task exitTask = process.WaitForExitAsync(CancellationToken.None);

        Task termination = TrustedSidecarProcessRunner.TerminateAsync(
            process,
            exitTask);

        Assert.IsFalse(termination.IsCompleted);
        Assert.IsTrue(process.KillObserved.IsCompleted);
        process.CompleteExit();
        await termination;
        Assert.AreEqual(1, process.WaitCallCount);
    }

    [TestMethod]
    public async Task ExitedProcessStillSettlesFaultedExitObservation()
    {
        var process = new DelayedExitProcess(
            cleanupWaitFailure: new InvalidOperationException("wait failed"));
        process.MarkExited();
        Task exitTask = process.WaitForExitAsync(CancellationToken.None);

        Task termination = TrustedSidecarProcessRunner.TerminateAsync(
            process,
            exitTask);

        Assert.IsFalse(termination.IsCompleted);
        Assert.IsFalse(process.KillObserved.IsCompleted);
        process.CompleteExitObservation();
        await termination;
        Assert.AreEqual(1, process.WaitCallCount);
    }

    [TestMethod]
    public async Task FaultedExitObservationWithoutExitProofRemainsFailClosed()
    {
        var process = new DelayedExitProcess(
            cleanupWaitFailure: new InvalidOperationException("wait failed"));
        Task exitTask = process.WaitForExitAsync(CancellationToken.None);

        Task termination = TrustedSidecarProcessRunner.TerminateAsync(
            process,
            exitTask);
        process.CompleteExitObservation();
        await process.SecondHasExitedObserved.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.IsFalse(termination.IsCompleted);
        Assert.IsTrue(process.KillObserved.IsCompleted);
        Assert.AreEqual(1, process.WaitCallCount);
    }

    private sealed class DelayedExitProcess : ITrustedSidecarProcess
    {
        private readonly TaskCompletionSource _exited =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _killObserved =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _waitStarted =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _writeStarted =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _outputStarted =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _secondHasExitedObserved =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly Exception? _killFailure;
        private readonly Exception? _cleanupWaitFailure;
        private readonly Exception? _disposeFailure;
        private readonly TaskCompletionSource? _write;
        private readonly TaskCompletionSource<string>? _output;
        private int _hasExited;
        private int _hasExitedReadCount;
        private int _waitCallCount;
        private int _readWasCancelable;

        public DelayedExitProcess(
            Exception? killFailure = null,
            Exception? cleanupWaitFailure = null,
            Exception? disposeFailure = null,
            bool writePending = false,
            bool outputPending = false)
        {
            _killFailure = killFailure;
            _cleanupWaitFailure = cleanupWaitFailure;
            _disposeFailure = disposeFailure;
            if (writePending)
                _write = new TaskCompletionSource(
                    TaskCreationOptions.RunContinuationsAsynchronously);
            if (outputPending)
                _output = new TaskCompletionSource<string>(
                    TaskCreationOptions.RunContinuationsAsynchronously);
        }

        public Task KillObserved => _killObserved.Task;

        public Task WaitStarted => _waitStarted.Task;

        public Task WriteStarted => _writeStarted.Task;

        public Task OutputStarted => _outputStarted.Task;

        public Task SecondHasExitedObserved => _secondHasExitedObserved.Task;

        public int WaitCallCount => _waitCallCount;

        public bool ReadWasCancelable => _readWasCancelable != 0;

        public bool HasExited
        {
            get
            {
                if (Interlocked.Increment(ref _hasExitedReadCount) >= 2)
                    _secondHasExitedObserved.TrySetResult();
                return Volatile.Read(ref _hasExited) != 0;
            }
        }

        public int ExitCode => 0;

        public bool Start() => true;

        public Task WriteStandardInputAsync(string input)
        {
            _writeStarted.TrySetResult();
            return _write?.Task ?? Task.CompletedTask;
        }

        public void CloseStandardInput()
        {
        }

        public Task<string> ReadStandardOutputToEndAsync(
            CancellationToken cancellationToken)
        {
            if (cancellationToken.CanBeCanceled)
                Interlocked.Exchange(ref _readWasCancelable, 1);
            _outputStarted.TrySetResult();
            return _output?.Task ?? Task.FromResult(string.Empty);
        }

        public Task<string> ReadStandardErrorToEndAsync(
            CancellationToken cancellationToken)
        {
            if (cancellationToken.CanBeCanceled)
                Interlocked.Exchange(ref _readWasCancelable, 1);
            return Task.FromResult(string.Empty);
        }

        public Task WaitForExitAsync(CancellationToken cancellationToken)
        {
            Interlocked.Increment(ref _waitCallCount);
            _waitStarted.TrySetResult();
            Assert.IsFalse(
                cancellationToken.CanBeCanceled,
                "The sole process-exit observation must outlive caller cancellation.");
            return WaitForCleanupAsync();
        }

        public void KillEntireProcessTree()
        {
            _killObserved.TrySetResult();
            if (_killFailure is not null)
                throw _killFailure;
        }

        public void FailOutput(Exception error) =>
            _output?.TrySetException(error);

        public void MarkExited() => Interlocked.Exchange(ref _hasExited, 1);

        public void CompleteExit()
        {
            MarkExited();
            _write?.TrySetResult();
            _exited.TrySetResult();
        }

        public void CompleteExitObservation() => _exited.TrySetResult();

        public void Dispose()
        {
            if (_disposeFailure is not null)
                throw _disposeFailure;
        }

        private async Task WaitForCleanupAsync()
        {
            await _exited.Task;
            if (_cleanupWaitFailure is not null)
                throw _cleanupWaitFailure;
        }
    }
}
