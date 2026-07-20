using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginSurfaceSessionManagerTests
{
    private const string PackageHash =
        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

    [TestMethod]
    public void SurfaceAcceptsOnlyVersionedEventsForActiveTokenAndCloseRevokesIt()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var surface = manager.Open(revision, "ui/index.html");
        var ready = new PluginSurfaceEvent(
            PluginContractVersions.Surface,
            surface.SurfaceToken,
            PluginSurfaceEvents.Ready,
            JsonDocument.Parse("{}").RootElement.Clone());

        Assert.IsTrue(manager.TryAccept(ready, out var accepted));
        Assert.AreEqual(surface.SurfaceToken, accepted!.SurfaceToken);
        Assert.AreEqual("allow-scripts allow-same-origin", surface.IframeSandbox);

        Assert.IsTrue(manager.Close(surface.SurfaceToken));
        Assert.IsFalse(manager.TryAccept(ready, out _));
        Assert.IsFalse(manager.IsActive(surface.SurfaceToken));
    }

    [TestMethod]
    public void ThemeProjectionRequiresStableThemeLocaleDensityContract()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var surface = manager.Open(revision, "ui/index.html");
        var state = new PluginSurfaceThemeSnapshot(
            PluginContractVersions.Theme,
            PluginThemeModes.Light,
            "en-US",
            PluginDensityModes.Comfortable,
            new Dictionary<string, string>
            {
                ["--vt-plugin-bg"] = "#ffffff",
                ["--vt-plugin-surface"] = "#ffffff",
                ["--vt-plugin-text"] = "#111111",
                ["--vt-plugin-text-muted"] = "#666666",
                ["--vt-plugin-border"] = "#dddddd",
                ["--vt-plugin-primary"] = "#3366ff",
                ["--vt-plugin-danger"] = "#cc2222",
                ["--vt-plugin-radius"] = "6px",
                ["--vt-plugin-space-unit"] = "4px",
            });

        var message = manager.UpdateTheme(surface.SurfaceToken, state);

        Assert.AreEqual(PluginSurfaceMessages.ThemeChanged, message.Event);
        Assert.AreEqual(surface.SurfaceToken, message.SurfaceToken);
        Assert.Throws<PluginSurfacePolicyException>(() => manager.UpdateTheme(
            surface.SurfaceToken,
            state with { Locale = "fr-FR" }));
    }

    [TestMethod]
    public void RendererGenerationBoundaryRevokesEverySurfaceToken()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var first = manager.Open(revision, "ui/first.html");
        var second = manager.Open(revision, "ui/second.html");

        manager.CloseAll();

        Assert.IsFalse(manager.IsActive(first.SurfaceToken));
        Assert.IsFalse(manager.IsActive(second.SurfaceToken));
    }
}
