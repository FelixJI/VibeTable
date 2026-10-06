using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostSourceImportRequestControllerTests
{
    [TestMethod]
    public async Task OnlyClosedProviderChoiceReachesNativeHost()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        foreach (string invalid in new[]
        {
            "{}", "null", "{\"provider\":\"other\"}",
            "{\"provider\":\"feishu\",\"token\":\"secret\"}",
            "{\"provider\":\"feishu\",\"provider\":\"wps\"}",
        })
        {
            await fixture.RequestAsync(invalid);
            Assert.AreEqual("SOURCE_IMPORT_BAD_PAYLOAD", fixture.Error);
        }
        Assert.AreEqual(0, fixture.Opens);
        await fixture.RequestAsync("{\"provider\":\"feishu\"}");
        Assert.AreEqual("feishu", fixture.Provider);
        Assert.IsFalse(fixture.Result.GetProperty("cancelled").GetBoolean());
        Assert.AreEqual("task-one", fixture.Result.GetProperty("taskId").GetString());
        Assert.AreEqual(2, fixture.Result.EnumerateObject().Count());
    }

    [TestMethod]
    public async Task CancellationAndProviderErrorsDoNotLeakNativeSecrets()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        fixture.Action = (_, _) => Task.FromResult(new HostSourceImportOpenResult(true, null));
        await fixture.RequestAsync("{\"provider\":\"wps\"}");
        Assert.IsTrue(fixture.Result.GetProperty("cancelled").GetBoolean());
        fixture.Action = (_, _) => throw new HttpRequestException("Bearer secret https://example.test/?token=secret");
        await fixture.RequestAsync("{\"provider\":\"wps\"}");
        Assert.AreEqual("SOURCE_IMPORT_UNAVAILABLE", fixture.Error);
        Assert.IsFalse(fixture.ErrorMessage!.Contains("secret", StringComparison.Ordinal));
        Assert.IsFalse(fixture.ErrorMessage!.Contains("https:", StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task RetiringWorkspaceCancelsWizardWithoutLateResponse()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Action = async (_, token) =>
        {
            entered.SetResult();
            await Task.Delay(Timeout.InfiniteTimeSpan, token);
            return new(false, "unreachable");
        };
        Task running = fixture.RequestAsync("{\"provider\":\"feishu\"}");
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await fixture.Manager.CloseAsync("retire cloud wizard").WaitAsync(TimeSpan.FromSeconds(2));
        await running.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(JsonValueKind.Undefined, fixture.Result.ValueKind);
        Assert.IsNull(fixture.Error);
    }

    [TestMethod]
    public async Task ConcurrentOpenIsRejectedAndGateReopensAfterCancel()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync();
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var finish = new TaskCompletionSource<HostSourceImportOpenResult>(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Action = (_, _) => { entered.SetResult(); return finish.Task; };
        Task first = fixture.RequestAsync("{\"provider\":\"feishu\"}");
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await fixture.RequestAsync("{\"provider\":\"wps\"}");
        Assert.AreEqual("SOURCE_IMPORT_BUSY", fixture.Error);
        Assert.AreEqual(1, fixture.Opens);
        finish.SetResult(new(true, null));
        await first;
        fixture.Action = (_, _) => Task.FromResult(new HostSourceImportOpenResult(true, null));
        await fixture.RequestAsync("{\"provider\":\"wps\"}");
        Assert.AreEqual(2, fixture.Opens);
        Assert.IsNull(fixture.Error);
    }

    private sealed class Fixture : IDisposable, IWebReplySink, IWorkspaceRuntimeFactory, IHostSourceImportActions
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(), "vibetable-source-wizard-" + Guid.NewGuid().ToString("N"));
        private readonly WorkspaceRegistry _registry;
        private readonly WorkspaceSessionEnvelopeFilter _filter;
        private ulong _sequence;
        public Fixture()
        {
            _registry = new WorkspaceRegistry(_root);
            Manager = new WorkspaceSessionManager(_registry, this);
            _filter = new WorkspaceSessionEnvelopeFilter(Manager);
            Manager.SetRequestDrainHook(_filter);
            Controller = new HostSourceImportRequestController(this, _filter, this);
        }
        public WorkspaceSessionManager Manager { get; }
        public HostSourceImportRequestController Controller { get; }
        public JsonElement Result { get; private set; }
        public string? Error { get; private set; }
        public string? ErrorMessage { get; private set; }
        public int Opens { get; private set; }
        public string? Provider { get; private set; }
        public Func<string, CancellationToken, Task<HostSourceImportOpenResult>> Action { get; set; }
            = (_, _) => Task.FromResult(new HostSourceImportOpenResult(false, "task-one"));
        public async Task OpenAsync()
        {
            var created = WorkspaceLayout.Create(Path.Combine(_root, "workspace"), "Source wizard test",
                WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
            var entry = _registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion, WorkspaceId = created.Manifest.WorkspaceId,
                DisplayName = "Source wizard test", SelectedRoot = created.SelectedRoot, ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed, CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null, LastKnownHealth = WorkspaceHealth.Healthy, LastSnapshotAt = null,
                LastSyncAt = null, PendingSync = false,
            });
            await Manager.OpenAsync(entry.WorkspaceId, WorkspaceOpenMode.Writable);
        }
        public Task RequestAsync(string json)
        {
            Result = default;
            Error = null;
            ErrorMessage = null;
            using JsonDocument document = JsonDocument.Parse(json);
            var scope = new WorkspaceWireScope
            {
                Scope = "workspace", WorkspaceId = Manager.Current.WorkspaceId!.Value,
                SessionEpoch = Manager.Current.SessionEpoch, Sequence = ++_sequence, OperationId = Guid.NewGuid(),
            };
            return Controller.DispatchAsync(new RoutedWebRequest("sourceImport.open", Guid.NewGuid().ToString("N"),
                document.RootElement.Clone(), "", scope));
        }
        public Task<HostSourceImportOpenResult> OpenAsync(string provider, Action ensureCurrent, CancellationToken token)
        {
            ensureCurrent();
            Opens++;
            Provider = provider;
            return Action(provider, token);
        }
        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong epoch) => new Runtime(workspace.WorkspaceId, epoch);
        public void PostResponse(string type, string? requestId, object? payload)
            => Result = JsonSerializer.SerializeToElement(payload, new JsonSerializerOptions(JsonSerializerDefaults.Web));
        public void PostNotification(string type, object? payload) => Assert.Fail("Unexpected notification.");
        public void PostOperationFailed(string? requestId, string message, string? code = null,
            string? operation = null, string? operationId = null) { Error = code; ErrorMessage = message; }
        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            _filter.Dispose();
            string resolved = Path.GetFullPath(_root);
            string parent = Path.GetFullPath(Path.GetTempPath()).TrimEnd(Path.DirectorySeparatorChar);
            if (!string.Equals(Path.GetDirectoryName(resolved), parent, StringComparison.OrdinalIgnoreCase)
                || !Path.GetFileName(resolved).StartsWith("vibetable-source-wizard-", StringComparison.Ordinal))
                throw new InvalidOperationException("Unexpected synthetic test root.");
            if (Directory.Exists(resolved)) Directory.Delete(resolved, recursive: true);
        }
    }
    private sealed class Runtime(Guid id, ulong epoch) : IWorkspaceRuntime
    {
        public Guid WorkspaceId => id;
        public ulong SessionEpoch => epoch;
        public Task StartAsync(WorkspaceOpenMode mode, WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task VerifyAsync(WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task DrainAsync(CancellationToken token) => Task.CompletedTask;
        public Task StopAsync(CancellationToken token) => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
