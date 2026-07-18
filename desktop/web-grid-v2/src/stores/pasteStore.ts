import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { ApplyPasteResult, PastePlan } from "@/contracts";
import { outcomeLine, summaryLine } from "./pasteFlowHelpers";

/**
 * pasteStore — renderer-side state machine for the two-phase paste flow.
 *
 * Phase transitions (driven by `pasteService` wiring host events):
 *
 *     idle --setPlan()----> previewing
 *     previewing --beginApply()--> applying
 *     applying --setResult()-----> applied
 *     any ------setError()-------> error
 *     any ------reset()---------> idle
 *
 * The inbound `table.pastePreviewReady` payload IS a `PastePlan` (no wrapper),
 * and `table.pasteApplied` IS an `ApplyPasteResult` (see `HostPayloadMap` in
 * `src/contracts/index.ts`). `setPlan`/`setResult` take those types directly.
 *
 * `summaryText` is a derived getter: while previewing it reads
 * `plan.summary` (a {@link PasteSummary}), and after apply it reads the
 * `ApplyPasteResult` row-key lists. It is NOT backed by a stored string.
 */
export type PastePhase =
  | "idle"
  | "previewing"
  | "applying"
  | "applied"
  | "error"
  | "overflow";

export const usePasteStore = defineStore("paste", () => {
  const phase = ref<PastePhase>("idle");
  const plan = ref<PastePlan | null>(null);
  const result = ref<ApplyPasteResult | null>(null);
  /** User acknowledgement of the preview before applying (e.g. "I reviewed"). */
  const acked = ref(false);
  /** Last error message, surfaced in the error phase. */
  const error = ref<string | null>(null);

  /**
   * Derived one-line summary shown in the PastePanel.
   *
   * - applied: derived from `result.createdRowKeys/updatedRowKeys/skippedRowKeys`.
   * - overflow: empty (the overflow UI replaces this line).
   * - previewing: derived from `plan.summary` (updateRows + insertRows, plus
   *   skip/error/warning hints).
   * - otherwise: empty.
   */
  const summaryText = computed<string>(() => {
    if (phase.value === "applied") return outcomeLine(result.value);
    if (phase.value === "overflow") return "";
    if (phase.value === "previewing" || phase.value === "applying") {
      return summaryLine(plan.value);
    }
    return "";
  });

  /**
   * Accept the plan from `table.pastePreviewReady`. The payload IS the plan
   * (no wrapper). Resets ack/error/result and moves to "previewing".
   * If `plan.overflow` is true we move to the "overflow" phase instead so the
   * panel can show the redirect UI.
   */
  function setPlan(p: PastePlan): void {
    plan.value = p;
    result.value = null;
    acked.value = false;
    error.value = null;
    phase.value = p.overflow ? "overflow" : "previewing";
  }

  /** Flip the user acknowledgement flag. */
  function toggleAck(): void {
    acked.value = !acked.value;
  }

  /** Move from "previewing" to "applying" (cleared error). */
  function beginApply(): void {
    phase.value = "applying";
    error.value = null;
  }

  /**
   * Accept the result from `table.pasteApplied`. The payload IS the result
   * (no wrapper). Moves to "applied" and clears any stale error.
   */
  function setResult(r: ApplyPasteResult): void {
    result.value = r;
    phase.value = "applied";
    error.value = null;
  }

  /** Record an error and move to the "error" phase. */
  function setError(message: string): void {
    error.value = message;
    phase.value = "error";
  }

  /** Reset the entire flow back to idle. */
  function reset(): void {
    phase.value = "idle";
    plan.value = null;
    result.value = null;
    acked.value = false;
    error.value = null;
  }

  return {
    phase,
    plan,
    result,
    acked,
    error,
    summaryText,
    setPlan,
    toggleAck,
    beginApply,
    setResult,
    setError,
    reset,
  };
});
