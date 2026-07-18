import { describe, expect, it, vi } from "vitest";

import { createTableFlow } from "./tableFlow";
import type {
  ColumnSchema,
  DatasetReadyPayload,
  TablePageLoadedPayload,
} from "./contracts";

/**
 * Tests for the renderer-side table-flow state machine.
 *
 * Pins the Task-10 brief's three required scenarios:
 *   - 501 rows (client mode, 2 pages, datasetReady after loadedRows == totalRows)
 *   - 25_000 rows (the inclusive client-mode boundary)
 *   - cancelled multi-page load (switching tables resets accumulated state and
 *     stale pages for the superseded table are dropped)
 *
 * The state machine is DOM-free; these tests drive it directly with typed
 * payloads and assert the resulting state.
 */

const COLUMNS: readonly ColumnSchema[] = [
  { name: "id", title: "id", dataType: "integer", editable: false, nullable: false },
];

const QUERY_SNAPSHOT = {
  snapshotId: "snapshot-1",
  digest: "digest-1",
  databaseId: "directus",
  table: "contracts",
  schemaRevision: "schema-1",
  dataRevision: 4,
  normalizedQuery: {},
};

const REVISION = {
  databaseSessionId: "directus",
  schemaRevision: "schema-1",
  dataRevision: 4,
};

function makeRows(start: number, count: number): ReadonlyArray<Record<string, unknown>> {
  const out: Record<string, unknown>[] = [];
  for (let i = 0; i < count; i++) {
    out.push({ id: start + i, rowKey: start + i });
  }
  return out;
}

function capture(): { controller: ReturnType<typeof createTableFlow> } {
  const controller = createTableFlow({
    onStateChange: () => {
      // State changes are observed via controller.getState() in the tests.
    },
  });
  return { controller };
}

describe("tableFlow state machine", () => {
  it("client mode 501 rows: accumulates two pages, datasetReady completes", () => {
    const { controller } = capture();
    const notify = vi.fn();

    // Select "contracts" (501 rows). The host drives a 2-page fetch.
    controller.selectTable("contracts", notify);

    // First page: offset 0, 500 rows, loadedRows 500.
    const page1: TablePageLoadedPayload = {
      table: "contracts",
      columns: COLUMNS,
      rows: makeRows(1, 500),
      offset: 0,
      limit: 500,
      totalRows: 501,
      mode: "client",
      loadedRows: 500,
      querySnapshot: QUERY_SNAPSHOT,
      revision: REVISION,
    };
    controller.onTablePageLoaded(page1);

    // Second page: offset 500, 1 row, loadedRows 501.
    const page2: TablePageLoadedPayload = {
      table: "contracts",
      columns: COLUMNS,
      rows: makeRows(501, 1),
      offset: 500,
      limit: 500,
      totalRows: 501,
      mode: "client",
      loadedRows: 501,
      querySnapshot: QUERY_SNAPSHOT,
      revision: REVISION,
    };
    controller.onTablePageLoaded(page2);

    // datasetReady: complete dataset, loadedRows == totalRows.
    const ready: DatasetReadyPayload = {
      table: "contracts",
      columns: COLUMNS,
      rows: [...makeRows(1, 500), ...makeRows(501, 1)],
      offset: 0,
      limit: 501,
      totalRows: 501,
      mode: "client",
      loadedRows: 501,
      querySnapshot: QUERY_SNAPSHOT,
      revision: REVISION,
    };
    controller.onDatasetReady(ready);

    const finalState = controller.getState();
    expect(finalState.currentTable).toBe("contracts");
    expect(finalState.loading).toBe(false);
    expect(finalState.mode).toBe("client");
    expect(finalState.totalRows).toBe(501);
    expect(finalState.loadedRows).toBe(501);
    expect(finalState.rows).toHaveLength(501);
    expect(finalState.querySnapshot).toEqual(QUERY_SNAPSHOT);
    expect(finalState.revision).toEqual(REVISION);
    expect(finalState.error).toBeNull();

    // The select fired table.selected exactly once.
    expect(notify).toHaveBeenCalledTimes(1);
    expect(notify).toHaveBeenCalledWith("table.selected", { table: "contracts" });
  });

  it("client mode 25_000 rows at the inclusive boundary completes", () => {
    const { controller } = capture();
    const notify = vi.fn();
    controller.selectTable("big", notify);

    // Drive 50 pages of 500 rows each, with cumulative loadedRows.
    let offset = 0;
    let loaded = 0;
    const totalRows = 25_000;
    while (offset < totalRows) {
      const thisPage = Math.min(500, totalRows - offset);
      loaded += thisPage;
      controller.onTablePageLoaded({
        table: "big",
        columns: COLUMNS,
        rows: makeRows(offset + 1, thisPage),
        offset,
        limit: 500,
        totalRows,
        mode: "client",
        loadedRows: loaded,
      });
      offset += 500;
    }

    // datasetReady with the full dataset shape.
    controller.onDatasetReady({
      table: "big",
      columns: COLUMNS,
      rows: makeRows(1, totalRows),
      offset: 0,
      limit: totalRows,
      totalRows,
      mode: "client",
      loadedRows: totalRows,
    });

    const finalState = controller.getState();
    expect(finalState.mode).toBe("client");
    expect(finalState.loading).toBe(false);
    expect(finalState.totalRows).toBe(25_000);
    expect(finalState.loadedRows).toBe(25_000);
    // The accumulated rows from page events should equal totalRows BEFORE the
    // datasetReady signal (the host emits datasetReady as the final step).
  });

  it("cancelled multi-page load: switching tables drops stale pages", () => {
    const { controller } = capture();
    const notify = vi.fn();

    // Select "alpha" and receive the FIRST page (loading still in progress).
    controller.selectTable("alpha", notify);
    controller.onTablePageLoaded({
      table: "alpha",
      columns: COLUMNS,
      rows: makeRows(1, 500),
      offset: 0,
      limit: 500,
      totalRows: 1_000,
      mode: "client",
      loadedRows: 500,
    });
    expect(controller.getState().currentTable).toBe("alpha");
    expect(controller.getState().rows).toHaveLength(500);

    // Switch to "beta" mid-load: state resets.
    controller.selectTable("beta", notify);
    expect(controller.getState().currentTable).toBe("beta");
    expect(controller.getState().rows).toHaveLength(0);
    expect(controller.getState().loading).toBe(true);

    // A LATE-arriving page for "alpha" must be IGNORED (stale).
    controller.onTablePageLoaded({
      table: "alpha",
      columns: COLUMNS,
      rows: makeRows(501, 500),
      offset: 500,
      limit: 500,
      totalRows: 1_000,
      mode: "client",
      loadedRows: 1_000,
    });
    // beta's state is untouched: still 0 rows, currentTable still beta.
    expect(controller.getState().currentTable).toBe("beta");
    expect(controller.getState().rows).toHaveLength(0);

    // A stale datasetReady for alpha is also ignored.
    controller.onDatasetReady({
      table: "alpha",
      columns: COLUMNS,
      rows: makeRows(1, 1_000),
      offset: 0,
      limit: 1_000,
      totalRows: 1_000,
      mode: "client",
      loadedRows: 1_000,
    });
    expect(controller.getState().currentTable).toBe("beta");
    expect(controller.getState().rows).toHaveLength(0);

    // beta's own page arrives and is rendered.
    controller.onTablePageLoaded({
      table: "beta",
      columns: COLUMNS,
      rows: makeRows(1, 1),
      offset: 0,
      limit: 500,
      totalRows: 1,
      mode: "client",
      loadedRows: 1,
    });
    expect(controller.getState().currentTable).toBe("beta");
    expect(controller.getState().rows).toHaveLength(1);

    // Both selects fired table.selected.
    expect(notify).toHaveBeenCalledTimes(2);
    expect(notify).toHaveBeenNthCalledWith(1, "table.selected", { table: "alpha" });
    expect(notify).toHaveBeenNthCalledWith(2, "table.selected", { table: "beta" });
  });

  it("remote mode: retains only the requested page, no accumulation", () => {
    const { controller } = capture();
    controller.selectTable("huge", vi.fn());

    // A single remote-mode page arrives.
    controller.onTablePageLoaded({
      table: "huge",
      columns: COLUMNS,
      rows: makeRows(1, 500),
      offset: 0,
      limit: 500,
      totalRows: 100_000,
      mode: "remote",
      loadedRows: 500,
    });

    const finalState = controller.getState();
    expect(finalState.mode).toBe("remote");
    expect(finalState.loading).toBe(false);
    expect(finalState.rows).toHaveLength(500);
    expect(finalState.totalRows).toBe(100_000);

    // A second remote page REPLACES (not accumulates) the rows.
    controller.onTablePageLoaded({
      table: "huge",
      columns: COLUMNS,
      rows: makeRows(501, 500),
      offset: 500,
      limit: 500,
      totalRows: 100_000,
      mode: "remote",
      loadedRows: 500,
    });
    expect(controller.getState().rows).toHaveLength(500);
    // The rows are the second page's (501..1000), not accumulated.
    expect(controller.getState().rows[0]).toMatchObject({ id: 501 });
  });

  it("operation.failed surfaces an error and clears loading", () => {
    const { controller } = capture();
    controller.selectTable("t", vi.fn());
    expect(controller.getState().loading).toBe(true);

    controller.onOperationFailed("the backend exploded");

    const finalState = controller.getState();
    expect(finalState.loading).toBe(false);
    expect(finalState.error).toBe("the backend exploded");
  });

  it("database.opened resets accumulated state", () => {
    const { controller } = capture();
    controller.selectTable("t", vi.fn());
    controller.onTablePageLoaded({
      table: "t",
      columns: COLUMNS,
      rows: makeRows(1, 10),
      offset: 0,
      limit: 500,
      totalRows: 10,
      mode: "client",
      loadedRows: 10,
    });
    expect(controller.getState().rows).toHaveLength(10);

    controller.onDatabaseOpened({ tables: ["u"], views: [] });

    const finalState = controller.getState();
    expect(finalState.currentTable).toBeNull();
    expect(finalState.rows).toHaveLength(0);
    expect(finalState.error).toBeNull();
  });
});
