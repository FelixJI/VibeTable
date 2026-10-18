import {
  createSurfaceClient, describeCellValue, isMissingCell, PluginCapabilityError,
  type DataDescription, type JsonObject, type JsonValue, type SurfaceMutationPlan,
  type SurfacePresentation,
} from "@vibetable/plugin-sdk";
import {
  buildNormalizationPlan, createPager, describeMutationFailure, describeSubmitOutcome,
  editableFields, foldAllPages, formInputValue, formValues, overviewSummary,
  type FormEdit, type MutationOutcomeView, type PagerView,
} from "./records.js";

const api = createSurfaceClient(window.name);
const statusEl = document.querySelector<HTMLElement>("#status")!;
const staleEl = document.querySelector<HTMLElement>("#stale")!;
let description: DataDescription | null = null;
let selectedFields: Set<string> | null = null;
let generation = 0;
let pendingPage: AbortController | null = null;
let saving: AbortController | null = null;
let unknownCommit = false;
const pagers = new Map<string, ReturnType<typeof createPager>>();
const setStatus = (text: string): void => { statusEl.textContent = text; };
let clearingPagers: Promise<void> = Promise.resolve();
let pagerCleanupFailure: unknown = null;
function clearPagers(explicit = false): Promise<void> {
  pendingPage?.abort(); generation++;
  if (pagerCleanupFailure && !explicit) return Promise.reject(pagerCleanupFailure);
  const entries = [...pagers];
  clearingPagers = Promise.all(entries.map(async ([key, pager]) => {
    await pager.close();
    if (pagers.get(key) === pager) pagers.delete(key);
  })).then(() => { pagerCleanupFailure = null; }, error => { pagerCleanupFailure = error; throw error; });
  return clearingPagers;
}
function cleanupFailed(error: unknown): void { setStatus(`游标释放失败：${error instanceof Error ? error.message : String(error)}；请显式刷新重试或关闭应用。`); }


function show(where: string, error: unknown): void {
  const host = document.querySelector<HTMLElement>(where)!;
  const node = document.createElement("p");
  node.className = "error";
  node.setAttribute("role", "alert");
  node.textContent = error instanceof PluginCapabilityError ? `${error.code}：${error.message}` : error instanceof Error ? error.message : String(error);
  host.replaceChildren(node);
}
function applyTheme(theme: SurfacePresentation): void {
  for (const [key, value] of Object.entries(theme.variables)) document.documentElement.style.setProperty(key, value);
  document.documentElement.style.colorScheme = theme.mode;
  document.documentElement.dataset.theme = theme.mode;
  document.documentElement.dataset.density = theme.density;
}
function cellText(value: JsonValue | undefined, computed: boolean): string {
  if (isMissingCell(value)) return "（空）";
  const display = describeCellValue(value, { computed });
  if (display.fresh === false) return "（计算中…）";
  return display.text === "" ? "（空文本）" : display.text;
}
async function loadDescription(): Promise<DataDescription> {
  if (description) return description;
  const context = await api.context.read();
  if (!context.collection) throw new Error("请先在平台打开一个数据表，再回到本应用。");
  const table = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection });
  if (!("fields" in table)) throw new Error("授权表清单与预期不符。");
  description = table;
  selectedFields ??= new Set(table.fields.filter(field => field.fieldId !== "id").map(field => field.fieldId));
  return table;
}
const listFields = (table: DataDescription): string[] => table.fields.filter(field => field.fieldId !== "id" && selectedFields?.has(field.fieldId)).map(field => field.fieldId);

async function refreshOverview(): Promise<void> {
  const current = generation;
  const table = await loadDescription();
  const probe = await api.data.query({ contract: "vibetable.plugin-query.v2", collection: table.collection, fields: ["id"], pageSize: 1 });
  try {
    if (current !== generation) return;
    const summary = overviewSummary(table, probe);
    const host = document.querySelector<HTMLElement>("#overview-body")!;
    host.replaceChildren();
    for (const text of [
      `授权表：${summary.displayName}（${summary.collection}）`,
      `授权字段：${summary.fieldCount} 个，可编辑 ${summary.editableCount} 个`,
      `关系字段：${summary.relations.map(item => `${item.displayName}(${item.fieldId})`).join("、") || "无"}`,
      `权威记录数：${summary.totalRows} 条（来自宿主 totalRows，非逐页累计）`,
    ]) { const p = document.createElement("p"); p.textContent = text; host.appendChild(p); }
    const fieldset = document.createElement("fieldset");
    const legend = document.createElement("legend"); legend.textContent = "选择列表、表单及导出的授权字段（关系使用目标记录 ID）"; fieldset.appendChild(legend);
    for (const field of table.fields.filter(item => item.fieldId !== "id")) {
      const label = document.createElement("label");
      const input = document.createElement("input"); input.type = "checkbox"; input.checked = selectedFields!.has(field.fieldId);
      input.addEventListener("change", () => { if (input.checked) selectedFields!.add(field.fieldId); else selectedFields!.delete(field.fieldId); void clearPagers(true).catch(cleanupFailed); });
      label.append(input, document.createTextNode(`${field.displayName} (${field.logicalType})`)); fieldset.appendChild(label);
    }
    host.appendChild(fieldset);
  } finally { if (probe.nextCursor) await api.data.releaseCursor(probe.nextCursor); }
}

function renderList(table: DataDescription, view: PagerView): void {
  const fields = ["id", ...listFields(table)];
  const host = document.querySelector<HTMLElement>("#list-body")!;
  host.replaceChildren();
  const grid = document.createElement("table");
  const head = document.createElement("tr");
  for (const field of fields) { const th = document.createElement("th"); th.textContent = table.fields.find(item => item.fieldId === field)?.displayName ?? field; head.appendChild(th); }
  grid.appendChild(head);
  for (const row of view.items) {
    const tr = document.createElement("tr");
    for (const field of fields) { const td = document.createElement("td"); td.textContent = cellText(row[field], ["formula", "lookup"].includes(table.fields.find(item => item.fieldId === field)?.logicalType ?? "")); tr.appendChild(td); }
    tr.tabIndex = 0;
    const open = (): void => { location.hash = `#detail/${encodeURIComponent(String(row.id))}`; };
    tr.addEventListener("click", open); tr.addEventListener("keydown", event => { if (event.key === "Enter") open(); });
    grid.appendChild(tr);
  }
  if (view.empty) { const p = document.createElement("p"); p.textContent = "表中暂无记录。"; host.appendChild(p); }
  else host.appendChild(grid);
  (document.querySelector<HTMLButtonElement>("#page-prev")!).disabled = !view.hasPrev;
  (document.querySelector<HTMLButtonElement>("#page-next")!).disabled = !view.hasNext;
  setStatus(`第 ${view.pageIndex + 1} 页，本页 ${view.items.length} 条；权威总数 ${view.totalRows} 条${view.hasNext ? "" : "（末页）"}。`);
}
async function refreshList(direction: "open" | "next" | "prev" = "open"): Promise<void> {
  pendingPage?.abort();
  const controller = new AbortController(); pendingPage = controller;
  const current = ++generation;
  for (const id of ["page-prev", "page-next"]) document.querySelector<HTMLButtonElement>(`#${id}`)!.disabled = true;
  try {
    await clearingPagers;
    const table = await loadDescription();
    let pager = pagers.get(table.collection);
    if (!pager) { pager = createPager((request, signal) => api.data.query(request, signal), cursor => api.data.releaseCursor(cursor), table.collection, ["id", ...listFields(table)], 50, [{ field: "id", direction: "asc" }]); pagers.set(table.collection, pager); }
    const view = direction === "open" ? pager.view ?? await pager.open(controller.signal) : await pager[direction](controller.signal);
    if (current === generation) renderList(table, view);
  } catch (error) {
    if (current !== generation) return;
    const pager = description && pagers.get(description.collection);
    if (pager?.view) renderList(description!, pager.view);
    setStatus(error instanceof PluginCapabilityError && error.code === "plugin_cancel_requested" ? "查询已取消。" : `查询失败：${error instanceof Error ? error.message : String(error)}；可刷新重查。`);
  } finally { if (pendingPage === controller) pendingPage = null; }
}

function reportOutcome(outcome: MutationOutcomeView, label: string): void {
  if (outcome.status === "succeeded") { void clearPagers().catch(cleanupFailed); setStatus(`${label}：已提交。`); }
  else if (outcome.status === "rejected") setStatus(`${label}：宿主拒绝，未写入。`);
  else if (outcome.status === "unknown") { unknownCommit = true; setStatus(`${label}：提交结果未知；不会自动重试。请返回列表核对权威数据，再刷新后操作。`); }
  else if (outcome.status === "cancelled") setStatus(`${label}：已取消。`);
  else setStatus(`${label}：失败 ${outcome.code}；请刷新后核对。`);
}
async function submitPlan(plan: SurfaceMutationPlan, signal: AbortSignal, label: string, enableCancel: () => void): Promise<void> {
  const preview = await api.mutation.preview(plan);
  if (signal.aborted) throw new PluginCapabilityError("plugin_cancel_requested", "提交已取消。");
  setStatus(`${label}：等待宿主确认（${preview.affectedCount} 项）…`);
  enableCancel();
  reportOutcome(describeSubmitOutcome(await api.mutation.submit(preview.planId, signal)), label);
}
async function renderDetail(recordId: string | null): Promise<void> {
  const current = generation;
  const table = await loadDescription();
  const fields = listFields(table);
  let row: JsonObject | null = null;
  let guard: string | null = null;
  if (recordId !== null) {
    const page = await api.data.query({ contract: "vibetable.plugin-query.v2", collection: table.collection, fields: ["id", ...fields], ids: [recordId], pageSize: 1 });
    row = page.items.find(item => item.id === recordId) ?? null;
    guard = page.rowGuards[recordId] ?? null;
  }
  if (current !== generation) return;
  const host = document.querySelector<HTMLElement>("#detail-body")!;
  host.replaceChildren();
  if (recordId !== null && !row) { show("#detail-body", new Error("记录不存在或不可读；未切换为创建。")); return; }
  const form = document.createElement("form"); form.id = "record-form";
  const inputs = new Map<string, { input: HTMLInputElement | HTMLSelectElement; nullInput: HTMLInputElement; initial: string; initialNull: boolean }>();
  for (const field of table.fields.filter(item => fields.includes(item.fieldId))) {
    const label = document.createElement("label"); label.textContent = `${field.displayName}（${field.logicalType}）`;
    const raw = row?.[field.fieldId];
    // Attachments use their dedicated API, never a JSON form replacement.
    const supported = !field.readonly && ["text", "number", "bool", "boolean", "date", "dateTime", "time", "email", "url", "select", "relation", "json", "multiSelect", "geoPoint"].includes(field.logicalType);
    if (!supported) { const output = document.createElement("output"); output.textContent = `只读：${cellText(raw, ["formula", "lookup"].includes(field.logicalType))}`; label.appendChild(output); form.appendChild(label); continue; }
    let input: HTMLInputElement | HTMLSelectElement;
    if (["bool", "boolean"].includes(field.logicalType)) {
      const select = document.createElement("select");
      for (const value of ["false", "true"]) { const option = document.createElement("option"); option.value = value; option.textContent = value; select.appendChild(option); }
      input = select;
    } else { const text = document.createElement("input"); text.type = "text"; input = text; }
    input.dataset.fieldId = field.fieldId;
    input.value = raw === undefined && ["bool", "boolean"].includes(field.logicalType) ? "false" : formInputValue(field, raw);
    const nullInput = document.createElement("input"); nullInput.type = "checkbox"; nullInput.checked = raw === null || raw === undefined && field.nullable;
    nullInput.disabled = !field.nullable; nullInput.dataset.nullFieldId = field.fieldId;
    nullInput.setAttribute("aria-label", `${field.displayName} 设为 null`);
    // Typing explicitly leaves null mode, while an empty text value remains "".
    input.addEventListener("input", () => { nullInput.checked = false; });
    inputs.set(field.fieldId, { input, nullInput, initial: input.value, initialNull: nullInput.checked });
    label.appendChild(input);
    const nullLabel = document.createElement("label");
    nullLabel.append(nullInput, document.createTextNode(`${field.displayName} 设为 null（与空文本不同）`));
    form.append(label, nullLabel);
  }
  const button = (text: string, type: "submit" | "button" = "button"): HTMLButtonElement => { const item = document.createElement("button"); item.type = type; item.textContent = text; form.appendChild(item); return item; };
  const save = button(recordId === null ? "预览并创建" : "预览并保存"); save.id = "record-save";
  const del = button("删除（宿主确认）");
  const normalize = button("预览批量规范化文本");
  const cancel = button("取消在途提交"); cancel.disabled = true;
  const writable = recordId === null || !!guard;
  save.disabled = !writable || unknownCommit; del.disabled = !row || !guard || unknownCommit; normalize.disabled = unknownCommit;
  if (!writable) setStatus("该记录缺少行保护；仅展示，不能保存或删除。");
  host.appendChild(form);
  const run = async (label: string, work: (signal: AbortSignal, enableCancel: () => void) => Promise<void>): Promise<void> => {
    if (saving || unknownCommit) return;
    const controller = new AbortController(); saving = controller;
    save.disabled = del.disabled = normalize.disabled = true; cancel.disabled = true;
    try { await work(controller.signal, () => { if (saving === controller) cancel.disabled = false; }); }
    catch (error) { reportOutcome(describeMutationFailure(error), label); }
    finally {
      if (saving === controller) saving = null;
      cancel.disabled = true;
      // Guarded edits require a fresh query after every outcome before resubmission.
      save.disabled = true;
      del.disabled = true; normalize.disabled = unknownCommit;
    }
  };
  const saveRecord = (): void => {
    if (!writable || save.disabled) return;
    void run(recordId === null ? "创建" : "保存", async (signal, enableCancel) => {
      const edits = new Map<string, FormEdit>();
      for (const [fieldId, item] of inputs) edits.set(fieldId, { text: item.input.value, isNull: item.nullInput.checked, changed: item.input.value !== item.initial || item.nullInput.checked !== item.initialNull });
      const values = formValues(table, edits, recordId === null);
      if (!Object.keys(values).length) { setStatus("没有修改的字段。"); return; }
      const operations: SurfaceMutationPlan["operations"] = recordId === null ? [{ kind: "create", values }] : [{ kind: "update", primaryKey: recordId, expectedDigest: guard!, values }];
      await submitPlan({ contract: "vibetable.surface-mutation-plan.v1", collection: table.collection, schemaRevision: table.schemaRevision, operations }, signal, recordId === null ? "创建" : "保存", enableCancel);
    });
  };
  // The isolated frame intentionally forbids native form submission (no allow-forms).
  // Use controlled button/Enter events; a submit listener alone never runs in that sandbox.
  save.addEventListener("click", saveRecord);
  form.addEventListener("submit", event => event.preventDefault());
  form.addEventListener("keydown", event => {
    if (event.key !== "Enter" || event.isComposing || !(event.target instanceof HTMLInputElement)) return;
    event.preventDefault();
    if (!event.ctrlKey && !event.altKey && !event.metaKey && !event.shiftKey) saveRecord();
  });
  del.addEventListener("click", () => { if (recordId && guard) void run("删除", (signal, enableCancel) => submitPlan({ contract: "vibetable.surface-mutation-plan.v1", collection: table.collection, schemaRevision: table.schemaRevision, operations: [{ kind: "delete", primaryKey: recordId, expectedDigest: guard }] }, signal, "删除", enableCancel)); });
  normalize.addEventListener("click", () => void run("批量规范化", async (signal, enableCancel) => {
    const target = editableFields(table).find(field => field.logicalType === "text" && fields.includes(field.fieldId));
    if (!target) { setStatus("当前选择没有可规范化的文本字段。"); return; }
    setStatus("读取全表用于批量规范化…");
    const fold = await foldAllPages((request, abort) => api.data.query(request, abort), cursor => api.data.releaseCursor(cursor), table.collection, ["id", target.fieldId], { sorts: [{ field: "id", direction: "asc" }], pageSize: 200, signal });
    const preview = buildNormalizationPlan(table, fold.rows, fold.guards, target.fieldId);
    if (!preview.plan) { setStatus(`批量规范化：无需要修改的记录（未变 ${preview.unchanged}，跳过 ${preview.skipped}）。`); return; }
    await submitPlan(preview.plan, signal, "批量规范化", enableCancel);
  }));
  cancel.addEventListener("click", () => { saving?.abort(); cancel.disabled = true; });
}

async function exportCsv(): Promise<void> {
  const button = document.querySelector<HTMLButtonElement>("#export")!;
  if (button.disabled) return;
  button.disabled = true;
  try {
    const table = await loadDescription();
    const task = await api.data.export({ collection: table.collection, fields: ["id", ...listFields(table)], query: { filters: [], sorts: [] }, format: "csv", suggestedName: "record-manager-export.csv" });
    if (!task) { setStatus("导出被取消。"); return; }
    const result = await task.result(undefined, progress => setStatus(`导出中 ${progress.done}/${progress.total}`));
    setStatus(`导出完成：${result.rowsWritten} 行 → ${result.outputDisplayName}`);
  } catch (error) { setStatus(`导出失败：${error instanceof Error ? error.message : String(error)}；不会自动重试，请核对输出文件。`); }
  finally { button.disabled = false; }
}
const pages = ["overview", "list", "detail"] as const;
async function route(): Promise<void> {
  generation++; pendingPage?.abort();
  const [name, argument] = (location.hash || "#overview").slice(1).split("/");
  const page = (pages as readonly string[]).includes(name) ? name as typeof pages[number] : "overview";
  for (const item of pages) document.getElementById(`page-${item}`)!.hidden = item !== page;
  for (const link of document.querySelectorAll<HTMLAnchorElement>("nav a")) { if (link.dataset.page === page) link.setAttribute("aria-current", "page"); else link.removeAttribute("aria-current"); }
  try { if (page === "overview") await refreshOverview(); else if (page === "list") await refreshList(); else await renderDetail(argument && argument !== "new" ? decodeURIComponent(argument) : null); }
  catch (error) { show(`#${page}-body`, error); }
}
document.getElementById("reload")!.addEventListener("click", () => { if (saving) { setStatus("请先取消在途提交，等待宿主终态后刷新。"); return; } void (async () => { await clearPagers(true); description = null; unknownCommit = false; staleEl.hidden = true; await route(); })().catch(cleanupFailed); });
document.getElementById("export")!.addEventListener("click", () => void exportCsv());
document.getElementById("page-prev")!.addEventListener("click", () => void refreshList("prev"));
document.getElementById("page-next")!.addEventListener("click", () => void refreshList("next"));
window.addEventListener("hashchange", () => void route());
window.addEventListener("pagehide", () => { pendingPage?.abort(); saving?.abort(); api.dispose(); });
async function boot(): Promise<void> {
  try {
    const context = await api.context.read();
    if (!context.collection) throw new Error("请先在平台打开一个数据表，再回到本应用。");
    applyTheme(await api.context.current()); api.context.onChanged(applyTheme);
    await api.data.subscribe({ collection: context.collection }, event => {
      if (event.type === "invalidated") {
        staleEl.hidden = false;
        void (async () => { await clearPagers(); description = null; if ((location.hash || "#overview").startsWith("#list")) await refreshList(); else if ((location.hash || "#overview") === "#overview") await refreshOverview(); })().catch(cleanupFailed);
      }
      else setStatus(`数据订阅已关闭（${event.code}）；请重新打开应用。`);
    });
    await route(); setStatus("ready");
  } catch (error) { show("#overview-body", error); }
}
void boot();
