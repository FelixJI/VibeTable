using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostCommandRequestControllerTests
{
    private const string Id = "11111111-1111-4111-8111-111111111111";
    private static string Saved(string target = "built-in-command", string? url = null) => JsonSerializer.Serialize(new
    {
        shortcut = new { shortcutId = Id, label = "Export", target, commandId = target == "built-in-command" ? "export.query" : null, url },
    });
    [TestMethod]
    public async Task DefinitionsPersistAcrossReopenAndAllSixRoutesExecute()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        await fixture.RequestAsync("command.list", "{}");
        Assert.AreEqual("export.query", fixture.Result.GetProperty("commands")[0].GetProperty("commandId").GetString());
        await fixture.RequestAsync("shortcut.save", Saved());
        Assert.IsNull(fixture.Error);
        var reopened = new HostShortcutStore(fixture.Storage);
        Assert.AreEqual(Id, (await reopened.ListAsync(() => { }, CancellationToken.None)).Single().ShortcutId);
        var old = fixture.Scope();
        await fixture.OpenAsync("second");
        await fixture.RequestAsync("shortcut.list", "{}");
        Assert.AreEqual(1, fixture.Result.GetProperty("shortcuts").GetArrayLength());
        await fixture.RequestAsync("shortcut.launch", JsonSerializer.Serialize(new { shortcutId = Id, @params = new { collection = "tbl_one", query = new { }, format = "csv" } }));
        Assert.IsTrue(fixture.Result.GetProperty("launched").GetBoolean());
        Assert.AreEqual(3, fixture.Result.GetProperty("output").GetProperty("rowsWritten").GetInt32());
        await fixture.RequestAsync("command.run", "{\"commandId\":\"export.query\",\"params\":{\"collection\":\"tbl_one\",\"query\":{},\"format\":\"xlsx\"}}");
        Assert.IsTrue(fixture.Result.GetProperty("success").GetBoolean());
        Assert.AreEqual(2, fixture.Actions.Exports);
        await fixture.RequestAsync("shortcut.delete", JsonSerializer.Serialize(new { shortcutId = Id }), old);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", fixture.Error);
        await fixture.RequestAsync("shortcut.delete", JsonSerializer.Serialize(new { shortcutId = Id }));
        Assert.AreEqual(0, (await reopened.ListAsync(() => { }, CancellationToken.None)).Count);
    }

    [TestMethod]
    public async Task InvalidTargetsAndPayloadsFailWithoutExecutionOrStorageChanges()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        await fixture.RequestAsync("shortcut.save", Saved());
        string before = await File.ReadAllTextAsync(Path.Combine(fixture.Storage, "shortcuts.json"));
        foreach (string url in new[] { "http://example.com", "file:///c:/test", "javascript:alert(1)", "https://user:password@example.com", "https:///", "https://example.com\\evil" })
        {
            await fixture.RequestAsync("shortcut.save", Saved("url", url));
            Assert.AreEqual("BAD_PAYLOAD", fixture.Error, url);
        }
        await fixture.RequestAsync("shortcut.save", Saved("file-action"));
        Assert.AreEqual("BAD_PAYLOAD", fixture.Error);
        foreach (string json in new[] {
            "{\"commandId\":\"shell\",\"params\":{}}",
            "{\"commandId\":\"export.query\",\"params\":{\"collection\":\"tbl_one\",\"format\":\"csv\",\"query\":{},\"path\":\"evil\"}}",
            "{\"commandId\":\"export.query\",\"params\":{},\"grantId\":\"untrusted\"}",
            "{\"commandId\":\"export.query\",\"commandId\":\"shell\",\"params\":{}}"
        }) {
            await fixture.RequestAsync("command.run", json);
            Assert.AreEqual("BAD_PAYLOAD", fixture.Error, json);
        }
        Assert.AreEqual(before, await File.ReadAllTextAsync(Path.Combine(fixture.Storage, "shortcuts.json")));
        Assert.AreEqual(0, fixture.Actions.Exports);
        Assert.AreEqual(0, fixture.Actions.Opens);
    }

    [TestMethod]
    public async Task HttpsConfirmationCancellationNeverReportsLaunched()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        await fixture.RequestAsync("shortcut.save", Saved("url", "https://example.com/path"));
        await fixture.RequestAsync("shortcut.launch", JsonSerializer.Serialize(new { shortcutId = Id }));
        Assert.IsFalse(fixture.Result.GetProperty("launched").GetBoolean());
        Assert.AreEqual("cancelled", fixture.Result.GetProperty("blockedReason").GetString());
        fixture.Actions.Confirm = true;
        await fixture.RequestAsync("shortcut.launch", JsonSerializer.Serialize(new { shortcutId = Id }));
        Assert.IsTrue(fixture.Result.GetProperty("launched").GetBoolean());
        Assert.AreEqual(2, fixture.Actions.Opens);
    }

    [TestMethod]
    public async Task EpochRetirementCancelsExecutionAndDrainsWithoutLateReply()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Actions.BeforeExport = async token => { entered.SetResult(); await Task.Delay(Timeout.InfiniteTimeSpan, token); };
        Task running = fixture.RequestAsync("command.run", "{\"commandId\":\"export.query\",\"params\":{\"collection\":\"tbl_one\",\"query\":{},\"format\":\"csv\"}}");
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(2));
        await fixture.Manager.CloseAsync("retire command").WaitAsync(TimeSpan.FromSeconds(2));
        await running.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(JsonValueKind.Undefined, fixture.Result.ValueKind);
        Assert.IsNull(fixture.Error);
    }

    [TestMethod]
    public async Task StoreRejectsStaleCommitAndCorruptDocumentWithoutOverwriting()
    {
        using var fixture = new Fixture();
        await fixture.OpenAsync("first");
        await fixture.RequestAsync("shortcut.save", Saved());
        string path = Path.Combine(fixture.Storage, "shortcuts.json");
        string before = await File.ReadAllTextAsync(path);
        var store = new HostShortcutStore(fixture.Storage);
        int calls = 0;
        await Assert.ThrowsAsync<OperationCanceledException>(() => store.SaveAsync(
            new HostShortcut(Id, "url", "changed", Url: "https://example.com"),
            () => { if (++calls == 2) throw new OperationCanceledException(); }, CancellationToken.None));
        Assert.AreEqual(before, await File.ReadAllTextAsync(path));
        await File.WriteAllTextAsync(path, "{broken");
        await fixture.RequestAsync("shortcut.save", Saved());
        Assert.AreEqual("BAD_PAYLOAD", fixture.Error);
        Assert.AreEqual("{broken", await File.ReadAllTextAsync(path));
    }
    private sealed class Fixture : IDisposable, IWebReplySink, IWorkspaceRuntimeFactory
    {
        private readonly string _root = Path.Combine(Path.GetTempPath(), "vibetable-command-controller-" + Guid.NewGuid().ToString("N"));
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
            Controller = new HostCommandRequestController(this, _filter, new HostShortcutStore(Path.Combine(_root, "commands")), Actions);
        }
        public Actions Actions { get; } = new();
        public string Storage => Path.Combine(_root, "commands");
        public WorkspaceSessionManager Manager { get; }
        public HostCommandRequestController Controller { get; }
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
    private sealed class Actions : IHostCommandActions
    {
        public int Exports;
        public int Opens;
        public bool Confirm;
        public Func<CancellationToken, Task>? BeforeExport;
        public async Task<JsonElement> ExportAsync(JsonElement parameters, Action current, CancellationToken token)
        {
            Exports++;
            if (BeforeExport is not null) await BeforeExport(token);
            current();
            return JsonSerializer.SerializeToElement(new { rowsWritten = 3, outputDisplayName = "export.csv" });
        }
        public Task<bool> OpenHttpsAsync(Uri uri, Action current, CancellationToken token)
        {
            current(); Opens++; return Task.FromResult(Confirm);
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
