import { mutationPlan, PluginCapabilityError, type DataPage, type MutationOperation, type PluginWriteAction } from "@vibetable/plugin-sdk";

type Input = {
  field: string;
  strategy: "trim" | "collapse-whitespace" | "lowercase" | "uppercase";
};

export function normalizeText(value: string, strategy: Input["strategy"]): string {
  if (strategy === "trim") return value.trim();
  if (strategy === "collapse-whitespace") return value.trim().replace(/\s+/gu, " ");
  if (strategy === "lowercase") return value.toLowerCase();
  if (strategy === "uppercase") return value.toUpperCase();
  throw new PluginCapabilityError("plugin_input_invalid", "不支持的规范化方式");
}

export const run: PluginWriteAction<Input> = async ({ field, strategy }, capabilities) => {
  const context = await capabilities.context.read();
  const collection = context.collection;
  if (!collection || !context.selectedKeys.length || !field) {
    throw new PluginCapabilityError("plugin_input_invalid", "请选择记录并指定文本字段");
  }
  const selected = new Set(context.selectedKeys.map(String));
  const operations: MutationOperation[] = [];
  const sampleRows: { id: string; before: string; after: string }[] = [];
  let cursor: string | null = null;
  let scanned = 0;
  let skipped = 0;
  do {
    const page: DataPage = await capabilities.data.read({ collection, fields: ["id", field], pageSize: 200,
      ...(cursor === null ? {} : { cursor }) });
    for (const row of page.items) {
      const id = String(row.id);
      if (!selected.delete(id)) continue;
      const before = row[field];
      if (typeof before !== "string") { skipped += 1; continue; }
      const after = normalizeText(before, strategy);
      if (after === before) { skipped += 1; continue; }
      const expectedDigest = page.rowGuards[id];
      if (!expectedDigest) throw new PluginCapabilityError("plugin_guard_unavailable", "宿主没有提供记录并发保护，请更新宿主");
      operations.push({ kind: "update", primaryKey: id, expectedDigest, values: { [field]: after } });
      if (sampleRows.length < 5) sampleRows.push({ id, before, after });
    }
    scanned += page.items.length;
    const receipt = await capabilities.ui.reportProgress({ current: scanned, total: 0,
      message: `已生成 ${operations.length} 条差异`, cancellable: true });
    if (receipt.cancelRequested) throw new PluginCapabilityError("plugin_cancel_requested", "已请求取消");
    cursor = page.nextCursor;
  } while (cursor !== null && selected.size > 0);
  if (selected.size) throw new PluginCapabilityError("plugin_selection_stale", "选中记录已变化，请重新选择后执行");
  return mutationPlan(collection, operations, {
    affectedCount: operations.length, sampleRows,
    summary: [{ label: "更新", value: operations.length }, { label: "跳过", value: skipped }],
  });
};
