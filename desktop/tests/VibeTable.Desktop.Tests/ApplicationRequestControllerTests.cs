using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ApplicationRequestControllerTests
{
    [TestMethod]
    public async Task PreferencesFlowPersistsStateAndAppliesOnlyNativeEffects()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-application-controller-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            var sink = new FakeWebReplySink();
            var host = new FakeApplicationRequestHost();
            using ApplicationRequestController controller = Controller(root, sink, host);

            await controller.DispatchAsync(Request(
                "appPreferences.update",
                """
                {
                  "minimizeToTrayOnClose": true,
                  "startWithWindows": true,
                  "updateProxy": "direct",
                  "customUpdateProxyUrl": ""
                }
                """));
            await controller.DispatchAsync(Request("appPreferences.get", "{}"));

            Assert.IsTrue(controller.CurrentPreferences.MinimizeToTrayOnClose);
            Assert.IsTrue(controller.CurrentPreferences.StartWithWindows);
            Assert.AreEqual(1, host.EnsureTrayIconCalls);
            Assert.HasCount(2, host.AppliedPreferences);
            Assert.HasCount(2, sink.Replies);
            Assert.IsTrue(
                ReadBoolean(sink.Replies[1].Payload, "minimizeToTrayOnClose"));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task InvalidApplicationRequestsFailBeforeProvidersOrLifecycleEffects()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-application-controller-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            var sink = new FakeWebReplySink();
            var host = new FakeApplicationRequestHost();
            using ApplicationRequestController controller = Controller(root, sink, host);

            await controller.DispatchAsync(Request("appPreferences.get", "[]"));
            await controller.DispatchAsync(Request("update.check", "[]"));
            await controller.DispatchAsync(Request("dailyQuote.fetch", "{}"));
            await controller.DispatchAsync(Request("application.raw", "{}"));

            CollectionAssert.AreEqual(
                new[]
                {
                    "APP_PREFERENCES_BAD_PAYLOAD",
                    "UPDATE_BAD_PAYLOAD",
                    "DAILY_QUOTE_BAD_PAYLOAD",
                    "UNKNOWN_TYPE",
                },
                sink.Replies.Select(FailureCode).ToArray());
            Assert.AreEqual(0, host.EnsureTrayIconCalls);
            Assert.AreEqual(0, host.RequestExitCalls);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public void HandlesOnlyTheClosedApplicationRequestUnion()
    {
        foreach (string type in new[]
        {
            "appPreferences.get",
            "appPreferences.update",
            "update.check",
            "update.install",
            "dailyQuote.fetch",
        })
        {
            Assert.IsTrue(ApplicationRequestController.Handles(type), type);
        }
        Assert.IsFalse(ApplicationRequestController.Handles("application.raw"));
        Assert.IsFalse(ApplicationRequestController.Handles("workspace.v2.request"));
    }

    [TestMethod]
    public async Task UnexpectedProviderFailuresReturnStableCodesAndEmitSafeDiagnostics()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-application-controller-" + Guid.NewGuid().ToString("N"));
        var sink = new FakeWebReplySink();
        var host = new FakeApplicationRequestHost();
        var preferences = new AppPreferencesService(
            new ThrowingAppPreferencesStore(),
            new InMemoryStartupRegistration());
        using var controller = new ApplicationRequestController(
            sink,
            host,
            preferences,
            new ReleaseUpdateCoordinator(root, "1.0.0", installationEnabled: false),
            new DailyQuoteHostClient(),
            AppPreferences.Default);

        await controller.DispatchAsync(Request("appPreferences.get", "{}"));
        await controller.DispatchAsync(Request(
            "appPreferences.update",
            """{"minimizeToTrayOnClose":false}"""));
        await controller.DispatchAsync(Request("update.check", "{}"));

        CollectionAssert.AreEqual(
            new[]
            {
                "APP_PREFERENCES_READ_FAILED",
                "APP_PREFERENCES_WRITE_FAILED",
                "UPDATE_CHECK_FAILED",
            },
            sink.Replies.Select(FailureCode).ToArray());
        CollectionAssert.AreEqual(
            new[]
            {
                "Application preferences read failed; exception=IOException",
                "Application preferences update failed; exception=IOException",
                "Release update check failed; exception=IOException",
            },
            host.Traces);
    }

    private static ApplicationRequestController Controller(
        string root,
        FakeWebReplySink sink,
        IApplicationRequestHost host)
    {
        var preferences = new AppPreferencesService(
            new JsonAppPreferencesStore(Path.Combine(root, "preferences.json")),
            new InMemoryStartupRegistration());
        return new ApplicationRequestController(
            sink,
            host,
            preferences,
            new ReleaseUpdateCoordinator(root, "1.0.0", installationEnabled: false),
            new DailyQuoteHostClient(),
            AppPreferences.Default);
    }

    private static RoutedWebRequest Request(string type, string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return new RoutedWebRequest(
            type,
            "request-" + type,
            document.RootElement.Clone(),
            string.Empty);
    }

    private static bool ReadBoolean(object? payload, string property)
        => JsonSerializer.SerializeToElement(payload)
            .GetProperty(property)
            .GetBoolean();

    private static string? FailureCode(FakeWebReplySink.Reply reply)
        => JsonSerializer.SerializeToElement(reply.Payload)
            .GetProperty("code")
            .GetString();

    private sealed class FakeApplicationRequestHost : IApplicationRequestHost
    {
        public List<AppPreferences> AppliedPreferences { get; } = [];
        public int EnsureTrayIconCalls { get; private set; }
        public int RequestExitCalls { get; private set; }
        public List<string> Traces { get; } = [];

        public void ApplyPreferences(AppPreferences preferences)
            => AppliedPreferences.Add(preferences);

        public void EnsureTrayIcon() => EnsureTrayIconCalls++;

        public void RequestExit() => RequestExitCalls++;

        public void Trace(string message) => Traces.Add(message);
    }

    private sealed class ThrowingAppPreferencesStore : IAppPreferencesStore
    {
        public PersistedAppPreferences Read() => throw new IOException("private path");

        public void Write(PersistedAppPreferences preferences)
            => throw new IOException("private path");
    }
}
