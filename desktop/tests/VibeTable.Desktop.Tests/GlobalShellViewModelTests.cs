using VibeTable.Desktop.ViewModels;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GlobalShellViewModelTests
{
    [TestMethod]
    public async Task BackendFaultKeepsAlreadyLoadedGlobalShellVisible()
    {
        var backend = new FakeBackendLifecycle { ShouldFail = true };
        var viewModel = new MainWindowViewModel(
            backend,
            new FakeWebViewBridge());
        viewModel.MarkShellLoaded();

        await viewModel.StartAsync();

        Assert.AreEqual(StartupState.Faulted, viewModel.State);
        Assert.IsFalse(viewModel.IsHostFallbackVisible);
        Assert.IsTrue(viewModel.IsRetryVisible);
    }

    [TestMethod]
    public void RendererFailureRestoresNativeFallback()
    {
        var viewModel = new MainWindowViewModel(
            new FakeBackendLifecycle(),
            new FakeWebViewBridge());
        viewModel.MarkShellLoaded();

        viewModel.MarkShellUnavailable();

        Assert.IsTrue(viewModel.IsHostFallbackVisible);
    }
}
