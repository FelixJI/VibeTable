using System.Reflection;
using System.Text.Json;
using System.Text.Json.Nodes;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// App-settings wire conformance (#534): the shared raw-JSON fixture in
/// contracts/v2/fixtures/app-settings-conformance.json must reproduce its
/// verdicts through the REAL router + application adapter chain. Duplicate
/// payload keys stay raw strings so the rejection happens before any
/// JsonElement information loss; invalid inputs must never reach the store,
/// startup registration or native lifecycle effects, and must not change the
/// current business preferences.
/// </summary>
[TestClass]
public sealed class AppSettingsContractConformanceTests
{
    private sealed class CountingStore : IAppPreferencesStore
    {
        private readonly JsonAppPreferencesStore _inner;
        public int Reads;
        public int Writes;

        public CountingStore(string path) => _inner = new JsonAppPreferencesStore(path);

        public PersistedAppPreferences Read()
        {
            Reads++;
            return _inner.Read();
        }

        public void Write(PersistedAppPreferences preferences)
        {
            Writes++;
            _inner.Write(preferences);
        }
    }

    private sealed class FakeAppearanceSource : ISystemAppearanceSource
    {
        public HostAppearanceSnapshot Read() =>
            new(SystemPrefersDark: false, HighContrast: false);

        public event EventHandler? Changed { add { } remove { } }

        public void Dispose() { }
    }

    private sealed class CountingApplicationRequestHost : IApplicationRequestHost
    {
        public int ApplyPreferencesCalls { get; private set; }
        public int EnsureTrayIconCalls { get; private set; }
        public int RequestExitCalls { get; private set; }
        public int TraceCalls { get; private set; }

        public void ApplyPreferences(AppPreferences preferences) => ApplyPreferencesCalls++;

        public void EnsureTrayIcon() => EnsureTrayIconCalls++;

        public void RequestExit() => RequestExitCalls++;

        public void Trace(string message) => TraceCalls++;
    }

    private sealed class Context : IDisposable
    {
        public string Root;
        public CountingStore Store;
        public FakeWebReplySink Sink = new();
        public CountingApplicationRequestHost Host = new();
        public HostAppearanceService Appearance;
        public ApplicationRequestController Controller;

        public Context(string id)
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-appsettings-" + id + "-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Store = new CountingStore(Path.Combine(Root, "preferences.json"));
            Appearance = new HostAppearanceService(new FakeAppearanceSource());
            Controller = new ApplicationRequestController(
                Sink,
                Host,
                new AppPreferencesService(Store, new InMemoryStartupRegistration()),
                new ReleaseUpdateCoordinator(Root, "1.0.0", installationEnabled: false),
                new DailyQuoteHostClient(),
                Appearance,
                AppPreferences.Default);
        }

        public void Dispose()
        {
            Controller.Dispose();
            Appearance.Dispose();
            Directory.Delete(Root, recursive: true);
        }
    }

    private static string RepoRoot()
    {
        var directory = new DirectoryInfo(AppContext.BaseDirectory);
        while (directory is not null
            && !File.Exists(Path.Combine(
                directory.FullName,
                "contracts",
                "v2",
                "fixtures",
                "app-settings-conformance.json")))
        {
            directory = directory.Parent;
        }
        Assert.IsNotNull(directory, "Repository root with the shared fixture was not found.");
        return directory.FullName;
    }

    private static JsonDocument LoadFixture()
    {
        string path = Path.Combine(
            RepoRoot(),
            "contracts",
            "v2",
            "fixtures",
            "app-settings-conformance.json");
        return JsonDocument.Parse(File.ReadAllText(path));
    }

    private static IEnumerable<(string Id, string Raw, bool Valid)> Cases(string section)
    {
        using JsonDocument fixture = LoadFixture();
        foreach (JsonElement element in fixture.RootElement
            .GetProperty(section)
            .EnumerateArray())
        {
            string id = element.GetProperty("id").GetString()!;
            string raw;
            if (element.TryGetProperty("raw", out JsonElement rawElement))
            {
                raw = rawElement.GetString()!;
            }
            else
            {
                int length = element.GetProperty("customUpdateProxyUrlLength").GetInt32();
                raw = "{\"customUpdateProxyUrl\":\"" + new string('a', length) + "\"}";
            }
            yield return (id, raw, element.GetProperty("valid").GetBoolean());
        }
    }

    private static (RoutedWebRequest? Routed, HostReplyMessage? Rejected) RouteRaw(
        string type, string id, string raw)
    {
        // The raw payload string is embedded verbatim into the envelope so
        // the router's real JsonDocument parse is the first decoder; no
        // pre-parsed object can hide duplicates.
        string envelope =
            "{\"type\":\"" + type + "\",\"requestId\":\"" + id
            + "\",\"payload\":" + raw + "}";
        RoutedWebRequest? routed = null;
        var router = new WebMessageRouter(request => routed = request) { IsReady = true };
        HostReplyMessage? rejected = router.Route(envelope);
        return (routed, rejected);
    }

    [TestMethod]
    public void PatchCases_ReproduceVerdicts_ThroughRealRouterAndAdapter()
    {
        using var context = new Context("patch");
        foreach ((string id, string raw, bool valid) in Cases("updatePatchCases"))
        {
            (RoutedWebRequest? routed, HostReplyMessage? rejected) =
                RouteRaw("appPreferences.update", id, raw);
            Assert.IsNull(rejected, $"{id}: the router must admit the closed settings type");
            Assert.IsNotNull(routed, $"{id}: the request must be dispatched");

            AppPreferences preferencesBefore = context.Controller.CurrentPreferences;
            int readsBefore = context.Store.Reads;
            int writesBefore = context.Store.Writes;
            int applyBefore = context.Host.ApplyPreferencesCalls;
            int trayBefore = context.Host.EnsureTrayIconCalls;
            int exitBefore = context.Host.RequestExitCalls;
            int traceBefore = context.Host.TraceCalls;
            int repliesBefore = context.Sink.Replies.Count;

            context.Controller.DispatchAsync(routed!).GetAwaiter().GetResult();
            var replies = context.Sink.Replies.Skip(repliesBefore).ToList();
            Assert.AreEqual(1, replies.Count, $"{id}: exactly one correlated reply");

            if (valid)
            {
                Assert.AreEqual(
                    "appPreferences.update",
                    replies[0].Type,
                    $"{id}: valid patches must succeed");
            }
            else
            {
                Assert.AreEqual(
                    "operation.failed",
                    replies[0].Type,
                    $"{id}: invalid patches must fail");
                Assert.AreEqual(
                    AppSettingsErrorCodes.BadPayload,
                    JsonSerializer.SerializeToElement(replies[0].Payload!)
                        .GetProperty("code")
                        .GetString(),
                    $"{id}: stable settings payload error code");
                Assert.AreEqual(writesBefore, context.Store.Writes,
                    $"{id}: invalid patches must not write preferences");
                Assert.AreEqual(readsBefore, context.Store.Reads,
                    $"{id}: invalid patches must not read the store");
                Assert.AreEqual(applyBefore, context.Host.ApplyPreferencesCalls,
                    $"{id}: invalid patches must not apply preferences");
                Assert.AreEqual(trayBefore, context.Host.EnsureTrayIconCalls,
                    $"{id}: invalid patches must not create native effects");
                Assert.AreEqual(exitBefore, context.Host.RequestExitCalls,
                    $"{id}: invalid patches must not request exit");
                Assert.AreEqual(traceBefore, context.Host.TraceCalls,
                    $"{id}: invalid patches must not trace unexpected failures");
                Assert.AreEqual(
                    preferencesBefore,
                    context.Controller.CurrentPreferences,
                    $"{id}: invalid patches must not change business preferences");
            }
        }

        Assert.IsTrue(context.Store.Writes > 0, "valid fixture cases must exercise the write path");
        Assert.IsTrue(context.Host.ApplyPreferencesCalls > 0,
            "valid fixture cases must exercise the apply path");
    }

    [TestMethod]
    public void GetCases_ReproduceVerdicts_ThroughRealRouterAndAdapter()
    {
        using var context = new Context("get");
        foreach ((string id, string raw, bool valid) in Cases("getCases"))
        {
            (RoutedWebRequest? routed, HostReplyMessage? rejected) =
                RouteRaw("appPreferences.get", id, raw);
            Assert.IsNull(rejected, $"{id}: the router must admit the closed settings type");
            Assert.IsNotNull(routed, $"{id}: the request must be dispatched");

            AppPreferences preferencesBefore = context.Controller.CurrentPreferences;
            int writesBefore = context.Store.Writes;
            int applyBefore = context.Host.ApplyPreferencesCalls;
            int trayBefore = context.Host.EnsureTrayIconCalls;
            int repliesBefore = context.Sink.Replies.Count;

            context.Controller.DispatchAsync(routed!).GetAwaiter().GetResult();
            var replies = context.Sink.Replies.Skip(repliesBefore).ToList();
            Assert.AreEqual(1, replies.Count, $"{id}: exactly one correlated reply");

            if (valid)
            {
                Assert.AreEqual("appPreferences.get", replies[0].Type, $"{id}: empty get succeeds");
                Assert.AreEqual(applyBefore + 1, context.Host.ApplyPreferencesCalls,
                    $"{id}: a valid get refreshes applied preferences");
            }
            else
            {
                Assert.AreEqual("operation.failed", replies[0].Type, $"{id}: invalid get fails");
                Assert.AreEqual(
                    AppSettingsErrorCodes.BadPayload,
                    JsonSerializer.SerializeToElement(replies[0].Payload!)
                        .GetProperty("code")
                        .GetString(),
                    $"{id}: stable settings payload error code");
                Assert.AreEqual(writesBefore, context.Store.Writes,
                    $"{id}: invalid get must not write");
                Assert.AreEqual(applyBefore, context.Host.ApplyPreferencesCalls,
                    $"{id}: invalid get must not apply preferences");
                Assert.AreEqual(trayBefore, context.Host.EnsureTrayIconCalls,
                    $"{id}: invalid get must not create native effects");
                Assert.AreEqual(
                    preferencesBefore,
                    context.Controller.CurrentPreferences,
                    $"{id}: invalid get must not change business preferences");
            }
        }
    }

    [TestMethod]
    public void ValidReply_UsesGeneratedWireShapeWithNullableThemeAndEmptyUrl()
    {
        using var context = new Context("shape");
        context.Controller.DispatchAsync(new RoutedWebRequest(
            "appPreferences.update",
            "shape-1",
            JsonDocument.Parse("{\"minimizeToTrayOnClose\":false}").RootElement.Clone(),
            string.Empty)).GetAwaiter().GetResult();

        FakeWebReplySink.Reply reply = context.Sink.Replies.Single();
        Assert.AreEqual("appPreferences.update", reply.Type);
        using JsonDocument payload = JsonSerializer.SerializeToDocument(reply.Payload);
        JsonElement rootElement = payload.RootElement;
        Assert.AreEqual(false, rootElement.GetProperty("minimizeToTrayOnClose").GetBoolean());
        Assert.AreEqual(false, rootElement.GetProperty("startWithWindows").GetBoolean());
        Assert.AreEqual(
            AppSettingsUpdateProxyOption.Direct,
            rootElement.GetProperty("updateProxy").GetString());
        Assert.AreEqual(
            "",
            rootElement.GetProperty("customUpdateProxyUrl").GetString());
        Assert.AreEqual(
            JsonValueKind.Null,
            rootElement.GetProperty("theme").ValueKind,
            "an unconfigured theme must serialize as explicit null, not be omitted");
        Assert.AreEqual("light", rootElement.GetProperty("effectiveTheme").GetString());
        Assert.AreEqual(false, rootElement.GetProperty("highContrast").GetBoolean());
    }

    [TestMethod]
    public void OperationFailedWire_MatchesRealRouterReplySerialization()
    {
        // The generated failure DTO and the real SerializeRouterReply output
        // must agree field-for-field, including the nulls the host emits for
        // unset metadata (Web defaults, no WhenWritingNull).
        foreach ((string? code, string? operation, string? operationId) in new[]
        {
            (null, null, null),
            ("APP_PREFERENCES_WRITE_FAILED", "appPreferences.update", "op-1"),
        })
        {
            string routerJson = ProductWebViewBridge.SerializeRouterReply(
                new HostReplyMessage(
                    "operation.failed",
                    "failure-1",
                    new OperationFailedPayload("m", code, operation, operationId)));
            string generatedJson = JsonSerializer.Serialize(
                new AppSettingsOperationFailed("m", code, operation, operationId));

            using JsonDocument routerDoc = JsonDocument.Parse(routerJson);
            using JsonDocument generatedDoc = JsonDocument.Parse(generatedJson);
            JsonNode? routerPayload = JsonNode.Parse(routerJson)?["payload"];
            JsonNode? generatedPayload = JsonNode.Parse(generatedJson);

            Assert.IsTrue(
                JsonNode.DeepEquals(routerPayload, generatedPayload),
                $"payloads differ for code={code}");
            var generatedElement = generatedDoc.RootElement;
            foreach (string field in new[] { "message", "code", "operation", "operationId" })
            {
                Assert.IsTrue(routerDoc.RootElement.GetProperty("payload").TryGetProperty(field, out _), field);
                Assert.IsTrue(generatedElement.TryGetProperty(field, out _), field);
            }
        }
    }
}

/// <summary>
/// Executable composition contract (#534): the router's effective admission
/// sets are exactly the declared authorities — the generated host-bridge
/// catalog plus the Product/Relation registries plus the generated
/// app-settings projection — with nothing silently opened or dropped.
/// </summary>
[TestClass]
public sealed class WebMessageRouterCompositionTests
{
    private static HashSet<string> EffectiveSet(string fieldName)
    {
        FieldInfo? field = typeof(WebMessageRouter).GetField(
            fieldName,
            BindingFlags.Static | BindingFlags.NonPublic);
        Assert.IsNotNull(field, fieldName);
        var set = (HashSet<string>)field.GetValue(null)!;
        return new HashSet<string>(set, StringComparer.Ordinal);
    }

    [TestMethod]
    public void RouterWebAdmission_IsExactlyTheDeclaredAuthorities()
    {
        var expected = new HashSet<string>(HostBridgeCatalog.RouterWebRequestTypes);
        expected.UnionWith(ProductDataRpcRegistry.RequestTypes);
        expected.UnionWith(RelationLookupRpcRegistry.RequestTypes);
        expected.UnionWith(AppSettingsWireMethods.WebRequestTypes);
        CollectionAssert.AreEquivalent(
            expected.ToList(),
            EffectiveSet("WebRequestWhitelist").ToList());
    }

    [TestMethod]
    public void RouterHostNotifications_AreExactlyTheDeclaredAuthorities()
    {
        var expected = new HashSet<string>(HostBridgeCatalog.RouterHostNotificationTypes);
        expected.UnionWith(ProductDataRpcRegistry.RequestTypes);
        expected.UnionWith(RelationLookupRpcRegistry.RequestTypes);
        expected.UnionWith(AppSettingsWireMethods.HostNotificationTypes);
        CollectionAssert.AreEquivalent(
            expected.ToList(),
            EffectiveSet("HostNotificationWhitelist").ToList());
    }

    [TestMethod]
    public void InternalOnlyAndRetiredSets_AreExactlyTheCatalogProjections()
    {
        CollectionAssert.AreEquivalent(
            HostBridgeCatalog.RendererInternalOnlyRequestTypes.ToList(),
            EffectiveSet("RendererInternalOnlyRequestTypes").ToList());
        CollectionAssert.AreEquivalent(
            HostBridgeCatalog.RetiredRequestTypes.ToList(),
            EffectiveSet("RetiredLayoutRequestTypes").ToList());
    }
}
