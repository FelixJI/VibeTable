using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginRequestDispatcherTests
{
    [TestMethod]
    public async Task DispatchUsesCorrelatedClosedResponseTypeAndNeverGenericRpc()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var picker = new FakePluginPackageSourcePicker(null);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(reply, surfaces, picker, resources);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.catalog.list",
            "request-1",
            """{"projectKey":"project-1"}"""));

        Assert.AreEqual("plugin.catalog.list", reply.ResponseType);
        Assert.AreEqual("request-1", reply.RequestId);
        Assert.IsInstanceOfType<PluginRuntimeSnapshot[]>(reply.Payload);
        Assert.AreEqual(1, gateway.ListCalls);
        Assert.IsNull(reply.FailureCode);
    }

    [TestMethod]
    public async Task SurfaceEventStaysLocalAndClosingEventRevokesToken()
    {
        const string hash =
            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        var surface = surfaces.Open(
            PluginPackageRevision.Create(@"C:\plugins\clean", hash),
            "ui/index.html");
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources);

        await dispatcher.DispatchAsync(Request(
            "plugin.surface.event",
            "request-close",
            JsonSerializer.Serialize(new
            {
                contract = PluginContractVersions.Surface,
                surfaceToken = surface.SurfaceToken,
                @event = PluginSurfaceEvents.Close,
                payload = new { },
            })));

        Assert.AreEqual("plugin.surface.event", reply.ResponseType);
        Assert.AreEqual("request-close", reply.RequestId);
        Assert.IsFalse(surfaces.IsActive(surface.SurfaceToken));
        Assert.IsFalse(dispatcher.HasGateway);
    }

    [TestMethod]
    public async Task InspectInstallResolvesHostPickerWithoutReturningNativePathToWeb()
    {
        const string nativePath = @"C:\trusted\clean.vtplugin";
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(nativePath),
            resources);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-1",
            """{"projectKey":"project-1","projectRevision":"r1","sourceLocation":"host-picker"}"""));

        Assert.AreEqual(nativePath, gateway.InspectRequest?.SourceLocation);
        var plan = (PluginRuntimeInstallPlan)reply.Payload!;
        Assert.AreEqual(PluginRequestDispatcher.HostManagedSource, plan.SourceLocation);
        Assert.IsFalse(JsonSerializer.Serialize(reply.Payload).Contains(nativePath, StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task InspectInstallRejectsRendererSuppliedNativePathBeforeGateway()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(@"C:\trusted\clean.vtplugin"),
            resources);
        dispatcher.SetGateway(gateway);

        await dispatcher.DispatchAsync(Request(
            "plugin.install.inspect",
            "inspect-raw",
            """{"projectKey":"project-1","projectRevision":"r1","sourceLocation":"C:/untrusted/evil.vtplugin"}"""));

        Assert.AreEqual("PLUGIN_SOURCE_NOT_HOST_SELECTED", reply.FailureCode);
        Assert.IsNull(gateway.InspectRequest);
    }

    [TestMethod]
    public void CatalogEventRemovesNativeSourceLocationBeforeWebNotification()
    {
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources);
        dispatcher.SetGateway(gateway);

        gateway.RaiseCatalogChanged();

        Assert.AreEqual("plugin.catalog.changed", reply.NotificationType);
        string serialized = JsonSerializer.Serialize(reply.NotificationPayload);
        Assert.IsFalse(serialized.Contains("package.vtplugin", StringComparison.OrdinalIgnoreCase));
        Assert.IsTrue(serialized.Contains(PluginRequestDispatcher.HostManagedSource, StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task FileCapabilityUsesNativePickerAndReturnsPathOnlyToPythonGateway()
    {
        const string selectedPath = @"C:\trusted\plugin-output.csv";
        var reply = new RecordingReplySink();
        var surfaces = new PluginSurfaceSessionManager();
        var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
        using var gateway = new FakePluginGateway();
        using var dispatcher = new PluginRequestDispatcher(
            reply,
            surfaces,
            new FakePluginPackageSourcePicker(null),
            resources,
            new FakePluginFilePicker(selectedPath));
        dispatcher.SetGateway(gateway);

        gateway.RaiseFileRequested();
        await gateway.FileResolution.Task.WaitAsync(TimeSpan.FromSeconds(2));

        Assert.AreEqual("file-1", gateway.FileResolution.Task.Result.RequestId);
        Assert.AreEqual(selectedPath, gateway.FileResolution.Task.Result.SelectedPath);
        Assert.IsFalse(JsonSerializer.Serialize(reply).Contains(selectedPath, StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task CatalogProjectionCreatesSafeCustomSurfaceWithoutExposingPackagePath()
    {
        string packageRoot = Path.Combine(
            Path.GetTempPath(),
            $"vibetable-dispatcher-{Guid.NewGuid():N}");
        Directory.CreateDirectory(Path.Combine(packageRoot, "ui"));
        try
        {
            var action = new PluginRuntimeAction(
                "open-dashboard",
                new Dictionary<string, string> { ["en-US"] = "Open dashboard" },
                new Dictionary<string, string>(),
                "interactive",
                PluginRisk.Read,
                "command",
                [],
                JsonSerializer.SerializeToElement(new { }),
                null,
                null,
                null,
                null,
                null);
            var manifest = FakePluginGateway.DefaultSnapshot.Manifest with
            {
                Actions = [action],
                Ui = JsonSerializer.SerializeToElement(new
                {
                    customViews = new[]
                    {
                        new
                        {
                            viewId = "dashboard",
                            actionId = "open-dashboard",
                            entry = "ui/index.html",
                        },
                    },
                }),
            };
            var reply = new RecordingReplySink();
            var surfaces = new PluginSurfaceSessionManager();
            var resources = new PluginWebViewResourceHost(new PluginResourceHost(), surfaces);
            using var gateway = new FakePluginGateway
            {
                CatalogSnapshot = FakePluginGateway.DefaultSnapshot with
                {
                    SourceLocation = packageRoot,
                    Manifest = manifest,
                },
            };
            using var dispatcher = new PluginRequestDispatcher(
                reply,
                surfaces,
                new FakePluginPackageSourcePicker(null),
                resources);
            dispatcher.SetGateway(gateway);

            await dispatcher.DispatchAsync(Request(
                "plugin.catalog.list",
                "catalog-surface",
                """{"projectKey":"project-1"}"""));

            string serialized = JsonSerializer.Serialize(reply.Payload);
            Assert.IsFalse(serialized.Contains(packageRoot, StringComparison.OrdinalIgnoreCase));
            Assert.IsTrue(serialized.Contains("surfaceToken", StringComparison.Ordinal));
            Assert.IsTrue(serialized.Contains(
                ".plugins.vibetable.local/ui/index.html",
                StringComparison.Ordinal));
        }
        finally
        {
            Directory.Delete(packageRoot, recursive: true);
        }
    }

    private static RoutedWebRequest Request(string type, string requestId, string payload)
    {
        using var document = JsonDocument.Parse(payload);
        return new RoutedWebRequest(type, requestId, document.RootElement.Clone(), string.Empty);
    }

    private sealed class RecordingReplySink : IWebReplySink
    {
        public string? ResponseType { get; private set; }
        public string? RequestId { get; private set; }
        public object? Payload { get; private set; }
        public string? FailureCode { get; private set; }
        public string? NotificationType { get; private set; }
        public object? NotificationPayload { get; private set; }

        public void PostNotification(string type, object? payload)
        {
            NotificationType = type;
            NotificationPayload = payload;
        }

        public void PostResponse(string type, string? requestId, object? payload)
        {
            ResponseType = type;
            RequestId = requestId;
            Payload = payload;
        }

        public void PostOperationFailed(string? requestId, string message, string? code = null)
        {
            RequestId = requestId;
            FailureCode = code;
        }
    }

    private sealed class FakePluginPackageSourcePicker(string? selectedPath)
        : IPluginPackageSourcePicker
    {
        public Task<string?> PickAsync(PluginPackagePickKind kind, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(selectedPath);
    }

    private sealed class FakePluginFilePicker(string? selectedPath) : IPluginFilePicker
    {
        public Task<string?> PickAsync(PluginRuntimeFileRequest request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(selectedPath);
    }

    private sealed class FakePluginGateway : IPluginRpcGateway
    {
        private static readonly PluginRuntimeManifest Manifest = new(
            "vibetable.plugin-manifest.v1", "com.acme.clean", "1.0.0",
            new Dictionary<string, string> { ["en-US"] = "Clean" },
            new Dictionary<string, string>(),
            JsonDocument.Parse("{}").RootElement.Clone(),
            JsonDocument.Parse("{}").RootElement.Clone(),
            [], [], JsonDocument.Parse("{}").RootElement.Clone());
        public static readonly PluginRuntimeSnapshot DefaultSnapshot = new(
            "project-1", "com.acme.clean", "1.0.0", new string('a', 64),
            "package", "package.vtplugin", Manifest, [], [],
            new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>(),
            "enabled", null, 1);
        private static readonly PluginRuntimeTaskSnapshot Task = new(
            "task-1", "run-1", "com.acme.clean", "1.0.0", "clean", "project-1",
            null, 0, PluginRisk.Read, "queued", false, null, null, null);

        public int ListCalls { get; private set; }
        public PluginInspectInstallParams? InspectRequest { get; private set; }
        public PluginRuntimeSnapshot CatalogSnapshot { get; set; } = DefaultSnapshot;
        public TaskCompletionSource<PluginResolveFileParams> FileResolution { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);
        private Action<PluginEventEnvelope>? _catalogChanged;
        private Action<PluginEventEnvelope>? _taskChanged;
        private Action<PluginEventEnvelope>? _interactionRequested;
        private Action<PluginEventEnvelope>? _fileRequested;
        public event Action<PluginEventEnvelope>? CatalogChanged
        {
            add => _catalogChanged += value;
            remove => _catalogChanged -= value;
        }
        public event Action<PluginEventEnvelope>? TaskChanged
        {
            add => _taskChanged += value;
            remove => _taskChanged -= value;
        }
        public event Action<PluginEventEnvelope>? InteractionRequested
        {
            add => _interactionRequested += value;
            remove => _interactionRequested -= value;
        }
        public event Action<PluginEventEnvelope>? FileRequested
        {
            add => _fileRequested += value;
            remove => _fileRequested -= value;
        }

        public void RaiseCatalogChanged()
        {
            _catalogChanged?.Invoke(new PluginEventEnvelope(
                PluginContractVersions.Event,
                "plugin.catalog.changed",
                "project-1",
                CatalogSnapshot.PluginId,
                CatalogSnapshot.Revision,
                JsonSerializer.SerializeToElement(CatalogSnapshot)));
        }

        public void RaiseFileRequested()
        {
            var request = new PluginRuntimeFileRequest(
                "file-1", "run-1", "project-1", CatalogSnapshot.PluginId,
                "export", "write", [], "plugin-output.csv", "text/csv", 1_800_000_000);
            _fileRequested?.Invoke(new PluginEventEnvelope(
                PluginContractVersions.Event,
                "plugin.file.requested",
                "project-1",
                request.RequestId,
                1,
                JsonSerializer.SerializeToElement(request)));
        }

        public Task<PluginRuntimeSnapshot[]> ListCatalogAsync(
            PluginCatalogListParams request, CancellationToken token)
        {
            ListCalls++;
            return System.Threading.Tasks.Task.FromResult(new[] { CatalogSnapshot });
        }
        public Task<PluginRuntimeAuditEvent[]> ListAuditAsync(PluginAuditListParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Array.Empty<PluginRuntimeAuditEvent>());
        public Task<PluginRuntimeAuditEvent[]> ListPendingCleanupAsync(PluginCatalogListParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Array.Empty<PluginRuntimeAuditEvent>());

        public Task<PluginRuntimeInstallPlan> InspectInstallAsync(PluginInspectInstallParams request, CancellationToken token)
        {
            InspectRequest = request;
            return System.Threading.Tasks.Task.FromResult(new PluginRuntimeInstallPlan(
                "plan-1", "project-1", "1", "package", "package.vtplugin",
                DefaultSnapshot.PackageHash, Manifest, [],
                new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>()));
        }
        public Task<PluginRuntimeSnapshot> CommitInstallAsync(PluginCommitInstallParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeExternalFlowCandidate[]> ListExternalFlowCandidatesAsync(PluginListExternalFlowCandidatesParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Array.Empty<PluginRuntimeExternalFlowCandidate>());
        public Task<PluginRuntimeFlowBindingSnapshot> BindExternalFlowAsync(PluginBindExternalFlowParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeFlowBindingSnapshot(
                "project-1", CatalogSnapshot.PluginId, "flow", "external", "flow-1",
                null, null, null, "manual", "1", null, new string('b', 64),
                1, "healthy", "not-applicable", null));
        public Task<PluginRuntimeSnapshot> SetEnabledAsync(PluginSetEnabledParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeSnapshot> UpgradeAsync(PluginUpgradeParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeSnapshot> RollbackAsync(PluginRollbackParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeSnapshot> ResolveDriftAsync(PluginResolveDriftParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(CatalogSnapshot);
        public Task<PluginRuntimeUninstallResult> UninstallAsync(PluginUninstallParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeUninstallResult(0, 0, true, true));
        public Task<PluginRuntimeActionAvailability> DescribeActionAsync(PluginDescribeActionParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeActionAvailability(true, []));
        public Task<PluginRuntimeTaskSnapshot> StartActionAsync(PluginStartActionParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Task);
        public Task<PluginRuntimeInteractionResolveResult> ResolveInteractionAsync(PluginResolveInteractionParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(new PluginRuntimeInteractionResolveResult("resolved", "rejected"));
        public Task<bool> ResolveFileAsync(PluginResolveFileParams request, CancellationToken token)
        {
            FileResolution.TrySetResult(request);
            return System.Threading.Tasks.Task.FromResult(true);
        }
        public Task<PluginRuntimeTaskSnapshot> CancelTaskAsync(PluginTaskParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Task);
        public Task<PluginRuntimeTaskSnapshot> GetTaskAsync(PluginTaskParams request, CancellationToken token)
            => System.Threading.Tasks.Task.FromResult(Task);
        public void Dispose() { }
    }
}
