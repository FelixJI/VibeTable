using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class AppPreferencesUseCasesTests
{
    [TestMethod]
    public void GetReadsOnceAndAppliesBeforeProjection()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.Seed(new PersistedAppPreferences(true, "direct", null, "dark"));
        AppSettingsPreferences result = fixture.UseCases.Get();
        Assert.IsTrue(result.MinimizeToTrayOnClose);
        Assert.AreEqual("dark", result.EffectiveTheme);
        Assert.AreEqual("dark", fixture.UseCases.CurrentPreferences.Theme);
        CollectionAssert.AreEqual(new[] { "read", "startup.read", "apply", "project" }, fixture.Events);
    }

    [TestMethod]
    public void PartialUpdatePersistsAndReopensWithoutChangingOtherPreferences()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.Seed(new PersistedAppPreferences(false, "custom", "https://example.com", "light"));
        AppSettingsPreferences result = fixture.UseCases.Update(new AppSettingsPreferencesPatch(true, true));
        Assert.IsTrue(result.MinimizeToTrayOnClose);
        Assert.IsTrue(result.StartWithWindows);
        Assert.AreEqual("custom", result.UpdateProxy);
        Assert.AreEqual("https://example.com", result.CustomUpdateProxyUrl);
        Assert.AreEqual("light", result.Theme);
        CollectionAssert.AreEqual(new[]
        {
            "tray", "read", "startup.read", "startup:True", "write", "theme:light", "apply", "project",
        }, fixture.Events);

        fixture.Events.Clear();
        var reopened = new AppPreferencesUseCases(
            new AppPreferencesService(new JsonAppPreferencesStore(fixture.Path), fixture.Startup),
            fixture.Effects, AppPreferences.Default);
        Assert.AreEqual(result, reopened.Get());
        CollectionAssert.AreEqual(new[] { "startup.read", "apply", "project" }, fixture.Events);
    }

    [TestMethod]
    public void LegacyAdoptionIsCompareUnsetAndNeverOverwritesExplicitSystem()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.UseCases.Update(new AppSettingsPreferencesPatch(
            AdoptLegacyTheme: "system", HasAdoptLegacyTheme: true));
        CollectionAssert.AreEqual(new[]
        {
            "read", "startup.read", "write", "theme:system", "apply", "project",
        }, fixture.Events);
        fixture.Events.Clear();
        AppSettingsPreferences result = fixture.UseCases.Update(new AppSettingsPreferencesPatch(
            AdoptLegacyTheme: "dark", HasAdoptLegacyTheme: true));
        Assert.AreEqual("system", result.Theme);
        CollectionAssert.AreEqual(new[]
        {
            "read", "startup.read", "theme:system", "apply", "project",
        }, fixture.Events);
    }

    [TestMethod]
    public void RefreshCurrentOnlyRefreshesSharedStateWithoutApplyingNativeEffects()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.Seed(new PersistedAppPreferences(true, "direct", null, "dark"));
        AppPreferences result = fixture.UseCases.RefreshCurrent();
        Assert.AreSame(result, fixture.UseCases.CurrentPreferences);
        CollectionAssert.AreEqual(new[] { "read", "startup.read" }, fixture.Events);
    }

    [TestMethod]
    public void ReadFailureKeepsCurrentStateAndDoesNotApplyEffects()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.ThrowOnRead = true;
        Assert.ThrowsExactly<IOException>(() => fixture.UseCases.Get());
        Assert.AreEqual(AppPreferences.Default, fixture.UseCases.CurrentPreferences);
        CollectionAssert.AreEqual(new[] { "read" }, fixture.Events);
    }

    [TestMethod]
    public void StoreFailureRollsBackStartupAndDoesNotApplyOrPublishNewCurrent()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.ThrowOnWrite = true;
        Assert.ThrowsExactly<IOException>(() => fixture.UseCases.Update(
            new AppSettingsPreferencesPatch(true, true)));
        Assert.IsFalse(fixture.Startup.Enabled);
        Assert.AreEqual(AppPreferences.Default, fixture.UseCases.CurrentPreferences);
        CollectionAssert.AreEqual(new[]
        {
            "tray", "read", "startup.read", "startup:True", "write", "startup:False",
        }, fixture.Events);
    }

    [TestMethod]
    public void NativeFailureKeepsCommittedStoreAndCurrentWithoutReplaying()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Effects.ThrowOnApply = true;
        Assert.ThrowsExactly<InvalidOperationException>(() => fixture.UseCases.Update(
            new AppSettingsPreferencesPatch(Theme: "dark", HasTheme: true)));
        Assert.AreEqual("dark", fixture.UseCases.CurrentPreferences.Theme);
        Assert.AreEqual("dark", new JsonAppPreferencesStore(fixture.Path).Read().Theme);
        CollectionAssert.AreEqual(new[]
        {
            "read", "startup.read", "write", "theme:dark", "apply",
        }, fixture.Events);
    }

    [TestMethod]
    public void TrayFailureStopsBeforeStoreAndInvalidTypedThemeUsesExistingValidation()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Effects.ThrowOnTray = true;
        Assert.ThrowsExactly<InvalidOperationException>(() => fixture.UseCases.Update(
            new AppSettingsPreferencesPatch(true)));
        CollectionAssert.AreEqual(new[] { "tray" }, fixture.Events);
        fixture.Events.Clear();
        Assert.ThrowsExactly<ArgumentException>(() => fixture.UseCases.Update(
            new AppSettingsPreferencesPatch(Theme: "unknown", HasTheme: true)));
        CollectionAssert.AreEqual(new[] { "read", "startup.read" }, fixture.Events);
    }
}

internal sealed class AppPreferencesFixture : IDisposable
{
    private readonly string _root = System.IO.Path.Combine(
        System.IO.Path.GetTempPath(), "vibetable-preferences-usecase-" + Guid.NewGuid().ToString("N"));

    public AppPreferencesFixture()
    {
        Path = System.IO.Path.Combine(_root, "preferences.json");
        Store = new CountingStore(Path, Events);
        Startup = new CountingStartup(Events);
        Effects = new CountingEffects(Events);
        UseCases = new AppPreferencesUseCases(new AppPreferencesService(Store, Startup), Effects, AppPreferences.Default);
    }

    public string Path { get; }
    public List<string> Events { get; } = [];
    public CountingStore Store { get; }
    public CountingStartup Startup { get; }
    public CountingEffects Effects { get; }
    public AppPreferencesUseCases UseCases { get; }

    public void Dispose()
    {
        if (Directory.Exists(_root)) Directory.Delete(_root, recursive: true);
    }

    internal sealed class CountingStore(string path, List<string> events) : IAppPreferencesStore
    {
        private readonly JsonAppPreferencesStore _store = new(path);
        public bool ThrowOnRead { get; set; }
        public bool ThrowOnWrite { get; set; }
        public void Seed(PersistedAppPreferences preferences) => _store.Write(preferences);
        public PersistedAppPreferences Read()
        {
            events.Add("read");
            if (ThrowOnRead) throw new IOException("isolated store read");
            return _store.Read();
        }
        public void Write(PersistedAppPreferences preferences)
        {
            events.Add("write");
            if (ThrowOnWrite) throw new IOException("isolated store write");
            _store.Write(preferences);
        }
    }

    internal sealed class CountingStartup(List<string> events) : IStartupRegistration
    {
        public bool Enabled { get; private set; }
        public bool IsEnabled() { events.Add("startup.read"); return Enabled; }
        public void SetEnabled(bool enabled) { events.Add("startup:" + enabled); Enabled = enabled; }
    }

    internal sealed class CountingEffects(List<string> events) : IAppPreferencesEffects
    {
        private string? _theme;
        public bool ThrowOnApply { get; set; }
        public bool ThrowOnTray { get; set; }
        public void EnsureTrayIcon()
        {
            events.Add("tray");
            if (ThrowOnTray) throw new InvalidOperationException("isolated tray");
        }
        public void SetPreferredTheme(string? theme) { events.Add("theme:" + theme); _theme = theme; }
        public void ApplyPreferences(AppPreferences preferences)
        {
            events.Add("apply");
            if (ThrowOnApply) throw new InvalidOperationException("isolated native effect");
            _theme = preferences.Theme;
        }
        public HostThemeProjection Projection
        {
            get
            {
                events.Add("project");
                return new(_theme, AppThemePolicy.ResolveEffectiveTheme(_theme, new(false, false)), false);
            }
        }
    }
}
