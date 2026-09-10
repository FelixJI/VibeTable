using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostGridStateStoreTests
{
    [TestMethod]
    [DataRow("{\"presetId\":\"preset-orders\"}")]
    [DataRow("{\"sorts\":[{\"field\":\"amount\",\"direction\":\"sideways\"}]}")]
    [DataRow("{\"filters\":[{\"field\":\"amount\",\"operator\":\"eq\",\"filters\":[{\"field\":\"title\",\"operator\":\"eq\"}]}]}")]
    [DataRow("{\"filters\":[{\"filters\":[{\"filters\":[{\"filters\":[{\"filters\":[{\"field\":\"title\",\"operator\":\"eq\"}]}]}]}]}]}")]
    public async Task InvalidPresetOrQueryTreeDoesNotReplaceConfirmedState(string json)
    {
        using var fixture = new Fixture();
        var store = new HostGridStateStore(fixture.Root);
        Guid workspace = Guid.NewGuid();
        var before = await store.ReadAsync(workspace, "orders", CancellationToken.None);
        var state = JsonSerializer.Deserialize<GridState>(json, new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
        await Assert.ThrowsAsync<ArgumentException>(() => store.SaveAsync(workspace, "orders", state,
            before.Revision, () => { }, CancellationToken.None));
        var after = await store.ReadAsync(workspace, "orders", CancellationToken.None);
        Assert.AreEqual(before.Revision, after.Revision);
    }
    [TestMethod]
    public async Task RecursiveFiltersAndPresetBindingSurviveReopen()
    {
        using var fixture = new Fixture();
        var store = new HostGridStateStore(fixture.Root);
        Guid workspace = Guid.NewGuid();
        var initial = await store.ReadAsync(workspace, "orders", CancellationToken.None);
        var options = new JsonSerializerOptions(JsonSerializerDefaults.Web);
        var state = JsonSerializer.Deserialize<GridState>("""
            {"filters":[{"groupLogic":"OR","filters":[
                {"field":"amount","operator":"eq","value":9007199254740993},
                {"groupLogic":"AND","filters":[{"field":"title","operator":"contains","value":"订单"}]}
            ]}],"presetId":"preset-orders","presetRevision":"revision-1"}
            """, options)!;
        await store.SaveAsync(workspace, "orders", state, initial.Revision, () => { }, CancellationToken.None);
        var reopened = await new HostGridStateStore(fixture.Root).ReadAsync(workspace, "orders", CancellationToken.None);
        var wire = JsonSerializer.SerializeToElement(reopened.State, options);
        Assert.IsTrue(wire.TryGetProperty("presetId", out var preset));
        Assert.AreEqual("preset-orders", preset.GetString());
        Assert.AreEqual("revision-1", wire.GetProperty("presetRevision").GetString());
        var group = wire.GetProperty("filters")[0];
        Assert.AreEqual("OR", group.GetProperty("groupLogic").GetString());
        Assert.AreEqual(9007199254740993L, group.GetProperty("filters")[0].GetProperty("value").GetInt64());
        Assert.AreEqual("AND", group.GetProperty("filters")[1].GetProperty("groupLogic").GetString());
    }
    [TestMethod]
    public async Task ReopenKeepsCompleteStateAndRejectsStaleRevision()
    {
        using var fixture = new Fixture();
        var store = new HostGridStateStore(fixture.Root);
        Guid workspace = Guid.NewGuid();
        GridStateResult initial = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        Assert.IsFalse(initial.Conflict);
        Assert.IsFalse(string.IsNullOrEmpty(initial.Revision));
        var state = new GridState(
            Columns: [new ColumnState("金额", 180, false, true, 2)],
            Sorts: [new SortCondition("金额", "desc", false)], Filters: [new FilterCondition("金额", "eq", 9007199254740993L)], Keyword: "中文 👩‍💻", Density: "compact", ForcedRemote: true);
        GridStateResult saved = await store.SaveAsync(workspace, "tbl_one", state,
            initial.Revision, () => { }, CancellationToken.None);
        Assert.IsFalse(saved.Conflict);
        var reopened = new HostGridStateStore(fixture.Root);
        GridStateResult read = await reopened.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        Assert.AreEqual(saved.Revision, read.Revision);
        Assert.AreEqual("中文 👩‍💻", read.State.Keyword);
        Assert.AreEqual(state.Columns![0], read.State.Columns![0]);
        Assert.AreEqual("compact", read.State.Density);
        Assert.IsTrue(read.State.ForcedRemote);
        Assert.AreEqual(state.Sorts![0], read.State.Sorts![0]);
        Assert.AreEqual(9007199254740993L, ((JsonElement)read.State.Filters![0].Value!).GetInt64());
        GridStateResult conflict = await reopened.SaveAsync(workspace, "tbl_one", new GridState(),
            initial.Revision, () => { }, CancellationToken.None);
        Assert.IsTrue(conflict.Conflict);
        Assert.AreEqual(saved.Revision, conflict.Revision);
        Assert.AreEqual(read.State.Keyword, conflict.State.Keyword);
    }

    [TestMethod]
    public async Task WorkspaceAndTableRemainIndependentAndExpiredWriteDoesNotCommit()
    {
        using var fixture = new Fixture();
        var store = new HostGridStateStore(fixture.Root);
        Guid workspace = Guid.NewGuid();
        GridStateResult initial = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        await Assert.ThrowsAsync<OperationCanceledException>(() => store.SaveAsync(
            workspace, "tbl_one", new GridState(Keyword: "stale"), initial.Revision,
            () => throw new OperationCanceledException(), CancellationToken.None));
        GridStateResult unchanged = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        Assert.AreEqual(initial.Revision, unchanged.Revision);
        Assert.IsNull(unchanged.State.Keyword);
        await store.SaveAsync(workspace, "tbl_one", new GridState(Keyword: "only A"),
            initial.Revision, () => { }, CancellationToken.None);
        GridStateResult otherTable = await store.ReadAsync(workspace, "tbl_two", CancellationToken.None);
        GridStateResult otherWorkspace = await store.ReadAsync(Guid.NewGuid(), "tbl_one", CancellationToken.None);
        Assert.IsNull(otherTable.State.Keyword);
        Assert.IsNull(otherWorkspace.State.Keyword);
        Assert.AreEqual(0, Directory.GetFiles(fixture.Root, "*.tmp", SearchOption.AllDirectories).Length);
    }

    [TestMethod]
    public async Task InvalidLayoutCannotReplaceConfirmedState()
    {
        using var fixture = new Fixture();
        var store = new HostGridStateStore(fixture.Root);
        Guid workspace = Guid.NewGuid();
        GridStateResult initial = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        await Assert.ThrowsAsync<ArgumentException>(() => store.SaveAsync(workspace, "tbl_one",
            new GridState(Columns: [new ColumnState("amount", 4097)]), initial.Revision,
            () => { }, CancellationToken.None));
        GridStateResult read = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        Assert.AreEqual(initial.Revision, read.Revision);
        await Assert.ThrowsAsync<ArgumentException>(() => store.ReadAsync(Guid.Empty, "tbl_one", CancellationToken.None));
    }

    [TestMethod]
    public async Task LeaseExpiredAfterTemporaryWritePreservesPriorFileAndRemovesTemporary()
    {
        using var fixture = new Fixture();
        var store = new HostGridStateStore(fixture.Root);
        Guid workspace = Guid.NewGuid();
        GridStateResult initial = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        int guards = 0;
        await Assert.ThrowsAsync<OperationCanceledException>(() => store.SaveAsync(
            workspace, "tbl_one", new GridState(Keyword: "must not publish"), initial.Revision,
            () => { if (++guards == 2) throw new OperationCanceledException(); }, CancellationToken.None));
        Assert.AreEqual(2, guards);
        GridStateResult read = await store.ReadAsync(workspace, "tbl_one", CancellationToken.None);
        Assert.AreEqual(initial.Revision, read.Revision);
        Assert.IsNull(read.State.Keyword);
        Assert.AreEqual(0, Directory.GetFiles(fixture.Root, "*.tmp", SearchOption.AllDirectories).Length);
    }

    [TestMethod]
    public async Task CorruptDocumentFailsWithoutReplacingItWithDefaultState()
    {
        using var fixture = new Fixture();
        Guid workspace = Guid.NewGuid();
        string directory = Path.Combine(fixture.Root, workspace.ToString("N"));
        Directory.CreateDirectory(directory);
        string path = Path.Combine(directory, "grid-state.json");
        await File.WriteAllTextAsync(path, "{broken");
        var store = new HostGridStateStore(fixture.Root);
        await Assert.ThrowsAsync<JsonException>(() => store.ReadAsync(workspace, "tbl_one", CancellationToken.None));
        Assert.AreEqual("{broken", await File.ReadAllTextAsync(path));
    }
    private sealed class Fixture : IDisposable
    {
        public string Root { get; } = Path.Combine(Path.GetTempPath(), "VibeTable-grid-state-tests", Guid.NewGuid().ToString("N"));
        public void Dispose()
        {
            if (Directory.Exists(Root)) Directory.Delete(Root, recursive: true);
        }
    }
}
