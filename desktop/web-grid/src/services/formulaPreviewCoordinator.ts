import type { HostBridge } from "@/bridge/hostBridge";
import type { FormulaPreviewRpcPayload } from "@/contracts";

export type FormulaPreviewRequest = FormulaPreviewRpcPayload;

export interface FormulaPreviewResult {
  readonly values: Readonly<Record<string, unknown>>;
}

export interface FormulaPreviewPort {
  preview(
    request: FormulaPreviewRequest,
    signal: AbortSignal,
  ): Promise<FormulaPreviewResult>;
}

export interface FormulaPreviewCallbacks {
  readonly onResult: (result: FormulaPreviewResult) => void;
  readonly onError?: (error: unknown) => void;
}

/**
 * Owns the asynchronous formula-editor policy: debounce, cooperative abort,
 * and generation-based stale-response suppression.
 */
export class FormulaPreviewCoordinator {
  private timer: ReturnType<typeof setTimeout> | null = null;
  private active: AbortController | null = null;
  private generation = 0;
  private disposed = false;

  constructor(
    private readonly port: FormulaPreviewPort,
    private readonly delayMs = 250,
  ) {}

  schedule(
    request: FormulaPreviewRequest,
    callbacks: FormulaPreviewCallbacks,
  ): void {
    if (this.disposed) return;
    const generation = ++this.generation;
    if (this.timer !== null) clearTimeout(this.timer);
    this.active?.abort();
    this.active = null;
    this.timer = setTimeout(() => {
      this.timer = null;
      if (this.disposed || generation !== this.generation) return;
      const controller = new AbortController();
      this.active = controller;
      void this.execute(generation, controller, request, callbacks);
    }, this.delayMs);
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.generation += 1;
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
    this.active?.abort();
    this.active = null;
  }

  private async execute(
    generation: number,
    controller: AbortController,
    request: FormulaPreviewRequest,
    callbacks: FormulaPreviewCallbacks,
  ): Promise<void> {
    try {
      const result = await this.port.preview(request, controller.signal);
      if (!this.disposed
          && !controller.signal.aborted
          && generation === this.generation) {
        callbacks.onResult(result);
      }
    } catch (error) {
      if (!this.disposed
          && !controller.signal.aborted
          && generation === this.generation
          && !isAbortError(error)) {
        callbacks.onError?.(error);
      }
    } finally {
      if (this.active === controller) this.active = null;
    }
  }
}

/** Adapter kept at the WebView boundary; cancellation still suppresses late replies. */
export function createBridgeFormulaPreviewPort(
  bridge: Pick<HostBridge, "request">,
): FormulaPreviewPort {
  return {
    async preview(request, signal) {
      signal.throwIfAborted();
      const result = await Promise.race([
        bridge.request("formula.preview", request),
        aborted(signal),
      ]);
      const mappedError = mappedProductError(result);
      if (mappedError !== null) throw mappedError;
      if (!isRecord(result) || !isRecord(result.values)) {
        throw new Error("Formula preview returned an invalid response.");
      }
      return result as unknown as FormulaPreviewResult;
    },
  };
}

function mappedProductError(value: unknown): Error | null {
  if (!isRecord(value) || !isRecord(value.error)) return null;
  const message = value.error.message;
  const code = value.error.code;
  if (typeof message !== "string" || message.length === 0) return null;
  const error = new Error(message);
  error.name = typeof code === "string" && code.length > 0
    ? code
    : "FormulaPreviewError";
  return error;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function aborted(signal: AbortSignal): Promise<never> {
  return new Promise((_resolve, reject) => {
    signal.addEventListener(
      "abort",
      () => reject(new DOMException("cancelled", "AbortError")),
      { once: true },
    );
  });
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}
