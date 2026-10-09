import { PluginCapabilityError, type DataDescription, type DataQueryRequest, type PluginReadAction } from "@vibetable/plugin-sdk";

export const run: PluginReadAction = async (input, capabilities) => {
  const { collection } = await capabilities.context.read();
  if (!collection) throw new PluginCapabilityError("plugin_input_invalid", "请选择数据表");
  const metadata = await capabilities.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection }) as DataDescription;
  const field = metadata.fields.find(item => item.fieldId !== "id" && item.sortable);
  const options = input as { fieldId?: string; contains?: string };
  const requested = options.fieldId ? metadata.fields.find(item => item.fieldId === options.fieldId) : field;
  if (options.fieldId && !requested) throw new PluginCapabilityError("plugin_read_denied", "字段不可读取");
  const query: DataQueryRequest = { contract: "vibetable.plugin-query.v2", collection,
    fields: requested ? ["id", requested.fieldId] : ["id"], pageSize: 200,
    filters: options.contains === undefined ? [] : [{ field: requested?.fieldId ?? "", operator: "contains", value: options.contains }],
    sorts: requested?.sortable ? [{ field: requested.fieldId, direction: "asc", nullsLast: true }] : [] };
  let cursor: string | undefined;
  let count = 0, nonNullCount = 0, complete = true, filteredRows = 0, totalRows = 0;
  const sampleRows: Record<string, import("@vibetable/plugin-sdk").JsonValue>[] = [];
  do {
    const page = await capabilities.data.query(cursor ? { ...query, cursor } : query);
    count += page.items.length;
    filteredRows = page.filteredRows; totalRows = page.totalRows; complete &&= page.complete;
    for (const row of page.items) {
      if (requested && row[requested.fieldId] !== null) nonNullCount++;
      if (sampleRows.length < 5) sampleRows.push(row);
    }
    cursor = page.nextCursor ?? undefined;
  } while (cursor);
  const receipt = await capabilities.ui.reportProgress({ current: count, total: filteredRows,
    message: `已统计 ${count} 条记录`, cancellable: true });
  if (receipt.cancelRequested) throw new PluginCapabilityError("plugin_cancel_requested", "已请求取消");
  if (count !== filteredRows) throw new PluginCapabilityError("plugin_query_failed", "分页统计不完整");
  return { contract: "vibetable.plugin-result.v1", status: "success", summary: `筛选后共有 ${count} 条记录`,
    metrics: [{ label: "记录", value: count }],
    table: { data: { count, totalRows, filteredRows, nonNullCount, complete, fieldId: requested?.fieldId ?? null, sampleRows } },
    artifacts: [], refresh: { collections: [collection] }, warnings: complete ? [] : ["部分计算值正在更新"] };
};
