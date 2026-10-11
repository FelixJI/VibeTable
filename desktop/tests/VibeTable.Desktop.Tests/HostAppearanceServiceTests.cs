using System.Windows;
using System.Windows.Media;
using System.Windows.Threading;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Contract tests for the Host-side appearance authority: system snapshot
/// tracking, preference/effective resolution (explicit wins, unconfigured is
/// distinct from explicit "system"), the Web projection shape, dispatcher
/// marshaling, and the Vt* theme dictionaries applied through standard WPF
/// dynamic resources. Real WM_SETTINGCHANGE/DPI behavior is manual
/// qualification, not asserted here.
/// </summary>
[TestClass]
public sealed class HostAppearanceServiceTests
{
    private sealed class FakeAppearanceSource : ISystemAppearanceSource
    {
        private HostAppearanceSnapshot _snapshot = new(SystemPrefersDark: false, HighContrast: false);

        public event EventHandler? Changed;

        public HostAppearanceSnapshot InitialSnapshot
        {
            init => _snapshot = value;
        }

        public int ReadCount { get; private set; }
        public bool Disposed { get; private set; }

        public HostAppearanceSnapshot Read()
        {
            ReadCount++;
            return _snapshot;
        }

        public void Simulate(HostAppearanceSnapshot snapshot)
        {
            _snapshot = snapshot;
            Changed?.Invoke(this, EventArgs.Empty);
        }

        public void Dispose() => Disposed = true;
    }

    [TestMethod]
    public void ConstructorReadsInitialSnapshotAndNormalizesUnknownPreference()
    {
        var source = new FakeAppearanceSource
        {
            InitialSnapshot = new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false),
        };
        using var service = new HostAppearanceService(source, preferredTheme: "sepia");

        Assert.AreEqual(new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false),
            service.Current);
        // Unknown values never become a persisted-looking preference.
        Assert.IsNull(service.PreferredTheme);
        Assert.AreEqual("dark", service.EffectiveTheme);
        HostThemeProjection projection = service.Projection;
        Assert.IsNull(projection.Preference);
        Assert.AreEqual("dark", projection.Effective);
        Assert.IsFalse(projection.HighContrast);
        Assert.AreEqual(1, source.ReadCount);
    }

    [TestMethod]
    public void ExplicitPreferenceWinsWhileUnconfiguredAndSystemFollowTheOs()
    {
        var source = new FakeAppearanceSource
        {
            InitialSnapshot = new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false),
        };
        using var service = new HostAppearanceService(source);

        // Unconfigured (null) follows the OS.
        Assert.AreEqual("dark", service.EffectiveTheme);
        Assert.IsNull(service.Projection.Preference);

        // Explicit light/dark overrides the OS; the projection carries the
        // explicit value so the Web layer can show the selected mode.
        service.SetPreferredTheme(AppThemeOptions.Light);
        Assert.AreEqual("light", service.EffectiveTheme);
        Assert.AreEqual(AppThemeOptions.Light, service.Projection.Preference);

        // Explicit "system" stays distinct from unconfigured while following
        // the OS: the projection must keep the "system" marker.
        service.SetPreferredTheme(AppThemeOptions.System);
        Assert.AreEqual("dark", service.EffectiveTheme);
        Assert.AreEqual(AppThemeOptions.System, service.Projection.Preference);

        // An unknown value resets to unconfigured, never to a fake default.
        service.SetPreferredTheme("unknown");
        Assert.IsNull(service.PreferredTheme);
        Assert.AreEqual("dark", service.EffectiveTheme);
    }

    [TestMethod]
    public void SystemChangesAreDeduplicatedIntoSingleProjectionEvents()
    {
        var source = new FakeAppearanceSource
        {
            InitialSnapshot = new HostAppearanceSnapshot(SystemPrefersDark: false, HighContrast: false),
        };
        using var service = new HostAppearanceService(source);
        var projections = new List<HostThemeProjection>();
        service.ProjectionChanged += projections.Add;

        // A notification without an actual snapshot change stays silent.
        source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: false, HighContrast: false));
        Assert.AreEqual(0, projections.Count);

        source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false));
        Assert.AreEqual(1, projections.Count);
        Assert.AreEqual("dark", projections[0].Effective);
        Assert.IsNull(projections[0].Preference);

        // High contrast rides the same projection.
        source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: true));
        Assert.AreEqual(2, projections.Count);
        Assert.IsTrue(projections[1].HighContrast);
        Assert.AreEqual("dark", projections[1].Effective);

        // A repeated SetPreferredTheme with the same value stays silent.
        service.SetPreferredTheme(AppThemeOptions.Dark);
        Assert.AreEqual(3, projections.Count);
        service.SetPreferredTheme(AppThemeOptions.Dark);
        Assert.AreEqual(3, projections.Count);
    }

    [TestMethod]
    public async Task SystemChangesAreMarshalledToTheSuppliedDispatcher()
    {
        var source = new FakeAppearanceSource
        {
            InitialSnapshot = new HostAppearanceSnapshot(SystemPrefersDark: false, HighContrast: false),
        };
        var ready = new ManualResetEventSlim(false);
        Dispatcher? dispatcher = null;
        var thread = new Thread(() =>
        {
            dispatcher = Dispatcher.CurrentDispatcher;
            ready.Set();
            Dispatcher.Run();
        });
        thread.SetApartmentState(ApartmentState.STA);
        thread.IsBackground = true;
        thread.Start();
        Assert.IsTrue(ready.Wait(TimeSpan.FromSeconds(30)), "dispatcher thread did not start");
        HostAppearanceService? service = null;
        var raised = new TaskCompletionSource<Thread>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        try
        {
            dispatcher!.Invoke(new Action(() =>
            {
                service = new HostAppearanceService(source, dispatcher: dispatcher);
                service.ProjectionChanged += _ => raised.TrySetResult(Thread.CurrentThread);
            }));

            // Raised from the test thread; the service must reconcile on the
            // supplied dispatcher instead of mutating shared state here.
            source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false));

            Thread raising = await raised.Task.WaitAsync(TimeSpan.FromSeconds(30));
            Assert.AreSame(thread, raising);
        }
        finally
        {
            service?.Dispose();
            dispatcher!.InvokeShutdown();
            Assert.IsTrue(thread.Join(TimeSpan.FromSeconds(10)));
        }
    }

    [TestMethod]
    public void ThemeResourcesResolveStandardKeysPerEffectiveTheme()
    {
        ResourceDictionary light = HostThemeResources.ForTheme("light");
        Assert.AreEqual(Colors.White, Brush(light, HostThemeResources.WindowBackgroundBrushKey));
        Assert.AreEqual(
            (Color)ColorConverter.ConvertFromString("#FF272E3B"),
            Brush(light, HostThemeResources.WindowTextBrushKey));

        ResourceDictionary dark = HostThemeResources.ForTheme("dark");
        Assert.AreEqual(
            (Color)ColorConverter.ConvertFromString("#FF17191F"),
            Brush(dark, HostThemeResources.WindowBackgroundBrushKey));
        Assert.AreEqual(
            (Color)ColorConverter.ConvertFromString("#FFE1E6ED"),
            Brush(dark, HostThemeResources.WindowTextBrushKey));
        Assert.AreEqual(
            (Color)ColorConverter.ConvertFromString("#FF2A2E37"),
            Brush(dark, HostThemeResources.SurfaceBorderBrushKey));
    }

    [TestMethod]
    public void ApplySwapsThemeDictionariesWithoutStackingAndClearsForHighContrast()
    {
        var target = new ResourceDictionary();

        HostThemeResources.Apply(target,
            new HostThemeProjection(null, "dark", HighContrast: false));
        Assert.AreEqual(1, ThemedMergedCount(target));
        Assert.AreEqual(
            (Color)ColorConverter.ConvertFromString("#FF17191F"),
            Brush(target, HostThemeResources.WindowBackgroundBrushKey));

        // A second apply swaps instead of stacking dictionaries.
        HostThemeResources.Apply(target,
            new HostThemeProjection(AppThemeOptions.Light, "light", HighContrast: false));
        Assert.AreEqual(1, ThemedMergedCount(target));
        Assert.AreEqual(Colors.White, Brush(target, HostThemeResources.WindowBackgroundBrushKey));

        // High contrast swaps in the SystemColors-backed fallback: every
        // Vt* key keeps a value derived from the system high-contrast palette.
        HostThemeResources.Apply(target,
            new HostThemeProjection(null, "dark", HighContrast: true));
        Assert.AreEqual(1, ThemedMergedCount(target));
        Assert.AreEqual(
            System.Windows.SystemColors.WindowBrush.Color,
            Brush(target, HostThemeResources.WindowBackgroundBrushKey));
        Assert.AreEqual(
            System.Windows.SystemColors.WindowTextBrush.Color,
            Brush(target, HostThemeResources.WindowTextBrushKey));

        HostThemeResources.Remove(target);
        Assert.AreEqual(0, ThemedMergedCount(target));
    }

    [TestMethod]
    public void HighContrastNotificationsReRaiseSoPaletteChangesRefresh()
    {
        var source = new FakeAppearanceSource
        {
            InitialSnapshot = new HostAppearanceSnapshot(SystemPrefersDark: false, HighContrast: true),
        };
        using var service = new HostAppearanceService(source);
        var projections = 0;
        service.ProjectionChanged += _ => projections++;

        // Same snapshot axes, but high contrast active: re-raise so the
        // SystemColors fallback brushes refresh after palette edits.
        source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: false, HighContrast: true));
        Assert.AreEqual(1, projections);

        // Outside high contrast, identical snapshots stay silent.
        source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false));
        Assert.AreEqual(2, projections);
        source.Simulate(new HostAppearanceSnapshot(SystemPrefersDark: true, HighContrast: false));
        Assert.AreEqual(2, projections);
    }

    [TestMethod]
    public void WindowsSourceReadsBothAxesAndDisposesCleanly()
    {
        // Smoke contract on the real source: reading never throws and the
        // snapshot axes resolve; actual OS transitions are manual
        // qualification, not unit assertions.
        using var source = new WindowsSystemAppearanceSource();
        HostAppearanceSnapshot snapshot = source.Read();
        Assert.IsTrue(snapshot.EffectiveBaseTheme is "light" or "dark");
    }

    private static Color Brush(ResourceDictionary dictionary, string key) =>
        ((SolidColorBrush)dictionary[key]).Color;

    private static int ThemedMergedCount(ResourceDictionary target) =>
        target.MergedDictionaries.Count(dictionary => dictionary.Contains(HostThemeResources.ThemeMarkerKey));
}
