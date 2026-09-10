using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class HostGridStateStoreTests
{
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
