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
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
  PastePlan,
  TableFieldType,
  TablePage,
} from "./contracts";
import {
  initialTableAdminState,
  reduce as reduceTableAdmin,
  requestCreate,
  requestDelete,
  type TableAdminEvent,
  type TableAdminState,
  type TableAdminStatus,
} from "./tableAdminFlow";
import {
  TABLE_FIELD_TYPES,
  validateFields,
  validateTableName,
} from "./tableAdminValidation";

// ---------------------------------------------------------------------------
// DOM bindings
// ---------------------------------------------------------------------------

const statusEl = document.getElementById("status");
const gridEl = document.getElementById("grid");
const openBtn = document.getElementById("open-database") as HTMLButtonElement | null;
const refreshBtn = document.getElementById("refresh") as HTMLButtonElement | null;
const rowCountEl = document.getElementById("row-count");
const loadingOverlay = document.getElementById("loading-overlay");
const errorOverlay = document.getElementById("error-overlay");
const errorMessageEl = document.getElementById("error-message");

// Sidebar + table-admin modal DOM bindings (Task 11).
const newTableBtn = document.getElementById("new-table-btn") as HTMLButtonElement | null;
const tableList = document.getElementById("table-list");
const createTableModal = document.getElementById("create-table-modal");
const createTableClose = document.getElementById("create-table-close");
const createTableName = document.getElementById("create-table-name") as HTMLInputElement | null;
const createTableFields = document.getElementById("create-table-fields");
const createTableAddField = document.getElementById("create-table-add-field");
const createTableError = document.getElementById("create-table-error");
const createTableCancel = document.getElementById("create-table-cancel");
const createTableSubmit = document.getElementById("create-table-submit") as HTMLButtonElement | null;
const deleteConfirmModal = document.getElementById("delete-confirm-modal");
const deleteConfirmText = document.getElementById("delete-confirm-text");
const deleteConfirmCancel = document.getElementById("delete-confirm-cancel");
const deleteConfirmOk = document.getElementById("delete-confirm-ok");

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

    // Keep the sidebar active highlight in sync with the selected table.
    renderSidebar();
  },
});

// ---------------------------------------------------------------------------
// Sidebar + table-admin state (create/delete modals)
// ---------------------------------------------------------------------------

let tableAdmin: TableAdminState = initialTableAdminState;
let pendingDeleteCollection: string | null = null;

/**
 * Apply a tableAdmin reducer event and react to the resulting state.
 *
 * Modal lifecycle: create/delete use `bridge.notify` (fire-and-forget, no
 * requestId). The orchestrator only sets status="creating"/"deleting"; it does
 * NOT emit success/failure. Instead:
 *   - Success is observed when the host pushes `database.collectionsChanged`
 *     → applyCollectionsChanged sets status back to "idle". When the create
 *     modal is open and status transitions creating→idle (no error), we close
 *     the modal.
 *   - Failure arrives as an uncorrelated `operation.failed` broadcast
 *     (requestId is null because notify carries none); the global
 *     operation.failed handler below routes it into createFailed/deleteFailed,
 *     which sets status="error". When the create modal is open and status is
 *     "error", we surface the message and re-enable the submit button.
 */
function dispatchTableAdmin(event: TableAdminEvent): void {
  const prevStatus = tableAdmin.status;
  tableAdmin = reduceTableAdmin(tableAdmin, event);
  renderSidebar();
  reactToTableAdminStatus(prevStatus, tableAdmin);
}

/**
 * Drive create-modal lifecycle from tableAdmin status transitions. Called after
 * every reducer step. The create modal is the only async-tracked modal; the
 * delete-confirm modal is dismissed immediately on click (delete errors surface
 * via the status line through the table-flow).
 */
function reactToTableAdminStatus(prev: TableAdminStatus, next: TableAdminState): void {
  if (!createTableModal || createTableModal.hidden) return;
  if (next.status === "idle" && prev === "creating") {
    // Create succeeded (collectionsChanged arrived). Close the modal.
    closeCreateTableModal();
    return;
  }
  if (next.status === "error" && next.error) {
    // A create (or delete) failed; surface the message and re-enable submit.
    if (createTableError) {
      createTableError.textContent = next.error;
      createTableError.hidden = false;
    }
    if (createTableSubmit) createTableSubmit.disabled = false;
  }
}

function renderSidebar(): void {
  if (!tableList) return;
  tableList.innerHTML = "";
  for (const name of tableAdmin.collections) {
    const li = document.createElement("li");
    li.className = "table-list__item";
    if (name === flow.getState().currentTable) {
      li.classList.add("table-list__item--active");
    }
    const span = document.createElement("span");
    span.className = "table-list__name";
    span.textContent = name;
    span.addEventListener("click", () => {
      flow.selectTable(name, (type, payload) => bridge.notify(type, payload));
    });
    const del = document.createElement("button");
    del.type = "button";
    del.className = "table-list__delete";
    del.textContent = "删除";
    del.addEventListener("click", (e) => {
      e.stopPropagation();
      openDeleteConfirm(name);
    });
    li.appendChild(span);
    li.appendChild(del);
    tableList.appendChild(li);
  }
}

// --- create-table modal ----------------------------------------------------

function openCreateTableModal(): void {
  if (!createTableModal || !createTableName || !createTableFields) return;
  createTableName.value = "";
  createTableFields.innerHTML = "";
  addFieldRow();
  if (createTableError) createTableError.hidden = true;
  if (createTableSubmit) createTableSubmit.disabled = true;
  createTableModal.hidden = false;
  createTableName.focus();
}

function addFieldRow(): void {
  if (!createTableFields) return;
  const row = document.createElement("div");
  row.className = "field-row";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "field__input";
  input.maxLength = 64;
  input.placeholder = "字段名";
  const select = document.createElement("select");
  select.className = "field__select";
  for (const t of TABLE_FIELD_TYPES) {
    const opt = document.createElement("option");
    opt.value = t;
    opt.textContent = t;
    select.appendChild(opt);
  }
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "btn btn--secondary";
  remove.textContent = "−";
  remove.addEventListener("click", () => row.remove());
  row.appendChild(input);
  row.appendChild(select);
  row.appendChild(remove);
  createTableFields.appendChild(row);
}

function closeCreateTableModal(): void {
  if (createTableModal) createTableModal.hidden = true;
}

function collectFieldRows(): Array<{ key: string; type: TableFieldType }> {
  if (!createTableFields) return [];
  const rows: Array<{ key: string; type: TableFieldType }> = [];
  for (const row of Array.from(createTableFields.querySelectorAll(".field-row"))) {
    const input = row.querySelector(".field__input") as HTMLInputElement | null;
    const select = row.querySelector(".field__select") as HTMLSelectElement | null;
    if (!input || !select) continue;
    rows.push({ key: input.value, type: select.value as TableFieldType });
  }
  return rows;
}

// --- delete-confirm modal --------------------------------------------------

function openDeleteConfirm(collection: string): void {
  pendingDeleteCollection = collection;
  if (deleteConfirmText) {
    deleteConfirmText.textContent = `确定要删除表 "${collection}" 吗？该操作将移除集合及其全部数据，且不可恢复。`;
  }
  if (deleteConfirmModal) deleteConfirmModal.hidden = false;
}

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
  // Seed the sidebar with the initial collection list and enable the
  // new-table button now that a database is open. The same collectionsChanged
  // event is reused for incremental updates pushed by the host after a
  // successful create/delete.
  //
  // KNOWN LIMITATION: only `payload.tables` is shown in the sidebar; views
  // (`payload.views`) are NOT surfaced, even though database.opened carries
  // them. This intentionally matches the minimal behavior shipped with the
  // web-unified sidebar: `database.collectionsChanged` (the incremental
  // refresh) has no `views` field, and views are not selectable through the
  // sidebar today. The deleted native populateTableSelect used [...tables,
  // ...views]; that behavior is intentionally dropped here. If views become
  // first-class (selectable/editable) later, add a `views` field to
  // collectionsChanged + sidebar rendering at the same time.
  dispatchTableAdmin({ type: "collectionsChanged", tables: payload.tables });
  if (newTableBtn) newTableBtn.disabled = false;
  const total = payload.tables.length + payload.views.length;
  setStatus(
    total === 0
      ? "Database opened (no tables)."
      : `Database opened: ${total} object${total === 1 ? "" : "s"}.`,
  );
});

bridge.on("database.collectionsChanged", (payload: CollectionsChangedPayload) => {
  dispatchTableAdmin({ type: "collectionsChanged", tables: payload.tables });
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
  // tableAdmin create/delete use notify (no requestId), so the host's
  // operation.failed reply for them is an UNCORRELATED broadcast (requestId
  // null) — it does not match a pending bridge.request(). Route it into the
  // tableAdmin reducer so the in-flight modal surfaces the error. Only route
  // when a create/delete is actually in flight (status creating/deleting), so
  // unrelated operation.failed broadcasts (e.g. paste) do not clobber the
  // table-admin error slot.
  if (tableAdmin.status === "creating") {
    dispatchTableAdmin({ type: "createFailed", message: payload.message });
  } else if (tableAdmin.status === "deleting") {
    dispatchTableAdmin({ type: "deleteFailed", message: payload.message });
  }
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

if (refreshBtn) {
  refreshBtn.addEventListener("click", () => {
    flow.refresh((type, payload) => bridge.notify(type, payload));
  });
}

// Sidebar + table-admin modal wiring (Task 11).
newTableBtn?.addEventListener("click", () => openCreateTableModal());
createTableClose?.addEventListener("click", closeCreateTableModal);
createTableCancel?.addEventListener("click", closeCreateTableModal);
createTableAddField?.addEventListener("click", () => addFieldRow());

createTableSubmit?.addEventListener("click", () => {
  if (!createTableName) return;
  const nameErr = validateTableName(createTableName.value);
  const { fields, errors } = validateFields(collectFieldRows());
  const allErrors = [nameErr, ...errors].filter((e): e is string => e !== null);
  if (allErrors.length > 0) {
    if (createTableError) {
      createTableError.textContent = allErrors.join(" / ");
      createTableError.hidden = false;
    }
    return;
  }
  if (createTableSubmit) createTableSubmit.disabled = true;
  // requestCreate is fire-and-forget (notify, no requestId): it dispatches
  // createStarted (status="creating") and returns. The modal stays open showing
  // a disabled-submit "creating" state; it is closed by reactToTableAdminStatus
  // when database.collectionsChanged arrives (creating→idle), or it surfaces an
  // error when an operation.failed broadcast is routed into createFailed.
  requestCreate(
    bridge,
    createTableName.value.trim(),
    fields,
    dispatchTableAdmin,
  );
});

deleteConfirmCancel?.addEventListener("click", () => {
  pendingDeleteCollection = null;
  if (deleteConfirmModal) deleteConfirmModal.hidden = true;
});

deleteConfirmOk?.addEventListener("click", () => {
  const collection = pendingDeleteCollection;
  if (!collection) return;
  pendingDeleteCollection = null;
  if (deleteConfirmModal) deleteConfirmModal.hidden = true;
  // requestDelete is fire-and-forget (notify): dispatches deleteStarted and
  // returns. Delete errors surface via the status line through the table-flow
  // (global operation.failed handler), and a declined delete (Deleted:false) is
  // posted by the host as operation.failed code DELETE_DECLINED.
  requestDelete(bridge, collection, dispatchTableAdmin);
});

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
