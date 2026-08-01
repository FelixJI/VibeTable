using System.Text.Json;
using VibeTable.Desktop.Services;

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

        Assert.IsFalse(store.ReadMinimizeToTrayOnClose());

        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        File.WriteAllText(path, "not-json");
        Assert.IsFalse(store.ReadMinimizeToTrayOnClose());

        store.WriteMinimizeToTrayOnClose(true);
        Assert.IsTrue(store.ReadMinimizeToTrayOnClose());
        using JsonDocument document = JsonDocument.Parse(File.ReadAllText(path));
        Assert.IsTrue(
            document.RootElement.GetProperty("minimizeToTrayOnClose").GetBoolean());
    }

    [TestMethod]
    public void UpdatePersistsCloseBehaviorAndCoordinatesStartupRegistration()
    {
        var store = new FakeStore();
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        AppPreferences updated = service.Update(new AppPreferencesPatch(true, true));

        Assert.AreEqual(new AppPreferences(true, true), updated);
        Assert.IsTrue(store.Value);
        Assert.IsTrue(startup.Enabled);
        CollectionAssert.AreEqual(new[] { true }, startup.Changes);
    }

    [TestMethod]
    public void UpdateRollsBackStartupRegistrationWhenPreferenceWriteFails()
    {
        var store = new FakeStore { ThrowOnWrite = true };
        var startup = new FakeStartupRegistration();
        var service = new AppPreferencesService(store, startup);

        Assert.ThrowsExactly<IOException>(() =>
            service.Update(new AppPreferencesPatch(true, true)));

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
    public void AppPreferencesPatchRejectsUnknownEmptyAndNonBooleanPayloads()
    {
        Assert.IsFalse(ParsePatch("{}", out _));
        Assert.IsFalse(ParsePatch("{\"unknown\":true}", out _));
        Assert.IsFalse(ParsePatch("{\"startWithWindows\":\"yes\"}", out _));
        Assert.IsTrue(ParsePatch(
            "{\"minimizeToTrayOnClose\":true,\"startWithWindows\":false}",
            out AppPreferencesPatch? patch));
        Assert.AreEqual(new AppPreferencesPatch(true, false), patch);
    }

    private static bool ParsePatch(string json, out AppPreferencesPatch? patch)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return MainWindow.TryReadAppPreferencesPatch(document.RootElement, out patch);
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
        private bool _value;

        public bool InitialValue
        {
            init => _value = value;
        }
        public bool Value => _value;
        public bool ThrowOnWrite { get; init; }

        public bool ReadMinimizeToTrayOnClose() => _value;

        public void WriteMinimizeToTrayOnClose(bool value)
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
