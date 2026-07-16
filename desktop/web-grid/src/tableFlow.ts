/**
 * tableFlow — the Phase-A frontend table flow state machine.
 *
 * This module owns the renderer-side bookkeeping for the host-driven table
 * flow. It is deliberately PURE w.r.t. the DOM: it takes inbound host events
 * and outbound bridge actions, and produces a state object + side-effectful
 * callbacks. main.ts binds those callbacks to the real DOM + HostBridge so the
 * unit tests can drive the state machine with a faked bridge.
 *
 * Flow (Task 10 brief):
 *   - `database.openRequested` is posted when the user reconnects Directus;
 *     the host replies with `database.opened` (tables + views).
 *   - Selecting a table posts `table.selected` (offset 0, limit 500). The host
 *     drives the multi-page CLIENT-mode fetch: one `table.pageLoaded` per page,
 *     then a single `table.datasetReady` once loadedRows == totalRows. In
 *     REMOTE mode only the requested page arrives.
 *   - Switching tables (or databases) must reset accumulated state: stale pages
 *     for a superseded table are dropped (the host also suppresses them, but
 *     the renderer defends in depth).
 */

import type {
  ColumnSchema,
  DatasetReadyPayload,
  DatabaseOpenedPayload,
  MutationRevision,
  QuerySnapshot,
  TablePageLoadedPayload,
} from "./contracts";

/** Phase-A client row budget; mirrors the backend constant. */
export const CLIENT_ROW_BUDGET = 25_000;

/** The accumulated state of the table flow. */
export interface TableFlowState {
  /** Name of the currently-open database table (or null if none selected). */
  currentTable: string | null;
  /** True while the host is fetching pages for the current table. */
  loading: boolean;
  /** Cumulative rows loaded so far for the current table. */
  loadedRows: number;
  /** Total rows in the current table (learned from the first page). */
  totalRows: number;
  /** Current table mode ("client" | "remote"); null before the first page. */
  mode: "client" | "remote" | null;
  /** Schema columns for the current table. */
  columns: readonly ColumnSchema[];
  /** Accumulated rows for the current table (client mode only). */
  rows: ReadonlyArray<Record<string, unknown>>;
  /** Query snapshot used to bind B2 paste selections to the rendered data. */
  querySnapshot: QuerySnapshot | null;
  /** Schema/data revision advertised by the Directus gateway. */
  revision: MutationRevision | null;
  /** Last error message, surfaced in the error overlay. Cleared on success. */
  error: string | null;
}

export const initialTableFlowState: TableFlowState = {
  currentTable: null,
  loading: false,
  loadedRows: 0,
  totalRows: 0,
  mode: null,
  columns: [],
  rows: [],
  querySnapshot: null,
  revision: null,
  error: null,
};

/**
 * Callbacks the state machine emits to bind to the DOM + bridge. Each method
 * receives the updated state so the binder can refresh the UI.
 */
export interface TableFlowCallbacks {
  /** Called whenever the state changes. Refreshes the toolbar + overlays. */
  onStateChange: (state: TableFlowState) => void;
}

/**
 * Create a table-flow controller. The controller owns the mutable state and
 * exposes typed event handlers that main.ts subscribes to the HostBridge.
 */
export function createTableFlow(callbacks: TableFlowCallbacks) {
  let state: TableFlowState = { ...initialTableFlowState };

  function setState(next: Partial<TableFlowState>): void {
    state = { ...state, ...next };
    callbacks.onStateChange(state);
  }

  function resetForTable(table: string | null): void {
    state = {
      ...initialTableFlowState,
      currentTable: table,
      loading: table !== null,
    };
    callbacks.onStateChange(state);
  }

  // -------------------------------------------------------------------
  // Outbound actions (called by main.ts in response to DOM events)
  // -------------------------------------------------------------------

  /** User requested a Directus reconnect. */
  function requestOpenDatabase(post: (type: "database.openRequested", payload: { path: "" }) => void): void {
    // The host ignores the renderer-supplied path (it uses the WPF file
    // picker); we send an empty path as a placeholder per the closed contract.
    post("database.openRequested", { path: "" });
    setState({ error: null });
  }

  /** User selected a table from the dropdown. */
  function selectTable(
    table: string,
    notify: (type: "table.selected", payload: { table: string }) => void,
  ): void {
    if (!table) {
      return;
    }
    resetForTable(table);
    notify("table.selected", { table });
  }

  /** User clicked "刷新" — re-selects the current table. */
  function refresh(
    notify: (type: "table.selected", payload: { table: string }) => void,
  ): void {
    if (state.currentTable) {
      const table = state.currentTable;
      resetForTable(table);
      notify("table.selected", { table });
    }
  }

  // -------------------------------------------------------------------
  // Inbound event handlers (subscribed to HostBridge.on)
  // -------------------------------------------------------------------

  function onDatabaseOpened(payload: DatabaseOpenedPayload): void {
    // The host opened a database; reset any accumulated table state.
    setState({
      ...initialTableFlowState,
    });
    // Note: the table dropdown is populated by main.ts from payload.tables,
    // since that is a DOM side-effect and this module stays DOM-free.
    void payload;
  }

  function onTablePageLoaded(payload: TablePageLoadedPayload): void {
    // Defend in depth against stale pages: ignore pages whose table is not the
    // current selection. The host suppresses these too, but the renderer must
    // not render data from a superseded table.
    if (state.currentTable !== payload.table) {
      return;
    }
    // CLIENT mode: accumulate rows across pages.
    // REMOTE mode: retain only the current page (do not accumulate).
    if (payload.mode === "remote") {
      setState({
        loading: false,
        loadedRows: payload.rows.length,
        totalRows: payload.totalRows,
        mode: "remote",
        columns: payload.columns,
        rows: payload.rows,
        querySnapshot: payload.querySnapshot ?? null,
        revision: payload.revision ?? null,
        error: null,
      });
      return;
    }
    // Client mode: the first page's columns + offset=0 establishes the schema.
    const isFirstPage = payload.offset === 0 || state.rows.length === 0;
    const accumulatedRows = isFirstPage
      ? [...payload.rows]
      : [...state.rows, ...payload.rows];
    setState({
      loading: true, // still loading until datasetReady
      loadedRows: payload.loadedRows ?? accumulatedRows.length,
      totalRows: payload.totalRows,
      mode: "client",
      columns: payload.columns,
      rows: accumulatedRows,
      querySnapshot: payload.querySnapshot ?? state.querySnapshot,
      revision: payload.revision ?? state.revision,
      error: null,
    });
  }

  function onDatasetReady(payload: DatasetReadyPayload): void {
    if (state.currentTable !== payload.table) {
      return;
    }
    // The complete client-mode dataset. loadedRows == totalRows.
    setState({
      loading: false,
      loadedRows: payload.loadedRows,
      totalRows: payload.totalRows,
      mode: "client",
      columns: payload.columns,
      rows: payload.rows,
      querySnapshot: payload.querySnapshot ?? state.querySnapshot,
      revision: payload.revision ?? state.revision,
      error: null,
    });
  }

  function onOperationFailed(message: string): void {
    setState({ loading: false, error: message });
  }

  return {
    getState: () => state,
    requestOpenDatabase,
    selectTable,
    refresh,
    onDatabaseOpened,
    onTablePageLoaded,
    onDatasetReady,
    onOperationFailed,
  };
}

export type TableFlowController = ReturnType<typeof createTableFlow>;
