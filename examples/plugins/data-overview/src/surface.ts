import { createSurfaceClient, type DataDescription } from "@vibetable/plugin-sdk";
import { collectOverview } from "./overview-action.js";

const api = createSurfaceClient(window.name);
const status = document.querySelector<HTMLParagraphElement>("#status")!;
const body = document.querySelector<HTMLDivElement>("#summary")!;
const field = document.querySelector<HTMLSelectElement>("#field")!;
const contains = document.querySelector<HTMLInputElement>("#contains")!;
const refresh = document.querySelector<HTMLButtonElement>("#refresh")!;

function render(result: Awaited<ReturnType<typeof collectOverview>>): void {
  const { stats, description } = result;
  body.innerHTML = "";
  const metrics = [
    ["筛选后记录", String(stats.count)],
    ["权威总行数", String(stats.totalRows)],
    ["非空值", String(stats.nonNullCount)],
    ["读取页数", String(stats.pagesRead)],
    ["完整计算", stats.complete ? "是" : "计算中"],
  ] as const;
  const list = document.createElement("dl");
  for (const [label, value] of metrics) {
    const term = document.createElement("dt"); term.textContent = label;
    const item = document.createElement("dd"); item.textContent = value;
    list.append(term, item);
  }
  body.append(list);
  if (stats.sampleRows.length) {
    const table = document.createElement("table");
    const head = document.createElement("tr");
    const columns = stats.fieldId ? ["id", stats.fieldId] : ["id"];
    for (const column of columns) { const th = document.createElement("th"); th.textContent = column; head.append(th); }
    table.append(head);
    for (const row of stats.sampleRows) {
      const tr = document.createElement("tr");
      for (const column of columns) { const td = document.createElement("td"); td.textContent = String(row[column] ?? ""); tr.append(td); }
      table.append(tr);
    }
    body.append(table);
  }
  const focus = stats.fieldId ? `聚焦字段 ${stats.fieldId}（${description.displayName}）` : "没有可聚焦字段";
  status.textContent = `已统计 ${result.collection}：${focus}。`;
}

function fillFields(description: DataDescription, selected?: string): void {
  field.innerHTML = "";
  const automatic = document.createElement("option");
  automatic.value = ""; automatic.textContent = "（自动选择可排序字段）";
  field.append(automatic);
  for (const item of description.fields.filter(entry => entry.fieldId !== "id")) {
    const option = document.createElement("option");
    option.value = item.fieldId;
    option.textContent = `${item.fieldId}（${item.displayName}）`;
    if (item.fieldId === selected) option.selected = true;
    field.append(option);
  }
}

async function load(): Promise<void> {
  refresh.disabled = true;
  try {
    const result = await collectOverview(api, {
      fieldId: field.value === "" ? undefined : field.value,
      contains: contains.value === "" ? undefined : contains.value,
    });
    fillFields(result.description, result.stats.fieldId ?? undefined);
    render(result);
  } catch (error) {
    status.textContent = error instanceof Error ? error.message : String(error);
  } finally {
    refresh.disabled = false;
  }
}

refresh.addEventListener("click", () => void load());
contains.addEventListener("keydown", event => { if (event.key === "Enter") void load(); });
field.addEventListener("change", () => void load());
// Presentation only: theme/density follow the trusted parent snapshot.
api.context.onChanged(presentation => {
  document.documentElement.dataset.theme = presentation.mode;
  document.documentElement.dataset.density = presentation.density;
  document.documentElement.lang = presentation.locale;
  for (const [name, value] of Object.entries(presentation.variables)) {
    document.documentElement.style.setProperty(name, value);
  }
});
void load();
window.addEventListener("pagehide", () => api.dispose());
