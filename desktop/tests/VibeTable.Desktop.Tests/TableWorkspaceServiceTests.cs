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
/// owns the bounded cursor-window open. These tests pin the four invariants
/// from the Task-10 brief:
/// </para>
/// <list type="bullet">
/// <item>Database paths come ONLY from the WPF file picker (the service never
/// invents or mutates a path).</item>
/// <item>Table names come ONLY from <c>database.open</c>/<c>table.list</c>
/// results; a name not in that list is rejected.</item>
/// <item>Page limits stay within 1..500.</item>
/// <item>STALE cursor responses are ignored after the user selects another table
/// (the old open's continuation must not emit rows for the superseded
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
            new DatabaseOpenResult(
                new[] { "contracts", "vendors" },
                Array.Empty<string>(),
                TestDisplayNames.For("contracts", "vendors"));
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
            new DatabaseOpenResult(
                new[] { "contracts" },
                Array.Empty<string>(),
                TestDisplayNames.For("contracts"));
        var service = new TableWorkspaceService(gateway);
        await service.OpenDatabaseAsync("db");

        await Assert.ThrowsExactlyAsync<ArgumentException>(
            async () => await service.SelectTableAsync("not-a-known-table"));
    }

    [TestMethod]
    public async Task SelectTableAsync_OpensOneBoundedCursorWindow_AndEmitsDatasetReady()
    {
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(
                new[] { "t" },
                Array.Empty<string>(),
                TestDisplayNames.For("t"));
        gateway.QueryWindowResults["t"] = BuildPages(
            "t", totalRows: 100_000, pageSize: 500, mode: "remote")[0] with
        {
            NextCursor = "opaque-2",
            HasMore = true,
        };
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += n => notifications.Add(n);

        await service.OpenDatabaseAsync("db");
        await service.SelectTableAsync("t");

        Assert.AreEqual(0, gateway.WindowWindowReadCalls.Count);
        Assert.AreEqual(1, gateway.QueryWindowCalls.Count);
        TableNotification ready = notifications.Single();
        Assert.AreEqual("table.datasetReady", ready.Type);
        Assert.HasCount(500, ready.Page!.Rows);
        Assert.AreEqual(100_000, ready.Page!.TotalRows);
        Assert.AreEqual("opaque-2", ready.Page.NextCursor);
        Assert.IsTrue(ready.Page.HasMore);
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
            new DatabaseOpenResult(
                new[] { "alpha", "beta" },
                Array.Empty<string>(),
                TestDisplayNames.For("alpha", "beta"));
        gateway.TablePages["alpha"] = BuildPages("alpha", totalRows: 1, pageSize: 500);
        gateway.TablePages["beta"] = BuildPages("beta", totalRows: 1, pageSize: 500);
        var service = new TableWorkspaceService(gateway);
        var notifications = new List<TableNotification>();
        service.Notification += n => notifications.Add(n);

        await service.OpenDatabaseAsync("db");

        // Block alpha's window read until we release it.
        var releaseAlpha = new TaskCompletionSource<bool>();
        gateway.SetWindowReadGate("alpha", releaseAlpha.Task);

        var selectAlpha = service.SelectTableAsync("alpha");
        // Let alpha's first read be issued and block on the gate.
        await Task.Yield();
        await Task.Delay(20);

        // Switch to beta WITHOUT waiting for alpha: the service must cancel the
        // in-flight alpha fetch.
        await service.SelectTableAsync("beta");

        // Now release alpha's stalled read. The window it returns MUST be dropped.
        releaseAlpha.SetResult(true);
        await selectAlpha; // alpha's select completes (cancelled/suppressed)

        var ready = notifications
            .Where(n => n.Type == "table.datasetReady").ToList();
        Assert.IsTrue(ready.All(p => p.Page!.Table == "beta"),
            "no stale 'alpha' page may be emitted after the switch to 'beta'");
        Assert.IsTrue(ready.Any(p => p.Page!.Table == "beta"),
            "the 'beta' page must be emitted");
    }

    [TestMethod]
    public async Task SelectTableAsync_PageLimitStaysWithin_1_To_500()
    {
        // Cursor Open always requests a window of size 500.
        var gateway = new FakeTableRpcGateway();
        gateway.DatabaseOpenResults["db"] =
            new DatabaseOpenResult(
                new[] { "t" },
                Array.Empty<string>(),
                TestDisplayNames.For("t"));
        gateway.TablePages["t"] = BuildPages("t", totalRows: 1_200, pageSize: 500);
        var service = new TableWorkspaceService(gateway);

        await service.OpenDatabaseAsync("db");
        await service.SelectTableAsync("t");

        foreach (var call in gateway.WindowWindowReadCalls)
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
            new DatabaseOpenResult(
                new[] { "old" },
                Array.Empty<string>(),
                TestDisplayNames.For("old"));
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
            new DatabaseOpenResult(
                Array.Empty<string>(),
                Array.Empty<string>(),
                TestDisplayNames.For());
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
            new DatabaseOpenResult(
                Array.Empty<string>(),
                Array.Empty<string>(),
                TestDisplayNames.For());
        gateway.ListTablesResult = new TableSummary(
            new[] { "alpha", "vibetable_settings", "beta" },
            Array.Empty<string>(),
            TestDisplayNames.For("alpha", "vibetable_settings", "beta"));
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
        string mode = "remote")
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
                Mode: mode);
            offset += pageSize;
        }
        return pages;
    }
}
