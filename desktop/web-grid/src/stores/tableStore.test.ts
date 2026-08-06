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

function lookupSnapshot(dataRevision: number) {
  return {
    snapshotId: `snapshot-${dataRevision}`,
    digest: `sha256:${"a".repeat(64)}`,
    databaseId: "database-1",
    table: "records",
    schemaRevision: "s",
    dataRevision,
    normalizedQuery: {},
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

  it("appendPage adopts the authoritative revision used by remote mode", () => {
    const s = useTableStore();
    const revision = {
      databaseSessionId: "remote-session",
      schemaRevision: "remote-schema",
      dataRevision: 9,
    };
    s.beginLoad();
    s.appendPage(makePage([{ id: 1 }], {
      mode: "remote",
      revision,
    }));

    expect(s.revision).toEqual(revision);
  });

  it("remote pages replace the requested window and can finish without datasetReady", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ id: 1 }], { mode: "remote", offset: 0 }));
    s.appendPage(makePage([{ id: 501 }], { mode: "remote", offset: 500 }));
    s.finishPageLoad();

    expect(s.allRows).toEqual([{ id: 501 }]);
    expect(s.loading).toBe(false);
    expect(s.datasetReady).toBe(false);
  });

  it("rejects a stale remote page after a newer edit committed", () => {
    const s = useTableStore();
    const revision = {
      databaseSessionId: "remote-session",
      schemaRevision: "remote-schema",
      dataRevision: 4,
    };
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "old" }], {
      mode: "remote",
      revision,
    }));
    s.applyCellEdit({
      rowKey: 1,
      column: "name",
      storedValue: "new",
      currentRow: { rowKey: 1, name: "new" },
      revision: { ...revision, dataRevision: 5 },
    });
    s.appendPage(makePage([{ rowKey: 1, name: "old" }], {
      mode: "remote",
      revision,
    }));

    expect(s.allRows).toEqual([{ rowKey: 1, name: "new" }]);
    expect(s.revision?.dataRevision).toBe(5);
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

  it("appends independently paged authoritative group rows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.setDatasetReady({
      ...makeDatasetReady([{ id: 1 }]),
      groupRows: [{ key: ["east"], count: 7000, summaries: [10] }],
      groupOffset: 0,
      groupLimit: 1,
      hasMoreGroups: true,
    });
    s.setDatasetReady({
      ...makeDatasetReady([{ id: 1 }]),
      groupRows: [{ key: ["west"], count: 5500, summaries: [20] }],
      groupOffset: 1,
      groupLimit: 1,
      hasMoreGroups: false,
    });

    expect(s.viewGroups.map((row) => row.key[0])).toEqual(["east", "west"]);
    expect(s.hasMoreViewGroups).toBe(false);
  });

  it("rejects a stale datasetReady that would overwrite a newer committed row", () => {
    const s = useTableStore();
    const revision = {
      databaseSessionId: "session-1",
      schemaRevision: "schema-1",
      dataRevision: 8,
    };
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "old" }], { revision }));
    s.applyCellEdit({
      rowKey: 1,
      column: "name",
      storedValue: "new",
      currentRow: { rowKey: 1, name: "new" },
      revision: { ...revision, dataRevision: 9 },
    });
    s.setDatasetReady({
      ...makeDatasetReady([{ rowKey: 1, name: "old" }]),
      revision,
    });

    expect(s.allRows).toEqual([{ rowKey: 1, name: "new" }]);
    expect(s.revision?.dataRevision).toBe(9);
    expect(s.loading).toBe(false);
  });

  it("retains a revision floor across same-schema refreshes", () => {
    const s = useTableStore();
    const oldRevision = {
      databaseSessionId: "session-1",
      schemaRevision: "schema-1",
      dataRevision: 8,
    };
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "old" }], {
      revision: oldRevision,
    }));
    s.applyCellEdit({
      rowKey: 1,
      column: "name",
      storedValue: "new",
      currentRow: { rowKey: 1, name: "new" },
      revision: { ...oldRevision, dataRevision: 9 },
    });

    s.reset({ preserveEditSchema: true });
    s.beginLoad();
    expect(s.appendPage(makePage([{ rowKey: 1, name: "old" }], {
      revision: oldRevision,
    }))).toBe(false);
    expect(s.setDatasetReady({
      ...makeDatasetReady([{ rowKey: 1, name: "old" }]),
      revision: oldRevision,
    })).toBe(false);
    expect(s.revision).toBeNull();
    expect(s.allRows).toEqual([]);

    const currentRevision = { ...oldRevision, dataRevision: 9 };
    expect(s.setDatasetReady({
      ...makeDatasetReady([{ rowKey: 1, name: "new" }]),
      revision: currentRevision,
    })).toBe(true);
    expect(s.revision).toEqual(currentRevision);
    expect(s.allRows).toEqual([{ rowKey: 1, name: "new" }]);
  });

  it("does not lower the public revision for an out-of-order mutation receipt", () => {
    const s = useTableStore();
    const base = {
      databaseSessionId: "session-1",
      schemaRevision: "schema-1",
      dataRevision: 10,
    };
    s.beginLoad();
    s.appendPage(makePage([
      { rowKey: 1, name: "one" },
      { rowKey: 2, name: "two" },
    ], { revision: base }));
    s.applyCellEdit({
      rowKey: 1,
      column: "name",
      storedValue: "new-one",
      currentRow: { rowKey: 1, name: "new-one" },
      revision: { ...base, dataRevision: 12 },
    });
    s.applyCellEdit({
      rowKey: 2,
      column: "name",
      storedValue: "new-two",
      currentRow: { rowKey: 2, name: "new-two" },
      revision: { ...base, dataRevision: 11 },
    });

    expect(s.revision?.dataRevision).toBe(12);
    expect(s.allRows).toEqual([
      { rowKey: 1, name: "new-one" },
      { rowKey: 2, name: "new-two" },
    ]);
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

  it("can retain edit schema across a same-schema data refresh", () => {
    const s = useTableStore();
    s.setEditSchema(editSchema, {
      databaseSessionId: "session",
      schemaRevision: "schema",
      dataRevision: 3,
    });

    s.reset({ preserveEditSchema: true });
    s.beginLoad();

    expect(s.editSchema).toEqual(editSchema);
    expect(s.revision).toBeNull();
    expect(s.loading).toBe(true);
  });

  it("drops retained edit schema when the refreshed page has a new schema revision", () => {
    const s = useTableStore();
    s.setEditSchema(editSchema, {
      databaseSessionId: "session",
      schemaRevision: "schema-1",
      dataRevision: 3,
    });
    s.reset({ preserveEditSchema: true });
    s.beginLoad();
    s.appendPage(makePage([], {
      revision: {
        databaseSessionId: "session",
        schemaRevision: "schema-2",
        dataRevision: 4,
      },
    }));

    expect(s.editSchema).toBeNull();
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

  it("late edit schema preserves an authoritative matching dataset revision", () => {
    const s = useTableStore();
    const authoritative = makeRevision({ dataRevision: 42 });
    s.setDatasetReady({
      ...makePage([]),
      loadedRows: 0,
      revision: authoritative,
    });

    s.setEditSchema(editSchema, {
      databaseSessionId: "",
      schemaRevision: authoritative.schemaRevision,
      dataRevision: 0,
    });

    expect(s.editSchema).toHaveLength(2);
    expect(s.revision).toEqual(authoritative);
  });

  it("applyCellEdit updates the edited cell in allRows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "old" }]));
    const before = s.allRows[0];
    const res: UpdateCellResult = {
      rowKey: 1,
      column: "name",
      storedValue: "new",
      currentRow: { rowKey: 1, name: "new" },
      revision: makeRevision({ dataRevision: 2 }),
    };
    s.applyCellEdit(res);
    expect(s.allRows[0]?.name).toBe("new");
    expect(s.allRows[0]).not.toBe(before);
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
      snapshot: lookupSnapshot(1),
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

  it("normalizes PocketBase id to rowKey when merging Lookup rows", () => {
    const s = useTableStore();
    s.setDatasetReady({
      ...makeDatasetReady([
        { rowKey: "r1", title: "draft", lookupValue: null },
      ], [
        { ...makeColumn("title"), fieldId: "records.title" },
        {
          ...makeColumn("lookupValue"),
          fieldId: "records.lookup",
          kind: "lookup",
          lookupId: "records.lookup",
        },
      ], 1),
      table: "records",
    });

    s.applyLookupQueryResult({
      contract: "vibetable.lookup-query.v1",
      collection: "records",
      requestGeneration: 2,
      schemaRevision: "s",
      permissionRevision: "p",
      lookupRevision: "l",
      columns: [],
      rows: [{ id: "r1", "records.lookup": "resolved" }],
      groups: [],
      offset: 0,
      limit: 1,
      filteredRows: 1,
      totalRows: 1,
      snapshot: lookupSnapshot(1),
    });

    expect(s.allRows).toEqual([{
      rowKey: "r1",
      title: "draft",
      lookupValue: "resolved",
    }]);
  });

  it("ignores a Lookup snapshot older than a committed cell mutation", () => {
    const s = useTableStore();
    s.setDatasetReady({
      ...makeDatasetReady([
        { rowKey: "r1", title: "committed", lookupValue: null },
      ], [
        { ...makeColumn("title"), fieldId: "records.title" },
        {
          ...makeColumn("lookupValue"),
          fieldId: "records.lookup",
          kind: "lookup",
          lookupId: "records.lookup",
        },
      ], 1),
      table: "records",
      revision: makeRevision({ dataRevision: 3 }),
    });

    s.applyLookupQueryResult({
      contract: "vibetable.lookup-query.v1",
      collection: "records",
      requestGeneration: 2,
      schemaRevision: "s",
      permissionRevision: "p",
      lookupRevision: "l",
      columns: [],
      rows: [{
        id: "r1",
        title: "before-edit",
        "records.lookup": "stale",
      }],
      groups: [],
      offset: 0,
      limit: 1,
      filteredRows: 1,
      totalRows: 1,
      snapshot: lookupSnapshot(2),
    });

    expect(s.allRows[0]).toEqual({
      rowKey: "r1",
      title: "committed",
      lookupValue: null,
    });
  });

  it("recovers the Lookup stable-key overlay after a valid refresh", () => {
    const s = useTableStore();
    s.setDatasetReady({
      ...makeDatasetReady([{ rowKey: "r1", lookupValue: null }], [{
        ...makeColumn("lookupValue"),
        fieldId: "records.lookup",
        kind: "lookup",
        lookupId: "records.lookup",
      }], 1),
      table: "records",
    });
    const base = {
      contract: "vibetable.lookup-query.v1" as const,
      collection: "records",
      requestGeneration: 2,
      schemaRevision: "s",
      permissionRevision: "p",
      lookupRevision: "l",
      columns: [],
      groups: [],
      offset: 0,
      limit: 1,
      filteredRows: 1,
      totalRows: 1,
      snapshot: lookupSnapshot(1),
    };

    s.applyLookupQueryResult({ ...base, rows: [{ "records.lookup": "bad" }] });
    expect(s.error).toBe("Lookup query returned a row without a stable key.");

    s.applyLookupQueryResult({
      ...base,
      rows: [{ id: "r1", "records.lookup": "recovered" }],
    });
    expect(s.error).toBeNull();
    expect(s.allRows[0]?.lookupValue).toBe("recovered");
  });
});
