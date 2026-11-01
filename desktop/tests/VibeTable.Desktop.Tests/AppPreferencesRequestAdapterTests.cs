using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class AppPreferencesRequestAdapterTests
{
    [TestMethod]
    [DataRow("appPreferences.get", "{}")]
    [DataRow("appPreferences.update", "{\"minimizeToTrayOnClose\":true,\"startWithWindows\":true,\"theme\":\"dark\"}")]
    [DataRow("appPreferences.update", "{\"adoptLegacyTheme\":\"system\"}")]
    public void RealAdapterMatchesTypedBusinessResultAndEffects(string type, string raw)
    {
        using var direct = new AppPreferencesFixture();
        using var adapted = new AppPreferencesFixture();
        AppSettingsPreferences expected;
        if (type == "appPreferences.get") expected = direct.UseCases.Get();
        else
        {
            using JsonDocument payload = JsonDocument.Parse(raw);
            Assert.IsTrue(AppSettingsPatchReader.TryRead(payload.RootElement, out var patch));
            expected = direct.UseCases.Update(patch!);
        }
        var sink = new RecordingSink(adapted.Events);
        var traces = new List<string>();
        var adapter = new AppPreferencesRequestAdapter(sink, adapted.UseCases, traces.Add);
        var router = new WebMessageRouter(adapter.Dispatch) { IsReady = true };
        Assert.IsNull(router.Route("{\"type\":\"" + type + "\",\"requestId\":\"correlated\",\"payload\":" + raw + "}"));

        FakeWebReplySink.Reply reply = sink.Replies.Replies.Single();
        Assert.AreEqual(type, reply.Type);
        Assert.AreEqual("correlated", reply.RequestId);
        Assert.AreEqual(expected, reply.Payload);
        Assert.AreEqual(direct.UseCases.CurrentPreferences, adapted.UseCases.CurrentPreferences);
        CollectionAssert.AreEqual(direct.Events.Append("reply").ToArray(), adapted.Events);
        Assert.IsEmpty(traces);
    }

    [TestMethod]
    [DataRow("appPreferences.get", "[]")]
    [DataRow("appPreferences.get", "{\"unexpected\":true}")]
    [DataRow("appPreferences.update", "{\"theme\":null}")]
    [DataRow("appPreferences.update", "{\"theme\":\"dark\",\"theme\":\"light\"}")]
    [DataRow("appPreferences.update", "{\"adoptLegacyTheme\":\"dark\",\"minimizeToTrayOnClose\":true}")]
    public void InvalidPayloadHasZeroBusinessEffects(string type, string raw)
    {
        using var fixture = new AppPreferencesFixture();
        var sink = new RecordingSink(fixture.Events);
        var adapter = new AppPreferencesRequestAdapter(sink, fixture.UseCases, _ => Assert.Fail("Invalid payload is not an unexpected failure."));
        adapter.Dispatch(Request(type, raw));
        Assert.AreEqual(AppSettingsErrorCodes.BadPayload, FailureCode(sink));
        CollectionAssert.AreEqual(new[] { "reply" }, fixture.Events);
        Assert.AreEqual(AppPreferences.Default, fixture.UseCases.CurrentPreferences);
    }

    [TestMethod]
    [DataRow("read", "appPreferences.get", "APP_PREFERENCES_READ_FAILED")]
    [DataRow("write", "appPreferences.update", "APP_PREFERENCES_WRITE_FAILED")]
    [DataRow("native", "appPreferences.update", "APP_PREFERENCES_WRITE_FAILED")]
    public void FailureMappingPreservesTypedOutcomeWithoutReexecution(string failure, string type, string code)
    {
        using var direct = new AppPreferencesFixture();
        using var adapted = new AppPreferencesFixture();
        ConfigureFailure(direct, failure);
        ConfigureFailure(adapted, failure);
        var patch = new AppSettingsPreferencesPatch(Theme: "dark", HasTheme: true);
        if (failure == "read") Assert.ThrowsExactly<IOException>(() => direct.UseCases.Get());
        else if (failure == "write") Assert.ThrowsExactly<IOException>(() => direct.UseCases.Update(patch));
        else Assert.ThrowsExactly<InvalidOperationException>(() => direct.UseCases.Update(patch));
        var sink = new RecordingSink(adapted.Events);
        var traces = new List<string>();
        new AppPreferencesRequestAdapter(sink, adapted.UseCases, traces.Add)
            .Dispatch(Request(type, type == "appPreferences.get" ? "{}" : "{\"theme\":\"dark\"}"));
        Assert.AreEqual(code, FailureCode(sink));
        Assert.HasCount(1, sink.Replies.Replies);
        Assert.HasCount(1, traces);
        Assert.IsFalse(traces[0].Contains("isolated", StringComparison.Ordinal));
        Assert.AreEqual(direct.UseCases.CurrentPreferences, adapted.UseCases.CurrentPreferences);
        CollectionAssert.AreEqual(direct.Events.Append("reply").ToArray(), adapted.Events);
        Assert.AreEqual(new JsonAppPreferencesStore(direct.Path).Read(), new JsonAppPreferencesStore(adapted.Path).Read());
    }

    [TestMethod]
    public void UnknownMethodCannotInvokeAnyBusinessOrFallback()
    {
        using var fixture = new AppPreferencesFixture();
        var sink = new RecordingSink(fixture.Events);
        var adapter = new AppPreferencesRequestAdapter(sink, fixture.UseCases, _ => Assert.Fail());
        Assert.IsFalse(AppPreferencesRequestAdapter.Handles("update.check"));
        Assert.IsFalse(AppPreferencesRequestAdapter.Handles("appPreferences.raw"));
        adapter.Dispatch(Request("appPreferences.raw", "{}"));
        Assert.AreEqual("UNKNOWN_TYPE", FailureCode(sink));
        CollectionAssert.AreEqual(new[] { "reply" }, fixture.Events);
    }

    [TestMethod]
    [DataRow("{}")]
    [DataRow("[]")]
    public void ThrowingSinkIsObservedWithoutSecondTerminalAttempt(string raw)
    {
        using var fixture = new AppPreferencesFixture();
        var sink = new RecordingSink(fixture.Events) { ThrowOnReply = true };
        var traces = new List<string>();
        new AppPreferencesRequestAdapter(sink, fixture.UseCases, traces.Add)
            .Dispatch(Request("appPreferences.get", raw));
        Assert.AreEqual(1, sink.Attempts);
        Assert.IsEmpty(sink.Replies.Replies);
        CollectionAssert.AreEqual(new[] { "Application preferences reply failed; exception=IOException" }, traces);
    }

    [TestMethod]
    public void LegacyAdoptionRejectsMixedPatchesInEitherPropertyOrder()
    {
        foreach (string other in new[]
        {
            "\"minimizeToTrayOnClose\":true", "\"startWithWindows\":true",
            "\"updateProxy\":\"direct\"", "\"customUpdateProxyUrl\":\"\"", "\"theme\":\"light\"",
        })
        {
            foreach (string raw in new[]
            {
                "{\"adoptLegacyTheme\":\"dark\"," + other + "}",
                "{" + other + ",\"adoptLegacyTheme\":\"dark\"}",
            })
            {
                using var fixture = new AppPreferencesFixture();
                var sink = new RecordingSink(fixture.Events);
                new AppPreferencesRequestAdapter(sink, fixture.UseCases, _ => Assert.Fail())
                    .Dispatch(Request("appPreferences.update", raw));
                Assert.AreEqual(AppSettingsErrorCodes.BadPayload, FailureCode(sink), raw);
                CollectionAssert.AreEqual(new[] { "reply" }, fixture.Events, raw);
            }
        }
    }

    [TestMethod]
    public void ProductionEffectsPushAppearanceBeforeNativeApplyAndReplyWithoutDuplicatePush()
    {
        using var fixture = new AppPreferencesFixture();
        using var appearance = new HostAppearanceService(new FixedAppearanceSource());
        appearance.ProjectionChanged += _ => fixture.Events.Add("appearance.changed");
        var effects = new AppPreferencesEffects(appearance,
            preferences =>
            {
                fixture.Events.Add("native");
                appearance.SetPreferredTheme(preferences.Theme);
            },
            () => fixture.Events.Add("tray"));
        var preferences = new AppPreferencesUseCases(
            new AppPreferencesService(fixture.Store, fixture.Startup, new InMemoryPortableModeStore()), effects, AppPreferences.Default);
        var sink = new RecordingSink(fixture.Events);
        var adapter = new AppPreferencesRequestAdapter(sink, preferences, _ => Assert.Fail());
        adapter.Dispatch(Request("appPreferences.update", "{\"theme\":\"dark\",\"minimizeToTrayOnClose\":true}"));
        CollectionAssert.AreEqual(new[]
        {
            "tray", "read", "startup.read", "write", "appearance.changed", "native", "reply",
        }, fixture.Events);
        Assert.AreEqual("dark", ((AppSettingsPreferences)sink.Replies.Replies.Single().Payload!).EffectiveTheme);
        fixture.Events.Clear();
        adapter.Dispatch(Request("appPreferences.get", "{}"));
        CollectionAssert.AreEqual(new[] { "read", "startup.read", "native", "reply" }, fixture.Events);
    }

    private sealed class FixedAppearanceSource : ISystemAppearanceSource
    {
        public HostAppearanceSnapshot Read() => new(false, false);
        public event EventHandler? Changed { add { } remove { } }
        public void Dispose() { }
    }
    private static void ConfigureFailure(AppPreferencesFixture fixture, string failure)
    {
        fixture.Store.ThrowOnRead = failure == "read";
        fixture.Store.ThrowOnWrite = failure == "write";
        fixture.Effects.ThrowOnApply = failure == "native";
    }

    private static RoutedWebRequest Request(string type, string raw)
    {
        using JsonDocument document = JsonDocument.Parse(raw);
        return new(type, "request-" + type, document.RootElement.Clone(), string.Empty);
    }

    private static string? FailureCode(RecordingSink sink) => JsonSerializer
        .SerializeToElement(sink.Replies.Replies.Single().Payload).GetProperty("code").GetString();

    private sealed class RecordingSink(List<string> events) : IWebReplySink
    {
        public FakeWebReplySink Replies { get; } = new();
        public bool ThrowOnReply { get; init; }
        public int Attempts { get; private set; }
        public void PostNotification(string type, object? payload) => Replies.PostNotification(type, payload);
        public void PostResponse(string type, string? requestId, object? payload)
        {
            Attempt();
            Replies.PostResponse(type, requestId, payload);
        }
        public void PostOperationFailed(string? requestId, string message, string? code = null,
            string? operation = null, string? operationId = null)
        {
            Attempt();
            Replies.PostOperationFailed(requestId, message, code, operation, operationId);
        }
        private void Attempt()
        {
            Attempts++;
            events.Add("reply");
            if (ThrowOnReply) throw new IOException("isolated reply sink");
        }
    }
}
