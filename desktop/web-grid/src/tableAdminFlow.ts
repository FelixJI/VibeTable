/**
 * tableAdminFlow — sidebar state machine for the table-management UI.
 *
 * Pattern: pure reducer functions (no I/O) + thin orchestrators that call the
 * bridge and dispatch reducer events. Modeled on fieldHistoryFlow.
 *
 * State is held by the caller (main.ts); the flow does not retain state.
 * The table list is NOT updated on create/delete success here — the host
 * pushes `database.collectionsChanged` after a successful mutation, and
 * `applyCollectionsChanged` is what updates `collections`.
 *
 * Request model: create/delete use `bridge.notify(...)` (fire-and-forget, no
 * requestId), matching the codebase convention for web→host mutations (see
 * paste flow + B1 cell edits). The orchestrator therefore only dispatches
 * `createStarted`/`deleteStarted` (sets status="creating"/"deleting"); it does
 * NOT dispatch createSucceeded/createFailed. Success is observed indirectly
 * when the host pushes `database.collectionsChanged` (status → "idle").
 * Failure arrives as an uncorrelated `operation.failed` broadcast (requestId
 * is null because notify carries none); main.ts routes that into the
 * createFailed/deleteFailed reducers when a modal is mid-flight.
 */
import type {
  TableAdminCreatePayload,
  TableAdminDeletePayload,
} from "./contracts";

export type TableAdminStatus = "idle" | "creating" | "deleting" | "error";

export interface TableAdminState {
  readonly collections: readonly string[];
  readonly status: TableAdminStatus;
  readonly error: string | null;
}

export const initialTableAdminState: TableAdminState = {
  collections: [],
  status: "idle",
  error: null,
};

/** Minimal bridge surface this flow needs. HostBridge satisfies this. */
export interface HostBridgeLike {
  notify(type: "tableAdmin.createRequested", payload: TableAdminCreatePayload): void;
  notify(type: "tableAdmin.deleteRequested", payload: TableAdminDeletePayload): void;
}

export type TableAdminEvent =
  | { readonly type: "createStarted" }
  | { readonly type: "createSucceeded" }
  | { readonly type: "createFailed"; readonly message: string }
  | { readonly type: "deleteStarted" }
  | { readonly type: "deleteSucceeded" }
  | { readonly type: "deleteFailed"; readonly message: string }
  | {
      readonly type: "collectionsChanged";
      readonly tables: readonly string[];
    };

// --- pure reducers ---

export function applyCreateStarted(s: TableAdminState): TableAdminState {
  return { ...s, status: "creating", error: null };
}
export function applyCreateSucceeded(s: TableAdminState): TableAdminState {
  return { ...s, status: "idle", error: null };
}
export function applyCreateFailed(s: TableAdminState, message: string): TableAdminState {
  return { ...s, status: "error", error: message };
}
export function applyDeleteStarted(s: TableAdminState): TableAdminState {
  return { ...s, status: "deleting", error: null };
}
export function applyDeleteSucceeded(s: TableAdminState): TableAdminState {
  return { ...s, status: "idle", error: null };
}
export function applyDeleteFailed(s: TableAdminState, message: string): TableAdminState {
  return { ...s, status: "error", error: message };
}
export function applyCollectionsChanged(
  s: TableAdminState,
  tables: readonly string[],
): TableAdminState {
  return { ...s, collections: tables, status: "idle", error: null };
}

export function reduce(state: TableAdminState, event: TableAdminEvent): TableAdminState {
  switch (event.type) {
    case "createStarted":
      return applyCreateStarted(state);
    case "createSucceeded":
      return applyCreateSucceeded(state);
    case "createFailed":
      return applyCreateFailed(state, event.message);
    case "deleteStarted":
      return applyDeleteStarted(state);
    case "deleteSucceeded":
      return applyDeleteSucceeded(state);
    case "deleteFailed":
      return applyDeleteFailed(state, event.message);
    case "collectionsChanged":
      return applyCollectionsChanged(state, event.tables);
  }
}

// --- orchestrators ---
//
// These use bridge.notify (fire-and-forget, no requestId). They only dispatch
// the *Started event; success/failure is observed via the host-pushed
// database.collectionsChanged / operation.failed broadcasts (routed by main.ts).

export function requestCreate(
  bridge: HostBridgeLike,
  name: string,
  fields: TableAdminCreatePayload["fields"],
  dispatch: (event: TableAdminEvent) => void,
): void {
  dispatch({ type: "createStarted" });
  bridge.notify("tableAdmin.createRequested", { name, fields });
}

export function requestDelete(
  bridge: HostBridgeLike,
  collection: string,
  dispatch: (event: TableAdminEvent) => void,
): void {
  dispatch({ type: "deleteStarted" });
  bridge.notify("tableAdmin.deleteRequested", { collection });
}
