using System.Net;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ApplicationRequestControllerTests
{
    [TestMethod]
    public async Task InvalidApplicationRequestsFailBeforeProvidersOrLifecycleEffects()
    {
        using var fixture = new AppPreferencesFixture();
        var sink = new FakeWebReplySink();
        var host = new FakeApplicationRequestHost();
        using var controller = Controller(fixture, sink, host);
        await controller.DispatchAsync(Request("update.check", "[]"));
        await controller.DispatchAsync(Request("dailyQuote.fetch", "{}"));
        await controller.DispatchAsync(Request("application.raw", "{}"));
        CollectionAssert.AreEqual(new[] { "UPDATE_BAD_PAYLOAD", "DAILY_QUOTE_BAD_PAYLOAD", "UNKNOWN_TYPE" },
            sink.Replies.Select(FailureCode).ToArray());
        Assert.IsEmpty(fixture.Events);
        Assert.AreEqual(0, host.RequestExitCalls);
    }

    [TestMethod]
    public void HandlesOnlyUpdatesAndQuotesAfterSettingsMovedToTheirOwnAdapter()
    {
        foreach (string type in new[] { "update.check", "update.install", "dailyQuote.fetch" })
            Assert.IsTrue(ApplicationRequestController.Handles(type), type);
        foreach (string type in new[] { "appPreferences.get", "appPreferences.update", "application.raw", "workspace.v2.request" })
            Assert.IsFalse(ApplicationRequestController.Handles(type), type);
    }

    [TestMethod]
    public async Task UpdateCheckReadFailureKeepsStableErrorAndSafeDiagnostics()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.ThrowOnRead = true;
        var sink = new FakeWebReplySink();
        var host = new FakeApplicationRequestHost();
        using var controller = Controller(fixture, sink, host);
        await controller.DispatchAsync(Request("update.check", "{}"));
        Assert.AreEqual("UPDATE_CHECK_FAILED", FailureCode(sink.Replies.Single()));
        CollectionAssert.AreEqual(new[] { "Release update check failed; exception=IOException" }, host.Traces);
        CollectionAssert.AreEqual(new[] { "read" }, fixture.Events);
    }

    [TestMethod]
    public async Task UpdateCheckRefreshesTheSameCurrentOwnerWithoutApplyingNativeEffects()
    {
        using var fixture = new AppPreferencesFixture();
        fixture.Store.Seed(new PersistedAppPreferences(true, "direct", null, "dark"));
        using var http = new HttpClient(new EmptyReleasesHandler());
        var updates = new ReleaseUpdateCoordinator(fixture.Path, "1.0.0",
            new GitHubReleaseUpdateClient(http), new ReleasePackageStager(), installationEnabled: false);
        var sink = new FakeWebReplySink();
        using var controller = Controller(fixture, sink, new FakeApplicationRequestHost(), updates);
        await controller.DispatchAsync(Request("update.check", "{}"));
        Assert.AreEqual("update.check", sink.Replies.Single().Type);
        Assert.AreSame(fixture.UseCases.CurrentPreferences, controller.CurrentPreferences);
        Assert.AreEqual("dark", controller.CurrentPreferences.Theme);
        CollectionAssert.AreEqual(new[] { "read", "startup.read" }, fixture.Events);
    }

    private static ApplicationRequestController Controller(AppPreferencesFixture fixture,
        IWebReplySink sink, IApplicationRequestHost host, ReleaseUpdateCoordinator? updates = null)
        => new(sink, host, fixture.UseCases,
            updates ?? new ReleaseUpdateCoordinator(fixture.Path, "1.0.0", installationEnabled: false),
            new DailyQuoteHostClient());

    private static RoutedWebRequest Request(string type, string raw)
    {
        using JsonDocument document = JsonDocument.Parse(raw);
        return new(type, "request-" + type, document.RootElement.Clone(), string.Empty);
    }

    private static string? FailureCode(FakeWebReplySink.Reply reply)
        => JsonSerializer.SerializeToElement(reply.Payload).GetProperty("code").GetString();

    private sealed class FakeApplicationRequestHost : IApplicationRequestHost
    {
        public int RequestExitCalls { get; private set; }
        public List<string> Traces { get; } = [];
        public void RequestExit() => RequestExitCalls++;
        public void Trace(string message) => Traces.Add(message);
    }

    private sealed class EmptyReleasesHandler : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
            => Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent("[]") });
    }
}
