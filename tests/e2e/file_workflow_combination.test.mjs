import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import {
  COMBINATION_DOCX,
  COMBINATION_XLSX,
  FILE_WORKFLOW_FINAL_CALCULATION_ORACLE,
  fileWorkflowCombinationBeforeCorpus,
  fileWorkflowCombinationSources,
  fileWorkflowHasCurrentBinding,
} from "./file_workflow_combination.mjs";

test("S39 source corpus pins the S38 fixture plus its single visible edit", () => {
  const sources = fileWorkflowCombinationSources();
  assert.equal(sources.length, 201);
  assert.deepEqual(sources[0], { marker: "来源-000", contract: "合同甲", amount: 4 });
  assert.deepEqual(sources[199], { marker: "来源-199", contract: "合同乙", amount: 7 });
  assert.deepEqual(sources[200], { marker: "来源-200", contract: "其它合同", amount: 900 });
  assert.equal(new Set(sources.map(row => row.marker)).size, 201);
});

test("S39 final calculation oracle is an independent literal pin", () => {
  // Recompute from the frozen corpus with plain JavaScript, never through the
  // product oracle helper, so the pinned literals below stay independent.
  const sources = fileWorkflowCombinationSources();
  const recomputed = ["合同甲", "合同乙", "合同丙"].map(contract => {
    const matched = sources.filter(row => row.contract === contract);
    const sum = matched.reduce((total, row) => total + row.amount, 0);
    return { contract, matches: matched.length, sum, doubled: sum * 3, total: sum * 3 + 1 };
  });
  assert.deepEqual(recomputed, [
    { contract: "合同甲", matches: 199, sum: 994, doubled: 2982, total: 2983 },
    { contract: "合同乙", matches: 1, sum: 7, doubled: 21, total: 22 },
    { contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 },
  ]);
  assert.deepEqual(FILE_WORKFLOW_FINAL_CALCULATION_ORACLE, recomputed);
});

test("S39 rejects unknown corpus names and resolves the two static before fixtures", () => {
  assert.throws(() => fileWorkflowCombinationBeforeCorpus("document-sparse.xlsx"), /unknown S39/);
  assert.throws(() => fileWorkflowCombinationBeforeCorpus("document-format.xlsx"), /unknown S39/);
  const fixtures = [COMBINATION_DOCX, COMBINATION_XLSX].map(fileWorkflowCombinationBeforeCorpus);
  for (const fixture of fixtures) {
    assert.ok(fixture.endsWith("-before.docx") || fixture.endsWith("-before.xlsx"));
    assert.ok(fs.statSync(fixture).isFile(), `${fixture} must exist`);
    assert.ok(fs.statSync(fixture).size > 0);
  }
});

test("restored current search rejects stale bindings and a missing file index", () => {
  const restored = { kind: "file", canonicalId: "doc", sourceRevision: "restored" };
  const stale = { ...restored, sourceRevision: "old" };
  assert.equal(fileWorkflowHasCurrentBinding([restored], "doc", "restored"), true);
  assert.equal(fileWorkflowHasCurrentBinding([restored, stale], "doc", "restored"), false);
  assert.equal(fileWorkflowHasCurrentBinding([], "doc", "restored"), false);
});
