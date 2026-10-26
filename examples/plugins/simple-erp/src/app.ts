import { createSurfaceClient, PluginCapabilityError, type JsonObject, type JsonValue, type SurfacePresentation } from "@vibetable/plugin-sdk";
import { definitions, loadTable, money, mutationPlan, orderValues, parseMoney, type BusinessTable, type Section } from "./erp.js";

const api = createSurfaceClient(window.name);
const get = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const tables = new Map<Section, BusinessTable>();
let section: Section = "products";
let editing: JsonObject | undefined;
let busy = false;
let dirty = false;
let uncertain = false;
let stale = false;
let leaveAction: (() => void) | undefined;
let leaveOrigin: HTMLElement | null = null;
const lifetime = new AbortController();
const subscriptions: string[] = [];
type Feedback = "info" | "loading" | "waiting" | "success" | "rejected" | "cancelled" | "warning" | "error" | "unknown";
function status(text: string, state: Feedback = "info"): void {
  const labels: Record<Feedback, string> = { info: "提示", loading: "处理中", waiting: "等待确认", success: "成功", rejected: "未保存", cancelled: "已取消", warning: "注意", error: "操作失败", unknown: "结果未知" };
  get("status").dataset.state = state;
  get("status").setAttribute("aria-live", state === "error" || state === "unknown" ? "off" : "polite");
  get("status").textContent = `${labels[state]}：${text}`;
  get("error").textContent = state === "error" || state === "unknown" ? text : "";
}
function availability(): void {
  const blocked = busy || leaveAction !== undefined;
  get("workspace-content").setAttribute("aria-busy", String(busy));
  document.querySelectorAll<HTMLButtonElement>("button").forEach(button => { button.disabled = blocked; });
  for (const id of ["leave-cancel", "leave-confirm"]) get<HTMLButtonElement>(id).disabled = busy;
  get<HTMLInputElement>("search").disabled = blocked;
  get("fields").querySelectorAll<HTMLInputElement | HTMLSelectElement>("input, select").forEach(input => { input.disabled = blocked; });
  get<HTMLButtonElement>("new").disabled = blocked || stale || uncertain || !tables.has(section);
  get<HTMLButtonElement>("export").disabled = blocked || !tables.has(section);
  document.querySelectorAll<HTMLButtonElement>("[data-edit]").forEach(button => { button.disabled = blocked || stale || uncertain; });
  get<HTMLFormElement>("form").querySelectorAll<HTMLButtonElement>("button[type=submit]").forEach(button => { button.disabled = blocked || stale || uncertain || !tables.has(section); });
  get("stale").hidden = !stale;
  get("uncertain").hidden = !uncertain;
}
async function run(action: () => Promise<void>): Promise<void> {
  if (busy) return;
  const focusBefore = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const editorHadFocus = get("editor").contains(focusBefore);
  busy = true; availability();
  try { await action(); }
  catch (error) {
    if (error instanceof PluginCapabilityError && error.code === "plugin_commit_unknown") {
      uncertain = true;
      status("提交结果未知。禁止直接重试：请先在平台表格和宿主任务中核对实际结果，再重新打开应用。", "unknown");
    } else status(`操作未完成：${error instanceof Error ? error.message : String(error)}`, "error");
  } finally {
    busy = false; availability();
    if (!tables.size) render();
    if (editorHadFocus && get("editor").hidden) get("list").focus();
    else if (editorHadFocus && focusBefore?.isConnected) focusBefore.focus();
  }
}
function leave(action: () => void): void {
  if (busy || leaveAction) return;
  if (!dirty) { action(); return; }
  leaveOrigin = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  leaveAction = action; get("leave").hidden = false; availability(); get("leave-cancel").focus();
}
function closeEditor(): void {
  const hadFocus = get("editor").contains(document.activeElement);
  dirty = false; editing = undefined; get("editor").hidden = true;
  if (hadFocus && !busy) get("list").focus();
}
function cancelLeave(): void {
  leaveAction = undefined; get("leave").hidden = true; availability();
  if (leaveOrigin?.isConnected) leaveOrigin.focus();
  else get("fields").querySelector<HTMLElement>("input, select")?.focus();
  leaveOrigin = null;
}
get("leave-cancel").onclick = cancelLeave;
const escapeLeave = (event: KeyboardEvent): void => { if (event.key === "Escape" && leaveAction) { event.preventDefault(); cancelLeave(); } };
document.addEventListener("keydown", escapeLeave);
get("leave-confirm").onclick = () => {
  const action = leaveAction; leaveAction = undefined; leaveOrigin = null; get("leave").hidden = true; closeEditor(); availability(); action?.();
};
function label(section: Section, id: JsonValue | undefined): string {
  const table = tables.get(section);
  const row = table?.rows.find(item => item.id === id);
  return row && table ? String(row[table.fields.name] ?? id) : String(id ?? "—");
}
function cell(key: string, value: JsonValue | undefined): string {
  if (value === null || value === undefined) return "—";
  if (key === "price" || key === "total") { try { return `¥ ${money(value)}`; } catch { return "无效金额（请在平台修正）"; } }
  if (key === "customer") return label("customers", value);
  if (key === "product") return label("products", value);
  return String(value);
}
function render(): void {
  get("title").textContent = definitions[section].title;
  document.querySelectorAll<HTMLElement>("[data-section]").forEach(button => button.setAttribute("aria-pressed", String(button.dataset.section === section)));
  const target = get("list"); target.replaceChildren();
  const data = tables.get(section);
  if (!data) { get("count").textContent = "未加载"; const p = document.createElement("p"); p.className = "empty"; p.textContent = busy ? "正在加载，请稍候…" : "数据尚未加载，请检查上方提示后刷新。"; target.append(p); return; }
  const keys = Object.keys(data.fields);
  const search = get<HTMLInputElement>("search").value.trim().toLocaleLowerCase();
  const rows = data.rows.filter(row => keys.some(key => cell(key, row[data.fields[key]]).toLocaleLowerCase().includes(search)));
  get("count").textContent = `${rows.length} / ${data.rows.length} 条`;
  if (!rows.length) { const p = document.createElement("p"); p.className = "empty"; p.textContent = search ? "没有匹配记录。" : "暂无记录，点击新增开始。"; target.append(p); return; }
  const table = document.createElement("table");
  table.createCaption().textContent = `${definitions[section].title}列表`;
  const header = table.createTHead().insertRow();
  for (const title of [...Object.values(definitions[section].fields).map(field => field[0]), "操作"]) {
    const th = document.createElement("th"); th.scope = "col"; th.textContent = title; header.append(th);
  }
  const body = table.createTBody();
  for (const row of rows) {
    const tr = body.insertRow();
    for (const key of keys) tr.insertCell().textContent = cell(key, row[data.fields[key]]);
    const button = document.createElement("button"); button.type = "button";
    button.dataset.edit = "";
    button.textContent = section === "orders" ? "变更状态" : "编辑";
    button.setAttribute("aria-label", `${button.textContent} ${String(row[data.fields.name ?? data.fields.number] ?? row.id)}`);
    button.disabled = busy || stale || uncertain;
    button.onclick = () => leave(() => openEditor(row)); tr.insertCell().append(button);
  }
  target.append(table);
}
function field(key: string, title: string, value = "", choices?: readonly { value: string; label: string }[]): void {
  const label = document.createElement("label"); label.textContent = `${title}（必填）`;
  const input = choices ? document.createElement("select") : document.createElement("input");
  input.name = key; input.id = `field-${key}`; input.required = true;
  input.dataset.label = title;
  input.setAttribute("aria-describedby", `error-${key}`);
  if (input instanceof HTMLInputElement) {
    if (key === "email") input.type = "email";
    if (key === "price") input.inputMode = "decimal";
    if (key === "quantity") { input.type = "number"; input.min = "1"; input.step = "1"; input.inputMode = "numeric"; }
  }
  if (input instanceof HTMLSelectElement) {
    const blank = document.createElement("option"); blank.value = ""; blank.textContent = "请选择"; input.append(blank);
    for (const choice of choices ?? []) { const option = document.createElement("option"); option.value = choice.value; option.textContent = choice.label; input.append(option); }
  }
  const error = document.createElement("small"); error.id = `error-${key}`; error.className = "field-error";
  input.value = value; label.append(input, error); get("fields").append(label);
}
function validateForm(): boolean {
  let first: HTMLInputElement | HTMLSelectElement | undefined;
  for (const input of get("fields").querySelectorAll<HTMLInputElement | HTMLSelectElement>("input, select")) {
    let message = "";
    if (!input.value.trim()) message = `请填写${input.dataset.label}。`;
    else if (input.name === "price") { try { parseMoney(input.value); } catch (error) { message = error instanceof Error ? error.message : "金额无效。"; } }
    else if (input.name === "quantity" && (!Number.isSafeInteger(Number(input.value)) || Number(input.value) <= 0)) message = "数量必须为正整数。";
    else if (!input.validity.valid) message = input.name === "email" ? "请输入有效的邮箱地址。" : "请检查此字段的输入。";
    input.setAttribute("aria-invalid", String(Boolean(message)));
    get(`error-${input.name}`).textContent = message;
    if (message) first ??= input;
  }
  if (first) { status("请修正标出的字段后再保存；输入已保留。", "error"); first.focus(); return false; }
  return true;
}
function openEditor(row?: JsonObject): void {
  if (stale || uncertain || !tables.has(section)) return;
  if (row && section === "products") {
    try { money(row[tables.get(section)!.fields.price]); }
    catch { status("商品单价不是非负安全整数分，请先在平台表格修正。", "error"); return; }
  }
  editing = row; dirty = false; get("fields").replaceChildren();
  get("editor-title").textContent = `${row ? "编辑" : "新增"}${definitions[section].title}`;
  const table = tables.get(section)!;
  if (section === "orders") {
    if (row) {
      const current = String(row[table.fields.status] ?? "");
      field("status", "订单状态（只变更状态，不重算价格或扣库存）", current, [current, ...["草稿", "已确认", "已取消"].filter(item => item !== current)].map(value => ({ value, label: value })));
    } else {
      field("number", "订单号");
      for (const [key, source, title] of [["product", "products", "商品"], ["customer", "customers", "客户"]] as const) {
        const related = tables.get(source)!;
        field(key, title, "", related.rows.map(item => ({ value: String(item.id), label: String(item[related.fields.name]) })));
      }
      field("quantity", "数量（正整数）", "1");
      const hint = document.createElement("p"); hint.textContent = "以本次读取的商品单价生成订单价格快照；保存前请在宿主预览中核对金额。"; get("fields").append(hint);
    }
  } else {
    for (const [key, [title]] of Object.entries(definitions[section].fields)) {
      const raw = row?.[table.fields[key]];
      field(key, key === "price" ? "单价（元，最多两位小数）" : title, raw == null ? "" : key === "price" ? money(raw) : String(raw));
    }
  }
  get("editor").hidden = false; get("fields").querySelector<HTMLElement>("input, select")?.focus(); availability();
}
async function refresh(): Promise<void> {
  status("正在读取商品、客户和订单…", "loading");
  closeEditor();
  // Unsubscribe stops callbacks even on failure; old data must no longer be writable.
  tables.clear(); render();
  while (subscriptions.length) { await api.data.unsubscribe(subscriptions[0]); subscriptions.shift(); }
  const catalog = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"] });
  if (!("tables" in catalog)) throw new Error("未收到授权表目录。");
  // Subscribe before reading so changes during the initial read cannot be missed.
  stale = false;
  for (const definition of Object.values(definitions)) {
    const matches = catalog.tables.filter(table => table.displayName === definition.table);
    if (matches.length !== 1) throw new Error(`请创建并授权唯一的「${definition.table}」，参见示例 README。`);
    const handle = await api.data.subscribe({ collection: matches[0].collection }, event => {
      stale = true; availability(); render();
      if (event.type === "closed") status(`实时订阅已关闭：${event.code}；请刷新或重新打开工作区。`, "warning");
    }, lifetime.signal);
    subscriptions.push(handle.subscriptionId);
  }
  const loaded = new Map<Section, BusinessTable>();
  for (const name of Object.keys(definitions) as Section[]) loaded.set(name, await loadTable(api, name, lifetime.signal));
  for (const [name, table] of loaded) tables.set(name, table);
  render(); status("已读取三张表。商品价格变更不影响历史订单；各表读取不构成跨表快照。");
}
document.querySelectorAll<HTMLElement>("[data-section]").forEach(button => {
  button.onclick = () => leave(() => { closeEditor(); section = button.dataset.section as Section; get<HTMLInputElement>("search").value = ""; render(); availability(); });
});
get("search").oninput = render;
get("new").onclick = () => leave(() => openEditor());
get("discard").onclick = () => leave(closeEditor);
get("reload").onclick = () => leave(() => { void run(refresh); });
get("back").onclick = () => leave(() => { void run(async () => { await api.ui.navigation.back(); }); });
get("form").oninput = event => {
  dirty = true;
  const input = event.target;
  if (input instanceof HTMLInputElement || input instanceof HTMLSelectElement) {
    input.removeAttribute("aria-invalid"); get(`error-${input.name}`).textContent = "";
  }
};
get("form").onsubmit = event => {
  event.preventDefault();
  if (busy || leaveAction || stale || uncertain || !tables.has(section) || !validateForm()) return;
  const data = new FormData(get<HTMLFormElement>("form"));
  status("正在生成保存预览…", "loading");
  void run(async () => {
    const value = (key: string): string => String(data.get(key) ?? "").trim();
    const table = tables.get(section)!;
    let values: JsonObject;
    if (section === "orders") {
      if (editing) {
        if (!["草稿", "已确认", "已取消"].includes(value("status"))) throw new Error("请选择支持的订单状态。");
        values = { [table.fields.status]: value("status") };
      } else values = orderValues(table, tables.get("products")!, tables.get("customers")!, {
        number: value("number"), product: value("product"), customer: value("customer"), quantity: Number(value("quantity")),
      });
    } else {
      const result: Record<string, JsonValue> = {};
      for (const key of Object.keys(table.fields)) {
        if (!value(key)) throw new Error("请填写所有字段。");
        result[table.fields[key]] = key === "price" ? parseMoney(value(key)) : value(key);
      }
      values = result;
    }
    const preview = await api.mutation.preview(mutationPlan(table, values, editing));
    status("请在宿主窗口核对精确计划并确认。", "waiting");
    const result = await api.mutation.submit(preview.planId, lifetime.signal);
    if (result.status === "rejected") { status("宿主拒绝保存，表单已保留。", "rejected"); return; }
    closeEditor();
    try { await refresh(); status("保存成功，已重新读取数据。", "success"); }
    catch (error) { status(`保存已成功，但刷新失败：${error instanceof Error ? error.message : String(error)}。请刷新列表，不要重复新增。`, "warning"); }
  });
};
get("export").onclick = () => { void run(async () => {
  const table = tables.get(section)!;
  status("请选择导出文件的保存位置。", "waiting");
  const task = await api.data.export({ collection: table.description.collection, fields: ["id", ...Object.values(table.fields)], format: "csv", suggestedName: `${definitions[section].title}.csv` });
  if (!task) { status("已取消导出。", "cancelled"); return; }
  const result = await task.result(lifetime.signal, progress => status(progress.message, "loading"));
  status(`已导出 ${result.rowsWritten} 条：${result.outputDisplayName}（当前表全量原值，金额单位为分）。`, "success");
}); };
function applyTheme(theme: SurfacePresentation): void {
  document.documentElement.style.colorScheme = theme.mode;
  document.documentElement.dataset.theme = theme.mode;
  document.documentElement.dataset.density = theme.density;
  for (const name of Array.from({ length: document.documentElement.style.length }, (_, index) => document.documentElement.style.item(index))) {
    if (name.startsWith("--vt-plugin-")) document.documentElement.style.removeProperty(name);
  }
  for (const [name, value] of Object.entries(theme.variables)) document.documentElement.style.setProperty(name, value);
}
let receivedTheme = false;
const stopTheme = api.context.onChanged(theme => { receivedTheme = true; applyTheme(theme); });
const stopTasks = api.tasks.onChanged(tasks => {
  const states = { queued: "排队中", running: "执行中", succeeded: "成功", failed: "失败", cancelled: "已取消", aborted: "已中止" };
  get("tasks").replaceChildren(...tasks.map(task => { const li = document.createElement("li"); li.textContent = `${states[task.state]} · ${task.result?.summary ?? task.error?.message ?? task.progress?.message ?? task.taskId}`; return li; }));
  if (!tasks.length) { const li = document.createElement("li"); li.textContent = "暂无本页面任务。"; get("tasks").append(li); }
});
window.addEventListener("pagehide", () => { document.removeEventListener("keydown", escapeLeave); lifetime.abort(); stopTheme(); stopTasks(); api.dispose(); }, { once: true });
void run(async () => {
  const initial = await api.context.current();
  if (!receivedTheme) applyTheme(initial);
  await refresh();
});
