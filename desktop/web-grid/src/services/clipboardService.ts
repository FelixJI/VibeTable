// #465: renderer clipboard service — runtime reply validation and stable
// failure mapping over the existing HostBridge; no retries, no second transport.

import {
  BridgeRequestRetiredError,
  BridgeTimeoutError,
  type HostBridge,
} from "@/bridge/hostBridge";
import {
  ClipboardOperationError,
  isClipboardOperationError,
  type ClipboardFailureReason,
  type ClipboardHostPayloadMap,
  type ClipboardWebMessageType,
  type ClipboardWebPayloadMap,
} from "@/contracts/clipboardContracts";
import { CLIPBOARD_FAILURE_CODES, CLIPBOARD_STALE_CODES } from "@/contracts/clipboardContracts";

/** Narrow seam so callers/tests can pass any real HostBridge instance. */
export type ClipboardBridge = Pick<HostBridge, "request">;

export interface ClipboardService {
  readText(): Promise<string>;
  writeText(text: string): Promise<void>;
}

/** True when the failure means the initiating workspace session went away. */
export function isClipboardStaleFailure(error: unknown): boolean {
  // BridgeRequestRetiredError is a DOMException whose `name` is "AbortError",
  // so only an instanceof match identifies it.
  if (error instanceof BridgeRequestRetiredError) return true;
  if (isPlainObject(error)) {
    const code = error.code;
    if (typeof code === "string" && STALE_CODES.has(code)) return true;
  }
  return false;
}

/** Map any HostBridge rejection onto a clipboard failure reason. */
export function clipboardFailureReason(error: unknown): ClipboardFailureReason {
  if (isClipboardOperationError(error)) return error.reason;
  if (error instanceof BridgeTimeoutError) return "timeout";
  if (isPlainObject(error)) {
    const code = error.code;
    if (typeof code === "string") {
      for (const reason of REASONS) {
        if (CLIPBOARD_FAILURE_CODES[reason] === code) return reason;
      }
      if (code === "MESSAGE_TOO_LARGE") return "tooLarge";
    }
  }
  return "failed";
}

export function createClipboardService(bridge: ClipboardBridge): ClipboardService {
  async function call<K extends ClipboardWebMessageType>(
    type: K,
    payload: ClipboardWebPayloadMap[K],
  ): Promise<ClipboardHostPayloadMap[K]> {
    try {
      return await bridge.request(type, payload);
    } catch (error) {
      // Retired/stale rejections keep their original identity (AbortError or
      // a stale operation.failed code) so callers can classify the
      // retirement; everything else maps onto a clipboard failure reason.
      if (isClipboardOperationError(error) || isClipboardStaleFailure(error)) throw error;
      throw new ClipboardOperationError(clipboardFailureReason(error));
    }
  }

  return {
    async readText() {
      const reply = await call("clipboard.readText", {});
      if (!isPlainObject(reply) || typeof reply.text !== "string") {
        throw new ClipboardOperationError("failed");
      }
      return reply.text;
    },
    async writeText(text: string) {
      const reply = await call("clipboard.writeText", { text });
      if (!isPlainObject(reply) || reply.written !== true) {
        throw new ClipboardOperationError("failed");
      }
    },
  };
}

// ---------------------------------------------------------------------------

const REASONS = Object.keys(CLIPBOARD_FAILURE_CODES) as ClipboardFailureReason[];
const STALE_CODES = new Set<string>(CLIPBOARD_STALE_CODES);

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
