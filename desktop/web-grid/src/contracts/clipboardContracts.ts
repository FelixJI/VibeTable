// #465: closed host-owned text clipboard DTOs (host mirrors codes exactly;
// empty-success is never mixed with failure states).

export const CLIPBOARD_WEB_MESSAGE_TYPES = [
  "clipboard.readText",
  "clipboard.writeText",
] as const;

export type ClipboardWebMessageType =
  (typeof CLIPBOARD_WEB_MESSAGE_TYPES)[number];

export const CLIPBOARD_HOST_MESSAGE_TYPES = CLIPBOARD_WEB_MESSAGE_TYPES;

export type ClipboardHostMessageType =
  (typeof CLIPBOARD_HOST_MESSAGE_TYPES)[number];

/** Actual full-JSON-envelope UTF-8 budget, mirrored from the host router cap. */
export const CLIPBOARD_MESSAGE_MAX_BYTES = 4 * 1024 * 1024;

/** Distinct non-success outcomes; empty text is a success, not a failure. */
export type ClipboardFailureReason =
  | "unavailable"
  | "busy"
  | "failed"
  | "timeout"
  | "tooLarge";

/** Typed clipboard failure carrying its stable reason. */
export class ClipboardOperationError extends Error {
  public override readonly name = "ClipboardOperationError";
  public readonly reason: ClipboardFailureReason;

  public constructor(reason: ClipboardFailureReason) {
    super(reason);
    this.reason = reason;
  }
}

export function isClipboardOperationError(error: unknown): error is ClipboardOperationError {
  return error instanceof ClipboardOperationError;
}

export const CLIPBOARD_FAILURE_CODES: Readonly<Record<ClipboardFailureReason, string>> = {
  unavailable: "CLIPBOARD_UNAVAILABLE",
  busy: "CLIPBOARD_BUSY",
  failed: "CLIPBOARD_FAILED",
  timeout: "CLIPBOARD_TIMEOUT",
  tooLarge: "CLIPBOARD_TOO_LARGE",
};

/** In-flight workspace-session loss is dropped silently, never shown as an error. */
export const CLIPBOARD_STALE_CODES: readonly string[] = [
  "WORKSPACE_SESSION_STALE",
  "WORKSPACE_SESSION_UNAVAILABLE",
];

export interface ClipboardWebPayloadMap {
  "clipboard.readText": Readonly<Record<string, never>>;
  "clipboard.writeText": { readonly text: string };
}

export interface ClipboardHostPayloadMap {
  "clipboard.readText": { readonly text: string };
  "clipboard.writeText": { readonly written: true };
}
