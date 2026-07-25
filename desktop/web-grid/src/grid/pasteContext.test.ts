import { describe, expect, it } from "vitest";

import type { MutationRevision, QuerySnapshot } from "@/contracts";
import { resolvePasteContext } from "./pasteContext";

const SNAPSHOT: QuerySnapshot = {
  snapshotId: "snapshot-1",
  digest: "digest-1",
  databaseId: "local",
  table: "vibetable_projects",
  schemaRevision: "schema-1",
  dataRevision: 7,
  normalizedQuery: {},
};

const REVISION: MutationRevision = {
  databaseSessionId: "local",
  schemaRevision: "schema-1",
  dataRevision: 7,
};

function grid(rowKeys: readonly (string | number)[], fields: readonly string[]) {
  return {
    getRanges: () => [{
      getRows: () => rowKeys.map((rowKey) => ({ getData: () => ({ rowKey }) })),
      getColumns: () => fields.map((field) => ({ getField: () => field })),
    }],
  };
}

const COLUMNS = [
  { name: "id", title: "Id", dataType: "text" as const, editable: false, nullable: false },
  { name: "name", title: "Name", dataType: "text" as const, editable: true, nullable: false },
  { name: "amount", title: "Amount", dataType: "decimal" as const, editable: true, nullable: true },
];

describe("resolvePasteContext", () => {
  it("uses the latest range, editable-column order and real page revision", () => {
    const context = resolvePasteContext({
      grid: grid(["p1", "p2"], ["amount"]),
      columns: COLUMNS,
      querySnapshot: SNAPSHOT,
      revision: REVISION,
    });

    expect(context.schemaRevision).toBe("schema-1");
    expect(context.editableColumns).toEqual(["name", "amount"]);
    expect(context.anchorColumnIndex).toBe(1);
    expect(context.startCell).toEqual({ rowKey: "p1", column: "amount" });
    expect(context.selection.rowKeys).toEqual(["p1", "p2"]);
    expect(context.selection.querySnapshot).toBe(SNAPSHOT);
  });

  it("rejects paste before product snapshot metadata is ready", () => {
    expect(() => resolvePasteContext({
      grid: grid(["p1"], ["name"]),
      columns: COLUMNS,
      querySnapshot: null,
      revision: REVISION,
    })).toThrow("元数据");
  });

  it("rejects a read-only anchor", () => {
    expect(() => resolvePasteContext({
      grid: grid(["p1"], ["id"]),
      columns: COLUMNS,
      querySnapshot: SNAPSHOT,
      revision: REVISION,
    })).toThrow("可编辑字段");
  });

  it("rejects a selection without stable row keys", () => {
    const invalidGrid = {
      getRanges: () => [{
        getRows: () => [{ getData: () => ({}) }],
        getColumns: () => [{ getField: () => "name" }],
      }],
    };
    expect(() => resolvePasteContext({
      grid: invalidGrid,
      columns: COLUMNS,
      querySnapshot: SNAPSHOT,
      revision: REVISION,
    })).toThrow("稳定行标识");
  });
});
