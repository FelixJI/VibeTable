import { BridgeRequestNotSentError } from "@/bridge/hostBridge";
import { useHostBridge } from "./bridgeContext";
import { useMutationScope } from "./mutationScope";
import { usePasteStore } from "@/stores/pasteStore";
import { useHistoryStore } from "@/stores/historyStore";
import type { ApplyPasteResult, PastePlan, PreviewPasteRequestedPayload } from "@/contracts";

export function usePasteService() {
  const bridge = useHostBridge();
  const store = usePasteStore();
  const history = useHistoryStore();
  let onApplied: ((result: ApplyPasteResult) => void) | undefined;
  let previewSequence = 0;
  let applying = false;
  let retireApplying: (() => void) | undefined;
  const scope = useMutationScope(() => {
    retireApplying?.();
    retireApplying = undefined;
    previewSequence += 1;
    applying = false;
    store.reset();
  });

  function init(handler?: (result: ApplyPasteResult) => void): void {
    onApplied = handler ?? onApplied;
  }

  function preview(payload: PreviewPasteRequestedPayload): void {
    if (applying) return;
    const sequence = ++previewSequence;
    const isCurrent = scope.capture();
    if (!isCurrent()) return;
    void bridge.request("table.previewPasteRequested", payload).then((result) => {
      if (isCurrent() && sequence === previewSequence) store.setPlan(result as PastePlan);
    }, (error: Error) => {
      if (isCurrent() && sequence === previewSequence) store.setError(error.message);
    });
  }

  function apply(payload: ApplyPasteInput): void {
    if (applying) return;
    if (history.writeUncertain) {
      store.setError("粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。");
      return;
    }
    const isCurrent = scope.capture();
    if (!isCurrent()) return;
    applying = true;
    previewSequence += 1;
    const historyIsCurrent = history.captureGeneration();
    const markUncertain = history.captureWriteUncertainty();
    store.beginApply();
    const request = bridge.requestWithHandle("table.applyPasteRequested", {
      collection: payload.collection, token: payload.token, idempotencyKey: payload.idempotencyKey,
    });
    if (request.sent) retireApplying = markUncertain;
    if (request.sent && isCurrent() && historyIsCurrent()) history.beginAction();
    void request.promise.then((result) => {
      if (!isCurrent()) return;
      applying = false;
      retireApplying = undefined;
      const applied = result as ApplyPasteResult;
      if (applied.outcome === "pending") {
        markUncertain();
        store.setError("粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。");
        return;
      }
      store.setResult(applied);
      if (applied.outcome === "committed" && historyIsCurrent()) onApplied?.(applied);
    }, (error: Error) => {
      if (error instanceof BridgeRequestNotSentError) {
        if (isCurrent()) {
          applying = false;
          retireApplying = undefined;
          store.setError(error.message);
        }
        return;
      }
      markUncertain();
      if (!isCurrent()) return;
      applying = false;
      retireApplying = undefined;
      store.setError(`${error.message} 粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。`);
    });
  }

  return { init, preview, apply, dispose: scope.dispose };
}

export interface ApplyPasteInput {
  readonly collection: string;
  readonly token: string;
  readonly idempotencyKey: string;
}
