using System.IO.Compression;
using System.Text;
using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
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
    [DataRow("application")]
    [DataRow("headless")]
    public async Task NativeDescribeRejectsNonmanualInvocation(string presentation)
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-nonmanual-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(root);
            var snapshot = PresentationSnapshot() with { Manifest = PresentationSnapshot().Manifest with
                { Actions = [Action("event-only", PluginRisk.Read, presentation, presentation == "application" ? "ui/index.html" : "ui/run.js") with { Invocation = "webhook" }] } };
            using (var zip = ZipFile.Open(PluginRetainedPackage.PathFor(root, snapshot.PackageHash)!, ZipArchiveMode.Create))
            { zip.CreateEntry("ui/index.html"); zip.CreateEntry("ui/run.js"); }
            var reply = new PluginRequestDispatcherTestsAccessibleSink(); var surfaces = new PluginSurfaceSessionManager();
            using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var dispatcher = new PluginRequestDispatcher(reply, surfaces, new FakePluginPackageSourcePickerForPresentation(null), resources,
                projectContext: ReadyContext, sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { snapshot })), packageCacheRoot: _ => root);
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "catalog", """{"projectKey":"project-1"}"""));
            Assert.IsNull(Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single().Manifest.Actions.Single().SurfaceToken);
            var context = new PluginRuntimeCommandContext("vibetable.command-context.v1", "project-1", null, [], null, "zh-CN", "light", "comfortable", JsonSerializer.SerializeToElement(new { }), "1.0.0");
            await dispatcher.DispatchAsync(Request("plugin.action.describe", "describe", JsonSerializer.Serialize(new { projectKey = "project-1", pluginId = snapshot.PluginId, actionId = "event-only", context })));
            var result = Assert.IsInstanceOfType<PluginRuntimeActionAvailability>(reply.Payload);
            Assert.IsFalse(result.Available); CollectionAssert.Contains(result.Reasons.ToArray(), "manual_invocation_required");
        }
        finally { Directory.Delete(root, recursive: true); }
    }

    [TestMethod]
    public async Task HistoricalCatalogInvalidationCannotRetireCurrentSurface()
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-catalog-history-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(root);
            var current = PresentationSnapshot() with { Revision = 2, InstallationId = "current-record" };
            var history = current with { Revision = 4, InstallationId = "retired-record", PackageHash = "sha256:" + new string('b', 64) };
            foreach (var snapshot in new[] { current, history })
            {
                using var package = ZipFile.Open(PluginRetainedPackage.PathFor(root, snapshot.PackageHash)!, ZipArchiveMode.Create);
                package.CreateEntry("ui/index.html"); package.CreateEntry("ui/run.js");
            }
            var reply = new PluginRequestDispatcherTestsAccessibleSink();
            var surfaces = new PluginSurfaceSessionManager();
            var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var dispatcher = new PluginRequestDispatcher(reply, surfaces,
                new FakePluginPackageSourcePickerForPresentation(null), resources, projectContext: ReadyContext,
                sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { current })), packageCacheRoot: _ => root);
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "current", """{"projectKey":"project-1"}"""));
            var projected = Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single();
            string token = projected.Manifest.Actions.Single(action => action.ActionId == "open-app").SurfaceToken!;
            Assert.IsTrue(surfaces.TryGet(token, out _));
            var payload = JsonSerializer.SerializeToElement(new PluginEventEnvelope(PluginContractVersions.Event,
                "plugin.catalog.changed", current.ProjectKey, current.PluginId, history.Revision,
                JsonSerializer.SerializeToElement(history, new JsonSerializerOptions(JsonSerializerDefaults.Web))));
            var invalidation = dispatcher.ProjectCatalogEvent(payload);
            Assert.IsTrue(surfaces.TryGet(token, out _), "Historical payload retired the current native token.");
            Assert.IsFalse(invalidation.Snapshot.GetRawText().Contains("surfaceToken", StringComparison.Ordinal));
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "still-current", """{"projectKey":"project-1"}"""));
            projected = Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single();
            Assert.AreEqual(token, projected.Manifest.Actions.Single(action => action.ActionId == "open-app").SurfaceToken);
            Assert.AreEqual("current-record", projected.InstallationId);
        }
        finally { Directory.Delete(root, recursive: true); }
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task LateCatalogReadOrLifecycleReplyCannotRetireReplacementSurface(bool lateWrite)
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-catalog-late-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(root);
            var current = PresentationSnapshot() with { Revision = 2, InstallationId = "replacement-record" };
            var old = current with { Revision = 4, InstallationId = "retired-record", PackageHash = "sha256:" + new string('b', 64) };
            foreach (var snapshot in new[] { current, old })
            {
                using var package = ZipFile.Open(PluginRetainedPackage.PathFor(root, snapshot.PackageHash)!, ZipArchiveMode.Create);
                package.CreateEntry("ui/index.html"); package.CreateEntry("ui/run.js");
            }
            var pending = new TaskCompletionSource<JsonElement>(TaskCreationOptions.RunContinuationsAsynchronously);
            var pendingWrite = new TaskCompletionSource<PluginRuntimeSnapshot>(TaskCreationOptions.RunContinuationsAsynchronously);
            using var gateway = new PluginRequestDispatcherTests.FakePluginGateway
            {
                SetEnabledHandler = (request, _) =>
                {
                    if (request.InstallationId == "retired-record") Assert.AreEqual(3, request.ExpectedRevision);
                    else { Assert.AreEqual("replacement-record", request.InstallationId); Assert.AreEqual(1, request.ExpectedRevision); }
                    return request.InstallationId == "retired-record" ? pendingWrite.Task : Task.FromResult(current);
                },
            };
            var reply = new PluginRequestDispatcherTestsAccessibleSink();
            var surfaces = new PluginSurfaceSessionManager();
            var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var dispatcher = new PluginRequestDispatcher(reply, surfaces,
                new FakePluginPackageSourcePickerForPresentation(null), resources, projectContext: ReadyContext,
                sharedRpc: (_, _, _) => pending.Task,
                packageCacheRoot: _ => root);
            dispatcher.SetGateway(gateway);
            var late = dispatcher.DispatchAsync(Request(lateWrite ? "plugin.lifecycle.setEnabled" : "plugin.catalog.list",
                "old", """{"projectKey":"project-1","pluginId":"com.acme.clean","enabled":true,"installationId":"retired-record","expectedRevision":3}"""));
            await dispatcher.DispatchAsync(Request("plugin.lifecycle.setEnabled", "replacement",
                """{"projectKey":"project-1","pluginId":"com.acme.clean","enabled":true,"installationId":"replacement-record","expectedRevision":1}"""));
            var projected = Assert.IsInstanceOfType<PluginRuntimeSnapshot>(reply.Payload);
            string token = projected.Manifest.Actions.Single(action => action.ActionId == "open-app").SurfaceToken!;
            Assert.IsTrue(surfaces.TryGet(token, out _));
            if (lateWrite) pendingWrite.SetResult(old);
            else pending.SetResult(JsonSerializer.SerializeToElement(new[] { old }));
            await late;
            Assert.IsTrue(surfaces.TryGet(token, out var session), "Late authority reply retired the replacement surface.");
            Assert.IsNotNull(session);
            Assert.AreEqual("replacement-record", session.InstallationId);
            if (lateWrite)
                Assert.IsNull(Assert.IsInstanceOfType<PluginRuntimeSnapshot>(reply.Payload).Manifest.Actions.Single(action => action.ActionId == "open-app").SurfaceToken);
            else Assert.AreEqual("PLUGIN_REQUEST_CANCELLED", reply.FailureCode);
        }
        finally { Directory.Delete(root, recursive: true); }
    }

    [TestMethod]
    [DataRow("entry")]
    [DataRow("html")]
    [DataRow("script")]
    [DataRow("style")]
    public async Task DeclaredCustomHostReservedResourcesReceiveNoProjection(string resource)
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-reserved-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(root);
            var view = new Dictionary<string, string> { ["viewId"] = "custom", ["actionId"] = "open", ["entry"] = "ui/index.html" };
            view[resource] = "__vibetable_host__/nested/resource.html";
            var baseline = PresentationSnapshot();
            var snapshot = baseline with { Manifest = baseline.Manifest with {
                Actions = [Action("open", PluginRisk.Read, "surface", "ui/index.html") with { Entry = null }],
                Ui = JsonSerializer.SerializeToElement(new { customViews = new[] { view } }) } };
            using (var zip = ZipFile.Open(PluginRetainedPackage.PathFor(root, snapshot.PackageHash)!, ZipArchiveMode.Create))
            { zip.CreateEntry("ui/index.html"); zip.CreateEntry(view[resource]); }
            var reply = new PluginRequestDispatcherTestsAccessibleSink(); var surfaces = new PluginSurfaceSessionManager();
            using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var dispatcher = new PluginRequestDispatcher(reply, surfaces, new FakePluginPackageSourcePickerForPresentation(null), resources,
                projectContext: ReadyContext, sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { snapshot })), packageCacheRoot: _ => root);
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "reserved", "{\"projectKey\":\"project-1\"}"));
            var projected = Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single().Manifest.Ui.GetProperty("customViews")[0];
            Assert.IsFalse(projected.TryGetProperty("surfaceToken", out _));
            Assert.IsFalse(projected.TryGetProperty("src", out _));
        }
        finally { Directory.Delete(root, recursive: true); }
    }

    [TestMethod]
    public async Task SamePackageRevisionChangeRetiresProjectedTokensButSameRevisionReadKeepsThem()
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-revision-{Guid.NewGuid():N}");
        try
        {
            Directory.CreateDirectory(root);
            var current = PresentationSnapshot() with { Revision = 1, InstallationId = "same-record" };
            using (var zip = ZipFile.Open(PluginRetainedPackage.PathFor(root, current.PackageHash)!, ZipArchiveMode.Create))
            { zip.CreateEntry("ui/index.html"); zip.CreateEntry("ui/run.js"); }
            var reply = new PluginRequestDispatcherTestsAccessibleSink();
            var surfaces = new PluginSurfaceSessionManager();
            using var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var dispatcher = new PluginRequestDispatcher(reply, surfaces, new FakePluginPackageSourcePickerForPresentation(null), resources,
                projectContext: ReadyContext, sharedRpc: (_, _, _) => Task.FromResult(JsonSerializer.SerializeToElement(new[] { current })), packageCacheRoot: _ => root);
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "before", "{\"projectKey\":\"project-1\"}"));
            var old = Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single().Manifest.Actions.Where(a => a.SurfaceToken is not null).ToArray();
            Assert.IsTrue(old.Length >= 2);
            current = current with { Revision = 2 };
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "after", "{\"projectKey\":\"project-1\"}"));
            var updated = Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single().Manifest.Actions.Where(a => a.SurfaceToken is not null).ToArray();
            foreach (var prior in old)
            {
                Assert.IsFalse(surfaces.IsActive(prior.SurfaceToken!), "Same-package revision reused an old capability.");
                var replacement = updated.Single(a => a.ActionId == prior.ActionId);
                Assert.AreNotEqual(prior.SurfaceToken, replacement.SurfaceToken);
                Assert.IsTrue(surfaces.TryGet(replacement.SurfaceToken!, out var surface));
                Assert.AreEqual(2, surface!.InstallationRevision);
                Assert.AreEqual("same-record", surface.InstallationId);
            }
            await dispatcher.DispatchAsync(Request("plugin.catalog.list", "unchanged", "{\"projectKey\":\"project-1\"}"));
            var unchanged = Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload).Single().Manifest.Actions;
            foreach (var action in updated) Assert.AreEqual(action.SurfaceToken, unchanged.Single(a => a.ActionId == action.ActionId).SurfaceToken);
        }
        finally { Directory.Delete(root, recursive: true); }
    }

    [TestMethod]
    [DataRow("application", "ui/ENTRY.html", true)]
    [DataRow("application", "ui/ENTRY.HTML", true)]
    [DataRow("headless", "ui/ENTRY.js", true)]
    [DataRow("headless", "ui/ENTRY.JS", true)]
    [DataRow("headless", "ui/ENTRY.mjs", true)]
    [DataRow("headless", "ui/ENTRY.MJS", true)]
    [DataRow("application", "ui/ENTRY.js", false)]
    [DataRow("headless", "ui/ENTRY.html", false)]
    [DataRow("application", "__vibetable_host__/headless.html", false)]
    [DataRow("headless", "__vibetable_host__/nested/entry.mjs", false)]
    [DataRow("surface", "__vibetable_host__/nested/index.html", false)]
    public void RuntimeEntryTypeMustMatchServedMime(string presentation, string entry, bool valid)
    {
        string root = Path.Combine(Path.GetTempPath(), $"vibetable-mime-{Guid.NewGuid():N}");
        Directory.CreateDirectory(Path.Combine(root, "ui"));
        Directory.CreateDirectory(Path.GetDirectoryName(Path.Combine(root, entry.Replace('/', Path.DirectorySeparatorChar)))!);
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

    [STATestMethod]
    public void NativeConfirmationWindowRendersTrustedPluginAndActionIdentity()
    {
        // Two application/headless plugins ask with the SAME plugin-authored
        // title: without the trusted identity line the windows are
        // indistinguishable, so the user could approve the wrong request.
        // WPF requires an already-shown owner; the bare owner window below is
        // not a product window.
        var owner = new Window
        {
            ShowInTaskbar = false,
            ShowActivated = false,
            WindowStyle = WindowStyle.None,
            Opacity = 0,
        };
        owner.Show();
        NativePluginConfirmationWindow? first = null;
        NativePluginConfirmationWindow? second = null;
        try
        {
            first = CreateConfirmationWindow(owner, "com.example.first", "open-app", "i-1");
            second = CreateConfirmationWindow(owner, "com.example.second", "run-job", "i-2");
            Assert.IsNotNull(first);
            Assert.IsNotNull(second);
            TextBlock firstIdentity = LogicalDescendants<TextBlock>(first)
                .Single(block => AutomationProperties.GetAutomationId(block) == "plugin-confirm-identity");
            TextBlock secondIdentity = LogicalDescendants<TextBlock>(second)
                .Single(block => AutomationProperties.GetAutomationId(block) == "plugin-confirm-identity");
            Assert.AreEqual("来源插件 com.example.first，动作 open-app", AutomationProperties.GetName(firstIdentity));
            Assert.AreEqual("来源插件 com.example.second，动作 run-job", AutomationProperties.GetName(secondIdentity));
            Assert.IsTrue(firstIdentity.Text.Contains("com.example.first", StringComparison.Ordinal), firstIdentity.Text);
            Assert.IsTrue(firstIdentity.Text.Contains("open-app", StringComparison.Ordinal), firstIdentity.Text);
            Assert.IsTrue(secondIdentity.Text.Contains("com.example.second", StringComparison.Ordinal), secondIdentity.Text);
            Assert.IsTrue(secondIdentity.Text.Contains("run-job", StringComparison.Ordinal), secondIdentity.Text);
            Assert.IsFalse(secondIdentity.Text.Contains("com.example.first", StringComparison.Ordinal), secondIdentity.Text);

            // The original risk badge, plugin title, count, sample and scrolling
            // presentation stay intact around the new trusted identity line.
            var blocks = LogicalDescendants<TextBlock>(first).ToArray();
            Assert.IsTrue(blocks.Any(block => block.Text.Contains("WRITE", StringComparison.Ordinal)));
            Assert.IsTrue(blocks.Any(block => block.Text.Contains("共 3 条", StringComparison.Ordinal)));
            Assert.AreEqual("保存修改", blocks.Single(block => block.FontSize == 15).Text);
            var sample = LogicalDescendants<TextBox>(first).Single();
            Assert.IsTrue(sample.Text.Contains("cell-1", StringComparison.Ordinal));
            Assert.AreEqual(ScrollBarVisibility.Auto, sample.VerticalScrollBarVisibility);
            Assert.AreEqual(240d, sample.MaxHeight);
        }
        finally
        {
            // Closing each window stops its expiry timer and runs the reject
            // path through the stubbed resolve, so no timer or pending
            // interaction outlives this STA test.
            first?.Close();
            second?.Close();
            owner.Close();
        }
    }

    private static NativePluginConfirmationWindow? CreateConfirmationWindow(
        Window owner, string pluginId, string actionId, string interactionId)
    {
        string payload = "{\"projectKey\":\"local:project-1\",\"snapshot\":{\"runId\":\"run-"
            + interactionId + "\",\"pluginId\":\"" + pluginId + "\",\"actionId\":\"" + actionId
            + "\",\"pendingConfirmation\":{\"interactionId\":\"" + interactionId
            + "\",\"title\":\"保存修改\",\"risk\":\"write\",\"preview\":{\"affectedCount\":3,\"summary\":[{\"collection\":\"tbl_tasks\"}],\"sampleRows\":[{\"after\":\"cell-1\"}]}}}}";
        using JsonDocument document = JsonDocument.Parse(payload);
        return NativePluginConfirmationWindow.TryCreate(
            owner,
            document.RootElement.Clone(),
            _ => Task.CompletedTask,
            _ => true);
    }

    private static IEnumerable<T> LogicalDescendants<T>(DependencyObject root) where T : DependencyObject
    {
        foreach (object child in LogicalTreeHelper.GetChildren(root))
        {
            if (child is not DependencyObject node) continue;
            if (node is T match) yield return match;
            foreach (T descendant in LogicalDescendants<T>(node)) yield return descendant;
        }
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
