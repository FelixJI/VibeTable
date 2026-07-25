import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTableStore } from "./tableStore";
import type {
  ColumnEditSchema,
  ColumnSchema,
  DatasetReadyPayload,
  MutationRevision,
  TablePage,
  UpdateCellResult,
} from "@/contracts";

/**
 * Build a valid `TablePage` with the REAL contract fields. The brief's
 * `makePage` used invented field names (`tableName`, `schemaRevision`,
 * `rowCount`, `isLast`, `pageIndex`, `pageCount`) that do not exist on the
 * actual `TablePage` interface (see `src/contracts/index.ts`). The real
 * required fields are: `table`, `columns`, `rows`, `offset`, `limit`,
 * `totalRows`, `mode`.
 */
function makePage(
  rows: Record<string, unknown>[],
  opts: Partial<TablePage> = {},
): TablePage {
  return {
    table: "users",
    columns: [],
    rows,
    offset: opts.offset ?? 0,
    limit: opts.limit ?? rows.length,
    totalRows: opts.totalRows ?? rows.length,
    mode: opts.mode ?? "client",
    ...opts,
  };
}

function makeColumn(name: string): ColumnSchema {
  return {
    name,
    title: name,
    dataType: "integer",
    editable: false,
    nullable: true,
  };
}

/**
 * `DatasetReadyPayload extends TablePage`, so it carries the full dataset.
 * The store treats it as the authoritative final page (replacing accumulated
 * incremental pages, mirroring the legacy `tableFlow.ts` behavior).
 */
function makeDatasetReady(
  rows: Record<string, unknown>[],
  columns: readonly ColumnSchema[] = [],
  totalRows: number = rows.length,
): DatasetReadyPayload {
  return {
    table: "users",
    columns,
    rows,
    offset: 0,
    limit: rows.length,
    totalRows,
    mode: "client",
    loadedRows: rows.length,
  };
}

describe("tableStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle with no rows", () => {
    const s = useTableStore();
    expect(s.loading).toBe(false);
    expect(s.allRows).toEqual([]);
    expect(s.schema).toBeNull();
    expect(s.rowCount).toBe(0);
    expect(s.datasetReady).toBe(false);
    expect(s.error).toBeNull();
  });

  it("beginLoad sets loading and clears previous data", () => {
    const s = useTableStore();
    s.appendPage(makePage([{ id: 1 }]));
    expect(s.loadGeneration).toBe(0);
    s.beginLoad();
    expect(s.loadGeneration).toBe(1);
    expect(s.loading).toBe(true);
    expect(s.allRows).toEqual([]);
    expect(s.schema).toBeNull();
    expect(s.rowCount).toBe(0);
    expect(s.datasetReady).toBe(false);
  });

  it("appendPage accumulates rows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ id: 1 }, { id: 2 }]));
    s.appendPage(makePage([{ id: 3 }], { offset: 2 }));
    expect(s.allRows).toHaveLength(3);
    expect(s.allRows).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }]);
  });

  it("setDatasetReady stores schema, replaces pages with the final dataset, and ends loading", () => {
    const s = useTableStore();
    s.beginLoad();
    // Accumulate some incremental pages first.
    s.appendPage(makePage([{ id: 1 }]));
    s.appendPage(makePage([{ id: 2 }], { offset: 1 }));
    // datasetReady carries the authoritative full dataset.
    const cols = [makeColumn("id"), makeColumn("name")];
    s.setDatasetReady(
      makeDatasetReady([{ id: 1 }, { id: 2 }, { id: 3 }], cols, 3),
    );
    expect(s.loading).toBe(false);
    expect(s.datasetReady).toBe(true);
    expect(s.schema).toHaveLength(2);
    expect(s.rowCount).toBe(3);
    // The accumulated pages are replaced by the single datasetReady page;
    // rows must not be double-counted.
    expect(s.allRows).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }]);
  });

  it("setError ends loading and records error", () => {
    const s = useTableStore();
    s.beginLoad();
    s.setError("boom");
    expect(s.loading).toBe(false);
    expect(s.error).toBe("boom");
  });

  it("keeps a mutation error when a concurrent dataset refresh finishes", () => {
    const s = useTableStore();
    s.beginLoad();
    s.setError("EDIT_CONFLICT");
    s.setDatasetReady(makeDatasetReady([{ id: 1 }], [makeColumn("id")], 1));

    expect(s.error).toBe("EDIT_CONFLICT");

    // A deliberate new load is the boundary that clears the stale error.
    s.beginLoad();
    expect(s.error).toBeNull();
  });

  it("reset clears everything", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ id: 1 }]));
    s.setDatasetReady(makeDatasetReady([{ id: 1 }], [makeColumn("id")], 1));
    s.reset();
    expect(s.loading).toBe(false);
    expect(s.allRows).toEqual([]);
    expect(s.rowCount).toBe(0);
    expect(s.schema).toBeNull();
    expect(s.datasetReady).toBe(false);
    expect(s.error).toBeNull();
  });
});

const editSchema: readonly ColumnEditSchema[] = [
  {
    name: "id",
    storageName: "id",
    dataType: "integer",
    editable: false,
    nullable: false,
    primaryKey: true,
    editor: { kind: "number", storage: "integer" },
    validation: [],
  },
  {
    name: "name",
    storageName: "name",
    dataType: "text",
    editable: true,
    nullable: true,
    primaryKey: false,
    editor: { kind: "text" },
    validation: [],
  },
];

function makeRevision(
  overrides: Partial<MutationRevision> = {},
): MutationRevision {
  return {
    databaseSessionId: "s",
    schemaRevision: "sr",
    dataRevision: 1,
    ...overrides,
  };
}

describe("tableStore mutation extensions", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("setEditSchema stores columns + revision", () => {
    const s = useTableStore();
    const rev = makeRevision({ schemaRevision: "sr1" });
    s.setEditSchema(editSchema, rev);
    expect(s.editSchema).toHaveLength(2);
    expect(s.revision?.schemaRevision).toBe("sr1");
  });

  it("applyCellEdit updates the edited cell in allRows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "old" }]));
    const res: UpdateCellResult = {
      rowKey: 1,
      column: "name",
      storedValue: "new",
      currentRow: { rowKey: 1, name: "new" },
      revision: makeRevision({ dataRevision: 2 }),
    };
    s.applyCellEdit(res);
    expect(s.allRows[0]?.name).toBe("new");
    expect(s.revision?.dataRevision).toBe(2);
  });

  it("applyInsert appends the new row", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1 }]));
    s.applyInsert({
      rowKey: 2,
      row: { rowKey: 2, name: "x" },
      revision: makeRevision({ dataRevision: 2 }),
    });
    expect(s.allRows).toHaveLength(2);
    expect(s.allRows[1]?.rowKey).toBe(2);
  });

  it("applyInsert merges a realtime-visible row instead of duplicating it", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 2, name: "realtime" }]));

    s.applyInsert({
      rowKey: 2,
      row: { rowKey: 2, name: "committed" },
      revision: makeRevision({ dataRevision: 2 }),
    });

    expect(s.allRows).toEqual([{ rowKey: 2, name: "committed" }]);
  });

  it("applyDelete removes the deleted rows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1 }, { rowKey: 2 }, { rowKey: 3 }]));
    s.applyDelete({
      deletedRowKeys: [1, 3],
      revision: makeRevision({ dataRevision: 2 }),
    });
    expect(s.allRows).toHaveLength(1);
    expect(s.allRows[0]?.rowKey).toBe(2);
  });

  it("snapshotRows returns full row data for given keys (for undo cache)", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "a" }, { rowKey: 2, name: "b" }]));
    const snap = s.snapshotRows([2]);
    expect(snap).toEqual([{ rowKey: 2, name: "b" }]);
  });

  it("reset also clears editSchema + revision", () => {
    const s = useTableStore();
    s.setEditSchema(editSchema, makeRevision());
    s.reset();
    expect(s.editSchema).toBeNull();
    expect(s.revision).toBeNull();
  });

  it("merges authoritative Lookup rows by fieldId and adopts server order", () => {
    const s = useTableStore();
    s.setDatasetReady({
      ...makeDatasetReady([
        { rowKey: "o1", amount: "10.00", contractPrice: null },
        { rowKey: "o2", amount: "20.00", contractPrice: null },
      ], [
        { ...makeColumn("amount"), fieldId: "orders.amount" },
        { ...makeColumn("contractPrice"), fieldId: "orders.contract_price", kind: "lookup", lookupId: "orders.contract_price" },
      ], 2),
      table: "orders",
    });
    s.applyLookupQueryResult({
      contract: "vibetable.lookup-query.v1",
      collection: "orders",
      requestGeneration: 1,
      schemaRevision: "s",
      permissionRevision: "p",
      lookupRevision: "l",
      columns: [],
      rows: [
        { rowKey: "o2", "orders.contract_price": "99.00" },
        { rowKey: "o1", "orders.contract_price": "11.00" },
      ],
      groups: [{
        path: [{ fieldRef: "customer", key: "Acme" }],
        key: "Acme",
        count: 2,
        aggregates: { total: "110.00" },
        childCursor: "cursor-1",
      }], offset: 0, limit: 2, filteredRows: 2, totalRows: 2,
    });
    expect(s.allRows.map((row) => row.rowKey)).toEqual(["o2", "o1"]);
    expect(s.allRows.map((row) => row.contractPrice)).toEqual(["99.00", "11.00"]);
    expect(s.allRows[0]?.amount).toBe("20.00");
    expect(s.lookupGroups).toEqual([{
      path: [{ fieldRef: "customer", key: "Acme" }],
      key: "Acme",
      count: 2,
      aggregates: { total: "110.00" },
      childCursor: "cursor-1",
    }]);
  });
});
