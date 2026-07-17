using System;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Startup state-machine tests for <see cref="MainWindowViewModel"/>.
/// </summary>
/// <remarks>
/// <para>
/// The ViewModel must be testable WITHOUT STA / WebView2 / a real backend
/// process. Every external capability is hidden behind an injected interface
/// (<see cref="IBackendLifecycle"/>, <see cref="IWebViewBridge"/>), and these
/// tests substitute fakes that the test drives synchronously.
/// </para>
/// <para>
/// Legal transitions (verbatim from the Task 9 brief):
/// </para>
/// <list type="bullet">
/// <item>StartingBackend -&gt; LoadingWeb -&gt; Ready</item>
/// <item>StartingBackend -&gt; Faulted</item>
/// <item>LoadingWeb -&gt; Faulted</item>
/// <item>Ready -&gt; Faulted</item>
/// <item>Faulted -&gt; StartingBackend (explicit retry only)</item>
/// </list>
/// </remarks>
[TestClass]
public sealed class MainWindowViewModelTests
{
    [TestMethod]
    public async Task Ctor_StartsInStartingBackend_AndTransitionsToReady_HappyPath()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = await CreateAndStartAsync(backend, web);

        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        Assert.AreEqual("正在加载界面…", vm.StatusText);
        Assert.IsFalse(vm.IsGridVisible);
        // The WebView2 HWND is realized during LoadingWeb so navigation has a
        // non-zero surface; IsGridVisible (user-facing) stays false until Ready.
        Assert.IsTrue(vm.IsWebViewVisible);
        Assert.IsFalse(vm.IsRetryVisible);

        // WebView reports the grid loaded -> Ready.
        web.CompleteLoad();

        Assert.AreEqual(StartupState.Ready, vm.State);
        Assert.AreEqual("就绪", vm.StatusText);
        Assert.IsTrue(vm.IsGridVisible);
        Assert.IsTrue(vm.IsWebViewVisible);
        Assert.IsFalse(vm.IsRetryVisible);
    }

    [TestMethod]
    public async Task BackendFailure_InStarting_MovesToFaulted()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = await CreateAndStartAsync(backend, web, failBackend: true);

        Assert.AreEqual(StartupState.Faulted, vm.State);
        Assert.AreEqual("出现错误", vm.StatusText);
        Assert.IsFalse(vm.IsGridVisible);
        Assert.IsTrue(vm.IsRetryVisible);
    }

    [TestMethod]
    public async Task WebViewFailure_InLoading_MovesToFaulted()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = await CreateAndStartAsync(backend, web);

        Assert.AreEqual(StartupState.LoadingWeb, vm.State);

        web.FailLoad(new InvalidOperationException("WebView2 runtime missing."));

        Assert.AreEqual(StartupState.Faulted, vm.State);
        Assert.IsFalse(vm.IsGridVisible);
        Assert.IsTrue(vm.IsRetryVisible);
    }

    [TestMethod]
    public async Task Ready_CanMoveToFaulted()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = await CreateAndStartAsync(backend, web);
        web.CompleteLoad();

        Assert.AreEqual(StartupState.Ready, vm.State);
        vm.MoveToFaulted("backend crashed");

        Assert.AreEqual(StartupState.Faulted, vm.State);
        Assert.IsFalse(vm.IsGridVisible);
        Assert.IsTrue(vm.IsRetryVisible);
    }

    [TestMethod]
    public async Task Retry_FromFaulted_ReentersStartingBackend()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = await CreateAndStartAsync(backend, web, failBackend: true);

        Assert.AreEqual(StartupState.Faulted, vm.State);

        // Second attempt succeeds.
        backend.ShouldFail = false;
        vm.RetryCommand.Execute(null);

        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        Assert.IsFalse(vm.IsRetryVisible);
    }

    [TestMethod]
    public async Task RetryCommand_CanExecute_DependsOnFaultedState()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = await CreateAndStartAsync(backend, web);

        Assert.AreEqual(StartupState.LoadingWeb, vm.State);
        Assert.IsFalse(vm.RetryCommand.CanExecute(null));

        web.FailLoad(new InvalidOperationException("boom"));
        Assert.AreEqual(StartupState.Faulted, vm.State);
        Assert.IsTrue(vm.RetryCommand.CanExecute(null));
    }

    [TestMethod]
    public async Task MoveToFaulted_FromStartingBackend_ThrowsInvalidOperation()
    {
        var backend = new FakeBackendLifecycle().BlockStart();
        var web = new FakeWebViewBridge();
        var vm = new MainWindowViewModel(backend, web);
        var startTask = vm.StartAsync();

        // Still in StartingBackend (StartAsync hasn't completed).
        Assert.AreEqual(StartupState.StartingBackend, vm.State);

        Assert.ThrowsExactly<InvalidOperationException>(
            () => vm.MoveToFaulted("nope"));

        backend.ReleaseStart();
        await startTask;
    }

    [TestMethod]
    public void RetryCommand_IsNull_OrDisabled_WhenNotFaulted()
    {
        var backend = new FakeBackendLifecycle().BlockStart();
        var web = new FakeWebViewBridge();
        var vm = new MainWindowViewModel(backend, web);

        // Pre-start: no Retry yet enabled.
        Assert.IsFalse(vm.RetryCommand.CanExecute(null));
    }

    [TestMethod]
    public void DetailMessage_RaisesPropertyChanged_WhenSet()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = new MainWindowViewModel(backend, web);

        var changed = new List<string?>();
        vm.PropertyChanged += (_, e) => changed.Add(e.PropertyName);

        vm.DetailMessage = "正在创建 VibeTable 数据结构…";

        Assert.AreEqual("正在创建 VibeTable 数据结构…", vm.DetailMessage);
        CollectionAssert.Contains(changed, nameof(MainWindowViewModel.DetailMessage));
    }

    [TestMethod]
    public void DetailMessage_SetToSameValue_DoesNotRaise()
    {
        var backend = new FakeBackendLifecycle();
        var web = new FakeWebViewBridge();
        var vm = new MainWindowViewModel(backend, web);
        vm.DetailMessage = "same";

        var changed = new List<string?>();
        vm.PropertyChanged += (_, e) => changed.Add(e.PropertyName);

        vm.DetailMessage = "same"; // identical

        CollectionAssert.DoesNotContain(changed, nameof(MainWindowViewModel.DetailMessage));
    }

    private static async Task<MainWindowViewModel> CreateAndStartAsync(
        FakeBackendLifecycle backend,
        FakeWebViewBridge web,
        bool failBackend = false)
    {
        backend.ShouldFail = failBackend;
        var vm = new MainWindowViewModel(backend, web);
        await vm.StartAsync();
        return vm;
    }
}

/// <summary>
/// Fake <see cref="IBackendLifecycle"/> for ViewModel tests. By default
/// <see cref="StartAsync"/> completes synchronously (Task.CompletedTask) so the
/// state machine proceeds through StartingBackend -&gt; LoadingWeb inline; tests
/// that need to observe the StartingBackend state call <see cref="BlockStart"/>.
/// </summary>
internal sealed class FakeBackendLifecycle : IBackendLifecycle
{
    private readonly TaskCompletionSource<bool> _startGate =
        new(TaskCreationOptions.RunContinuationsAsynchronously);
    private bool _blocked;

    public bool ShouldFail { get; set; }

    public FakeBackendLifecycle BlockStart()
    {
        _blocked = true;
        return this;
    }

    public void ReleaseStart() => _startGate.TrySetResult(true);

    public Task StartAsync(CancellationToken cancellationToken)
    {
        if (ShouldFail)
        {
            return Task.FromException(
                new InvalidOperationException("Fake backend failed to start."));
        }
        if (!_blocked)
        {
            return Task.CompletedTask;
        }
        return _startGate.Task;
    }

    public Task StopAsync(CancellationToken cancellationToken)
    {
        return Task.CompletedTask;
    }
}

/// <summary>
/// Fake <see cref="IWebViewBridge"/> for ViewModel tests. LoadAsync completes
/// or fails only when the test signals it. The TCS does NOT use
/// <see cref="TaskCreationOptions.RunContinuationsAsynchronously"/> so the
/// ViewModel's continuation (which transitions to Ready / Faulted) runs inline
/// on the thread that calls <see cref="CompleteLoad"/> / <see cref="FailLoad"/>
/// — this lets the tests assert the post-transition state synchronously.
/// </summary>
internal sealed class FakeWebViewBridge : IWebViewBridge
{
    private readonly TaskCompletionSource<bool> _loadTcs = new();

    public void CompleteLoad() => _loadTcs.TrySetResult(true);

    public void FailLoad(Exception ex) => _loadTcs.TrySetException(ex);

    public Task LoadAsync(CancellationToken cancellationToken)
        => _loadTcs.Task;
}
