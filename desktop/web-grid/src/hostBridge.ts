/**
 * HostBridge — a typed, whitelist-only bridge over `window.chrome.webview`.
 *
 * The bridge is deliberately NOT a generic forwarder. It exposes two surfaces:
 *
 *   - `request(type, payload)`  : web -> host request/response. Adds a unique
 *     `requestId`, posts `{type, requestId, payload}` via
 *     `window.chrome.webview.postMessage`, and returns a Promise that:
 *        * resolves on the matching `{type:<resultType>, requestId}` payload,
 *        * rejects  on `{type:"operation.failed", requestId}` payloads,
 *        * rejects  with a TimeoutError after `timeoutMs`.
 *
 *   - `notify(type, payload)`   : fire-and-forget web -> host (no requestId),
 *     used for `app.ready` and other one-shot notifications.
 *
 *   - `on(type, handler)`       : subscribe to whitelisted host -> web events.
 *
 * Inbound safety:
 *   - Every inbound message is runtime-checked to be an object with a string
 *     `type` and optional `requestId`/`payload`. Malformed messages are dropped
 *     after invoking `onDiagnostic`.
 *   - Inbound `type`s are filtered against the closed whitelist of host
 *     message types; unknown types are dropped after `onDiagnostic`.
 *
 * Dependency injection: tests pass a `webview` shim and an optional
 * `generateRequestId` for deterministic IDs. In production, `createHostBridge()`
 * (no args) binds to `window.chrome.webview` if present.
 */

import type {
  BridgeMessage,
  HostMessageType,
  HostPayloadMap,
  WebMessageType,
  WebPayloadMap,
  OperationFailedPayload,
} from "./contracts";

/** Diagnostic emitted when an inbound message is dropped. */
export type DiagnosticKind = "unknown-type" | "malformed";

export interface Diagnostic {
  readonly kind: DiagnosticKind;
  /** Present for `unknown-type`. */
  readonly type?: string;
  /** Human-readable detail. */
  readonly reason: string;
  /** The raw inbound value (best-effort). */
  readonly raw?: unknown;
}

/**
 * Narrow view of `window.chrome.webview`. Keeping this minimal avoids coupling
 * the bridge to the full WebView2 surface and makes the shim trivial in tests.
 */
export interface WebViewLike {
  postMessage: (message: unknown) => void;
  addEventListener: (
    type: "message",
    listener: (event: { readonly data: unknown }) => void,
  ) => void;
  removeEventListener: (
    type: "message",
    listener: (event: { readonly data: unknown }) => void,
  ) => void;
}

export interface HostBridgeOptions {
  /** WebView2 handle. Defaults to `window.chrome.webview` when omitted. */
  readonly webview?: WebViewLike;
  /** Per-request timeout in ms. Default 10_000. */
  readonly timeoutMs?: number;
  /** Deterministic requestId source (tests). Defaults to UUID-ish counter. */
  readonly generateRequestId?: () => string;
  /** Invoked for every dropped inbound message. No-op by default. */
  readonly onDiagnostic?: (d: Diagnostic) => void;
}

/** Error thrown on bridge timeout. */
export class BridgeTimeoutError extends Error {
  public override readonly name = "BridgeTimeoutError";
  public readonly requestId: string;
  public readonly messageType: string;
  public constructor(messageType: string, requestId: string, timeoutMs: number) {
    super(
      `HostBridge timeout: no response for "${messageType}" (requestId=${requestId}) after ${timeoutMs}ms`,
    );
    this.requestId = requestId;
    this.messageType = messageType;
  }
}

/** Error thrown when the host replies with `operation.failed`. */
export class BridgeOperationError extends Error {
  public override readonly name = "BridgeOperationError";
  public readonly code?: string;
  public constructor(payload: OperationFailedPayload) {
    super(payload.message);
    this.code = payload.code;
  }
}

/**
 * Whitelist of host -> web event types that carry a payload the web layer
 * understands. Anything else is dropped after a diagnostic.
 */
const HOST_EVENT_TYPES: ReadonlySet<HostMessageType> = new Set<
  HostMessageType
>([
  "database.opened",
  "table.pageLoaded",
  "table.datasetReady",
  "operation.failed",
  // B1 mutation notifications.
  "table.editSchemaLoaded",
  "table.editCommitted",
  "table.editRejected",
  "table.rowsInserted",
  "table.rowsDeleted",
  "directus.changed",
  // Table management: collection lifecycle events.
  "database.collectionsChanged",
]);

/**
 * Whitelist of web -> host message types that this layer is allowed to
 * produce. The bridge refuses to post anything outside this set.
 */
const WEB_MESSAGE_TYPES: ReadonlySet<WebMessageType> = new Set<
  WebMessageType
>([
  "app.ready",
  "database.openRequested",
  "table.selected",
  "table.pageRequested",
  "table.updateCellRequested",
  "table.insertRowRequested",
  "table.deleteRowsRequested",
  // B3 query + state requests.
  "table.queryRequested",
  "gridState.saveRequested",
  // B2 paste preview + apply requests.
  "table.previewPasteRequested",
  "table.applyPasteRequested",
  // Table management: create/delete collection requests.
  "tableAdmin.createRequested",
  "tableAdmin.deleteRequested",
  // Open the embedded Directus admin (Data Studio) in this webview.
  "admin.openRequested",
]);

// ---------------------------------------------------------------------------
// Pending request bookkeeping
// ---------------------------------------------------------------------------

interface Pending {
  readonly messageType: WebMessageType;
  readonly resolve: (value: unknown) => void;
  readonly reject: (error: unknown) => void;
  readonly timer: ReturnType<typeof setTimeout>;
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

export interface HostBridge {
  /** Begin listening to host `message` events. Idempotent. */
  start(): void;
  /** Stop listening. Idempotent. */
  stop(): void;
  /** Outbound request awaiting a matching response. */
  request<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): Promise<unknown>;
  /** Outbound fire-and-forget notification (no requestId). */
  notify<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): void;
  /** Subscribe to a whitelisted host -> web event. Returns an unsubscribe fn. */
  on<K extends HostMessageType>(
    type: K,
    handler: (payload: HostPayloadMap[K]) => void,
  ): () => void;
}

/**
 * Create a HostBridge. With no options, the bridge binds to
 * `window.chrome.webview` lazily; in jsdom (unit tests) the caller passes an
 * explicit `webview` shim.
 */
export function createHostBridge(options: HostBridgeOptions = {}): HostBridge {
  const timeoutMs = options.timeoutMs ?? 10_000;
  const onDiagnostic = options.onDiagnostic ?? (() => undefined);
  const generateRequestId =
    options.generateRequestId ?? defaultGenerateRequestId;

  // Pending requests keyed by requestId.
  const pending = new Map<string, Pending>();

  // Typed event handlers keyed by host event type.
  const handlers = new Map<HostMessageType, Set<(payload: unknown) => void>>();

  let boundListener: ((event: { readonly data: unknown }) => void) | null = null;
  let started = false;

  const webview: () => WebViewLike = () => {
    if (options.webview) return options.webview;
    const wv =
      typeof window !== "undefined" &&
      (window as { chrome?: { webview?: WebViewLike } }).chrome?.webview;
    if (!wv) {
      throw new Error(
        "HostBridge: window.chrome.webview is unavailable. " +
          "Pass an explicit `webview` (e.g. in tests or outside WebView2).",
      );
    }
    return wv;
  };

  // -----------------------------------------------------------------------
  // Inbound dispatch
  // -----------------------------------------------------------------------

  const onHostMessage = (event: { readonly data: unknown }): void => {
    // The bridge treats `event.data` as the envelope. (WebView2 posts the
    // message object directly as `e.data`.)
    handleMessage(event.data);
  };

  function handleMessage(raw: unknown): void {
    // --- Structural validation -------------------------------------------
    if (!isPlainObject(raw)) {
      onDiagnostic({
        kind: "malformed",
        reason: "inbound message is not an object",
        raw,
      });
      return;
    }
    const typeCandidate = (raw as { type?: unknown }).type;
    if (typeof typeCandidate !== "string" || typeCandidate.length === 0) {
      onDiagnostic({
        kind: "malformed",
        reason: "inbound message missing string `type`",
        raw,
      });
      return;
    }
    // The C# host's PostReply serializes a null RequestId as `"requestId":null`
    // when a notify-based request (fire-and-forget, no requestId) fails. Treat
    // `null` the same as absent (not malformed) so failure broadcasts still
    // reach the handler fan-out below. A null requestId will NOT match any
    // pending request() (the `pending` Map is keyed by strings), so it falls
    // through to the fan-out path exactly like an absent requestId.
    const requestId = (raw as { requestId?: unknown }).requestId;
    if (
      requestId !== undefined &&
      requestId !== null &&
      typeof requestId !== "string"
    ) {
      onDiagnostic({
        kind: "malformed",
        reason: "inbound message `requestId` must be a string when present",
        raw,
      });
      return;
    }
    const payload = (raw as { payload?: unknown }).payload;

    const type = typeCandidate as string;

    // --- Whitelist filter -------------------------------------------------
    if (!HOST_EVENT_TYPES.has(type as HostMessageType)) {
      onDiagnostic({
        kind: "unknown-type",
        type,
        reason: `inbound message type "${type}" is not in the host whitelist`,
        raw,
      });
      return;
    }

    // --- Resolve pending request (if any) --------------------------------
    // Only a real string requestId can match a pending request(). null (the
    // PostReply-with-null shape) and undefined (PostNotification shape) fall
    // through to the handler fan-out below.
    if (typeof requestId === "string") {
      const entry = pending.get(requestId);
      if (entry) {
        clearTimeout(entry.timer);
        pending.delete(requestId);
        if (type === "operation.failed") {
          entry.reject(
            new BridgeOperationError(
              normalizeFailure(payload),
            ),
          );
        } else {
          entry.resolve(payload);
        }
        // Note: for a request-response type we still ALSO fan out to handlers
        // only when the host uses it as a broadcast (no requestId). Since this
        // branch has a requestId, we return after resolving the request.
        return;
      }
    }

    // --- Fan out to typed handlers ---------------------------------------
    const set = handlers.get(type as HostMessageType);
    if (set) {
      for (const h of set) {
        try {
          h(payload);
        } catch (err) {
          onDiagnostic({
            kind: "malformed",
            type,
            reason: `handler for "${type}" threw: ${
              err instanceof Error ? err.message : String(err)
            }`,
            raw: payload,
          });
        }
      }
    }
  }

  // -----------------------------------------------------------------------
  // Public surface
  // -----------------------------------------------------------------------

  function start(): void {
    if (started) return;
    boundListener = onHostMessage;
    webview().addEventListener("message", boundListener);
    started = true;
  }

  function stop(): void {
    if (!started || !boundListener) return;
    webview().removeEventListener("message", boundListener);
    boundListener = null;
    started = false;
    // Reject any still-pending requests so callers don't hang forever.
    for (const entry of pending.values()) {
      clearTimeout(entry.timer);
      entry.reject(
        new BridgeTimeoutError(entry.messageType, "<bridge stopped>", timeoutMs),
      );
    }
    pending.clear();
  }

  function postEnvelope(env: BridgeMessage): void {
    if (!WEB_MESSAGE_TYPES.has(env.type as WebMessageType)) {
      throw new Error(
        `HostBridge: refusing to post non-whitelisted web message type "${env.type}"`,
      );
    }
    webview().postMessage(env);
  }

  function request<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): Promise<unknown> {
    const requestId = generateRequestId();
    const env: BridgeMessage = { type, requestId, payload };
    return new Promise<unknown>((resolve, reject) => {
      const timer = setTimeout(() => {
        if (pending.delete(requestId)) {
          reject(new BridgeTimeoutError(type, requestId, timeoutMs));
        }
      }, timeoutMs);
      pending.set(requestId, { messageType: type, resolve, reject, timer });
      try {
        postEnvelope(env);
      } catch (err) {
        clearTimeout(timer);
        pending.delete(requestId);
        reject(err);
      }
    });
  }

  function notify<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): void {
    postEnvelope({ type, payload });
  }

  function on<K extends HostMessageType>(
    type: K,
    handler: (payload: HostPayloadMap[K]) => void,
  ): () => void {
    let set = handlers.get(type);
    if (!set) {
      set = new Set();
      handlers.set(type, set);
    }
    set.add(handler as (payload: unknown) => void);
    return () => {
      const s = handlers.get(type);
      if (s) {
        s.delete(handler as (payload: unknown) => void);
        if (s.size === 0) handlers.delete(type);
      }
    };
  }

  return { start, stop, request, notify, on };
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function defaultGenerateRequestId(): string {
  // Lightweight unique id. WebView2 bridges do not require cryptographic
  // randomness; monotonic counter + timestamp + random suffix is enough to
  // avoid collisions across reconnects.
  defaultCounter += 1;
  const rand =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : Math.random().toString(36).slice(2, 10);
  return `r${Date.now().toString(36)}-${defaultCounter}-${rand}`;
}
let defaultCounter = 0;

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function normalizeFailure(payload: unknown): OperationFailedPayload {
  if (
    isPlainObject(payload) &&
    typeof payload.message === "string"
  ) {
    return {
      message: payload.message,
      ...(typeof payload.code === "string"
        ? { code: payload.code }
        : {}),
    };
  }
  return {
    message: "host reported operation.failed with no message",
  };
}

// Exposed for unit tests that want to drive inbound messages directly.
export interface HostBridgeTestHandle {
  handleMessage(raw: unknown): void;
}
