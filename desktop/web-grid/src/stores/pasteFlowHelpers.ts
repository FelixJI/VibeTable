/**
 * Pure helpers for the paste flow. State lives in `pasteStore`; these functions
 * only compute derived text/structure from a `PastePlan` / `ApplyPasteResult`.
 *
 * Adapted from the brief's `pasteFlow.ts` sketch, but the brief assumed wrong
 * field names (`plan.rows` as a number count, `plan.columns`, `plan.cells`).
 * The real `PastePlan` (see `src/contracts/index.ts`) carries:
 *   - `summary: PasteSummary` with explicit updateRows/insertRows/skipRows/
 *     errorCount/warningCount counts (used by {@link summaryLine}),
 *   - `rows: PastePlanRow[]` where each row has its own `diagnostics`,
 *   - a top-level `diagnostics: PasteCellDiagnostic[]` aggregate.
 *
 * Contract-shape deviations from the brief (field: brief-assumed -> actual):
 *   - `plan.rows` (number)   -> `plan.summary.updateRows + .insertRows` (text)
 *     and `plan.rows: PastePlanRow[]` (a list, used for diagnostics).
 *   - `plan.columns` (number) -> no equivalent count; the brief's "x N 列"
 *     column count is dropped because the contract has no column-count field.
 *     The preview panel derives per-row column counts from `PastePlanRow.changes`.
 *   - `plan.cells` (array with per-cell diagnostics) -> no `cells` field;
 *     diagnostics live in `plan.diagnostics` (top-level aggregate) and
 *     `plan.rows[i].diagnostics` (per-row). {@link errorsByRow} aggregates both.
 */

import type {
  ApplyPasteResult,
  PasteCellDiagnostic,
  PastePlan,
  PasteSummary,
} from "@/contracts";

/**
 * One-line preview summary derived from {@link PasteSummary}, e.g.
 * "将写入 12 行". Uses `updateRows + insertRows` (the rows that will actually
 * be written); skipped rows are surfaced separately by the panel.
 *
 * Includes a trailing warning/error hint when the summary reports any, so the
 * user is warned before applying.
 */
export function summaryLine(plan: PastePlan | null): string {
  if (!plan) return "";
  const s = plan.summary;
  const written = s.updateRows + s.insertRows;
  const parts: string[] = [`将写入 ${written} 行`];
  if (s.skipRows > 0) parts.push(`跳过 ${s.skipRows} 行`);
  if (s.errorCount > 0) parts.push(`${s.errorCount} 项错误`);
  else if (s.warningCount > 0) parts.push(`${s.warningCount} 项警告`);
  return parts.join("，");
}

/**
 * Outcome line after apply, e.g. "已创建 5 行，更新 7 行". Returns "无变更"
 * when nothing was written.
 */
export function outcomeLine(result: ApplyPasteResult | null): string {
  if (!result) return "";
  const parts: string[] = [];
  if (result.createdRowKeys.length) {
    parts.push(`创建 ${result.createdRowKeys.length} 行`);
  }
  if (result.updatedRowKeys.length) {
    parts.push(`更新 ${result.updatedRowKeys.length} 行`);
  }
  if (result.skippedRowKeys.length) {
    parts.push(`跳过 ${result.skippedRowKeys.length} 行`);
  }
  return parts.length ? `已${parts.join("，")}` : "无变更";
}

/**
 * Group diagnostics by row index for the preview panel, sorted ascending.
 *
 * The real `PastePlan` exposes diagnostics in TWO places: a top-level
 * `plan.diagnostics` aggregate AND a per-row `plan.rows[i].diagnostics` list.
 * To give the preview panel a single, complete picture of which rows have
 * problems, we AGGREGATE BOTH sources (de-duplicating identical diagnostics by
 * their `rowIndex`/`columnIndex`/`code` triple). This matches how a user
 * thinks about paste problems ("which rows have issues?"), independent of
 * where the host happened to attach the diagnostic.
 */
export function errorsByRow(
  plan: PastePlan | null,
): ReadonlyArray<{
  readonly rowIndex: number;
  readonly diagnostics: readonly PasteCellDiagnostic[];
}> {
  if (!plan) return [];

  const seen = new Set<string>();
  const byRow = new Map<number, PasteCellDiagnostic[]>();

  const ingest = (diag: PasteCellDiagnostic): void => {
    const key = `${diag.rowIndex}:${diag.columnIndex}:${diag.code}`;
    if (seen.has(key)) return;
    seen.add(key);
    const row = diag.rowIndex;
    let bucket = byRow.get(row);
    if (!bucket) {
      bucket = [];
      byRow.set(row, bucket);
    }
    bucket.push(diag);
  };

  // 1) Top-level aggregate diagnostics.
  for (const diag of plan.diagnostics) ingest(diag);
  // 2) Per-row diagnostics attached to each PastePlanRow.
  for (const row of plan.rows) {
    for (const diag of row.diagnostics) ingest(diag);
  }

  return Array.from(byRow.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([rowIndex, diagnostics]) => ({ rowIndex, diagnostics }));
}

/**
 * Convenience: count total rows the plan will write (update + insert). Exposed
 * so the panel/tests can assert the count without re-reading the summary.
 */
export function writtenRowCount(summary: PasteSummary): number {
  return summary.updateRows + summary.insertRows;
}
