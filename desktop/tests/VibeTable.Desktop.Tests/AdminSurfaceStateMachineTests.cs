using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class AdminSurfaceStateMachineTests
{
    [TestMethod]
    public void FirstOpenInitializes_ClosePreservesInstance_ReopenIsReady()
    {
        var surface = new AdminSurfaceStateMachine();

        Assert.IsTrue(surface.BeginOpen());
        Assert.AreEqual(AdminSurfaceState.Initializing, surface.State);

        surface.MarkReady();
        surface.Close();

        Assert.IsTrue(surface.IsInitialized);
        Assert.IsTrue(surface.HasReadyPage);
        Assert.IsFalse(surface.IsVisible);
        Assert.IsFalse(surface.BeginOpen());
        Assert.AreEqual(AdminSurfaceState.Ready, surface.State);
    }

    [TestMethod]
    public void FailureStaysVisibleUntilUserClosesAndCanRetryInitialization()
    {
        var surface = new AdminSurfaceStateMachine();
        surface.BeginOpen();
        surface.MarkFailed("WebView2 unavailable");

        Assert.AreEqual(AdminSurfaceState.Failed, surface.State);
        Assert.AreEqual("WebView2 unavailable", surface.LastError);
        Assert.IsFalse(surface.HasReadyPage);
        Assert.IsTrue(surface.IsVisible);

        surface.Close();
        Assert.IsTrue(surface.BeginOpen());
        Assert.AreEqual(AdminSurfaceState.Initializing, surface.State);
    }

    [TestMethod]
    public void MarkInitializedDoesNotReopenAClosedSurface()
    {
        var surface = new AdminSurfaceStateMachine();
        surface.BeginOpen();
        surface.Close();

        surface.MarkInitialized();

        Assert.IsTrue(surface.IsInitialized);
        Assert.AreEqual(AdminSurfaceState.Hidden, surface.State);
    }

    [TestMethod]
    public void ReleaseForgetsInitializedRendererSoNextOpenReinitializes()
    {
        var surface = new AdminSurfaceStateMachine();
        surface.BeginOpen();
        surface.MarkReady();
        surface.Close();

        surface.Release();

        Assert.IsFalse(surface.IsInitialized);
        Assert.IsFalse(surface.HasReadyPage);
        Assert.AreEqual(AdminSurfaceState.Hidden, surface.State);
        Assert.IsTrue(surface.BeginOpen());
        Assert.AreEqual(AdminSurfaceState.Initializing, surface.State);
    }

    [TestMethod]
    public void FloatingButtonScriptUsesShadowDomAndClosedHostMessage()
    {
        string script = MainWindow.BuildAdminFloatingButtonScript(enabled: true);

        StringAssert.Contains(script, "attachShadow({ mode: 'closed' })");
        StringAssert.Contains(script, "admin.closeRequested");
        StringAssert.Contains(script, "vibetable.admin-return-position.v1");
        StringAssert.Contains(script, "if (true)");
    }
}
