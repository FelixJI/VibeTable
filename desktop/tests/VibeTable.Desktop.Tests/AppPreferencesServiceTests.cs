using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class AppPreferencesServiceTests
{
    private string? _root;

    [TestCleanup]
    public void Cleanup()
    {
        if (_root is not null && Directory.Exists(_root))
        {
            Directory.Delete(_root, recursive: true);
        }
    }

    [TestMethod]
    public void JsonStoreDefaultsToDisabledAndRecoversFromMalformedJson()
    {
        string path = PreferencesPath();
        var store = new JsonAppPreferencesStore(path);

        Assert.AreEqual(PersistedAppPreferences.Default, store.Read());

        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        File.WriteAllText(path, "not-json");
        Assert.AreEqual(PersistedAppPreferences.Default, store.Read());

        store.Write(new PersistedAppPreferences(
            true,
            AppSettingsUpdateProxyOption.GhproxyNet,
            "https://proxy.example.com/"));
        Assert.AreEqual(
            new PersistedAppPreferences(
                true,
                AppSettingsUpdateProxyOption.GhproxyNet,
                "https://proxy.example.com/"),
            store.Read());
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(path));
        Assert.IsTrue(
            document.RootElement.GetProperty("minimizeToTrayOnClose").GetBoolean());
        Assert.AreEqual(
            AppSettingsUpdateProxyOption.GhproxyNet,
            document.RootElement.GetProperty("updateProxy").GetString());
    }

    [TestMethod]
    public void UpdatePersistsCloseBehaviorAndCoordinatesStartupRegistration()
    {
        var store = new FakeStore();
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        AppPreferences updated = service.Update(new AppSettingsPreferencesPatch(true, true));

        Assert.AreEqual(new AppPreferences(true, true), updated);
        Assert.IsTrue(store.Value.MinimizeToTrayOnClose);
        Assert.IsTrue(startup.Enabled);
        CollectionAssert.AreEqual(new[] { true }, startup.Changes);
    }

    [TestMethod]
    public void UpdatePersistsProxySelectionWithoutChangingStartup()
    {
        var store = new FakeStore();
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        AppPreferences updated = service.Update(new AppSettingsPreferencesPatch(
            null,
            null,
            AppSettingsUpdateProxyOption.Custom,
            " https://proxy.example.com/base ",
            HasCustomUpdateProxyUrl: true));

        Assert.AreEqual(AppSettingsUpdateProxyOption.Custom, updated.UpdateProxy);
        Assert.AreEqual("https://proxy.example.com/base", updated.CustomUpdateProxyUrl);
        Assert.AreEqual(updated.MinimizeToTrayOnClose, store.Value.MinimizeToTrayOnClose);
        Assert.AreEqual(updated.UpdateProxy, store.Value.UpdateProxy);
        Assert.IsEmpty(startup.Changes);
    }

    [TestMethod]
    public void UpdateRollsBackStartupRegistrationWhenPreferenceWriteFails()
    {
        var store = new FakeStore { ThrowOnWrite = true };
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        Assert.ThrowsExactly<IOException>(() =>
            service.Update(new AppSettingsPreferencesPatch(true, true)));

        Assert.IsFalse(startup.Enabled);
        CollectionAssert.AreEqual(new[] { true, false }, startup.Changes);
    }

    [TestMethod]
    public void StartupReadKeepsTheApplicationAvailableWhenRegistryReadFails()
    {
        var store = new FakeStore { InitialValue = true };
        var startup = new FakeStartupRegistration { ThrowOnRead = true };
        var service = new AppPreferencesService(store, startup);

        AppPreferences preferences = service.ReadForStartup();

        Assert.AreEqual(new AppPreferences(true, false), preferences);
        Assert.ThrowsExactly<UnauthorizedAccessException>(() => service.Read());
    }

    [TestMethod]
    public void WindowClosePolicyMinimizesOnlyForOrdinaryCloseRequests()
    {
        Assert.IsTrue(WindowClosePolicy.ShouldMinimizeToTray(
            new AppPreferences(true, false),
            explicitExitRequested: false));
        Assert.IsFalse(WindowClosePolicy.ShouldMinimizeToTray(
            new AppPreferences(true, false),
            explicitExitRequested: true));
        Assert.IsFalse(WindowClosePolicy.ShouldMinimizeToTray(
            AppPreferences.Default,
            explicitExitRequested: false));
    }

    [TestMethod]
    public void StartupCommandQuotesTheExecutableAndUsesAStableArgument()
    {
        string executable = Path.Combine(
            Path.GetTempPath(),
            "Vibe Table",
            "VibeTable.Next.exe");

        string command = WindowsStartupRegistration.BuildCommand(executable);

        Assert.AreEqual($"\"{Path.GetFullPath(executable)}\" --autostart", command);
    }

    [TestMethod]
    public void IsStaleRunValueDetectsMismatchedAndMissingValues()
    {
        const string current = "\"C:\\Apps\\VibeTable\\VibeTable.Next.exe\" --autostart";

        Assert.IsFalse(WindowsStartupRegistration.IsStaleRunValue(current, current));
        Assert.IsFalse(WindowsStartupRegistration.IsStaleRunValue(current, null));
        Assert.IsFalse(WindowsStartupRegistration.IsStaleRunValue(current, ""));
        Assert.IsFalse(WindowsStartupRegistration.IsStaleRunValue(current, "   "));
        Assert.IsTrue(WindowsStartupRegistration.IsStaleRunValue(
            current,
            "\"C:\\Old\\VibeTable.Next.exe\" --autostart"));
    }

    [TestMethod]
    public void IsStaleRunValueIsCaseInsensitiveAndTrims()
    {
        const string current = "\"C:\\Apps\\VibeTable.Next.exe\" --autostart";

        // Same value with different case and surrounding whitespace is a match.
        Assert.IsFalse(WindowsStartupRegistration.IsStaleRunValue(
            current,
            "  \"c:\\apps\\VibeTable.Next.exe\" --autostart  "));
    }

    [TestMethod]
    public void StartupVisibilityPolicyHidesOnlyForAutoStartAndTray()
    {
        Assert.IsTrue(StartupVisibilityPolicy.ShouldStartHidden(
            autoStart: true,
            minimizeToTrayOnClose: true));
        Assert.IsFalse(StartupVisibilityPolicy.ShouldStartHidden(
            autoStart: false,
            minimizeToTrayOnClose: true));
        Assert.IsFalse(StartupVisibilityPolicy.ShouldStartHidden(
            autoStart: true,
            minimizeToTrayOnClose: false));
        Assert.IsFalse(StartupVisibilityPolicy.ShouldStartHidden(
            autoStart: false,
            minimizeToTrayOnClose: false));
    }

    [TestMethod]
    public void AppPreferencesPatchRejectsUnknownEmptyAndNonBooleanPayloads()
    {
        Assert.IsFalse(ParsePatch("{}", out _));
        Assert.IsFalse(ParsePatch("{\"unknown\":true}", out _));
        Assert.IsFalse(ParsePatch("{\"startWithWindows\":\"yes\"}", out _));
        Assert.IsTrue(ParsePatch(
            "{\"minimizeToTrayOnClose\":true,\"startWithWindows\":false}",
            out AppSettingsPreferencesPatch? patch));
        Assert.AreEqual(new AppSettingsPreferencesPatch(true, false), patch);
        Assert.IsTrue(ParsePatch(
            "{\"updateProxy\":\"custom\",\"customUpdateProxyUrl\":\"https://proxy.example/\"}",
            out patch));
        Assert.AreEqual(AppSettingsUpdateProxyOption.Custom, patch!.UpdateProxy);
        Assert.IsTrue(patch.HasCustomUpdateProxyUrl);
        Assert.IsFalse(ParsePatch("{\"updateProxy\":\"automatic\"}", out _));
        Assert.IsTrue(ParsePatch("{\"theme\":\"dark\"}", out patch));
        Assert.AreEqual(AppSettingsThemeOption.Dark, patch!.Theme);
        Assert.IsTrue(patch.HasTheme);
        Assert.IsFalse(patch.HasAdoptLegacyTheme);
        Assert.IsFalse(ParsePatch("{\"theme\":\"sepia\"}", out _));
        Assert.IsFalse(ParsePatch("{\"theme\":\"dark\",\"adoptLegacyTheme\":\"light\"}", out _));
        Assert.IsTrue(ParsePatch("{\"adoptLegacyTheme\":\"system\"}", out patch));
        Assert.AreEqual(AppSettingsThemeOption.System, patch!.AdoptLegacyTheme);
        Assert.IsTrue(patch.HasAdoptLegacyTheme);
        Assert.IsFalse(patch.HasTheme);
        Assert.IsFalse(ParsePatch("{\"adoptLegacyTheme\":\"nope\"}", out _));
    }

    [TestMethod]
    public void JsonStorePersistsThemeAndKeepsExplicitSystemDistinctFromUnconfigured()
    {
        string path = PreferencesPath();
        var store = new JsonAppPreferencesStore(path);

        // Absent theme (never configured) round-trips as null; an explicit
        // "system" choice stays a persisted, distinct value.
        Assert.IsNull(store.Read().Theme);
        store.Write(new PersistedAppPreferences(
            true,
            AppSettingsUpdateProxyOption.Direct,
            null,
            AppSettingsThemeOption.System));
        Assert.AreEqual(AppSettingsThemeOption.System, store.Read().Theme);
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(path));
        Assert.AreEqual(
            AppSettingsThemeOption.System,
            document.RootElement.GetProperty("theme").GetString());

        store.Write(new PersistedAppPreferences(true, AppSettingsUpdateProxyOption.Direct, null, null));
        Assert.IsNull(store.Read().Theme);
    }

    [TestMethod]
    public void JsonStoreFallsBackToUnconfiguredForUnknownThemeWhileKeepingOtherPreferences()
    {
        string path = PreferencesPath();
        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        File.WriteAllText(path, """
            {
              "minimizeToTrayOnClose": true,
              "updateProxy": "ghproxyNet",
              "theme": "sepia"
            }
            """);
        var store = new JsonAppPreferencesStore(path);

        PersistedAppPreferences stored = store.Read();
        Assert.IsNull(stored.Theme);
        Assert.IsTrue(stored.MinimizeToTrayOnClose);
        Assert.AreEqual(AppSettingsUpdateProxyOption.GhproxyNet, stored.UpdateProxy);
    }

    [TestMethod]
    public void UpdatePersistsThemeWithoutTouchingStartupOrOtherPreferences()
    {
        var store = new FakeStore();
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        AppPreferences updated = service.Update(new AppSettingsPreferencesPatch(
            null,
            null,
            Theme: AppSettingsThemeOption.Dark,
            HasTheme: true));

        Assert.AreEqual(AppSettingsThemeOption.Dark, updated.Theme);
        Assert.AreEqual(AppSettingsThemeOption.Dark, store.Value.Theme);
        Assert.IsEmpty(startup.Changes);

        // A patch without HasTheme never clears a configured theme.
        updated = service.Update(new AppSettingsPreferencesPatch(null, null));
        Assert.AreEqual(AppSettingsThemeOption.Dark, updated.Theme);

        // Unknown themes are rejected before any write lands.
        Assert.ThrowsExactly<ArgumentException>(() =>
            service.Update(new AppSettingsPreferencesPatch(
                null,
                null,
                Theme: "sepia",
                HasTheme: true)));
        Assert.AreEqual(AppSettingsThemeOption.Dark, store.Value.Theme);
        Assert.IsFalse(store.Value.MinimizeToTrayOnClose);
    }

    [TestMethod]
    public void AdoptLegacyThemePersistsOnlyWhileUnconfigured()
    {
        var store = new FakeStore();
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        AppPreferences adopted = service.AdoptLegacyTheme(AppSettingsThemeOption.Dark);
        Assert.AreEqual(AppSettingsThemeOption.Dark, adopted.Theme);
        Assert.AreEqual(AppSettingsThemeOption.Dark, store.Value.Theme);
        Assert.IsEmpty(startup.Changes);

        // A later adoption attempt never overwrites the now-explicit value,
        // including an explicit "system".
        AppPreferences kept = service.AdoptLegacyTheme(AppSettingsThemeOption.Light);
        Assert.AreEqual(AppSettingsThemeOption.Dark, kept.Theme);
        Assert.AreEqual(AppSettingsThemeOption.Dark, store.Value.Theme);

        Assert.ThrowsExactly<ArgumentException>(() => service.AdoptLegacyTheme("sepia"));
    }

    private static bool ParsePatch(string json, out AppSettingsPreferencesPatch? patch)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return AppSettingsPatchReader.TryRead(
            document.RootElement,
            out patch);
    }

    private string PreferencesPath()
    {
        _root ??= Path.Combine(
            Path.GetTempPath(),
            "vibetable-app-preferences-" + Guid.NewGuid().ToString("N"));
        return Path.Combine(_root, "app-preferences.json");
    }

    private sealed class FakeStore : IAppPreferencesStore
    {
        private PersistedAppPreferences _value = PersistedAppPreferences.Default;

        public bool InitialValue
        {
            init => _value = _value with { MinimizeToTrayOnClose = value };
        }
        public PersistedAppPreferences Value => _value;
        public bool ThrowOnWrite { get; init; }

        public PersistedAppPreferences Read() => _value;

        public void Write(PersistedAppPreferences value)
        {
            if (ThrowOnWrite) throw new IOException("simulated write failure");
            _value = value;
        }
    }

    private sealed class FakeStartupRegistration : IStartupRegistration
    {
        public bool Enabled { get; private set; }
        public List<bool> Changes { get; } = [];
        public bool ThrowOnRead { get; init; }

        public bool IsEnabled() => ThrowOnRead
            ? throw new UnauthorizedAccessException("simulated registry denial")
            : Enabled;

        public void SetEnabled(bool enabled)
        {
            Enabled = enabled;
            Changes.Add(enabled);
        }
    }
}
