import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTableStore } from "./tableStore";
import type {
  ColumnSchema,
  DatasetReadyPayload,
  TablePage,
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
    s.beginLoad();
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
