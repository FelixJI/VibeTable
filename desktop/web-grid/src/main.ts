/**
 * main.ts — Phase A entry point (Task 10).
 *
 * Wires the HostBridge to the table-flow state machine and the DOM:
 *   - Toolbar: reconnect Directus (database.openRequested), collection selector
 *     (table.selected), refresh (re-select), row count.
 *   - Loading / error overlays covering the grid during fetches and failures.
 *   - The read-only Tabulator grid renders the dataset on `table.datasetReady`
 *     (client mode) or `table.pageLoaded` (remote mode).
 *
 * The host owns the multi-page client-mode fetch; the renderer accumulates
 * pages and renders the final dataset. Everything is local: no fetch, no CDN,
 * all data arrives over the bridge.
 */

import "tabulator-tables/dist/css/tabulator.min.css";
import "./styles.css";

import { createHostBridge, type Diagnostic, type HostBridge } from "./hostBridge";
import { createGrid } from "./grid/createGrid";
import {
  createTableFlow,
  type TableFlowState,
} from "./tableFlow";
import {
  createPasteFlow,
  errorsByRow,
  outcomeLine,
  summaryLine,
  type PasteBridgeAction,
} from "./pasteFlow";
import { classifyClipboard, mapCellsToColumns, parseClipboard } from "./grid/clipboardParser";
import { resolvePasteContext } from "./grid/pasteContext";
import type {
  ApplyPasteResult,
  DatabaseOpenedPayload,
  PastePlan,
  TablePage,
} from "./contracts";

// ---------------------------------------------------------------------------
// DOM bindings
// ---------------------------------------------------------------------------

const statusEl = document.getElementById("status");
const gridEl = document.getElementById("grid");
const openBtn = document.getElementById("open-database") as HTMLButtonElement | null;
const tableSelect = document.getElementById("table-select") as HTMLSelectElement | null;
const refreshBtn = document.getElementById("refresh") as HTMLButtonElement | null;
const rowCountEl = document.getElementById("row-count");
const loadingOverlay = document.getElementById("loading-overlay");
const errorOverlay = document.getElementById("error-overlay");
const errorMessageEl = document.getElementById("error-message");

// B2 paste panel DOM bindings (Task 4).
const pastePanel = document.getElementById("paste-panel");
const pasteTitle = document.getElementById("paste-title");
const pasteSummary = document.getElementById("paste-summary");
const pasteDiagnostics = document.getElementById("paste-diagnostics");
const pasteOverflow = document.getElementById("paste-overflow");
const pasteAckWrap = document.getElementById("paste-ack-wrap");
const pasteAck = document.getElementById("paste-ack") as HTMLInputElement | null;
const pasteConfirm = document.getElementById("paste-confirm") as HTMLButtonElement | null;
const pasteCancel = document.getElementById("paste-cancel") as HTMLButtonElement | null;
const pasteClose = document.getElementById("paste-close");

function setStatus(text: string): void {
  if (statusEl) statusEl.textContent = text;
}

function setRowCount(text: string): void {
  if (rowCountEl) rowCountEl.textContent = text;
}

function showLoading(visible: boolean): void {
  if (loadingOverlay) (loadingOverlay as HTMLElement).hidden = !visible;
}

function showError(message: string | null): void {
  if (!errorOverlay || !errorMessageEl) return;
  if (message) {
    errorMessageEl.textContent = message;
    (errorOverlay as HTMLElement).hidden = false;
  } else {
    (errorOverlay as HTMLElement).hidden = true;
  }
}

function populateTableSelect(tables: readonly string[], views: readonly string[]): void {
  if (!tableSelect) return;
  tableSelect.innerHTML = "";
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = tables.length + views.length === 0
    ? "(no tables)"
    : "(select a table)";
  tableSelect.appendChild(placeholder);
  for (const name of [...tables, ...views]) {
    const opt = document.createElement("option");
    opt.value = name;
    opt.textContent = name;
    tableSelect.appendChild(opt);
  }
  tableSelect.disabled = tables.length + views.length === 0;
}

// ---------------------------------------------------------------------------
// Bridge + flow controller
// ---------------------------------------------------------------------------

const bridge: HostBridge = createHostBridge({
  onDiagnostic: (d: Diagnostic) => {
    // eslint-disable-next-line no-console
    console.warn("[HostBridge diagnostic]", d);
  },
});

let currentGrid: ReturnType<typeof createGrid> | null = null;

function renderGrid(state: TableFlowState): void {
  if (!gridEl) return;
  if (state.rows.length === 0 || state.columns.length === 0) {
    return;
  }
  // Build a TablePage from the accumulated state. createGrid owns the
  // Tabulator lifecycle; we destroy the previous instance before building a
  // fresh one so re-selections don't leak.
  const page: TablePage = {
    table: state.currentTable ?? "",
    columns: state.columns,
    rows: state.rows,
    offset: 0,
    limit: state.totalRows,
    totalRows: state.totalRows,
    mode: state.mode ?? "client",
    querySnapshot: state.querySnapshot,
    revision: state.revision,
  };
  if (currentGrid && typeof currentGrid.destroy === "function") {
    try {
      currentGrid.destroy();
    } catch {
      // Best-effort teardown; a stale Tabulator instance is harmless if it
      // throws on destroy.
    }
  }
  currentGrid = createGrid(gridEl, page);
}

const flow = createTableFlow({
  onStateChange: (state: TableFlowState) => {
    // Toolbar enabled-state.
    if (openBtn) openBtn.disabled = false;
    if (tableSelect) {
      // Disable the selector only while a load is in progress so the user
      // can't re-select mid-fetch (the host cancels, but we lock the UI too).
      // It stays enabled after a database is open.
      tableSelect.disabled = state.loading && state.currentTable !== null;
    }
    if (refreshBtn) {
      refreshBtn.disabled = state.currentTable === null;
    }

    // Status + row count + overlays.
    if (state.error) {
      setStatus(`Error: ${state.error}`);
      showError(state.error);
      showLoading(false);
      setRowCount("");
      return;
    }
    showError(null);
    showLoading(state.loading);
    if (state.currentTable === null) {
      setStatus("Ready.");
      setRowCount("");
    } else if (state.loading) {
      setStatus(
        `Loading "${state.currentTable}"… ${state.loadedRows} / ${state.totalRows} rows.`,
      );
      setRowCount(
        state.totalRows > 0
          ? `${state.loadedRows} / ${state.totalRows} rows`
          : "",
      );
    } else {
      setStatus(
        state.mode === "remote"
          ? `Loaded page: ${state.loadedRows} of ${state.totalRows} rows in "${state.currentTable}" (remote).`
          : `Loaded ${state.loadedRows} rows from "${state.currentTable}".`,
      );
      setRowCount(
        state.totalRows > 0 ? `${state.loadedRows} / ${state.totalRows} rows` : "",
      );
    }

    // Render the grid only when not loading (avoid partial repaints mid-fetch
    // in client mode; in remote mode loading flips false on the single page).
    if (!state.loading) {
      renderGrid(state);
    }
  },
});

// ---------------------------------------------------------------------------
// B2 paste flow controller + bridge sender
// ---------------------------------------------------------------------------

function sendPasteAction(action: PasteBridgeAction): void {
  if (action.kind === "preview") {
    bridge.notify("table.previewPasteRequested", {
      collection: action.collection,
      schemaRevision: action.schemaRevision,
      selection: action.selection as never,
      startCell: action.startCell,
      cells: action.cells as never,
    });
  } else {
    bridge.notify("table.applyPasteRequested", {
      collection: action.collection,
      token: action.token,
      idempotencyKey: action.idempotencyKey,
    });
  }
}

const pasteFlow = createPasteFlow({
  onStateChange: (state) => {
    if (!pastePanel) return;
    const visible = state.phase !== "idle";
    (pastePanel as HTMLElement).hidden = !visible;
    if (!visible) return;
    if (pasteTitle) {
      pasteTitle.textContent =
        state.phase === "overflow"
          ? "粘贴过大"
          : state.phase === "committed" || state.phase === "conflict" || state.phase === "pending"
            ? "粘贴结果"
            : state.phase === "error"
              ? "粘贴出错"
              : "粘贴预览";
    }
    // Summary line.
    if (pasteSummary) {
      if (state.plan) {
        pasteSummary.textContent = summaryLine(state.plan.summary);
      } else if (state.result) {
        pasteSummary.textContent = outcomeLine(state.result);
      } else if (state.phase === "overflow") {
        pasteSummary.textContent = `${state.cellCount} 单元格，已超过 10,000 上限。`;
      } else {
        pasteSummary.textContent = state.error ?? "";
      }
    }
    // Diagnostics.
    if (pasteDiagnostics) {
      if (state.plan && state.plan.summary.errorCount > 0) {
        const grouped = errorsByRow(state.plan);
        pasteDiagnostics.innerHTML = grouped
          .map((g) =>
            g.diagnostics
              .map(
                (d) =>
                  `<div class="paste-panel__diagnostics__item paste-panel__diagnostics__item--${d.severity}">行 ${g.rowIndex + 1} 列 ${d.columnIndex + 1}: ${escapeHtml(d.message)}</div>`,
              )
              .join(""),
          )
          .join("");
      } else if (state.phase === "conflict" && state.result) {
        pasteDiagnostics.innerHTML = state.result.conflicts
          .map(
            (c) =>
              `<div class="paste-panel__diagnostics__item paste-panel__diagnostics__item--error">行 ${escapeHtml(String(c.rowKey))} 已被他人修改，请重新预览。</div>`,
          )
          .join("");
      } else {
        pasteDiagnostics.innerHTML = "";
      }
    }
    // Overflow notice.
    if (pasteOverflow) (pasteOverflow as HTMLElement).hidden = state.phase !== "overflow";
    // Warning acknowledgement.
    const hasWarnings = state.plan !== null && state.plan.summary.warningCount > 0;
    if (pasteAckWrap) (pasteAckWrap as HTMLElement).hidden = !hasWarnings;
    if (pasteAck) pasteAck.checked = state.warningsAcknowledged;
    // Confirm button.
    if (pasteConfirm) {
      pasteConfirm.disabled = !pasteFlow.canSubmit();
      pasteConfirm.textContent =
        state.phase === "applying" ? "提交中…" : state.phase === "pending" ? "等待确认…" : "提交";
      (pasteConfirm as HTMLElement).hidden =
        state.phase === "overflow" ||
        state.phase === "committed" ||
        state.phase === "conflict" ||
        state.phase === "pending" ||
        state.phase === "error";
    }
  },
});

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (ch) => {
    switch (ch) {
      case "&": return "&amp;";
      case "<": return "&lt;";
      case ">": return "&gt;";
      case '"': return "&quot;";
      default: return "&#39;";
    }
  });
}

// ---------------------------------------------------------------------------
// Host event subscriptions
// ---------------------------------------------------------------------------

bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
  flow.onDatabaseOpened(payload);
  populateTableSelect(payload.tables, payload.views);
  const total = payload.tables.length + payload.views.length;
  setStatus(
    total === 0
      ? "Database opened (no tables)."
      : `Database opened: ${total} object${total === 1 ? "" : "s"}.`,
  );
});

bridge.on("table.pageLoaded", flow.onTablePageLoaded);
bridge.on("table.datasetReady", flow.onDatasetReady);

// B2 paste outcomes.
bridge.on("table.pastePreviewReady", (plan: PastePlan) => {
  pasteFlow.onPreviewReady(plan);
});
bridge.on("table.pasteApplied", (result: ApplyPasteResult) => {
  pasteFlow.onApplyResult(result);
});

bridge.on("directus.changed", (payload) => {
  if (payload.invalidateQuery && flow.getState().currentTable === payload.collection) {
    flow.refresh((type, body) => bridge.notify(type, body));
  }
});

bridge.on("operation.failed", (payload) => {
  flow.onOperationFailed(payload.message);
});

// ---------------------------------------------------------------------------
// Outbound DOM → host wiring
// ---------------------------------------------------------------------------

if (openBtn) {
  openBtn.addEventListener("click", () => {
    // The host resolves the configured Directus source; renderer paths are ignored.
    flow.requestOpenDatabase((type, payload) => bridge.notify(type, payload));
  });
}

if (tableSelect) {
  tableSelect.addEventListener("change", () => {
    const table = tableSelect.value;
    if (table) {
      flow.selectTable(table, (type, payload) => bridge.notify(type, payload));
    }
  });
}

if (refreshBtn) {
  refreshBtn.addEventListener("click", () => {
    flow.refresh((type, payload) => bridge.notify(type, payload));
  });
}

// ---------------------------------------------------------------------------
// B2 paste wiring: Ctrl+V / paste over a selection → preview → confirm → apply.
// ---------------------------------------------------------------------------

/**
 * Resolve the editable columns + the current selection anchor for a paste.
 * The grid is read-only in earlier phases; B2 enables paste-driven edits by
 * reading the current table's edit schema. When no schema is available the
 * paste is rejected with a clear status message.
 */
function startPasteFromClipboard(rawClipboard: string): void {
  const state = flow.getState();
  if (!state.currentTable) {
    setStatus("请先选择一个表再粘贴。");
    return;
  }
  let parsed;
  try {
    parsed = parseClipboard(rawClipboard);
  } catch {
    setStatus("剪贴板内容为空，无法粘贴。");
    return;
  }
  const classified = classifyClipboard(parsed);
  if ("overflow" in classified) {
    // Oversize: surface the C1 import path without submitting.
    pasteFlow.requestPreview(sendPasteAction, {
      collection: state.currentTable,
      schemaRevision: "unknown",
      selection: { rowKeys: [] },
      startCell: { rowKey: null, column: "" },
      cells: [],
      cellCount: classified.cellCount,
    });
    return;
  }
  let context;
  try {
    context = resolvePasteContext({
      grid: currentGrid,
      columns: state.columns,
      querySnapshot: state.querySnapshot,
      revision: state.revision,
    });
  } catch (error) {
    setStatus(error instanceof Error ? error.message : "无法确定粘贴位置。");
    return;
  }
  const mapped = mapCellsToColumns(
    parsed,
    context.editableColumns,
    context.anchorColumnIndex,
  );
  pasteFlow.requestPreview(sendPasteAction, {
    collection: state.currentTable,
    schemaRevision: context.schemaRevision,
    selection: context.selection,
    startCell: context.startCell,
    cells: mapped.map((row) =>
      row.map((cell) => ({
        rowIndex: cell.rowIndex,
        columnIndex: cell.columnIndex,
        column: cell.column,
        rawValue: cell.rawValue,
        parsedValue: cell.rawValue,
      })),
    ),
    cellCount: parsed.cellCount,
  });
}

if (gridEl) {
  (gridEl as HTMLElement).addEventListener("paste", (event: ClipboardEvent) => {
    const data = event.clipboardData?.getData("text/plain") ?? "";
    if (data) {
      event.preventDefault();
      startPasteFromClipboard(data);
    }
  });
  // Ctrl+V fallback for environments where the paste event does not fire.
  (gridEl as HTMLElement).addEventListener("keydown", (event: KeyboardEvent) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v") {
      event.preventDefault();
      // Read from the navigator clipboard when available.
      const navClipboard = (navigator as { clipboard?: { readText?: () => Promise<string> } }).clipboard;
      if (navClipboard?.readText) {
        navClipboard.readText().then(
          (text) => startPasteFromClipboard(text),
          () => setStatus("无法读取剪贴板，请重试。"),
        );
      }
    }
  });
}

if (pasteAck) {
  pasteAck.addEventListener("change", () => {
    pasteFlow.acknowledgeWarnings(pasteAck.checked);
  });
}

if (pasteConfirm) {
  pasteConfirm.addEventListener("click", () => {
    // Fresh idempotency key per submission so retries are deduped server-side.
    pasteFlow.requestApply(sendPasteAction, crypto.randomUUID());
  });
}

if (pasteCancel) {
  pasteCancel.addEventListener("click", () => pasteFlow.cancel());
}

if (pasteClose) {
  pasteClose.addEventListener("click", () => pasteFlow.cancel());
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

bridge.start();
bridge.notify("app.ready", {});
setStatus("Ready.");
if (openBtn) openBtn.disabled = false;

// Expose for ad-hoc debugging in WebView2 devtools.
declare global {
  interface Window {
    __vibeTableBridge?: unknown;
  }
}
window.__vibeTableBridge = bridge;
