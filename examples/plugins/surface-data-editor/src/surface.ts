import { createSurfaceClient, type DataDescription, type JsonObject } from "@vibetable/plugin-sdk";

const api = createSurfaceClient(window.name);
const status = document.querySelector<HTMLParagraphElement>("#status")!;
const draft = document.querySelector<HTMLTextAreaElement>("#draft")!;
const save = document.querySelector<HTMLButtonElement>("#save")!;
const cancel = document.querySelector<HTMLButtonElement>("#cancel")!;
const output = document.querySelector<HTMLPreElement>("#result")!;
let row: JsonObject;
let table: DataDescription;
let field: string;
let guard: string;
let saving: AbortController | null = null;

async function load(): Promise<void> {
  const context = await api.context.read();
  if (!context.collection) throw new Error("宿主没有活动数据表。");
  table = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection }) as DataDescription;
  field = table.fields.find(item => !item.readonly && item.logicalType === "text")?.fieldId ?? "";
  if (!field) throw new Error("示例需要一个可写文本字段。");
  const rows: JsonObject[] = [];
  const pages: number[] = [];
  let cursor: string | null = null;
  do {
    const page = await api.data.query({ contract: "vibetable.plugin-query.v2", collection: context.collection, fields: ["id", field], sorts: [{ field: "id", direction: "asc" }], pageSize: 200, ...(cursor ? { cursor } : {}) });
    if (!page.complete) throw new Error("计算数据尚未就绪；未把不完整结果当成全部记录。");
    if (!rows.length && page.items[0]) guard = page.rowGuards[String(page.items[0].id)]!;
    rows.push(...page.items);
    pages.push(page.items.length);
    cursor = page.nextCursor;
  } while (cursor);
  if (!rows[0] || !guard) throw new Error("表中没有可编辑记录。");
  row = rows[0];
  draft.value = String(row[field] ?? "");
  await api.storage.set("lastRead", { rows: rows.length, pages });
  status.textContent = `已读取 ${rows.length} 条记录（${pages.join("/")}），可在草稿中编辑后一次保存。`;
  save.disabled = false;
}
save.addEventListener("click", async () => {
  save.disabled = true;
  cancel.disabled = false;
  saving = new AbortController();
  try {
    const preview = await api.mutation.preview({ contract: "vibetable.surface-mutation-plan.v1", collection: table.collection, schemaRevision: table.schemaRevision,
      operations: [{ kind: "update", primaryKey: String(row.id), expectedDigest: guard, values: { [field]: draft.value } }] });
    const result = await api.mutation.submit(preview.planId, saving.signal);
    output.textContent = result.status === "succeeded" ? "保存成功" : "宿主拒绝，未写入";
    if (result.status === "succeeded") await load();
  } catch (error) { output.textContent = error instanceof Error ? error.message : String(error); }
  finally { saving = null; cancel.disabled = true; save.disabled = false; }
});
cancel.addEventListener("click", () => saving?.abort());
window.addEventListener("pagehide", () => api.dispose());
load().catch(error => { status.textContent = error instanceof Error ? error.message : String(error); });
