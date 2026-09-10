using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class GridPresentationRequestControllerTests
{
    [TestMethod]
    public async Task RealWorkspaceLeaseStoresAndReadsWithoutBackendAndRejectsRetiredScope()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        WorkspaceWireScope oldScope = fixture.Scope();
        await fixture.RequestAsync("gridState.get", "{\"table\":\"tbl_one\"}");
        string revision = fixture.Result.GetProperty("revision").GetString()!;
        await fixture.RequestAsync("gridState.save", JsonSerializer.Serialize(new
        {
            table = "tbl_one", state = new { keyword = "first workspace", columns = new[] { new { name = "title", width = 222 } } }, revision,
        }));
        Assert.AreEqual("first workspace", fixture.Result.GetProperty("state").GetProperty("keyword").GetString());
        await fixture.OpenAsync("second");
        await fixture.RequestAsync("gridState.get", "{\"table\":\"tbl_one\"}");
        Assert.AreEqual(JsonValueKind.Null, fixture.Result.GetProperty("state").GetProperty("keyword").ValueKind);
        await fixture.RequestAsync("gridState.save", "{\"table\":\"tbl_one\",\"state\":{},\"revision\":null}", oldScope);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", fixture.Error);
        await fixture.RequestAsync("gridState.get", "{\"table\":\"tbl_one\"}");
        Assert.AreEqual(JsonValueKind.Null, fixture.Result.GetProperty("state").GetProperty("keyword").ValueKind);
    }

    [TestMethod]
    public async Task ClosedPayloadsAndMissingScopeFailWithoutChangingStoredRevision()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        await fixture.RequestAsync("gridState.get", "{\"table\":\"tbl_one\"}");
        string revision = fixture.Result.GetProperty("revision").GetString()!;
        foreach (string invalid in new[]
        {
            "{\"table\":\"tbl_one\",\"state\":{}}",
            "{\"table\":\"tbl_one\",\"state\":{},\"revision\":null,\"path\":\"elsewhere\"}",
            "{\"table\":\"tbl_one\",\"state\":{\"unknown\":true},\"revision\":null}",
            "{\"table\":\"tbl_one\",\"table\":\"tbl_two\",\"state\":{},\"revision\":null}",
        })
        {
            await fixture.RequestAsync("gridState.save", invalid);
            Assert.AreEqual("BAD_PAYLOAD", fixture.Error);
        }
        await fixture.Controller.DispatchAsync(new RoutedWebRequest("gridState.get", "no-scope",
            JsonSerializer.SerializeToElement(new { table = "tbl_one" }), ""));
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", fixture.Error);
        await fixture.RequestAsync("gridState.get", "{\"table\":\"tbl_one\"}");
        Assert.AreEqual(revision, fixture.Result.GetProperty("revision").GetString());
    }

    [TestMethod]
    public async Task ReplyRetainsEpochLeaseUntilCloseCanDrain()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        Task? closing = null;
        fixture.OnReply = () =>
        {
            closing = fixture.Manager.CloseAsync("close during layout reply");
            Assert.IsFalse(closing.IsCompleted);
        };
        await fixture.RequestAsync("gridState.get", "{\"table\":\"tbl_one\"}");
        Assert.IsNotNull(closing);
        await closing.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual(WorkspaceSessionState.Closed, fixture.Manager.Current.State);
    }

    private sealed class Fixture : IDisposable, IWebReplySink, IWorkspaceRuntimeFactory
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(), "vibetable-grid-controller-" + Guid.NewGuid().ToString("N"));
        private readonly WorkspaceRegistry _registry;
        private readonly WorkspaceSessionEnvelopeFilter _filter;
        private ulong _sequence;
        private bool _opened;
        public Fixture()
        {
            _registry = new WorkspaceRegistry(_root);
            Manager = new WorkspaceSessionManager(_registry, this);
            _filter = new WorkspaceSessionEnvelopeFilter(Manager);
            Manager.SetRequestDrainHook(_filter);
            Controller = new GridPresentationRequestController(this, _filter, new HostGridStateStore(Path.Combine(_root, "layouts")));
        }
        public WorkspaceSessionManager Manager { get; }
        public GridPresentationRequestController Controller { get; }
        public JsonElement Result { get; private set; }
        public string? Error { get; private set; }
        public Action? OnReply { get; set; }
        public WorkspaceWireScope Scope() => new()
        {
            Scope = "workspace", WorkspaceId = Manager.Current.WorkspaceId!.Value,
            SessionEpoch = Manager.Current.SessionEpoch, Sequence = ++_sequence, OperationId = Guid.NewGuid(),
        };
        public async Task OpenAsync(string folder)
        {
            var created = WorkspaceLayout.Create(Path.Combine(_root, folder), "Grid",
                WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
            var entry = _registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion, WorkspaceId = created.Manifest.WorkspaceId,
                DisplayName = "Grid", SelectedRoot = created.SelectedRoot, ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed, CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null, LastKnownHealth = WorkspaceHealth.Healthy, LastSnapshotAt = null,
                LastSyncAt = null, PendingSync = false,
            });
            if (_opened) await Manager.SwitchAsync(entry.WorkspaceId, WorkspaceOpenMode.Writable);
            else await Manager.OpenAsync(entry.WorkspaceId, WorkspaceOpenMode.Writable);
            _opened = true;
        }
        public async Task RequestAsync(string method, string json, WorkspaceWireScope? scope = null)
        {
            Result = default;
            Error = null;
            using JsonDocument document = JsonDocument.Parse(json);
            await Controller.DispatchAsync(new RoutedWebRequest(method, Guid.NewGuid().ToString("N"),
                document.RootElement.Clone(), "", scope ?? Scope()));
        }
        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong epoch) => new Runtime(workspace.WorkspaceId, epoch);
        public void PostResponse(string type, string? requestId, object? payload)
        {
            OnReply?.Invoke();
            Result = JsonSerializer.SerializeToElement(payload, new JsonSerializerOptions(JsonSerializerDefaults.Web));
        }
        public void PostNotification(string type, object? payload) => Assert.Fail("Unexpected notification.");
        public void PostOperationFailed(string? requestId, string message, string? code = null,
            string? operation = null, string? operationId = null) => Error = code;
        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            _filter.Dispose();
            Directory.Delete(_root, recursive: true);
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
