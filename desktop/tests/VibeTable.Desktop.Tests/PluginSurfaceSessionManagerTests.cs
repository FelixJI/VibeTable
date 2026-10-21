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
    public void RetiredCloseAcknowledgementDoesNotAuthorizeAnyOtherEventOrToken()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var old = manager.Open("workspace-a", "same-plugin", revision, "ui/index.html");
        var replacement = manager.Open("workspace-b", "same-plugin", revision, "ui/index.html");
        var close = new PluginSurfaceEvent(PluginContractVersions.Surface, old.SurfaceToken,
            PluginSurfaceEvents.Close, JsonSerializer.SerializeToElement(new { }));
        Assert.IsFalse(manager.TryAcknowledgeRetiredClose(close), "Active tokens are not retired acknowledgements.");
        Assert.IsTrue(manager.Close(old.SurfaceToken));

        Assert.IsTrue(manager.TryAcknowledgeRetiredClose(close));
        Assert.IsFalse(manager.TryAccept(close, out _), "Retired acknowledgement does not authenticate an active session.");
        foreach (var rejected in new[]
        {
            close with { Event = PluginSurfaceEvents.Ready },
            close with { Event = PluginSurfaceEvents.Action },
            close with { Contract = "vibetable.plugin-surface.v0" },
            close with { SurfaceToken = new string('f', 64) },
        })
        {
            Assert.IsFalse(manager.TryAcknowledgeRetiredClose(rejected));
            Assert.IsFalse(manager.TryAccept(rejected, out _));
        }
        Assert.IsTrue(manager.IsActive(replacement.SurfaceToken));
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public void ConcurrentNativeAndRendererClosePublishCleanupAndAcceptedEventAtMostOnce(bool nativeClose)
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var surface = manager.Open(revision, "ui/index.html");
        var replacement = manager.Open(revision, "ui/replacement.html");
        var close = new PluginSurfaceEvent(PluginContractVersions.Surface, surface.SurfaceToken,
            PluginSurfaceEvents.Close, JsonSerializer.SerializeToElement(new { }));
        int cleanups = 0, accepted = 0;
        manager.Closed += _ =>
        {
            Interlocked.Increment(ref cleanups);
            Assert.IsTrue(Task.Run(() => manager.TryAcknowledgeRetiredClose(close)).Wait(TimeSpan.FromSeconds(2)),
                "Cleanup callbacks must run outside the manager lock.");
        };
        Parallel.For(0, 16, index =>
        {
            if (nativeClose && index % 2 == 0) manager.Close(surface.SurfaceToken);
            else if (manager.TryAccept(close, out _)) Interlocked.Increment(ref accepted);
        });

        Assert.AreEqual(1, cleanups);
        if (nativeClose) Assert.IsTrue(accepted <= 1);
        else Assert.AreEqual(1, accepted);
        Assert.IsTrue(manager.TryAcknowledgeRetiredClose(close));
        Assert.IsFalse(manager.Close(surface.SurfaceToken));
        Assert.IsTrue(manager.IsActive(replacement.SurfaceToken));
    }

    [TestMethod]
    public void RetiredCloseAcknowledgementsEvictTheOldestTokenAtTheirFixedBound()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var first = manager.Open(revision, "ui/index.html");
        var close = new PluginSurfaceEvent(PluginContractVersions.Surface, first.SurfaceToken,
            PluginSurfaceEvents.Close, JsonSerializer.SerializeToElement(new { }));
        manager.Close(first.SurfaceToken);
        for (int index = 0; index < PluginSurfaceSessionManager.RetiredCloseLimit - 1; index++)
        {
            var next = manager.Open(revision, "ui/index.html");
            manager.Close(next.SurfaceToken);
        }
        Assert.IsTrue(manager.TryAcknowledgeRetiredClose(close));
        var newest = manager.Open(revision, "ui/index.html");
        manager.Close(newest.SurfaceToken);
        Assert.IsFalse(manager.TryAcknowledgeRetiredClose(close));
        Assert.IsTrue(manager.TryAcknowledgeRetiredClose(close with { SurfaceToken = newest.SurfaceToken }));
        Assert.IsFalse(manager.IsActive(first.SurfaceToken));
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
