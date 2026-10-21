using System.Windows;
using System.Windows.Threading;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Regression for the cancellable owned .base picker. The pre-Show cancel and
/// closer-ordering cases run everywhere on STA; the real native modal test is
/// gated to actual CI runners (GITHUB_ACTIONS=true and CI=true) and reports
/// Inconclusive elsewhere instead of faking the modal.
/// </summary>
[TestClass]
public sealed class FeishuBaseFileDialogTests
{
    private static bool IsCiRunner =>
        Environment.GetEnvironmentVariable("GITHUB_ACTIONS") == "true"
        && Environment.GetEnvironmentVariable("CI") == "true";

    [TestMethod]
    public async Task CancelledBeforeShowThrowsWithoutOpeningAnyDialog()
    {
        using var harness = new StaHarness();
        using var cancelled = new CancellationTokenSource();
        cancelled.Cancel();
        await harness.RunActionAsync(() =>
        {
            Assert.ThrowsExactly<OperationCanceledException>(() =>
                FeishuBaseFileDialog.PickFileAsync(harness.Owner, cancelled.Token));
        });
    }

    [TestMethod]
    public async Task RequestCloseBeforeCaptureStaysPendingUntilVerifiedCapture()
    {
        // Cancel-before-capture ordering: the request is recorded, nothing is
        // posted (no verified owned dialog exists), and the state stays
        // observable for the after-Show fault path.
        using var harness = new StaHarness();
        await harness.RunActionAsync(() =>
        {
            using CbtOwnedDialogCloser closer = CbtOwnedDialogCloser.Arm(harness.Owner);
            closer.RequestClose();
            Assert.IsTrue(closer.CloseRequested);
            Assert.IsFalse(closer.EverCaptured);
            Assert.IsNull(closer.Fault);
        });
    }

    [TestMethod]
    public async Task RealOwnedModalClosesOnCancellationAndLeavesNoDialogBehind()
    {
        if (!IsCiRunner)
            Assert.Inconclusive("真实模态取消回归仅在受控 CI runner 上执行（GITHUB_ACTIONS=true 且 CI=true）。");

        using var harness = new StaHarness();
        using var cts = new CancellationTokenSource();
        var shown = new TaskCompletionSource<IntPtr>(TaskCreationOptions.RunContinuationsAsynchronously);
        IntPtr handle = IntPtr.Zero;
        // Start the operation without awaiting its inner task: ShowDialog
        // runs on the STA dispatcher and only ends once the cancellation
        // below dismisses the modal.
        Task<string?> pickLater = harness.BeginOperation(() =>
            FeishuBaseFileDialog.PickFileAsync(harness.Owner, cts.Token,
                captured => { handle = captured; shown.TrySetResult(captured); }));
        try
        {
            handle = await shown.Task.WaitAsync(TimeSpan.FromSeconds(20));
            cts.Cancel();
            // The synchronous picker throws OCE on the STA once the modal
            // ends; the dispatcher operation faults with the same exception.
            await Assert.ThrowsAsync<OperationCanceledException>(() => pickLater);
            await harness.RunActionAsync(() =>
                Assert.IsFalse(CbtOwnedDialogCloser.IsWindowAlive(handle), "picker 句柄必须已销毁"));
        }
        finally
        {
            // Recovery close with full identity re-verification so a failed
            // cancel cannot leave a live modal on the runner. Both handles
            // were captured on the STA thread — no cross-thread WPF access.
            CbtOwnedDialogCloser.CloseVerifiedDialog(handle, harness.OwnerHandle);
        }
    }

    /// <summary>Minimal STA harness mirroring the existing wizard fixtures:
    /// an off-screen, non-activating owner window on its own dispatcher.</summary>
    private sealed class StaHarness : IDisposable
    {
        private readonly Thread _thread;
        private readonly TaskCompletionSource _ready = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _stopped = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private Dispatcher? _dispatcher;

        internal StaHarness()
        {
            _thread = new Thread(() =>
            {
                try
                {
                    _dispatcher = Dispatcher.CurrentDispatcher;
                    Owner = new Window
                    {
                        WindowStartupLocation = WindowStartupLocation.Manual,
                        Left = -32000,
                        Top = -32000,
                        ShowInTaskbar = false,
                        ShowActivated = false,
                    };
                    Owner.Show();
                    // Captured on the STA thread: cleanup never touches the
                    // WPF object from another thread.
                    OwnerHandle = new System.Windows.Interop.WindowInteropHelper(Owner).Handle;
                    _ready.TrySetResult();
                    Dispatcher.Run();
                }
                finally
                {
                    _stopped.TrySetResult();
                }
            })
            { IsBackground = true, Name = "vibetable-base-file-dialog-tests" };
            _thread.SetApartmentState(ApartmentState.STA);
            _thread.Start();
            if (!_ready.Task.Wait(TimeSpan.FromSeconds(15)))
                Assert.Fail("STA harness did not start.");
        }

        internal Window Owner { get; private set; } = null!;
        internal IntPtr OwnerHandle { get; private set; }

        internal Task<T> RunAsync<T>(Func<T> func)
        {
            DispatcherOperation<T> operation = (_dispatcher ?? throw new AssertFailedException("no dispatcher"))
                .InvokeAsync(func);
            return operation.Task.WaitAsync(TimeSpan.FromSeconds(20));
        }

        /// <summary>Queues the operation and unwraps the inner task: awaiting
        /// the result yields the picker task's outcome only after the modal on
        /// the STA thread has ended.</summary>
        internal Task<T> BeginOperation<T>(Func<Task<T>> func) => RunAsync(func).Unwrap();

        internal Task RunActionAsync(Action action) => RunAsync<object?>(() => { action(); return null; });

        public void Dispose()
        {
            try
            {
                _dispatcher?.BeginInvoke(new Action(() =>
                {
                    try { Owner?.Close(); }
                    catch (Exception) { /* keep shutdown going */ }
                }));
                _dispatcher?.BeginInvokeShutdown(DispatcherPriority.Background);
            }
            catch (Exception) { /* dispatcher already down */ }
            if (!_thread.Join(TimeSpan.FromSeconds(10)))
                Assert.Fail("STA harness did not shut down within the budget.");
        }
    }
}
