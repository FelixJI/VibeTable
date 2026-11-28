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

    [TestMethod]
    public void BackgroundSurfacesAreBoundedPromoteOnceAndOnlyCountForegroundMounts()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var context = new PluginRuntimeCommandContext("vibetable.command-context.v1", "local:ws", "orders",
            [], null, "zh-CN", "light", "comfortable", default, "1.0");
        PluginSurfaceSession OpenBackground(string actionId) => manager.Open("local:ws", "plugin", revision, "ui/index.html", phase: PluginSurfacePhases.Background);
        var prewarmed = new List<PluginSurfaceSession>();
        for (int index = 0; index < PluginSurfaceSessionManager.MaxBackgroundSurfaces; index++)
        {
            var surface = OpenBackground($"app-{index}");
            manager.BindIdentity(surface.SurfaceToken, new PluginProjectContext("local:ws", "workspace:7", 7), 1, "3.x", actionId: $"app-{index}", presentation: "application");
            manager.TryGet(surface.SurfaceToken, out var bound);
            prewarmed.Add(bound!);
        }
        var overflow = OpenBackground("app-overflow");
        var legacy = manager.Open("local:ws", "legacy", revision, "ui/index.html");

        Assert.IsTrue(prewarmed.All(surface => surface.Phase == PluginSurfacePhases.Background));
        Assert.AreEqual(PluginSurfacePhases.Foreground, overflow.Phase, "Beyond the bound a surface opens foreground, never as a hidden writer.");
        Assert.AreEqual(PluginSurfacePhases.Foreground, legacy.Phase, "Undeclared surfaces keep the foreground default.");
        Assert.AreEqual(PluginSurfaceSessionManager.MaxBackgroundSurfaces, manager.BackgroundSurfaceCount);

        // A mounted pre-warmed frame is not an open application: it carries no foreground authority.
        Assert.IsTrue(manager.MarkMounted(prewarmed[0].SurfaceToken));
        Assert.IsTrue(manager.MarkMounted(legacy.SurfaceToken));
        Assert.AreEqual(2, manager.MountedSurfaceCount);
        Assert.AreEqual(1, manager.MountedForegroundSurfaceCount);

        int changed = 0;
        manager.Changed += () => changed++;
        manager.BindActiveCollection("local:ws", "plugin", "app-0", context);
        Assert.IsTrue(manager.TryGet(prewarmed[0].SurfaceToken, out var promoted));
        Assert.AreEqual(PluginSurfacePhases.Foreground, promoted!.Phase);
        Assert.AreEqual("orders", promoted.ActiveCollection);
        Assert.AreEqual(1, changed, "Promotion republishes which mounted frames carry authority.");
        Assert.AreEqual(2, manager.MountedForegroundSurfaceCount);
        Assert.IsTrue(manager.TryGet(prewarmed[1].SurfaceToken, out var untouched));
        Assert.AreEqual(PluginSurfacePhases.Background, untouched!.Phase, "Binding one action leaves its siblings pre-warmed.");

        manager.BindActiveCollection("local:ws", "plugin", "app-0", context with { Collection = "notes" });
        Assert.AreEqual(PluginSurfacePhases.Foreground, promoted.Phase, "There is no demotion.");
        Assert.AreEqual(1, changed, "A plain re-bind does not republish.");

        // Retiring a pre-warmed surface frees its slot for the next declared application.
        Assert.IsTrue(manager.Close(prewarmed[1].SurfaceToken));
        var again = OpenBackground("app-again");
        Assert.AreEqual(PluginSurfacePhases.Background, again.Phase);
        Assert.AreEqual(PluginSurfaceSessionManager.MaxBackgroundSurfaces - 1, manager.BackgroundSurfaceCount, "The promoted surface keeps its foreground slot; only a retired one frees a background slot.");
    }

    [TestMethod]
    public void MountedSurfacesRetireBeforeChangedAndIgnoreLateMounts()
    {
        var manager = new PluginSurfaceSessionManager();
        var revision = PluginPackageRevision.Create(@"C:\plugins\clean", PackageHash);
        var first = manager.Open(revision, "ui/first.html");
        var second = manager.Open(revision, "ui/second.html");
        var unused = manager.Open(revision, "ui/unused.html");
        Assert.AreEqual(0, manager.MountedSurfaceCount);
        Assert.IsTrue(manager.MarkMounted(first.SurfaceToken));
        Assert.IsTrue(manager.MarkMounted(second.SurfaceToken));
        Assert.AreEqual(2, manager.MountedSurfaceCount);
        manager.Changed += () => Assert.AreEqual(1, manager.MountedSurfaceCount);

        Assert.IsTrue(manager.TryAccept(new PluginSurfaceEvent(PluginContractVersions.Surface,
            first.SurfaceToken, PluginSurfaceEvents.Close, JsonSerializer.SerializeToElement(new { })), out _));
        Assert.IsFalse(manager.MarkMounted(first.SurfaceToken));
        Assert.IsTrue(manager.IsActive(unused.SurfaceToken), "Unused catalog capabilities remain authorized.");
    }
}
