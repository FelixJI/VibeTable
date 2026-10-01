// S39 / #415: frozen, literal oracles for the same-package file workflow
// combination. Nothing in this module is derived from a product response:
// the source corpus is the S38 synthetic fixture with its single visible UI
// edit applied, and the final calculation numbers are pinned literals that a
// cold-reopened second Host must reproduce independently.
import path from "node:path";
import { fileURLToPath } from "node:url";
import { calculationChainUiFixture } from "./calculation_chain_journey.mjs";

export const COMBINATION_DOCX = "document-format.docx";
export const COMBINATION_XLSX = "document-content.xlsx";

export function fileWorkflowHasCurrentBinding(hits, documentId, revisionId) {
  const current = hits.filter(hit => hit.kind === "file" && hit.canonicalId === documentId);
  return current.length > 0 && current.every(hit => hit.sourceRevision === revisionId);
}

export function fileWorkflowCombinationSources() {
  const sources = calculationChainUiFixture();
  const first = sources[0];
  if (!first || first.marker !== "来源-000" || first.contract !== "合同甲" || first.amount !== 1) {
    throw new Error("frozen S38 source fixture drifted; re-pin the S39 combination oracle");
  }
  // The S38 journey performs exactly one visible source edit: 来源-000 amount +3.
  first.amount = 4;
  return sources;
}

export const FILE_WORKFLOW_FINAL_CALCULATION_ORACLE = Object.freeze([
  Object.freeze({ contract: "合同甲", matches: 199, sum: 994, doubled: 2982, total: 2983 }),
  Object.freeze({ contract: "合同乙", matches: 1, sum: 7, doubled: 21, total: 22 }),
  Object.freeze({ contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 }),
]);

// The static before-corpus fixtures are the immutable import revisions. Both
// the restored effective revision and the materialized workspace file must
// keep these exact bytes; no new hash is introduced for that comparison.
export function fileWorkflowCombinationBeforeCorpus(name) {
  const extension = /\.(docx|xlsx)$/u.exec(name)?.[0];
  if (!extension || !["document-format.docx", "document-content.xlsx"].includes(name)) {
    throw new Error(`unknown S39 combination corpus fixture: ${name}`);
  }
  // The S14 import names are "document-<case>.<ext>"; the static qualification
  // fixtures are "<case>-before.<ext>" in the OpenXml test corpus.
  const stem = name.slice("document-".length, -extension.length);
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)),
    "../../desktop/tests/VibeTable.DocumentDiff.OpenXml.Tests/TestData/Qualification",
    extension.slice(1));
  return path.join(root, `${stem}-before${extension}`);
}
