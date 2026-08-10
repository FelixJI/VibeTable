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
 *        * rejects  with a TimeoutError after its request-specific timeout.
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
  HostMessageType as SharedHostMessageType,
  HostPayloadMap as SharedHostPayloadMap,
  WebMessageType as SharedWebMessageType,
  WebPayloadMap as SharedWebPayloadMap,
  OperationFailedPayload,
} from "@/contracts";
import type {
  RuntimeDiagnosticsHostMessageType,
  RuntimeDiagnosticsHostPayloadMap,
  RuntimeDiagnosticsWebMessageType,
  RuntimeDiagnosticsWebPayloadMap,
} from "@/contracts/runtimeDiagnosticsContracts";
import {
  RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES,
  RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES,
} from "@/contracts/runtimeDiagnosticsContracts";
import type {
  AppPreferencesHostMessageType,
  AppPreferencesHostPayloadMap,
  AppPreferencesWebMessageType,
  AppPreferencesWebPayloadMap,
} from "@/contracts/appPreferencesContracts";
import {
  APP_PREFERENCES_HOST_MESSAGE_TYPES,
  APP_PREFERENCES_WEB_MESSAGE_TYPES,
} from "@/contracts/appPreferencesContracts";
import type {
  ReleaseUpdateHostMessageType,
  ReleaseUpdateHostPayloadMap,
  ReleaseUpdateWebMessageType,
  ReleaseUpdateWebPayloadMap,
} from "@/contracts/releaseUpdateContracts";
import {
  RELEASE_UPDATE_HOST_MESSAGE_TYPES,
  RELEASE_UPDATE_WEB_MESSAGE_TYPES,
} from "@/contracts/releaseUpdateContracts";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import {
  configureWorkspaceWire,
  nextWorkspaceWire,
} from "@/services/workspaceWireAllocator";
import type {
  WorkspaceV2HostMessageType,
  WorkspaceV2HostPayloadMap,
  WorkspaceV2RequestPayload,
  WorkspaceV2WebMessageType,
  WorkspaceV2WebPayloadMap,
} from "@/contracts/workspaceV2Bridge";
import {
  WORKSPACE_V2_HOST_MESSAGE_TYPES,
  WORKSPACE_V2_WEB_MESSAGE_TYPES,
} from "@/contracts/workspaceV2Bridge";

type HostMessageType =
  | SharedHostMessageType
  | RuntimeDiagnosticsHostMessageType
  | AppPreferencesHostMessageType
  | ReleaseUpdateHostMessageType
  | WorkspaceV2HostMessageType;
type HostPayloadMap =
  & SharedHostPayloadMap
  & RuntimeDiagnosticsHostPayloadMap
  & AppPreferencesHostPayloadMap
  & ReleaseUpdateHostPayloadMap
  & WorkspaceV2HostPayloadMap;
type WebMessageType =
  | SharedWebMessageType
  | RuntimeDiagnosticsWebMessageType
  | AppPreferencesWebMessageType
  | ReleaseUpdateWebMessageType
  | WorkspaceV2WebMessageType;
type WebPayloadMap =
  & SharedWebPayloadMap
  & RuntimeDiagnosticsWebPayloadMap
  & AppPreferencesWebPayloadMap
  & ReleaseUpdateWebPayloadMap
  & WorkspaceV2WebPayloadMap;

/** Diagnostic emitted when an inbound message is dropped. */
export type DiagnosticKind =
  | "unknown-type"
  | "malformed"
  | "mismatched-response";

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
  /** WebView2 native object channel; File objects never enter the JSON envelope. */
  postMessageWithAdditionalObjects?: (
    message: unknown,
    additionalObjects: readonly File[],
  ) => void;
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
  /**
   * Timeout for requests that open a host-owned path picker. The user's time
   * in a native dialog must not consume the ordinary RPC budget.
   * Default 30 minutes.
   */
  readonly nativePickerTimeoutMs?: number;
  /**
   * Native File-object delivery should be acknowledged almost immediately.
   * A shorter timeout lets the UI fall back to a host-owned picker when a
   * WebView runtime accepts the call but silently drops synthetic/unsupported
   * File objects.
   */
  readonly additionalObjectsTimeoutMs?: number;
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
  "host.startupStateChanged",
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
  "data.changed",
  "task.changed",
  "data.importSourceRequested",
  "data.exportTargetRequested",
  "data.previewImport",
  "data.applyImport",
  "data.export",
  "task.create",
  "task.cancel",
  "task.status",
  "dailyQuote.fetch",
  "field.settings.describe",
  "field.change.plan",
  "field.change.apply",
  "field.change.status",
  "field.change.cancel",
  "field.recycleBin.list",
  "schema.getTable",
  "query.page",
  "mutation.preview",
  "mutation.apply",
  "formula.validate",
  "formula.draft.validate",
  "formula.preview",
  "file.list",
  "file.token",
  "file.uploadRequested",
  "file.replaceRequested",
  "file.removeRequested",
  "events.reconcile",
  "schema.describe",
  "relation.searchTargets",
  "relation.createTarget",
  "relation.updateSingle",
  "relation.previewDelta",
  "relation.applyDelta",
  "lookup.list",
  "lookup.validate",
  "lookup.preview",
  "lookup.query",
	"lookup.valuePage",
  "preset.list",
  "preset.save",
  "preset.delete",
  "version.list",
  "version.create",
  "version.save",
  "version.compare",
  "version.promote",
  "version.delete",
  ...RUNTIME_DIAGNOSTICS_HOST_MESSAGE_TYPES,
  ...APP_PREFERENCES_HOST_MESSAGE_TYPES,
  ...RELEASE_UPDATE_HOST_MESSAGE_TYPES,
  ...WORKSPACE_V2_HOST_MESSAGE_TYPES,
  // B2 paste preview + apply outcomes.
  "table.pastePreviewReady",
  "table.pasteApplied",
  "history.pageLoaded",
  "history.restorePreviewReady",
  "history.restoreApplied",
  // Table management: collection lifecycle events.
  "database.collectionsChanged",
  "identifierMappings.result",
  "dashboard.listLoaded",
  "dashboard.loaded",
  "dashboard.manifestLoaded",
  "dashboard.queryLoaded",
  "dashboard.saved",
  "dashboard.deleted",
  "document.listLoaded",
  "document.actionCompleted",
  "document.diffCompleted",
  "document.operationFailed",
  "document.workspaceChanged",
  "plugin.catalog.changed",
  "plugin.task.changed",
  "plugin.interaction.requested",
  "plugin.surface.message",
  "plugin.catalog.list",
  "plugin.audit.list",
  "plugin.cleanup.listPending",
  "plugin.install.inspect",
  "plugin.install.github.inspect",
  "plugin.install.commit",
  "plugin.install.cancel",
  "plugin.lifecycle.setEnabled",
  "plugin.lifecycle.upgrade",
  "plugin.lifecycle.rollback",
  "plugin.lifecycle.uninstall",
  "plugin.action.describe",
  "plugin.action.start",
  "plugin.interaction.resolve",
  "plugin.task.cancel",
  "plugin.task.get",
  "plugin.surface.event",
]);

/**
 * Whitelist of web -> host message types that this layer is allowed to
 * produce. The bridge refuses to post anything outside this set.
 */
const WEB_MESSAGE_TYPES: ReadonlySet<WebMessageType> = new Set<
  WebMessageType
>([
  "app.ready",
  "host.startupRetryRequested",
  "host.startupCancelRequested",
  "database.openRequested",
  "table.selected",
  "table.pageRequested",
  "table.updateCellRequested",
  "table.insertRowRequested",
  "table.deleteRowsRequested",
  "field.settings.describe",
  "field.change.plan",
  "field.change.apply",
  "field.change.status",
  "field.change.cancel",
  "field.recycleBin.list",
  "schema.getTable",
  "query.page",
  "mutation.preview",
  "mutation.apply",
  "formula.validate",
  "formula.draft.validate",
  "formula.preview",
  "file.list",
  "file.token",
  "file.uploadRequested",
  "file.replaceRequested",
  "file.removeRequested",
  "file.previewRequested",
  "file.downloadRequested",
  "events.reconcile",
  "schema.describe",
  "relation.searchTargets",
  "relation.createTarget",
  "relation.updateSingle",
  "relation.previewDelta",
  "relation.applyDelta",
  "lookup.list",
  "lookup.validate",
  "lookup.preview",
  "lookup.query",
	"lookup.valuePage",
  "preset.list",
  "preset.save",
  "preset.delete",
  "version.list",
  "version.create",
  "version.save",
  "version.compare",
  "version.promote",
  "version.delete",
  ...RUNTIME_DIAGNOSTICS_WEB_MESSAGE_TYPES,
  ...APP_PREFERENCES_WEB_MESSAGE_TYPES,
  ...RELEASE_UPDATE_WEB_MESSAGE_TYPES,
  ...WORKSPACE_V2_WEB_MESSAGE_TYPES,
  // B3 query + state requests.
  "table.queryRequested",
  "gridState.saveRequested",
  // B2 paste preview + apply requests.
  "table.previewPasteRequested",
  "table.applyPasteRequested",
  "data.importSourceRequested",
  "data.exportTargetRequested",
  "data.previewImport",
  "data.applyImport",
  "data.export",
  "task.create",
  "task.cancel",
  "task.status",
  "dailyQuote.fetch",
  "history.queryRequested",
  "history.previewRestoreRequested",
  "history.applyRestoreRequested",
  // Table management: create/delete collection requests.
  "tableAdmin.createRequested",
  "tableAdmin.deleteRequested",
  "identifierMappings.listRequested",
  "identifierMappings.updateAliasesRequested",
  "identifierMappings.reconcileRequested",
  "dashboard.listRequested",
  "dashboard.readRequested",
  "dashboard.manifestRequested",
  "dashboard.queryRequested",
  "dashboard.saveRequested",
  "dashboard.deleteRequested",
  "dashboard.cancelRequested",
  "plugin.catalog.list",
  "plugin.audit.list",
  "plugin.cleanup.listPending",
  "plugin.install.inspect",
  "plugin.install.github.inspect",
  "plugin.install.commit",
  "plugin.install.cancel",
  "plugin.lifecycle.setEnabled",
  "plugin.lifecycle.upgrade",
  "plugin.lifecycle.rollback",
  "plugin.lifecycle.uninstall",
  "plugin.action.describe",
  "plugin.action.start",
  "plugin.interaction.resolve",
  "plugin.task.cancel",
  "plugin.task.get",
  "plugin.surface.event",
  "document.listRequested",
  "document.importRequested",
  "document.externalDropRequested",
  "document.dragOutRequested",
  "document.openRequested",
  "document.previewRequested",
  "document.diffRequested",
  "document.diffCancelRequested",
  "document.revealRequested",
  "document.relinkRequested",
  // Open the embedded data administration surface in this webview.
  "admin.openRequested",
]);

// ---------------------------------------------------------------------------
// Pending request bookkeeping
// ---------------------------------------------------------------------------

interface Pending {
  readonly messageType: WebMessageType;
  readonly responseTypes: ReadonlySet<HostMessageType>;
  readonly resolve: (value: unknown) => void;
  readonly reject: (error: unknown) => void;
  readonly timer: ReturnType<typeof setTimeout>;
}

/**
 * Correlated response names implemented by the desktop host. Closed RPC
 * endpoints (including plugin endpoints) reply with their request
 * type; workflow endpoints use the explicit outcome names below.
 */
const RESPONSE_TYPE_OVERRIDES: Readonly<
  Partial<Record<WebMessageType, readonly HostMessageType[]>>
> = {
  "database.openRequested": ["database.opened"],
  "table.selected": ["table.editSchemaLoaded"],
  "table.pageRequested": ["table.pageLoaded"],
  "table.updateCellRequested": ["table.editCommitted", "table.editRejected"],
  "table.insertRowRequested": ["table.rowsInserted"],
  "table.deleteRowsRequested": ["table.rowsDeleted"],
  "table.queryRequested": ["table.pageLoaded"],
  "table.previewPasteRequested": ["table.pastePreviewReady"],
  "table.applyPasteRequested": ["table.pasteApplied"],
  "history.queryRequested": ["history.pageLoaded"],
  "history.previewRestoreRequested": ["history.restorePreviewReady"],
  "history.applyRestoreRequested": ["history.restoreApplied"],
  "tableAdmin.createRequested": ["database.collectionsChanged"],
  "tableAdmin.deleteRequested": ["database.collectionsChanged"],
  "identifierMappings.listRequested": ["identifierMappings.result"],
  "identifierMappings.updateAliasesRequested": ["identifierMappings.result"],
  "identifierMappings.reconcileRequested": ["identifierMappings.result"],
  "dashboard.listRequested": ["dashboard.listLoaded"],
  "dashboard.readRequested": ["dashboard.loaded"],
  "dashboard.manifestRequested": ["dashboard.manifestLoaded"],
  "dashboard.queryRequested": ["dashboard.queryLoaded"],
  "dashboard.saveRequested": ["dashboard.saved"],
  "dashboard.deleteRequested": ["dashboard.deleted"],
  "document.listRequested": ["document.listLoaded"],
  "document.openRequested": ["document.actionCompleted"],
  "document.previewRequested": ["document.actionCompleted"],
  "document.diffRequested": ["document.diffCompleted"],
  "document.diffCancelRequested": ["document.diffCancelCompleted"],
  "document.revealRequested": ["document.actionCompleted"],
  // Desktop currently names correlated replies `workspace.v2.response`.
  // Keep `reply` accepted for protocol-v2 producers that use the catalog term.
  "workspace.v2.request": ["workspace.v2.response", "workspace.v2.reply"],
};

const HOST_PICKER_PREFIX = "host-picker://";
const WORKSPACE_BOOTSTRAP_METHODS = new Set([
  "workspace.create",
  "workspace.open",
  "workspace.register",
  "workspace.relink",
]);

function containsHostPickerSentinel(value: unknown): boolean {
  if (typeof value === "string") return value.startsWith(HOST_PICKER_PREFIX);
  if (Array.isArray(value)) return value.some(containsHostPickerSentinel);
  if (value && typeof value === "object") {
    return Object.values(value as Readonly<Record<string, unknown>>)
      .some(containsHostPickerSentinel);
  }
  return false;
}

function isWorkspaceBootstrapRequest(value: unknown): boolean {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  return WORKSPACE_BOOTSTRAP_METHODS.has(
    String((value as Readonly<Record<string, unknown>>).method ?? ""),
  );
}

function responseTypesFor(type: WebMessageType): ReadonlySet<HostMessageType> {
  const overrides = RESPONSE_TYPE_OVERRIDES[type];
  if (overrides) return new Set(overrides);
  // Product-data, relation/Lookup, attachment and plugin RPC replies
  // reuse the closed request type. Only accept that default when it is also a
  // declared host message type.
  return HOST_EVENT_TYPES.has(type as HostMessageType)
    ? new Set([type as HostMessageType])
    : new Set();
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
  request<K extends RuntimeDiagnosticsWebMessageType>(
    type: K,
    payload: RuntimeDiagnosticsWebPayloadMap[K],
  ): Promise<RuntimeDiagnosticsHostPayloadMap[K]>;
  request<K extends AppPreferencesWebMessageType>(
    type: K,
    payload: AppPreferencesWebPayloadMap[K],
  ): Promise<AppPreferencesHostPayloadMap[K]>;
  request<K extends ReleaseUpdateWebMessageType>(
    type: K,
    payload: ReleaseUpdateWebPayloadMap[K],
  ): Promise<ReleaseUpdateHostPayloadMap[K]>;
  request<K extends WorkspaceV2WebMessageType>(
    type: K,
    payload: WorkspaceV2WebPayloadMap[K],
  ): Promise<unknown>;
  request<K extends SharedWebMessageType>(
    type: K,
    payload: SharedWebPayloadMap[K],
  ): Promise<unknown>;
  /** Begin a correlated request and expose its envelope id for typed cancellation. */
  requestWithHandle<K extends RuntimeDiagnosticsWebMessageType>(
    type: K,
    payload: RuntimeDiagnosticsWebPayloadMap[K],
  ): { readonly requestId: string; readonly promise: Promise<unknown> };
  requestWithHandle<K extends WorkspaceV2WebMessageType>(
    type: K,
    payload: WorkspaceV2WebPayloadMap[K],
  ): { readonly requestId: string; readonly promise: Promise<unknown> };
  requestWithHandle<K extends SharedWebMessageType>(
    type: K,
    payload: SharedWebPayloadMap[K],
  ): { readonly requestId: string; readonly promise: Promise<unknown> };
  /** Outbound fire-and-forget notification (no requestId). */
  notify<K extends RuntimeDiagnosticsWebMessageType>(
    type: K,
    payload: RuntimeDiagnosticsWebPayloadMap[K],
  ): void;
  notify<K extends WorkspaceV2WebMessageType>(
    type: K,
    payload: WorkspaceV2WebPayloadMap[K],
  ): void;
  notify<K extends SharedWebMessageType>(
    type: K,
    payload: SharedWebPayloadMap[K],
  ): void;
  /**
   * Post an envelope and DOM File objects over WebView2's native additional
   * objects channel. Returns false when the installed runtime lacks the API.
   */
  notifyWithAdditionalObjects<K extends RuntimeDiagnosticsWebMessageType>(
    type: K,
    payload: RuntimeDiagnosticsWebPayloadMap[K],
    additionalObjects: readonly File[],
  ): boolean;
  notifyWithAdditionalObjects<K extends SharedWebMessageType>(
    type: K,
    payload: SharedWebPayloadMap[K],
    additionalObjects: readonly File[],
  ): boolean;
  /**
   * Correlated native-file request. Returns null when WebView2 cannot carry
   * the supplied native objects, allowing a closed host-owned picker fallback.
   */
  requestWithAdditionalObjects<K extends SharedWebMessageType>(
    type: K,
    payload: SharedWebPayloadMap[K],
    additionalObjects: readonly File[],
  ): Promise<unknown> | null;
  /** Subscribe to a whitelisted host -> web event. Returns an unsubscribe fn. */
  on<K extends RuntimeDiagnosticsHostMessageType>(
    type: K,
    handler: (payload: RuntimeDiagnosticsHostPayloadMap[K]) => void,
  ): () => void;
  on<K extends WorkspaceV2HostMessageType>(
    type: K,
    handler: (payload: WorkspaceV2HostPayloadMap[K]) => void,
  ): () => void;
  on<K extends SharedHostMessageType>(
    type: K,
    handler: (payload: SharedHostPayloadMap[K]) => void,
  ): () => void;
}

/**
 * Create a HostBridge. With no options, the bridge binds to
 * `window.chrome.webview` lazily; in jsdom (unit tests) the caller passes an
 * explicit `webview` shim.
 */
export function createHostBridge(options: HostBridgeOptions = {}): HostBridge {
  const timeoutMs = options.timeoutMs ?? 10_000;
  const nativePickerTimeoutMs = options.nativePickerTimeoutMs
    ?? Math.max(timeoutMs, 30 * 60_000);
  const additionalObjectsTimeoutMs =
    options.additionalObjectsTimeoutMs ?? Math.min(timeoutMs, 1_500);
  const updateCheckTimeoutMs = Math.max(timeoutMs, 30_000);
  const updateInstallTimeoutMs = Math.max(timeoutMs, 30 * 60_000);
  const workspaceBootstrapTimeoutMs = Math.max(timeoutMs, 60_000);
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
    // Normalize the envelope shape. The C# host posts via
    // `CoreWebView2.PostWebMessageAsString(json)` (see MainWindow.xaml.cs),
    // so `event.data` arrives as a JSON *string* and must be parsed here.
    // `PostWebMessageAsJson` (and the object-based webview shims used in unit
    // tests) deliver an already-parsed object, which we pass through as-is.
    const data = event.data;
    if (typeof data === "string") {
      let parsed: unknown;
      try {
        parsed = JSON.parse(data);
      } catch {
        onDiagnostic({
          kind: "malformed",
          reason: "inbound message string is not valid JSON",
          raw: data,
        });
        return;
      }
      handleMessage(parsed);
      return;
    }
    handleMessage(data);
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
        if (type === "operation.failed") {
          clearTimeout(entry.timer);
          pending.delete(requestId);
          entry.reject(
            new BridgeOperationError(
              normalizeFailure(payload),
            ),
          );
          return;
        }
        if (!entry.responseTypes.has(type as HostMessageType)) {
          onDiagnostic({
            kind: "mismatched-response",
            type,
            reason:
              `response type "${type}" does not match request type ` +
              `"${entry.messageType}" (requestId=${requestId}); expected: ` +
              `${[...entry.responseTypes].join(", ") || "<failure only>"}`,
            raw,
          });
          // Keep the request pending: a valid response or operation.failed may
          // still arrive, otherwise the existing timeout closes the request.
          return;
        }
        clearTimeout(entry.timer);
        pending.delete(requestId);
        entry.resolve(payload);
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

  function operationId(): string {
    if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
      return crypto.randomUUID();
    }
    const suffix = Math.floor(Math.random() * 0xffff_ffff)
      .toString(16)
      .padStart(12, "0");
    return `00000000-0000-4000-8000-${suffix}`;
  }

  function activeWorkspaceScope(): ReturnType<typeof nextWorkspaceWire> | undefined {
    try {
      const session = useWorkspaceSessionStore();
      if (
        !session.hasOpenWorkspace
        || !session.activeWorkspaceId
        || session.sessionEpoch < 1
      ) return undefined;
      configureWorkspaceWire(session.activeWorkspaceId, session.sessionEpoch);
      return nextWorkspaceWire(operationId());
    } catch {
      // HostBridge is also used in isolated tests without an active Pinia.
      return undefined;
    }
  }

  function outboundEnvelope(
    type: WebMessageType,
    payload: unknown,
    requestId?: string,
    nativeObjects?: true,
  ): BridgeMessage {
    if (type === "workspace.v2.request") {
      return {
        type,
        ...(requestId ? { requestId } : {}),
        ...(nativeObjects ? { nativeObjects } : {}),
        payload,
        wire: (payload as WorkspaceV2RequestPayload).wire,
      };
    }
    const scope = activeWorkspaceScope();
    return {
      type,
      ...(requestId ? { requestId } : {}),
      ...(nativeObjects ? { nativeObjects } : {}),
      ...(scope ? { scope } : {}),
      payload,
    };
  }

  function request<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): Promise<unknown> {
    return requestWithHandle(type, payload).promise;
  }

  function requestWithHandle<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): { readonly requestId: string; readonly promise: Promise<unknown> } {
    const requestId = generateRequestId();
    const env = outboundEnvelope(type, payload, requestId);
    const requestTimeoutMs = type === "update.install"
      ? updateInstallTimeoutMs
      : type === "update.check"
        ? updateCheckTimeoutMs
        : type === "workspace.v2.request" && containsHostPickerSentinel(payload)
          ? nativePickerTimeoutMs
          : type === "workspace.v2.request" && isWorkspaceBootstrapRequest(payload)
            ? workspaceBootstrapTimeoutMs
          : timeoutMs;
    const promise = new Promise<unknown>((resolve, reject) => {
      const timer = setTimeout(() => {
        if (pending.delete(requestId)) {
          reject(new BridgeTimeoutError(type, requestId, requestTimeoutMs));
        }
      }, requestTimeoutMs);
      pending.set(requestId, {
        messageType: type,
        responseTypes: responseTypesFor(type),
        resolve,
        reject,
        timer,
      });
      try {
        postEnvelope(env);
      } catch (err) {
        clearTimeout(timer);
        pending.delete(requestId);
        reject(err);
      }
    });
    return { requestId, promise };
  }

  function notify<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
  ): void {
    postEnvelope(outboundEnvelope(type, payload));
  }

  function notifyWithAdditionalObjects<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
    additionalObjects: readonly File[],
  ): boolean {
    if (!WEB_MESSAGE_TYPES.has(type)) {
      throw new Error(
        `HostBridge: refusing to post non-whitelisted web message type "${type}"`,
      );
    }
    const target = webview();
    if (!target.postMessageWithAdditionalObjects) return false;
    // The JSON-shaped envelope contains only the typed payload. File objects
    // travel exclusively in WebView2's native additionalObjects collection.
    try {
      target.postMessageWithAdditionalObjects(
        outboundEnvelope(type, payload, undefined, true),
        [...additionalObjects],
      );
      return true;
    } catch {
      // WebView2 rejects synthetic File objects that are not backed by a disk
      // path. Return the same capability signal as an unavailable native API;
      // the caller may use a closed host-owned picker flow.
      return false;
    }
  }

  function requestWithAdditionalObjects<K extends WebMessageType>(
    type: K,
    payload: WebPayloadMap[K],
    additionalObjects: readonly File[],
  ): Promise<unknown> | null {
    if (!WEB_MESSAGE_TYPES.has(type)) {
      throw new Error(
        `HostBridge: refusing to post non-whitelisted web message type "${type}"`,
      );
    }
    const target = webview();
    if (!target.postMessageWithAdditionalObjects) return null;
    const requestId = generateRequestId();
    const promise = new Promise<unknown>((resolve, reject) => {
      const timer = setTimeout(() => {
        if (pending.delete(requestId)) {
          reject(
            new BridgeTimeoutError(
              type,
              requestId,
              additionalObjectsTimeoutMs,
            ),
          );
        }
      }, additionalObjectsTimeoutMs);
      pending.set(requestId, {
        messageType: type,
        responseTypes: responseTypesFor(type),
        resolve,
        reject,
        timer,
      });
    });
    try {
      target.postMessageWithAdditionalObjects(
        outboundEnvelope(type, payload, requestId, true),
        [...additionalObjects],
      );
      return promise;
    } catch {
      const entry = pending.get(requestId);
      if (entry) clearTimeout(entry.timer);
      pending.delete(requestId);
      // Prevent an unhandled rejection: the pending promise was never exposed.
      promise.catch(() => undefined);
      return null;
    }
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

  return {
    start,
    stop,
    request,
    requestWithHandle,
    notify,
    notifyWithAdditionalObjects,
    requestWithAdditionalObjects,
    on,
  };
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
