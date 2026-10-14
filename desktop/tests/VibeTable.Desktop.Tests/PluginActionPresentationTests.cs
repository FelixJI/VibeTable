using System.IO.Compression;
using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Task #482 boundaries: application/headless presentations are projected as
/// host-owned surfaces on the action, availability follows the live token,
/// and the headless controlled document is reserved against package forgery.
/// Every accepting assertion fails on the pre-#482 dispatcher, which only
/// projected ui.customViews.
/// </summary>
[TestClass]
public sealed class PluginActionPresentationTests
{
    private static PluginProjectContext ReadyContext() => new("project-1", "1", 1);

    [TestMethod]
    [DataRow("application", "ui/ENTRY.html", true)]
    [DataRow("application", "ui/ENTRY.HTML", true)]
    [DataRow("headless", "ui/ENTRY.js", true)]
    [DataRow("headless", "ui/ENTRY.JS", true)]
    [DataRow("headless", "ui/ENTRY.mjs", true)]
    [DataRow("headless", "ui/ENTRY.MJS", true)]
    [DataRow("application", "ui/ENTRY.js", false)]
    [DataRow("headless", "ui/ENTRY.html", false)]
    public void RuntimeEntryTypeMustMatchServedMime(string presentation, string entry, bool valid)
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-mime-{Guid.NewGuid():N}");
        Directory.CreateDirectory(Path.Combine(root, "ui"));
        File.WriteAllText(Path.Combine(root, entry.Replace('/', Path.DirectorySeparatorChar)), "entry");
        try
        {
            var surfaces = new PluginSurfaceSessionManager();
            using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            var revision = PluginPackageRevision.Create(root, new string('a', 64));
            resources.RegisterInstalled("project-1", "com.acme.clean", revision);
            if (valid)
            {
                var surface = resources.OpenSurface("project-1", "com.acme.clean", entry, "open", presentation);
                Assert.IsTrue(surfaces.IsActive(surface.SurfaceToken));
            }
            else
            {
                try
                {
                    resources.OpenSurface("project-1", "com.acme.clean", entry, "open", presentation);
                    Assert.Fail("Unsupported entry MIME obtained a surface token");
                }
                catch (PluginSurfacePolicyException error)
                {
                    Assert.AreEqual("PLUGIN_ENTRY_TYPE_INVALID", error.Code);
                }
            }
        }
        finally { Directory.Delete(root, recursive: true); }
    }


    private static PluginRuntimeAction Action(
        string actionId,
        string risk,
        string? presentation = null,
        string? entry = null) => new(
            actionId,
            new Dictionary<string, string> { ["zh-CN"] = actionId },
            new Dictionary<string, string>(),
            "local",
            risk,
            "manual",
            [],
            JsonSerializer.SerializeToElement(new { }),
            null,
            null,
            null,
            null,
            presentation,
            entry);

    private static PluginRuntimeSnapshot PresentationSnapshot() => new(
        "project-1",
        "com.acme.clean",
        "1.0.0",
        "sha256:" + new string('a', 64),
        "package",
        "package.vtplugin",
        new PluginRuntimeManifest(
            "vibetable.plugin-manifest.v2",
            "com.acme.clean",
            "1.0.0",
            new Dictionary<string, string> { ["zh-CN"] = "演示" },
            new Dictionary<string, string>(),
            JsonSerializer.SerializeToElement(new { pluginApi = "3.x" }),
            JsonSerializer.SerializeToElement(new { }),
            [
                Action("open-app", PluginRisk.Read, "application", "ui/index.html"),
                Action("run-job", PluginRisk.Write, "headless", "ui/run.js"),
            ],
            JsonSerializer.SerializeToElement(new { customViews = Array.Empty<object>() })),
        new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>(),
        "enabled",
        null,
        1);

    [TestMethod]
    public async Task ApplicationAndHeadlessActionsProjectSurfacesAndAvailability()
    {
        string packageRoot = Path.Combine(Path.GetTempPath(), $"vibetable-presentation-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(packageRoot);
            var reply = new PluginRequestDispatcherTestsAccessibleSink();
            var surfaces = new PluginSurfaceSessionManager();
            var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            var snapshot = PresentationSnapshot();
            string currentCacheRoot = packageRoot;
            using var dispatcher = new PluginRequestDispatcher(
                reply,
                surfaces,
                new FakePluginPackageSourcePickerForPresentation(null),
                resources,
                projectContext: ReadyContext,
                sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { snapshot })),
                packageCacheRoot: _ => currentCacheRoot);
            string retained = PluginRetainedPackage.PathFor(packageRoot, snapshot.PackageHash)!;
            using (var package = ZipFile.Open(retained, ZipArchiveMode.Create))
            {
                package.CreateEntry("ui/index.html");
                package.CreateEntry("ui/run.js");
            }

            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list",
                "catalog-presentation",
                """{"projectKey":"project-1"}"""));

            JsonElement manifest = JsonSerializer.SerializeToElement(
                reply.Payload,
                new JsonSerializerOptions(JsonSerializerDefaults.Web))
                .EnumerateArray().Single().GetProperty("manifest");
            JsonElement application = manifest.GetProperty("actions")
                .EnumerateArray().Single(action => action.GetProperty("actionId").GetString() == "open-app");
            JsonElement headless = manifest.GetProperty("actions")
                .EnumerateArray().Single(action => action.GetProperty("actionId").GetString() == "run-job");
            Assert.AreEqual("application", application.GetProperty("presentation").GetString());
            Assert.IsTrue(
                application.GetProperty("surfaceSrc").GetString()!
                    .EndsWith(".plugins.vibetable.local/ui/index.html", StringComparison.Ordinal),
                application.GetProperty("surfaceSrc").GetString());
            Assert.AreEqual("ui/index.html", application.GetProperty("entry").GetString());
            Assert.AreEqual("headless", headless.GetProperty("presentation").GetString());
            string headlessSrc = headless.GetProperty("surfaceSrc").GetString()!;
            Assert.IsTrue(
                headlessSrc.Contains("__vibetable_host__/headless.html?entry=", StringComparison.Ordinal),
                headlessSrc);
            Assert.IsFalse(headlessSrc.Contains("ui/run.js", StringComparison.Ordinal), "the module path must stay URL-encoded");

            string applicationToken = application.GetProperty("surfaceToken").GetString()!;
            string headlessToken = headless.GetProperty("surfaceToken").GetString()!;
            Assert.IsTrue(surfaces.TryGet(applicationToken, out var applicationSurface));
            Assert.AreEqual("open-app", applicationSurface!.ActionId);
            Assert.AreEqual(PluginRisk.Read, applicationSurface.Risk);
            Assert.AreEqual("ui/index.html", applicationSurface.Entry);
            Assert.IsTrue(surfaces.TryGet(headlessToken, out var headlessSurface));
            Assert.AreEqual("ui/run.js", headlessSurface!.Entry);
            Assert.AreEqual(
                PluginResourceHost.HeadlessDocumentPath,
                headlessSurface.DocumentUri.AbsolutePath.TrimStart('/'));

            // Availability follows the live action-level token for both shapes.
            foreach (string actionId in new[] { "open-app", "run-job" })
            {
                string describePayload =
                    "{\"projectKey\":\"project-1\",\"pluginId\":\"com.acme.clean\",\"actionId\":\"" + actionId
                    + "\",\"context\":{\"contract\":\"vibetable.command-context.v1\",\"projectKey\":\"project-1\",\"collection\":null,\"selectedKeys\":[],\"querySnapshot\":null,\"locale\":\"zh-CN\",\"theme\":\"light\",\"density\":\"comfortable\",\"user\":{},\"hostVersion\":\"1.0.0\"}}";
                await dispatcher.DispatchAsync(Request(
                    "plugin.action.describe",
                    $"describe-{actionId}",
                    describePayload));
                var availability = Assert.IsInstanceOfType<PluginRuntimeActionAvailability>(reply.Payload);
                Assert.IsTrue(availability.Available, $"{actionId}: {string.Join(",", availability.Reasons)}");
            }

            snapshot = snapshot with { Status = "disabled" };
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "disabled-catalog", """{"projectKey":"project-1"}"""));
            Assert.IsFalse(surfaces.IsActive(applicationToken));
            Assert.IsFalse(surfaces.IsActive(headlessToken));
            snapshot = snapshot with { Status = "enabled" };

            // Losing the retained package removes the projected surface and the
            // availability; no dead entry survives in the catalog.
            currentCacheRoot = Path.Combine(packageRoot, "relocated");
            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list",
                "catalog-presentation-missing",
                """{"projectKey":"project-1"}"""));
            Assert.IsFalse(
                JsonSerializer.Serialize(reply.Payload).Contains("surfaceToken", StringComparison.Ordinal));
        }
        finally
        {
            Directory.Delete(packageRoot, recursive: true);
        }
    }

    [TestMethod]
    public void HeadlessControlledDocumentIsReservedAgainstPackageForgery()
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-headless-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(Path.Combine(root, "ui"));
            Directory.CreateDirectory(Path.Combine(root, "__vibetable_host__"));
            File.WriteAllText(Path.Combine(root, "ui", "index.html"), "<!doctype html>", Encoding.UTF8);
            File.WriteAllText(Path.Combine(root, "__vibetable_host__", "headless.html"), "FORGED", Encoding.UTF8);
            File.WriteAllText(Path.Combine(root, "__vibetable_host__", "evil.html"), "FORGED", Encoding.UTF8);

            var host = new PluginResourceHost();
            var revision = PluginPackageRevision.Create(root, new string('a', 64));

            // The embedded host document always wins over a package file with
            // the reserved name.
            using var document = host.Open(
                revision,
                PluginResourceHost.HeadlessDocumentPath,
                PluginResourceRequestKind.Document);
            using var reader = new StreamReader(document.Content, Encoding.UTF8);
            string body = reader.ReadToEnd();
            Assert.IsFalse(body.Contains("FORGED", StringComparison.Ordinal));
            Assert.IsTrue(body.Contains("headless", StringComparison.Ordinal));
            Assert.IsTrue(body.Contains("src=\"/__vibetable_host__/headless.js\"", StringComparison.Ordinal));
            Assert.IsFalse(body.Contains("await import", StringComparison.Ordinal));
            using var bootstrap = host.Open(revision, "__vibetable_host__/headless.js", PluginResourceRequestKind.Script);
            using var bootstrapReader = new StreamReader(bootstrap.Content, Encoding.UTF8);
            Assert.IsTrue(bootstrapReader.ReadToEnd().Contains("await import", StringComparison.Ordinal));
            Assert.IsFalse(PluginResourceHost.ContentSecurityPolicy.Contains("unsafe-inline'; connect", StringComparison.Ordinal));
            Assert.AreEqual("no-store", document.Headers["Cache-Control"]);

            // Any other reserved-path member is refused, not served from the package.
            try
            {
                using var refused = host.Open(
                    revision,
                    "__vibetable_host__/evil.html",
                    PluginResourceRequestKind.Document);
                Assert.Fail("reserved package member was served");
            }
            catch (PluginResourcePolicyException exception)
            {
                Assert.AreEqual("PLUGIN_RESOURCE_NOT_FOUND", exception.Code);
            }

            Uri headless = PluginResourceHost.BuildHeadlessDocumentUri(revision, "ui/run.js");
            Assert.AreEqual(revision.Origin.Host, headless.Host);
            Assert.IsTrue(headless.Query.Contains("entry=ui%2Frun.js", StringComparison.Ordinal), headless.Query);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static RoutedWebRequest Request(string type, string requestId, string payload)
    {
        using var document = JsonDocument.Parse(payload);
        return new RoutedWebRequest(type, requestId, document.RootElement.Clone(), string.Empty);
    }

    private sealed class PluginRequestDispatcherTestsAccessibleSink : IWebReplySink
    {
        public string? ResponseType { get; private set; }
        public string? RequestId { get; private set; }
        public object? Payload { get; private set; }
        public string? FailureCode { get; private set; }

        public void PostNotification(string type, object? payload)
        {
        }

        public void PostResponse(string type, string? requestId, object? payload)
        {
            ResponseType = type;
            RequestId = requestId;
            Payload = payload;
            FailureCode = null;
        }

        public void PostOperationFailed(
            string? requestId,
            string message,
            string? code = null,
            string? operation = null,
            string? operationId = null)
        {
            ResponseType = "operation.failed";
            RequestId = requestId;
            Payload = new { code, message };
            FailureCode = code;
        }
    }

    private sealed class FakePluginPackageSourcePickerForPresentation(string? selectedPath) : IPluginPackageSourcePicker
    {
        public Task<string?> PickAsync(PluginPackagePickKind kind, CancellationToken token)
            => Task.FromResult(selectedPath);
    }
    [TestMethod]
    public void NativeConfirmationRequestParsesTypedEnvelopeAndRawJson()
    {
        string snapshotJson = """
            {"runId":"run-1","pluginId":"com.example.app","actionId":"open-app","pendingConfirmation":{"interactionId":"i-1","title":"保存修改","risk":"write","preview":{"affectedCount":3,"summary":[{"collection":"tbl_tasks"}]},"expiresAt":1}}
            """;
        using JsonDocument document = JsonDocument.Parse(snapshotJson);
        var typed = new PluginEventEnvelope(
            "vibetable.plugin-event.v1", "plugin.interaction.requested", "local:project-1", "com.example.app", 1,
            document.RootElement.Clone());
        NativeConfirmationRequest? parsed = NativeConfirmationRequest.TryParse(typed);
        Assert.IsNotNull(parsed);
        Assert.AreEqual("local:project-1", parsed.ProjectKey);
        Assert.AreEqual("com.example.app", parsed.PluginId);
        Assert.AreEqual("open-app", parsed.ActionId);
        Assert.AreEqual("run-1", parsed.RunId);
        Assert.AreEqual("i-1", parsed.InteractionId);
        Assert.AreEqual("保存修改", parsed.Title);
        Assert.AreEqual(3, parsed.AffectedCount);
        Assert.AreEqual("write", parsed.Risk);

        // Raw camelCase JsonElement (the wire shape the web consumes) parses
        // identically; payloads without a pending confirmation return null.
        using JsonDocument raw = JsonDocument.Parse(
            $$"""{"projectKey":"local:project-1","snapshot":{{snapshotJson}}}""");
        Assert.IsNotNull(NativeConfirmationRequest.TryParse(raw.RootElement.Clone()));
        using JsonDocument noPending = JsonDocument.Parse("""{"projectKey":"p","snapshot":{"runId":"r"}}""");
        Assert.IsNull(NativeConfirmationRequest.TryParse(noPending.RootElement.Clone()));
        Assert.IsNull(NativeConfirmationRequest.TryParse("not-an-envelope"));
    }

    [TestMethod]
    public async Task WebResolveIsRejectedForApplicationHeadlessButNativeEntryResolves()
    {
        string packageRoot = Path.Combine(Path.GetTempPath(), $"vibetable-native-resolve-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(packageRoot);
            var reply = new PluginRequestDispatcherTestsAccessibleSink();
            var surfaces = new PluginSurfaceSessionManager();
            var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            var snapshot = PresentationSnapshot();
            string currentCacheRoot = packageRoot;
            using var dispatcher = new PluginRequestDispatcher(
                reply,
                surfaces,
                new FakePluginPackageSourcePickerForPresentation(null),
                resources,
                projectContext: ReadyContext,
                sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { snapshot })),
                packageCacheRoot: _ => currentCacheRoot);
            string retained = PluginRetainedPackage.PathFor(packageRoot, snapshot.PackageHash)!;
            using (var package = System.IO.Compression.ZipFile.Open(retained, System.IO.Compression.ZipArchiveMode.Create))
            {
                package.CreateEntry("ui/index.html");
                package.CreateEntry("ui/run.js");
            }
            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list", "catalog-native-resolve", """{"projectKey":"project-1"}"""));

            // Register a pending native confirmation for the application action
            // through the real registry path.
            dispatcher.SetProjectContext(ReadyContext());
            var tasks = dispatcher.Tasks;
            tasks.SetWorkspaceContext(ReadyContext());
            var binding = tasks.CaptureNative();
            Assert.IsNotNull(binding);
            var taskSnapshot = new PluginRuntimeTaskSnapshot(
                "task-app-1", "run-app-1", snapshot.PluginId, snapshot.Version, "open-app",
                snapshot.ProjectKey, null, 1, PluginRisk.Read, "running", false, null, null, null);
            tasks.NativeTask(binding!, taskSnapshot, nativeOnlyApproval: true);
            var confirmation = tasks.NativeConfirmation(
                "task-app-1",
                JsonSerializer.SerializeToElement(new { collection = "tbl_tasks", operations = Array.Empty<object>() }),
                1);
            string interactionId = "";
            using (JsonDocument parsed = JsonDocument.Parse(
                JsonSerializer.Serialize(confirmation.Envelope, new JsonSerializerOptions(JsonSerializerDefaults.Web))))
            {
                interactionId = parsed.RootElement.GetProperty("snapshot")
                    .GetProperty("pendingConfirmation").GetProperty("interactionId").GetString()!;
            }
            Assert.AreNotEqual("", interactionId);

            // The WEB resolve path is rejected before consuming the one-shot
            // decision; the operation fails with the dedicated code.
            await dispatcher.DispatchAsync(Request("plugin.interaction.resolve", "web-resolve",
                $$"""{"runId":"run-app-1","interactionId":"{{interactionId}}","decision":"approved"}"""));
            Assert.AreEqual("PLUGIN_NATIVE_CONFIRMATION_REQUIRED", reply.FailureCode);

            // Catalog loss cannot downgrade an already-owned native decision.
            snapshot = snapshot with { Status = "disabled" };
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "disabled-native-catalog", """{"projectKey":"project-1"}"""));
            await dispatcher.DispatchAsync(Request("plugin.interaction.resolve", "web-resolve-after-removal",
                $$"""{"runId":"run-app-1","interactionId":"{{interactionId}}","decision":"approved"}"""));
            Assert.AreEqual("PLUGIN_NATIVE_CONFIRMATION_REQUIRED", reply.FailureCode);

            // The native host entry resolves the same confirmation.
            var native = await dispatcher.ResolveInteractionForHostAsync(
                new PluginResolveInteractionParams("run-app-1", interactionId, "approved"));
            Assert.AreEqual("resolved", native.Status);
        }
        finally
        {
            Directory.Delete(packageRoot, recursive: true);
        }
    }
}
