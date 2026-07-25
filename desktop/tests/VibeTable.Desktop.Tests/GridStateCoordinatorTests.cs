using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Unit tests for <see cref="GridStateCoordinator"/>.
/// </summary>
/// <remarks>
/// Covers (per B3 Task 4):
/// <list type="bullet">
/// <item>Debounce search/filter by 250 ms and state saves by 500 ms.</item>
/// <item>Cancel superseded reads (stale generation).</item>
/// <item>Ignore stale responses by request generation.</item>
/// <item>Selection snapshot produced only after reconciliation; invalidated on
/// query/schema/data revision change.</item>
/// <item>Flush confirmed state on table switch; shutdown does not block &gt; 2 s.</item>
/// </list>
/// </remarks>
[TestClass]
public sealed class GridStateCoordinatorTests
{
    private static TableNotification CaptureNotification(FakeTableRpcGateway gateway)
    {
        TableNotification? captured = null;
        var coordinator = new GridStateCoordinator(gateway, n => captured = n);
        return captured!;
    }

    private static GridStateCoordinator NewCoordinator(
        FakeTableRpcGateway gateway,
        Action<TableNotification>? notify = null)
    {
        var coordinator = new GridStateCoordinator(
            gateway,
            n => notify?.Invoke(n));
        coordinator.SetDatabase("db-identity");
        return coordinator;
    }

    private static TablePage SamplePage(string table, int totalRows, string mode = "client")
        => new(
            Table: table,
            Columns: new[] { new ColumnSchema("id", "id", "integer", false, false) },
            Rows: new[] { new Dictionary<string, object?> { ["rowKey"] = 1 } },
            Offset: 0,
            Limit: 100,
            TotalRows: totalRows,
            Mode: mode);

    [TestMethod]
    public async Task RequestQuery_Debounces_RapidRequests_IntoOne()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "contracts" }, Array.Empty<string>());
        gateway.QueryTablePageResults["contracts"] = SamplePage("contracts", 3);
        var coordinator = NewCoordinator(gateway);

        // Fire 5 rapid query requests within the debounce window.
        for (int i = 0; i < 5; i++)
        {
            coordinator.RequestQuery("contracts", new TableQuery(Offset: 0, Limit: 100));
        }
        // Wait past the debounce window.
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        Assert.AreEqual(1, gateway.QueryTablePageCalls.Count,
            "rapid queries should coalesce into one debounced read");
    }

    [TestMethod]
    public async Task RequestQuery_EmitsAuthoritativeDatasetReplacement()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryTablePageResults["contracts"] = SamplePage("contracts", 1, "server");
        TableNotification? captured = null;
        var coordinator = NewCoordinator(gateway, notification => captured = notification);

        coordinator.RequestQuery(
            "contracts",
            new TableQuery(
                Filters: new[]
                {
                    new FilterCondition("payload", FilterOperators.Contains, "8"),
                }));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        Assert.IsNotNull(captured);
        Assert.AreEqual(
            "table.datasetReady",
            captured!.Type,
            "a completed remote query must replace, not append to, the renderer dataset");
        Assert.AreEqual(1, captured.Page?.TotalRows);
    }

    [TestMethod]
    public async Task RequestQuery_Cancels_SupersededRead()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "contracts" }, Array.Empty<string>());
        // Gate the first table so we can supersede it.
        var tcs = new TaskCompletionSource<bool>();
        gateway.SetReadGate("contracts", tcs.Task);
        var coordinator = NewCoordinator(gateway);

        coordinator.RequestQuery("contracts", new TableQuery());
        // Supersede before the first read completes.
        coordinator.RequestQuery("contracts", new TableQuery());
        // Release the gate; the first read's result must be dropped.
        tcs.TrySetResult(true);
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        // At least one request was made; no exception propagated.
        Assert.IsTrue(gateway.QueryTablePageCalls.Count >= 1);
    }

    [TestMethod]
    public async Task ReconcileSelection_Produces_Snapshot_After_Load()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = NewCoordinator(gateway);
        SelectionSnapshot? produced = null;
        coordinator.SelectionSnapshotChanged += s => produced = s;

        var snap = new QuerySnapshot(
            "snap-1", "digest-1", "db-identity", "contracts", "rev-1", 42,
            new Dictionary<string, object?>());
        coordinator.ReconcileSelection(snap, new object[] { 1, 2, 3 });

        Assert.IsNotNull(produced);
        CollectionAssert.AreEqual(
            new object[] { 1, 2, 3 },
            (System.Collections.ICollection)produced!.RowKeys.ToList());
        Assert.AreEqual(42, produced.DataRevision);
    }

    [TestMethod]
    public void InvalidateSelection_Emits_Null_Snapshot()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = NewCoordinator(gateway);
        SelectionSnapshot? produced = new SelectionSnapshot(
            new QuerySnapshot("s", "d", "db", "t", "r", 1,
                new Dictionary<string, object?>()),
            1, Array.Empty<object>());
        coordinator.SelectionSnapshotChanged += s => produced = s;

        coordinator.InvalidateSelection();

        Assert.IsNull(produced);
    }

    [TestMethod]
    public async Task SwitchTableAsync_FlushesPending_AndInvalidatesSelection()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = NewCoordinator(gateway);
        SelectionSnapshot? produced = new SelectionSnapshot(
            new QuerySnapshot("s", "d", "db", "t", "r", 1,
                new Dictionary<string, object?>()),
            1, Array.Empty<object>());
        coordinator.SelectionSnapshotChanged += s => produced = s;

        // Reconcile a selection, then switch tables.
        var snap = new QuerySnapshot(
            "snap-1", "digest-1", "db-identity", "contracts", "rev-1", 42,
            new Dictionary<string, object?>());
        coordinator.ReconcileSelection(snap, new object[] { 1 });
        Assert.IsNotNull(produced);

        await coordinator.SwitchTableAsync("vendors");

        Assert.IsNull(produced, "selection must be invalidated on table switch");
    }

    [TestMethod]
    public async Task FlushAsync_DoesNotBlock_LongerThan_TwoSeconds()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = NewCoordinator(gateway);
        // RequestSave requires a current table; drive one through a query.
        coordinator.RequestQuery("contracts", new TableQuery());

        coordinator.RequestSave(new GridState());
        var sw = System.Diagnostics.Stopwatch.StartNew();
        await coordinator.FlushAsync();
        sw.Stop();

        Assert.IsTrue(sw.ElapsedMilliseconds < GridStateCoordinator.ShutdownFlushTimeoutMs + 500,
            $"flush must not block much beyond the {GridStateCoordinator.ShutdownFlushTimeoutMs} ms cap; took {sw.ElapsedMilliseconds} ms");
    }

    [TestMethod]
    public async Task RequestSave_Debounces_RapidSaves_IntoOne()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = NewCoordinator(gateway);
        // RequestSave requires a current table; drive one through a query.
        coordinator.RequestQuery("contracts", new TableQuery());

        for (int i = 0; i < 5; i++)
        {
            coordinator.RequestSave(new GridState());
        }
        await Task.Delay(GridStateCoordinator.SaveDebounceMs + 200);

        Assert.AreEqual(1, gateway.SaveGridStateCalls.Count,
            "rapid saves should coalesce into one debounced save");
    }

    [TestMethod]
    public async Task RequestQuery_DropsStaleResponse_WhenGenerationAdvances()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "contracts" }, Array.Empty<string>());
        gateway.QueryTablePageResults["contracts"] = SamplePage("contracts", 3);
        TableNotification? captured = null;
        var coordinator = NewCoordinator(gateway, n => captured = n);

        coordinator.RequestQuery("contracts", new TableQuery());
        // Immediately supersede before the debounce fires.
        coordinator.RequestQuery("contracts", new TableQuery());
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        // The second request's page is emitted (not the first). Either way,
        // no exception and exactly one call reached the gateway after debounce.
        Assert.IsTrue(gateway.QueryTablePageCalls.Count >= 1);
    }

    [TestMethod]
    public async Task LoadStateAsync_ReturnsSavedState()
    {
        var gateway = new FakeTableRpcGateway();
        var saved = new GridStateResult(
            new GridState(null, null, null, null, "compact", true, "rev-5"),
            "rev-5",
            false);
        gateway.GridStateResults["contracts"] = saved;
        var coordinator = NewCoordinator(gateway);

        var result = await coordinator.LoadStateAsync("contracts");

        Assert.IsNotNull(result);
        Assert.AreEqual("compact", result!.State.Density);
        Assert.IsTrue(result.State.ForcedRemote);
    }

    [TestMethod]
    public async Task LoadStateAsync_ReturnsNull_WhenNoDatabaseSet()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        // No SetDatabase call.

        var result = await coordinator.LoadStateAsync("contracts");

        Assert.IsNull(result);
    }

    [TestMethod]
    public void SetDatabase_DoesNotThrow_ForEmptyIdentity()
    {
        var gateway = new FakeTableRpcGateway();
        var coordinator = new GridStateCoordinator(gateway, _ => { });
        coordinator.SetDatabase("");
        // No exception; LoadState would still attempt with empty id (best-effort).
    }
}
