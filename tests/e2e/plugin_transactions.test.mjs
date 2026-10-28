import test from "node:test";
import assert from "node:assert/strict";
import { assertAppliedTransaction } from "./plugin_transactions.mjs";

test("three-table acceptance rejects partial, foreign and mismapped successful-looking receipts", () => {
  const preview = { workspaceId: "w", installationId: "i", requestId: "r", model: { bindings: {} }, newIds: {} };
  const rows = [];
  for (const entity of ["heads", "lines", "allocations"]) {
    preview.model.bindings[entity] = { tableId: `table-${entity}` };
    preview.newIds[entity] = { new: `row-${entity}` };
    rows.push({ tableId: `table-${entity}`, recordId: `row-${entity}`, operation: "insert" });
  }
  const value = { ...preview, status: "applied", receipt: { status: "applied", changeSetId: "c", rows } };
  assert.doesNotThrow(() => assertAppliedTransaction(value, preview));
  for (const changed of [
    { ...value, workspaceId: "other" },
    { ...value, status: "unknown" },
    { ...value, receipt: { ...value.receipt, rows: rows.slice(0, 2) } },
    { ...value, receipt: { ...value.receipt, rows: [rows[0], rows[1], { ...rows[2], recordId: "wrong" }] } },
  ]) assert.throws(() => assertAppliedTransaction(changed, preview));
});
