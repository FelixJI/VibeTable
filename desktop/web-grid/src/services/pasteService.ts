import { useHostBridge } from "./bridgeContext";
import { usePasteStore } from "@/stores/pasteStore";
import type {
  ApplyPasteRequestedPayload,
  ApplyPasteResult,
  PastePlan,
  PreviewPasteRequestedPayload,
} from "@/contracts";

/**
 * pasteService — wires inbound host events to `pasteStore` and exposes the
 * outbound paste preview/apply notifications.
 *
 * Inbound flow (host -> web):
 *   - `table.pastePreviewReady` (payload type `PastePlan`): the host emits this
 *     after computing a paste preview. The payload IS the `PastePlan` (there is
 *     NO wrapper like `{ plan }` — see `HostPayloadMap` in
 *     `src/contracts/index.ts`). We forward it directly to `pasteStore.setPlan`,
 *     which derives the phase from `plan.overflow` (overflow -> redirect UI).
 *   - `table.pasteApplied` (payload type `ApplyPasteResult`): the host emits
 *     this after applying. The payload IS the `ApplyPasteResult` (no wrapper).
 *     Forwarded directly to `pasteStore.setResult`.
 *
 * Outbound flow (web -> host):
 *   - `table.previewPasteRequested` (notify, fire-and-forget): posted with a
 *     {@link PreviewPasteRequestedPayload}. The grid layer assembles the
 *     `selection`/`startCell`/`cells` from the active Tabulator range and
 *     parsed clipboard (see `src/grid/pasteContext.ts` + `clipboardParser.ts`);
 *     the service only forwards the assembled payload.
 *   - `table.applyPasteRequested` (notify, fire-and-forget): posted with an
 *     {@link ApplyPasteRequestedPayload}, which carries `collection`, the
 *     single-use `token` from the plan, and an `idempotencyKey` the caller
 *     generates. The service stamps `beginApply()` on the store first so the
 *     UI flips to "applying" synchronously.
 *
 * Call `init()` once at app boot to subscribe to inbound events.
 */
export function usePasteService(): {
  init: () => void;
  preview: (payload: PreviewPasteRequestedPayload) => void;
  apply: (payload: ApplyPasteInput) => void;
} {
  const bridge = useHostBridge();
  const store = usePasteStore();

  function init(): void {
    bridge.on("table.pastePreviewReady", (payload: PastePlan) => {
      store.setPlan(payload);
    });
    bridge.on("table.pasteApplied", (payload: ApplyPasteResult) => {
      store.setResult(payload);
    });
  }

  /**
   * Request a paste preview. The grid layer assembles the full
   * {@link PreviewPasteRequestedPayload} (collection, schemaRevision, selection
   * snapshot, anchor cell, parsed cells) and passes it here; the service
   * forwards it to the host unchanged.
   */
  function preview(payload: PreviewPasteRequestedPayload): void {
    bridge.notify("table.previewPasteRequested", payload);
  }

  /**
   * Request applying the current preview. Flips the store to "applying" and
   * posts an {@link ApplyPasteRequestedPayload}. The caller supplies the
   * `collection`, the `token` (from `plan.token.token`), and an
   * `idempotencyKey` (so retries don't double-write).
   */
  function apply(payload: ApplyPasteInput): void {
    store.beginApply();
    bridge.notify("table.applyPasteRequested", {
      collection: payload.collection,
      token: payload.token,
      idempotencyKey: payload.idempotencyKey,
    });
  }

  return { init, preview, apply };
}

/**
 * Input to `apply()`. Mirrors {@link ApplyPasteRequestedPayload} but split out
 * so callers do not need to import the wire type. The `idempotencyKey` should
 * be a fresh UUID-ish string per logical apply attempt.
 */
export interface ApplyPasteInput {
  readonly collection: string;
  readonly token: string;
  readonly idempotencyKey: string;
}
