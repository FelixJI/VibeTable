import { BridgeRequestNotSentError } from "@/bridge/hostBridge";
import { useHostBridge } from "./bridgeContext";
import { useMutationScope } from "./mutationScope";
import { isDefiniteMutationRejection } from "./notificationPolicy";
import { usePasteStore } from "@/stores/pasteStore";
import { useHistoryStore } from "@/stores/historyStore";
import type { ApplyPasteResult, MutationErrorPayload, PastePlan, PreviewPasteRequestedPayload } from "@/contracts";

export function usePasteService() {
  const bridge = useHostBridge();
  const store = usePasteStore();
  const history = useHistoryStore();
  let onApplied: ((result: ApplyPasteResult) => void) | undefined;
  let previewSequence = 0;
  let applying = false;
  let disposed = false;
  let retireApplying: (() => void) | undefined;
  const scope = useMutationScope(() => {
    retireApplying?.();
    retireApplying = undefined;
    previewSequence += 1;
    applying = false;
    store.reset();
  });

  function init(handler?: (result: ApplyPasteResult) => void): void {
    // Dispose is permanent: init can never re-arm a retired owner's callback.
    if (disposed) return;
    onApplied = handler ?? onApplied;
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    // scope.dispose keeps #506 retirement semantics for an in-flight apply.
    scope.dispose();
    onApplied = undefined;
  }

  function preview(payload: PreviewPasteRequestedPayload): void {
    if (disposed || applying) return;
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
    if (disposed || applying) return;
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
    let request: ReturnType<typeof bridge.requestWithHandle>;
    try {
      request = bridge.requestWithHandle("table.applyPasteRequested", {
        collection: payload.collection, token: payload.token, idempotencyKey: payload.idempotencyKey,
      });
    } catch (error) {
      const notSent = error instanceof BridgeRequestNotSentError;
      if (!notSent) markUncertain();
      if (isCurrent()) {
        applying = false;
        store.setError(notSent ? error.message : "粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。");
      }
      return;
    }
    if (request.sent) retireApplying = markUncertain;
    const settleAction = request.sent && isCurrent() && historyIsCurrent() ? history.beginAction() : undefined;
    void request.promise.then((result) => {
      if (typeof result === "object" && result !== null && "kind" in result) {
        const rejection = result as MutationErrorPayload;
        const zeroWrites = isDefiniteMutationRejection(rejection);
        settleAction?.(zeroWrites);
        if (!zeroWrites) markUncertain();
        if (!isCurrent()) return;
        applying = false;
        retireApplying = undefined;
        store.setError(zeroWrites ? rejection.message : "粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。");
        return;
      }
      const applied = result as ApplyPasteResult;
      // Conflict is an atomic rejection; an empty committed result is the all-skip path.
      const zeroWrites = applied.outcome === "conflict" || (applied.outcome === "committed"
        && applied.createdRowKeys.length === 0 && applied.updatedRowKeys.length === 0);
      settleAction?.(zeroWrites);
      if (!isCurrent()) return;
      applying = false;
      retireApplying = undefined;
      if (applied.outcome === "pending") {
        markUncertain();
        store.setError("粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。");
        return;
      }
      store.setResult(applied);
      if (applied.outcome === "committed" && historyIsCurrent()) onApplied?.(applied);
    }, (error: Error) => {
      const notSent = !request.sent || error instanceof BridgeRequestNotSentError;
      settleAction?.(notSent);
      if (notSent) {
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

  return { init, preview, apply, dispose };
}

export interface ApplyPasteInput {
  readonly collection: string;
  readonly token: string;
  readonly idempotencyKey: string;
}
