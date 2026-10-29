import assert from "node:assert/strict";
import test from "node:test";
import { exactSyntheticCents, assertDatasetSeedPreview, assertDatasetSeedReceipt, assertDatasetSeedAuthority } from "./plugin_datasets.mjs";

test("independent synthetic dataset reconciler preserves cents beyond JS safe integers", () => {
  assert.equal(exactSyntheticCents([{ amount: "9007199254740993.01" }, { amount: "0.01" }, { amount: "-0.01" }], "amount"), "900719925474099301");
  for (const amount of [null, 0.1, "", "pending", "0.001"]) assert.throws(() => exactSyntheticCents([{ amount }], "amount"), /fixed-scale/);
});

function seedFixture() {
  const bindings = { heads: { tableId: "heads" }, lines: { tableId: "lines" }, allocations: { tableId: "allocations" } };
  const original = { newIds: { heads: { h: "original-head" }, lines: { l: "original-line" }, allocations: { a: "original-allocation" } } };
  const preview = { status: "prepared", requestId: "seed-request", workspaceId: "workspace", installationId: "installation",
    plan: { operations: [] }, newIds: { lines: {}, allocations: {} }, effects: [{ tableId: "heads", recordId: "original-head", operation: "update" }] };
  const rows = {
    heads: [{ id: "original-head", code: "SYNTHETIC-01", budget: "10.00", lines: ["original-line"] }],
    lines: [{ id: "original-line", amount: "8.00", head: "original-head", allocations: ["original-allocation"] }],
    allocations: [{ id: "original-allocation", amount: "8.00", line: "original-line" }],
  };
  for (let i = 0; i < 200; i++) {
    const line = `line-${i}`, allocation = `allocation-${i}`;
    preview.newIds.lines[`dl${i}`] = line;
    preview.newIds.allocations[`da${i}`] = allocation;
    preview.plan.operations.push({ entity: "lines", kind: "create", tempId: `dl${i}`, values: { amount: "0.01", head: "original-head" } },
      { entity: "allocations", kind: "create", tempId: `da${i}`, values: { amount: "0.01", line: { entity: "lines", tempId: `dl${i}` } } });
    preview.effects.push({ tableId: "lines", recordId: line, operation: "insert" }, { tableId: "allocations", recordId: allocation, operation: "insert" });
    rows.heads[0].lines.push(line);
    rows.lines.push({ id: line, amount: "0.01", head: "original-head", allocations: [allocation] });
    rows.allocations.push({ id: allocation, amount: "0.01", line });
  }
  const applied = { status: "applied", requestId: preview.requestId, workspaceId: preview.workspaceId, installationId: preview.installationId,
    newIds: structuredClone(preview.newIds), receipt: { status: "applied", changeSetId: "seed-change", rows: structuredClone(preview.effects) } };
  return { bindings, original, preview, applied, rows };
}

test("400 public creates have 401 canonical effects including the original head backlink", () => {
  const { preview, bindings, original, applied } = seedFixture();
  const expected = assertDatasetSeedPreview(preview, bindings, original.newIds.heads.h);
  assert.equal(expected.length, 401);
  assertDatasetSeedReceipt(applied, preview, expected);
  for (const mutate of [
    value => value.effects.shift(),
    value => value.effects.push({ tableId: "lines", recordId: "extra-business-row", operation: "insert" }),
    value => { value.effects[0].recordId = "another-head"; },
  ]) {
    const changed = structuredClone(preview); mutate(changed);
    assert.throws(() => assertDatasetSeedPreview(changed, bindings, original.newIds.heads.h), /canonical effects/);
  }
  const changed = structuredClone(applied);
  changed.receipt.rows[1].recordId = "unprepared-line";
  assert.throws(() => assertDatasetSeedReceipt(changed, preview, expected), /canonical preview boundary/);
});

test("independent authority oracle rejects extra or missing rows and incorrect reciprocal links", () => {
  const { preview, original, rows } = seedFixture();
  assertDatasetSeedAuthority(rows, preview, original);
  for (const mutate of [
    value => value.lines.pop(),
    value => value.allocations.push({ id: "extra-allocation", amount: "0.01", line: "line-0" }),
    value => { value.lines[1].head = "another-head"; },
    value => value.heads[0].lines.pop(),
    value => { value.allocations[1].line = "original-line"; },
    value => { value.lines[0].amount = "0.00"; },
  ]) {
    const changed = structuredClone(rows); mutate(changed);
    assert.throws(() => assertDatasetSeedAuthority(changed, preview, original));
  }
});
