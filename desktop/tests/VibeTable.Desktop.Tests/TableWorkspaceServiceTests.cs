using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// Unit tests for <see cref="TableWorkspaceService"/>.
/// </summary>
/// <remarks>
/// <para>
/// The workspace service orchestrates the Phase-A table flow
/// (database.open -&gt; table.list -&gt; table.read -&gt; notifications) and
/// owns the multi-page client-mode fetch. These tests pin the four invariants
/// from the Task-10 brief:
/// </para>
/// <list type="bullet">
/// <item>Database paths come ONLY from the WPF file picker (the service never
/// invents or mutates a path).</item>
/// <item>Table names come ONLY from <c>database.open</c>/<c>table.list</c>
/// results; a name not in that list is rejected.</item>
/// <item>Page limits stay within 1..500.</item>
/// <item>STALE page responses are ignored after the user selects another table
/// (the old fetch's continuation must not emit pages for the superseded
/// table).</item>
/// </list>
/// <para>
/// TDD order: written BEFORE the service implementation. The fake gateway
/// (<see cref="FakeTableRpcGateway"/>) is deterministic; the RED run fails
/// because <see cref="TableWorkspaceService"/> does not exist yet.
/// </para>
/// </remarks>
[TestClass]
public sealed class TableWorkspaceServiceTests
{
    [TestMethod]
    public async Task OpenDatabaseAsync_ForwardsPath_Verbatim_AndReturnsTables()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["C:/data/sample.db"] =
            new DatabaseOpenResult(new[] { "contracts", "vendors" }, Array.Empty<string>());
        var service = new TableWorkspaceService(gateway);

        var result = await service.OpenDatabaseAsync("C:/data/sample.db");

        CollectionAssert.AreEqual(
            new[] { "contracts", "vendors" },
            (System.Collections.ICollection)result.Tables);
        Assert.AreEqual(1, gateway.OpenDatabaseCalls.Count);
        Assert.AreEqual("C:/data/sample.db", gateway.OpenDatabaseCalls[0]);
    }

    [TestMethod]
    public async Task OpenDatabaseAsync_RejectsNullOrEmptyPath()
    {
        var service = new TableWorkspaceService(new FakeTableRpcGateway());

        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.OpenDatabaseAsync(""));
        await Assert.ThrowsExactlyAsync<ArgumentNullException>(
            async () => await service.OpenDatabaseAsync(null!));
    }

    [TestMethod]
    public async Task SelectTableAsync_RejectsTableNotFromOpenResult()
    {
        // Table names must come ONLY from database.open/table.list. Selecting a
        // name that was never advertised is a contract violation.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "contracts" }, Array.Empty<string>());
        var service = new TableWorkspaceService(gateway);
        await service.OpenDatabaseAsync("db");

        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.SelectTableAsync("not-a-known-table"));
    }

    [TestMethod]
    public async Task SelectTableAsync_ClientMode_FetchesAllPages_AndEmitsDatasetReady()
    {
        // 501 rows, client mode (<= 25000). The host must fetch ALL pages in
        // deterministic 500-row chunks and emit table.datasetReady only after
        // loadedRows == totalRows.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "t" }, Array.Empty<string>());
        gateway.TablePages["t"] = BuildPages("t", totalRows: 501, pageSize: 500);
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += n => notifications.Add(n);

        await service.OpenDatabaseAsync("db");
        await service.SelectTableAsync("t");

        // Two page reads: offset 0/500 and offset 500/500.
        Assert.AreEqual(2, gateway.ReadTablePageCalls.Count);
        Assert.AreEqual(0, gateway.ReadTablePageCalls[0].Offset);
        Assert.AreEqual(500, gateway.ReadTablePageCalls[1].Offset);

        // Every table.pageLoaded is for table "t" with correct cumulative count.
        var pageLoaded = notifications
            .Where(n => n.Type == "table.pageLoaded").ToList();
        Assert.AreEqual(2, pageLoaded.Count);
        Assert.AreEqual("t", pageLoaded[0].Page!.Table);
        Assert.AreEqual(500, pageLoaded[0].Page!.Rows.Count);
        Assert.AreEqual("t", pageLoaded[1].Page!.Table);
        Assert.AreEqual(1, pageLoaded[1].Page!.Rows.Count);

        // datasetReady fires EXACTLY once, after the full dataset is loaded.
        var ready = notifications.Where(n => n.Type == "table.datasetReady").ToList();
        Assert.AreEqual(1, ready.Count);
        Assert.AreEqual("t", ready[0].Page!.Table);
        Assert.AreEqual(501, ready[0].Page!.TotalRows);
        Assert.AreEqual(501, ready[0].LoadedRows);
    }

    [TestMethod]
    public async Task SelectTableAsync_RemoteMode_RetainsOnlyRequestedPage()
    {
        // 25_000 rows total: exactly at the client boundary (<= 25000), so still
        // client mode. We instead test remote mode by exceeding the boundary.
        // 25_001 rows -> remote mode: retain only the requested page, no
        // datasetReady.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "t" }, Array.Empty<string>());
        gateway.TablePages["t"] = BuildPages(
            "t", totalRows: 25_001, pageSize: 500, mode: "remote");
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += n => notifications.Add(n);

        await service.OpenDatabaseAsync("db");
        await service.SelectTableAsync("t");

        // Remote mode: exactly ONE page read (offset 0/500).
        Assert.AreEqual(1, gateway.ReadTablePageCalls.Count);
        Assert.IsFalse(notifications.Any(n => n.Type == "table.datasetReady"));
        var pageLoaded = notifications
            .Where(n => n.Type == "table.pageLoaded").ToList();
        Assert.AreEqual(1, pageLoaded.Count);
        Assert.AreEqual("remote", pageLoaded[0].Page!.Mode);
    }

    [TestMethod]
    public async Task SelectTableAsync_AtClientBoundary_25000_IsClientMode()
    {
        // Exactly 25_000 rows is the inclusive client budget: client mode,
        // full dataset load, datasetReady fires.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "t" }, Array.Empty<string>());
        gateway.TablePages["t"] = BuildPages("t", totalRows: 25_000, pageSize: 500);
        var service = new TableWorkspaceService(gateway);
        var readyCount = 0;
        service.Notification += n =>
        {
            if (n.Type == "table.datasetReady") readyCount++;
        };

        await service.OpenDatabaseAsync("db");
        await service.SelectTableAsync("t");

        // 25000 / 500 = 50 pages.
        Assert.AreEqual(50, gateway.ReadTablePageCalls.Count);
        Assert.AreEqual(1, readyCount);
    }

    [TestMethod]
    public async Task SelectTableAsync_SuppressesStalePages_AfterTableSwitch()
    {
        // STALE page suppression: after switching from table A to table B, a
        // late-arriving page for A MUST NOT be emitted. We simulate this by
        // stalling A's first read until AFTER we've selected B, then releasing
        // it. The released page for A must be dropped.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "alpha", "beta" }, Array.Empty<string>());
        gateway.TablePages["alpha"] = BuildPages("alpha", totalRows: 1, pageSize: 500);
        gateway.TablePages["beta"] = BuildPages("beta", totalRows: 1, pageSize: 500);
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += n => notifications.Add(n);

        await service.OpenDatabaseAsync("db");

        // Block alpha's page read until we release it.
        var releaseAlpha = new TaskCompletionSource<bool>();
        gateway.SetReadGate("alpha", releaseAlpha.Task);

        var selectAlpha = service.SelectTableAsync("alpha");
        // Let alpha's first read be issued and block on the gate.
        await Task.Yield();
        await Task.Delay(20);

        // Switch to beta WITHOUT waiting for alpha: the service must cancel the
        // in-flight alpha fetch.
        await service.SelectTableAsync("beta");

        // Now release alpha's stalled read. The page it returns MUST be dropped
        // (stale) — no table.pageLoaded for "alpha".
        releaseAlpha.SetResult(true);
        await selectAlpha; // alpha's select completes (cancelled/suppressed)

        var pageLoaded = notifications
            .Where(n => n.Type == "table.pageLoaded").ToList();
        Assert.IsTrue(pageLoaded.All(p => p.Page!.Table == "beta"),
            "no stale 'alpha' page may be emitted after the switch to 'beta'");
        Assert.IsTrue(pageLoaded.Any(p => p.Page!.Table == "beta"),
            "the 'beta' page must be emitted");
    }

    [TestMethod]
    public async Task SelectTableAsync_PageLimitStaysWithin_1_To_500()
    {
        // The service always requests pages of size 500 (the Phase-A cap). No
        // request may exceed 500 or fall below 1.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "t" }, Array.Empty<string>());
        gateway.TablePages["t"] = BuildPages("t", totalRows: 1_200, pageSize: 500);
        var service = new TableWorkspaceService(gateway);

        await service.OpenDatabaseAsync("db");
        await service.SelectTableAsync("t");

        foreach (var call in gateway.ReadTablePageCalls)
        {
            Assert.IsTrue(call.Limit >= 1 && call.Limit <= 500,
                $"limit out of range [1,500]: {call.Limit}");
            Assert.IsTrue(call.Offset >= 0, $"offset negative: {call.Offset}");
        }
    }

    // -----------------------------------------------------------------------
    // Known-tables cache refresh (regression for the "create-then-open throws
    // ArgumentException" bug). The cache MUST be refreshable after the initial
    // OpenDatabaseAsync so create/delete/reconcile flows keep SelectTableAsync's
    // membership check in sync with what the sidebar shows.
    // -----------------------------------------------------------------------

    [TestMethod]
    public async Task UpdateKnownTables_ReplacesCache_AndAllowsSelectOfNewName()
    {
        // Open seeds the cache with "old". A subsequent UpdateKnownTables swaps
        // the cache to a fresh list, so selecting a name that was NOT in the
        // open result but IS in the update succeeds.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(new[] { "old" }, Array.Empty<string>());
        gateway.TablePages["fresh"] = BuildPages("fresh", totalRows: 1, pageSize: 500);
        var service = new TableWorkspaceService(gateway);
        await service.OpenDatabaseAsync("db");

        // Before the update, "fresh" is unknown.
        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.SelectTableAsync("fresh"));

        service.UpdateKnownTables(new[] { "fresh" });

        // After the update, "fresh" is known and "old" is not.
        await service.SelectTableAsync("fresh");
        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.SelectTableAsync("old"));
    }

    [TestMethod]
    public async Task UpdateKnownTables_FiltersSystemTables_Defensively()
    {
        // Even if a caller hands in system-prefixed names, they must NOT become
        // selectable — product-owned vibetable_* metadata never belongs in
        // the user-table cache.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(Array.Empty<string>(), Array.Empty<string>());
        var service = new TableWorkspaceService(gateway);
        await service.OpenDatabaseAsync("db");

        service.UpdateKnownTables(new[] { "real", "vibetable_settings" });

        await service.SelectTableAsync("real"); // user table: OK
        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.SelectTableAsync("vibetable_settings"));
    }

    [TestMethod]
    public async Task RefreshKnownTablesAsync_FiltersSystemTablesAndUpdatesCache()
    {
        // The gateway's ListTablesAsync returns the RAW collection list (incl.
        // product metadata). RefreshKnownTablesAsync keeps those names out of
        // the selectable cache.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(Array.Empty<string>(), Array.Empty<string>());
        gateway.ListTablesResult = new TableSummary(
            new[] { "alpha", "vibetable_settings", "beta" },
            Array.Empty<string>());
        gateway.TablePages["alpha"] = BuildPages("alpha", totalRows: 1, pageSize: 500);
        gateway.TablePages["beta"] = BuildPages("beta", totalRows: 1, pageSize: 500);
        var service = new TableWorkspaceService(gateway);
        await service.OpenDatabaseAsync("db");

        await service.RefreshKnownTablesAsync(CancellationToken.None);

        // User tables selectable; system tables rejected.
        await service.SelectTableAsync("alpha");
        await service.SelectTableAsync("beta");
        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.SelectTableAsync("vibetable_settings"));
    }

    // -----------------------------------------------------------------------
    // Helpers
    // -----------------------------------------------------------------------

    /// <summary>
    /// Builds a deterministic set of <see cref="TablePage"/> fixtures for a
    /// table with <paramref name="totalRows"/> rows, paging in
    /// <paramref name="pageSize"/>-row chunks. The gateway returns the chunk
    /// matching the requested offset.
    /// </summary>
    private static Dictionary<int, TablePage> BuildPages(
        string table,
        int totalRows,
        int pageSize,
        string? mode = null)
    {
        var pages = new Dictionary<int, TablePage>();
        int offset = 0;
        int rowKey = 1;
        while (offset < totalRows)
        {
            int thisPage = Math.Min(pageSize, totalRows - offset);
            var rows = new List<Dictionary<string, object?>>(thisPage);
            for (int i = 0; i < thisPage; i++)
            {
                rows.Add(new Dictionary<string, object?>
                {
                    ["id"] = rowKey,
                    ["rowKey"] = rowKey,
                });
                rowKey++;
            }
            string resolvedMode = mode ??
                (totalRows <= 25_000 ? "client" : "remote");
            pages[offset] = new TablePage(
                Table: table,
                Columns: new[]
                {
                    new ColumnSchema("id", "id", "integer", Editable: false, Nullable: false),
                },
                Rows: rows,
                Offset: offset,
                Limit: pageSize,
                TotalRows: totalRows,
                Mode: resolvedMode);
            offset += pageSize;
        }
        return pages;
    }
}
