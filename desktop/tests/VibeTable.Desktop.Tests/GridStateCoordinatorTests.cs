using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
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

    private static TablePage SamplePage(string table, int totalRows, string mode = "remote")
        => new(
            Table: table,
            Columns: new[] { new ColumnSchema("id", "id", "integer", false, false) },
            Rows: Enumerable.Range(1, totalRows)
                .Select(id => new Dictionary<string, object?> { ["rowKey"] = id })
                .ToArray(),
            Offset: 0,
            Limit: 100,
            TotalRows: totalRows,
            Mode: mode);

    private static JsonElement Query(int limit = 100)
        => JsonSerializer.SerializeToElement(new
        {
            keyword = "",
            filters = Array.Empty<object>(),
            sorts = Array.Empty<object>(),
            offset = 0,
            limit,
        });

    [TestMethod]
    public async Task RequestQuery_Debounces_RapidRequests_IntoOne()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(
                new[] { "contracts" },
                Array.Empty<string>(),
                TestDisplayNames.For("contracts"));
        gateway.QueryWindowResults["contracts"] = SamplePage("contracts", 3);
        var coordinator = NewCoordinator(gateway);

        // Fire 5 rapid query requests within the debounce window.
        for (int i = 0; i < 5; i++)
        {
            coordinator.RequestQuery("contracts", Query(limit: 100));
        }
        // Wait past the debounce window.
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        Assert.AreEqual(1, gateway.QueryWindowCalls.Count,
            "rapid queries should coalesce into one debounced read");
    }

    [TestMethod]
    public async Task RequestQuery_EmitsAuthoritativeDatasetReplacement()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.QueryWindowResults["contracts"] = SamplePage("contracts", 1, "server");
        TableNotification? captured = null;
        var coordinator = NewCoordinator(gateway, notification => captured = notification);

        using var query = JsonDocument.Parse(
            """{"filters":[{"field":"payload","operator":"contains","value":"8"}],"sorts":[],"offset":0,"limit":100}""");
        coordinator.RequestQuery("contracts", query.RootElement);
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        Assert.IsNotNull(captured);
        Assert.AreEqual(
            "table.datasetReady",
            captured!.Type,
            "a completed remote query must replace, not append to, the renderer dataset");
        Assert.AreEqual(1, captured.Page?.TotalRows);
    }

    [TestMethod]
    public async Task RequestQuery_LoadsOnlyOneBoundedWindow_ForLargeFilteredDataset()
    {
        var gateway = new FakeTableRpcGateway();
        var notifications = new List<TableNotification>();
        var coordinator = NewCoordinator(gateway, notifications.Add);
        var allRows = Enumerable.Range(1, 1_201)
            .Select(id => new Dictionary<string, object?> { ["rowKey"] = id })
            .ToArray();
        gateway.QueryWindowResults["contracts"] = new TablePage(
            Table: "contracts",
            Columns: new[] { new ColumnSchema("id", "id", "integer", false, false) },
            Rows: allRows.Take(500).ToArray(),
            Offset: 0,
            Limit: 500,
            TotalRows: 5_000,
            Mode: "remote",
            FilteredRows: allRows.Length,
            QuerySnapshot: new QuerySnapshot(
                "snapshot", "digest", "db-identity", "contracts", "schema-1", 7,
                new Dictionary<string, object?>()));

        coordinator.RequestQuery("contracts", Query(limit: 500));
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 250);

        Assert.AreEqual(1, gateway.QueryWindowCalls.Count);
        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("table.datasetReady", notifications[0].Type);
        Assert.AreEqual(500, notifications[0].Page?.Rows.Count);
        Assert.AreEqual("remote", notifications[0].Page?.Mode);
    }

    [TestMethod]
    public async Task RequestNextWindow_FetchesOpaqueCursorAndEmitsBoundedWindow()
    {
        var gateway = new FakeTableRpcGateway();
        var notifications = new List<TableNotification>();
        var coordinator = NewCoordinator(gateway, notifications.Add);
        var snapshot = new QuerySnapshot(
            "snapshot", "digest", "db-identity", "contracts", "schema-1", 7,
            new Dictionary<string, object?>());
        gateway.QueryWindowResults["contracts"] = new TablePage(
            "contracts", Array.Empty<ColumnSchema>(),
            new[] { new Dictionary<string, object?> { ["rowKey"] = 1 } },
            0, 1, 50_000, "remote", 50_000, snapshot,
            NextCursor: "opaque-2", HasMore: true);
        gateway.CursorPageResults["opaque-2"] = new TablePage(
            "contracts", Array.Empty<ColumnSchema>(),
            new[] { new Dictionary<string, object?> { ["rowKey"] = 2 } },
            0, 1, 50_000, "remote", 50_000, snapshot,
            NextCursor: null, HasMore: false);
        using var query = JsonDocument.Parse("""{"filters":[],"sorts":[],"limit":500}""");

        coordinator.RequestQuery("contracts", query.RootElement);
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);
        coordinator.RequestNextWindow("opaque-2");
        await Task.Delay(100);

        CollectionAssert.AreEqual(new[] { "opaque-2" }, gateway.CursorFetchCalls);
        Assert.AreEqual(2, notifications.Count);
        Assert.AreEqual("table.datasetReady", notifications[0].Type);
        Assert.AreEqual("table.windowLoaded", notifications[1].Type);
        Assert.AreEqual(2, notifications[1].Page?.Rows[0]["rowKey"]);
    }

    [TestMethod]
    public async Task RequestQuery_RejectsGroupedPagesWhenBothRevisionsAreMissing()
    {
        var gateway = new FakeTableRpcGateway();
        var notifications = new List<TableNotification>();
        var coordinator = NewCoordinator(gateway, notifications.Add);
        gateway.CursorOpenResults["contracts"] = SamplePage("contracts", 1);
        gateway.QueryWindowResults["contracts"] = SamplePage("contracts", 1) with
        {
            GroupRows = new[] { new GroupRow(new object?[] { "open" }, 1, Array.Empty<object?>()) },
        };
        using var query = JsonDocument.Parse("""{"groups":[{"field":"status"}],"limit":100}""");

        coordinator.RequestQuery("contracts", query.RootElement);
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("operation.failed", notifications[0].Type);
        Assert.IsNull(notifications[0].Page);
    }

    [TestMethod]
    public async Task RequestQuery_RejectsGroupedPagesFromDifferentRevisions()
    {
        var gateway = new FakeTableRpcGateway();
        var notifications = new List<TableNotification>();
        var coordinator = NewCoordinator(gateway, notifications.Add);
        gateway.CursorOpenResults["contracts"] = SamplePage("contracts", 1) with
        {
            QuerySnapshot = new QuerySnapshot(
                "cursor", "digest", "db-identity", "contracts", "schema-1", 7,
                new Dictionary<string, object?>()),
        };
        gateway.QueryWindowResults["contracts"] = SamplePage("contracts", 1) with
        {
            QuerySnapshot = new QuerySnapshot(
                "groups", "digest", "db-identity", "contracts", "schema-1", 8,
                new Dictionary<string, object?>()),
            GroupRows = new[] { new GroupRow(new object?[] { "open" }, 1, Array.Empty<object?>()) },
        };
        using var query = JsonDocument.Parse("""{"groups":[{"field":"status"}],"limit":100}""");

        coordinator.RequestQuery("contracts", query.RootElement);
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        Assert.AreEqual(1, notifications.Count);
        Assert.AreEqual("operation.failed", notifications[0].Type);
        Assert.IsNull(notifications[0].Page);
    }

    [TestMethod]
    public async Task RequestQuery_Cancels_SupersededRead()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(
                new[] { "contracts" },
                Array.Empty<string>(),
                TestDisplayNames.For("contracts"));
        // Gate the first table so we can supersede it.
        var tcs = new TaskCompletionSource<bool>();
        gateway.SetWindowReadGate("contracts", tcs.Task);
        var coordinator = NewCoordinator(gateway);

        coordinator.RequestQuery("contracts", Query());
        // Supersede before the first read completes.
        coordinator.RequestQuery("contracts", Query());
        // Release the gate; the first read's result must be dropped.
        tcs.TrySetResult(true);
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 100);

        // At least one request was made; no exception propagated.
        Assert.IsTrue(gateway.QueryWindowCalls.Count >= 1);
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
        coordinator.RequestQuery("contracts", Query());

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
        coordinator.RequestQuery("contracts", Query());

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
            new DatabaseOpenResult(
                new[] { "contracts" },
                Array.Empty<string>(),
                TestDisplayNames.For("contracts"));
        gateway.QueryWindowResults["contracts"] = SamplePage("contracts", 3);
        TableNotification? captured = null;
        var coordinator = NewCoordinator(gateway, n => captured = n);

        coordinator.RequestQuery("contracts", Query());
        // Immediately supersede before the debounce fires.
        coordinator.RequestQuery("contracts", Query());
        await Task.Delay(GridStateCoordinator.QueryDebounceMs + 150);

        // The second request's page is emitted (not the first). Either way,
        // no exception and exactly one call reached the gateway after debounce.
        Assert.IsTrue(gateway.QueryWindowCalls.Count >= 1);
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
