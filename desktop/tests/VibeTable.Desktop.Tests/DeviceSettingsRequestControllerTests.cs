using System.Collections.Concurrent;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DeviceSettingsRequestControllerTests
{
    [TestMethod]
    public async Task RouterSavesAndReadsWorkspaceLocalDeviceSettingsWithoutBackend()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        JsonElement saved = await fixture.RequestAsync("settings.saveDevice", """
            {"settings":{"schemaVersion":2,"theme":{"mode":"dark"},"windowPosition":{"x":42},"recentCollections":["表格"]}}
            """);
        Assert.AreEqual("dark", saved.GetProperty("theme").GetProperty("mode").GetString());
        JsonElement read = await fixture.RequestAsync("settings.readDevice", "{}");
        Assert.AreEqual(saved.GetRawText(), read.GetRawText());
        using var disk = JsonDocument.Parse(File.ReadAllText(fixture.SettingsPath));
        Assert.AreEqual(2, disk.RootElement.GetProperty("schema_version").GetInt32());
        Assert.AreEqual(42, disk.RootElement.GetProperty("window_position").GetProperty("x").GetInt32());
    }

    [TestMethod]
    public async Task ConcurrentRequestsKeepSaveSaveReadOrder()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        var firstReply = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseFirst = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int replies = 0;
        fixture.OnResponse = () =>
        {
            if (Interlocked.Increment(ref replies) != 1) return;
            firstReply.TrySetResult();
            releaseFirst.Task.GetAwaiter().GetResult();
        };
        Task<JsonElement> first = Task.Run(() => fixture.RequestAsync(
            "settings.saveDevice", "{\"settings\":{\"schemaVersion\":2}}"));
        await firstReply.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Task<JsonElement> second = fixture.RequestAsync(
            "settings.saveDevice", "{\"settings\":{\"schemaVersion\":3}}");
        Task<JsonElement> read = fixture.RequestAsync("settings.readDevice", "{}");
        Task timeout = Task.Delay(250);
        Assert.AreSame(timeout, await Task.WhenAny(second, read, timeout),
            "A follower overtook the blocked first write.");
        Assert.AreEqual(1, Volatile.Read(ref replies));

        releaseFirst.TrySetResult();
        await Task.WhenAll(first, second).WaitAsync(TimeSpan.FromSeconds(5));
        JsonElement observed = await read.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(3, observed.GetProperty("schemaVersion").GetInt32());
        Assert.AreEqual(3, Volatile.Read(ref replies));
    }

    [TestMethod]
    public async Task CancelledQueueWaiterDoesNotBlockTheNextEpoch()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        var firstReply = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseFirst = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.OnResponse = () =>
        {
            firstReply.TrySetResult();
            releaseFirst.Task.GetAwaiter().GetResult();
        };
        Task<JsonElement> first = Task.Run(() => fixture.RequestAsync(
            "settings.saveDevice", "{\"settings\":{\"schemaVersion\":2}}"));
        await firstReply.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Task<JsonElement> cancelled = fixture.RequestAsync(
            "settings.readDevice", "{}", "WORKSPACE_SESSION_STALE");

        Task closing = fixture.Manager.CloseAsync("cancel queued settings request");
        await cancelled.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.IsFalse(closing.IsCompleted);
        releaseFirst.TrySetResult();
        await Task.WhenAll(first, closing).WaitAsync(TimeSpan.FromSeconds(5));

        await fixture.Manager.OpenAsync(
            fixture.Workspace!.WorkspaceId,
            WorkspaceOpenMode.Writable);
        JsonElement reopened = await fixture.RequestAsync("settings.readDevice", "{}");
        Assert.AreEqual(2, reopened.GetProperty("schemaVersion").GetInt32());
    }

    [TestMethod]
    public async Task RouterRejectsOldWorkspaceScopeAndReplayedCurrentSequence()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        var oldScope = new WorkspaceWireScope
        {
            Scope = "workspace",
            WorkspaceId = fixture.Workspace!.WorkspaceId,
            SessionEpoch = fixture.Manager.Current.SessionEpoch,
            Sequence = 1,
            OperationId = Guid.NewGuid(),
        };
        await fixture.OpenAsync(folder: "second");
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}",
            "WORKSPACE_SESSION_UNAVAILABLE", oldScope);
        Assert.IsFalse(File.Exists(fixture.SettingsPath));
        var currentScope = oldScope with
        {
            WorkspaceId = fixture.Workspace!.WorkspaceId,
            SessionEpoch = fixture.Manager.Current.SessionEpoch,
        };
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}", scope: currentScope);
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{\"schemaVersion\":2}}",
            "WORKSPACE_SESSION_UNAVAILABLE", currentScope);
        JsonElement read = await fixture.RequestAsync("settings.readDevice", "{}");
        Assert.AreEqual(1, read.GetProperty("schemaVersion").GetInt32());
    }

    [TestMethod]
    public async Task MirroredWorkspaceUsesActivityRootNotReplicaOrGlobalRoot()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync(mirrored: true);
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}");
        Assert.IsTrue(File.Exists(Path.Combine(fixture.Root, "active", ".vibetable", "data", "state", "device-settings.json")));
        Assert.IsFalse(File.Exists(Path.Combine(fixture.Root, "workspace", ".vibetable", "data", "state", "device-settings.json")));
    }

    [TestMethod]
    public async Task LegacyAliasesAndUnboundedIntegerCoercionRoundTrip()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync(WorkspaceOpenMode.ReadOnly);
        JsonElement result = await fixture.RequestAsync("settings.saveDevice", """
            {"settings":{"schema_version":"2147483648","window_position":{"x":-1000000000000000000000000000000,"y":"-12","width":1.0,"maximized":true}}}
            """);
        Assert.AreEqual("2147483648", result.GetProperty("schemaVersion").GetRawText());
        JsonElement position = result.GetProperty("windowPosition");
        Assert.AreEqual("-1000000000000000000000000000000", position.GetProperty("x").GetRawText());
        Assert.AreEqual(-12, position.GetProperty("y").GetInt32());
        Assert.AreEqual(1, position.GetProperty("width").GetInt32());
        Assert.AreEqual(1, position.GetProperty("maximized").GetInt32());
        Assert.AreEqual(result.GetRawText(), (await fixture.RequestAsync("settings.readDevice", "{}")).GetRawText());
    }

    [TestMethod]
    public async Task IntegerStringsMatchPydanticInSaveAndLegacyRead()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        foreach (var (input, expected) in new[] { ("1_0", 10), ("1_000.0", 1000), ("+0.0", 0) })
        {
            string settings = JsonSerializer.Serialize(new { window_position = new { x = input } });
            JsonElement saved = await fixture.RequestAsync(
                "settings.saveDevice", "{\"settings\":" + settings + "}");
            Assert.AreEqual(expected, saved.GetProperty("windowPosition").GetProperty("x").GetInt32());
            await File.WriteAllTextAsync(fixture.SettingsPath, settings);
            JsonElement read = await fixture.RequestAsync("settings.readDevice", "{}");
            Assert.AreEqual(expected, read.GetProperty("windowPosition").GetProperty("x").GetInt32());
        }
        foreach (string input in new[] { "1.", "1_", "_1", "1__0", "1_.0", "1._0", "1.0_0", "1_000.0_0", ".0" })
        {
            string settings = JsonSerializer.Serialize(new { window_position = new { x = input } });
            await fixture.RequestAsync("settings.saveDevice", "{\"settings\":" + settings + "}", "BAD_PAYLOAD");
            await File.WriteAllTextAsync(fixture.SettingsPath, settings);
            JsonElement read = await fixture.RequestAsync("settings.readDevice", "{}");
            Assert.AreEqual(0, read.GetProperty("windowPosition").EnumerateObject().Count());
        }
    }

    [TestMethod]
    public async Task MissingAndCorruptSettingsRecoverWithoutRewritingLegacyFile()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        JsonElement defaults = await fixture.RequestAsync("settings.readDevice", "{}");
        Assert.AreEqual(1, defaults.GetProperty("schemaVersion").GetInt32());
        Assert.AreEqual("#2563eb", defaults.GetProperty("theme").GetProperty("accent").GetString());
        Assert.AreEqual("#ffffff", defaults.GetProperty("theme").GetProperty("background").GetString());
        Assert.AreEqual("#111827", defaults.GetProperty("theme").GetProperty("foreground").GetString());
        Assert.AreEqual(0, defaults.GetProperty("recentCollections").GetArrayLength());
        Assert.IsFalse(File.Exists(fixture.SettingsPath));
        Directory.CreateDirectory(Path.GetDirectoryName(fixture.SettingsPath)!);
        foreach (string corrupt in new[] { "{broken", "[]", "{\"schema_version\":0}", "{\"theme\":null}" })
        {
            await File.WriteAllTextAsync(fixture.SettingsPath, corrupt);
            Assert.AreEqual(defaults.GetRawText(), (await fixture.RequestAsync("settings.readDevice", "{}")).GetRawText());
            Assert.AreEqual(corrupt, await File.ReadAllTextAsync(fixture.SettingsPath));
        }
    }

    [TestMethod]
    public async Task CorruptLegacySettingsRetainValidSchemaVersionWithoutWriting()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        Directory.CreateDirectory(Path.GetDirectoryName(fixture.SettingsPath)!);
        foreach (string corrupt in new[]
        {
            "{\"schema_version\":2,\"theme\":{\"mode\":\"broken\"}}",
            "{\"schema_version\":2.5}",
        })
        {
            await File.WriteAllTextAsync(fixture.SettingsPath, corrupt);
            JsonElement recovered = await fixture.RequestAsync("settings.readDevice", "{}");
            Assert.AreEqual(2, recovered.GetProperty("schemaVersion").GetInt32());
            Assert.AreEqual("system", recovered.GetProperty("theme").GetProperty("mode").GetString());
            Assert.AreEqual(corrupt, await File.ReadAllTextAsync(fixture.SettingsPath));
        }
    }

    [TestMethod]
    public async Task CompleteValidationAndAtomicReplacementFailurePreservePreviousFile()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}");
        string previous = await File.ReadAllTextAsync(fixture.SettingsPath);
        string longTheme = string.Concat(Enumerable.Repeat("😀", 17));
        foreach (string invalid in new[]
        {
            "{\"schemaVersion\":0}", "{\"schemaVersion\":2.5}", "{\"windowPosition\":{\"x\":1.5}}", "{\"unknown\":1}",
            "{\"theme\":{\"mode\":\"other\"}}", "{\"recentCollections\":[1]}",
            JsonSerializer.Serialize(new { theme = new { accent = longTheme } }),
            JsonSerializer.Serialize(new { recentCollections = Enumerable.Repeat("x", 33) }),
        })
        {
            await fixture.RequestAsync("settings.saveDevice", "{\"settings\":" + invalid + "}", "BAD_PAYLOAD");
            Assert.AreEqual(previous, await File.ReadAllTextAsync(fixture.SettingsPath));
        }
        using (var locked = new FileStream(fixture.SettingsPath, FileMode.Open, FileAccess.Read, FileShare.Read))
            await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{\"schemaVersion\":2}}", "DEVICE_SETTINGS_WRITE_FAILED");
        Assert.AreEqual(previous, await File.ReadAllTextAsync(fixture.SettingsPath));
        Assert.AreEqual(0, Directory.GetFiles(Path.GetDirectoryName(fixture.SettingsPath)!, "*.tmp").Length);
        string validTheme = string.Concat(Enumerable.Repeat("😀", 16));
        JsonElement saved = await fixture.RequestAsync("settings.saveDevice",
            JsonSerializer.Serialize(new { settings = new { theme = new { accent = validTheme } } }));
        Assert.AreEqual(validTheme, saved.GetProperty("theme").GetProperty("accent").GetString());
    }

    [TestMethod]
    public async Task NoSessionMismatchedRuntimeAndDrainedEpochNeverWrite()
    {
        using var fixture = new Fixture();
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}", "WORKSPACE_SESSION_UNAVAILABLE");
        await fixture.OpenAsync();
        WorkspaceRegistryEntryV2 original = fixture.Workspace!;
        fixture.Workspace = original with { WorkspaceId = Guid.NewGuid() };
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}", "WORKSPACE_SESSION_UNAVAILABLE");
        fixture.Workspace = original;
        WorkspaceSessionV2 session = fixture.Manager.Current;
        await fixture.Filter.DrainAsync(original.WorkspaceId, session.SessionEpoch, CancellationToken.None);
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}", "WORKSPACE_SESSION_UNAVAILABLE");
        await fixture.Manager.CloseAsync("test close");
        await fixture.RequestAsync("settings.readDevice", "{}", "WORKSPACE_SESSION_UNAVAILABLE");
        Assert.IsFalse(File.Exists(fixture.SettingsPath));
    }

    [TestMethod]
    public async Task StoreRechecksRealEpochBeforeCommitAndReleasesDrainAfterCancellation()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}");
        string previous = await File.ReadAllTextAsync(fixture.SettingsPath);
        WorkspaceSessionV2 session = fixture.Manager.Current;
        Assert.IsTrue(fixture.Filter.TryCaptureHost(session.WorkspaceId!.Value, session.SessionEpoch, Guid.NewGuid(), out var lease));
        Task? draining = null;
        using (lease)
        {
            var store = new DeviceSettingsStore(WorkspaceLayout.Paths(fixture.Workspace!.SelectedRoot).Data);
            int guards = 0;
            await Assert.ThrowsAsync<OperationCanceledException>(() => store.SaveAsync(
                JsonSerializer.SerializeToElement(new { schemaVersion = 2 }),
                () =>
                {
                    if (++guards == 2)
                    {
                        draining = fixture.Filter.DrainAsync(session.WorkspaceId.Value, session.SessionEpoch, CancellationToken.None);
                        Assert.IsFalse(draining.IsCompleted);
                    }
                    if (!fixture.Filter.IsCurrent(lease)) throw new OperationCanceledException();
                }, lease!.CancellationToken));
            Assert.AreEqual(2, guards);
        }
        Assert.IsNotNull(draining);
        await draining.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(previous, await File.ReadAllTextAsync(fixture.SettingsPath));
    }

    [TestMethod]
    public async Task ControllerKeepsEpochLeaseThroughReplyUntilRealCloseDrains()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        Task? closing = null;
        fixture.OnResponse = () =>
        {
            closing = fixture.Manager.CloseAsync("close during reply");
            Assert.IsFalse(closing.IsCompleted);
        };
        await fixture.RequestAsync("settings.readDevice", "{}");
        Assert.IsNotNull(closing);
        await closing.WaitAsync(TimeSpan.FromSeconds(5));
        await fixture.RequestAsync("settings.saveDevice", "{\"settings\":{}}", "WORKSPACE_SESSION_UNAVAILABLE");
        Assert.IsFalse(File.Exists(fixture.SettingsPath));
    }

    [TestMethod]
    public void RouterRequiresExactClosedHostCapabilityAndKeepsStartupGate()
    {
        foreach (string method in new[] { "settings.readDevice", "settings.saveDevice" })
        {
            foreach (var capability in new[]
            {
                new ProductRpcCapability(method, "global", "rendererPublic", "settings", "pythonBff", "read"),
                new ProductRpcCapability(method, "workspace", "rendererPublic", "settings", "wpfHost", "read"),
                new ProductRpcCapability(method, "global", "hostOnly", "settings", "wpfHost", "read"),
            })
            {
                var router = new WebMessageRouter(
                    _ => Assert.Fail("Rejected capability dispatched."),
                    WorkspaceRpcCapabilityManifest.Default,
                    ProductRpcCapabilityManifest.CreateForTests(capability))
                { IsReady = true };
                var failure = router.Route(JsonSerializer.Serialize(new { type = method, payload = new { } }));
                Assert.AreEqual("CAPABILITY_NOT_PUBLIC", ((OperationFailedPayload)failure!.Payload!).Code);
            }
            var startup = new WebMessageRouter(_ => Assert.Fail("Startup request dispatched."));
            var startupFailure = startup.Route(JsonSerializer.Serialize(new { type = method, payload = new { } }));
            Assert.AreEqual("HOST_NOT_READY", ((OperationFailedPayload)startupFailure!.Payload!).Code);
            Assert.IsTrue(startup.IsHostNotificationAllowed(method));
        }
    }

    private sealed class Fixture : IDisposable, IWebReplySink, IWorkspaceRuntimeFactory
    {
        private readonly WorkspaceRegistry _registry;
        private readonly ConcurrentDictionary<string, TaskCompletionSource<RequestReply>> _pending = new();
        public Fixture()
        {
            Root = Path.Combine(Path.GetTempPath(), "vibetable-device-" + Guid.NewGuid().ToString("N"));
            _registry = new WorkspaceRegistry(Root);
            Manager = new WorkspaceSessionManager(_registry, this);
            Filter = new WorkspaceSessionEnvelopeFilter(Manager);
            Manager.SetRequestDrainHook(Filter);
            var controller = new DeviceSettingsRequestController(this, Filter, () => Workspace);
            Router = new WebMessageRouter(request => _ = controller.DispatchAsync(request)) { IsReady = true };
        }
        public string Root { get; }
        public WorkspaceRegistryEntryV2? Workspace { get; set; }
        public WorkspaceSessionManager Manager { get; }
        public WorkspaceSessionEnvelopeFilter Filter { get; }
        public WebMessageRouter Router { get; }
        public Action? OnResponse { get; set; }
        public string SettingsPath => Path.Combine(WorkspaceLayout.Paths(Workspace!.SelectedRoot).Data, "state", "device-settings.json");

        public async Task OpenAsync(
            WorkspaceOpenMode mode = WorkspaceOpenMode.Writable,
            bool mirrored = false,
            string folder = "workspace")
        {
            var created = WorkspaceLayout.Create(
                Path.Combine(Root, folder), "Device",
                mirrored ? WorkspaceStorageMode.Mirrored : WorkspaceStorageMode.Direct,
                WorkspaceEncryptionMode.Convenient,
                activityRoot: mirrored ? Path.Combine(Root, "active") : null);
            var entry = _registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = created.Manifest.WorkspaceId,
                DisplayName = "Device",
                SelectedRoot = created.SelectedRoot,
                ActivityRoot = mirrored ? created.ActivityRoot : null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
            if (Workspace is null) await Manager.OpenAsync(entry.WorkspaceId, mode);
            else await Manager.SwitchAsync(entry.WorkspaceId, mode);
        }
        public async Task<JsonElement> RequestAsync(
            string method, string payload,
            string? expectedError = null, WorkspaceWireScope? scope = null)
        {
            string requestId = Guid.NewGuid().ToString("N");
            var completion = new TaskCompletionSource<RequestReply>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            Assert.IsTrue(_pending.TryAdd(requestId, completion));
            string scopeJson = scope is null ? "" : ",\"scope\":" + JsonSerializer.Serialize(scope, WorkspaceV2Json.StrictOptions);
            try
            {
                HostReplyMessage? rejection = Router.Route($$"""{"type":"{{method}}","requestId":"{{requestId}}","payload":{{payload}}{{scopeJson}}} """);
                Assert.IsNull(rejection, rejection?.Payload?.ToString());
                RequestReply result = await completion.Task.WaitAsync(TimeSpan.FromSeconds(5));
                Assert.AreEqual(expectedError, result.Error);
                return result.Result;
            }
            finally
            {
                _pending.TryRemove(requestId, out _);
            }
        }
        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong sessionEpoch)
        {
            Workspace = workspace;
            return new Runtime(workspace.WorkspaceId, sessionEpoch);
        }
        public void PostResponse(string type, string? requestId, object? payload)
        {
            OnResponse?.Invoke();
            Complete(requestId, new RequestReply(JsonSerializer.SerializeToElement(payload), null));
        }
        public void PostNotification(string type, object? payload) => Assert.Fail("Unexpected notification.");
        public void PostOperationFailed(
            string? requestId, string message, string? code = null,
            string? operation = null, string? operationId = null) =>
            Complete(requestId, new RequestReply(default, code));
        private void Complete(string? requestId, RequestReply reply)
        {
            Assert.IsNotNull(requestId);
            Assert.IsTrue(_pending.TryGetValue(requestId, out var completion));
            completion.TrySetResult(reply);
        }
        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            Filter.Dispose();
            Directory.Delete(Root, recursive: true);
        }

        private sealed record RequestReply(JsonElement Result, string? Error);
    }

    private sealed class Runtime(Guid workspaceId, ulong sessionEpoch) : IWorkspaceRuntime
    {
        public Guid WorkspaceId => workspaceId;
        public ulong SessionEpoch => sessionEpoch;
        public Task StartAsync(WorkspaceOpenMode mode, WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task VerifyAsync(WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task DrainAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
