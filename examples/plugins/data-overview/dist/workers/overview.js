// ../../../sdk/plugin/src/capabilities.ts
var PluginCapabilityError = class extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
    this.name = "PluginCapabilityError";
  }
  code;
};

// src/overview.ts
var run = async (input, capabilities) => {
  const { collection } = await capabilities.context.read();
  if (!collection) throw new PluginCapabilityError("plugin_input_invalid", "\u8BF7\u9009\u62E9\u6570\u636E\u8868");
  const metadata = await capabilities.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection });
  const field = metadata.fields.find((item) => item.fieldId !== "id" && item.sortable);
  const options = input;
  const requested = options.fieldId ? metadata.fields.find((item) => item.fieldId === options.fieldId) : field;
  if (options.fieldId && !requested) throw new PluginCapabilityError("plugin_read_denied", "\u5B57\u6BB5\u4E0D\u53EF\u8BFB\u53D6");
  const query = {
    contract: "vibetable.plugin-query.v2",
    collection,
    fields: requested ? ["id", requested.fieldId] : ["id"],
    pageSize: 200,
    filters: options.contains === void 0 ? [] : [{ field: requested?.fieldId ?? "", operator: "contains", value: options.contains }],
    sorts: requested?.sortable ? [{ field: requested.fieldId, direction: "asc", nullsLast: true }] : []
  };
  let cursor;
  let count = 0, nonNullCount = 0, complete = true, filteredRows = 0, totalRows = 0;
  const sampleRows = [];
  do {
    const page = await capabilities.data.query(cursor ? { ...query, cursor } : query);
    count += page.items.length;
    filteredRows = page.filteredRows;
    totalRows = page.totalRows;
    complete &&= page.complete;
    for (const row of page.items) {
      if (requested && row[requested.fieldId] !== null) nonNullCount++;
      if (sampleRows.length < 5) sampleRows.push(row);
    }
    cursor = page.nextCursor ?? void 0;
  } while (cursor);
  const receipt = await capabilities.ui.reportProgress({
    current: count,
    total: filteredRows,
    message: `\u5DF2\u7EDF\u8BA1 ${count} \u6761\u8BB0\u5F55`,
    cancellable: true
  });
  if (receipt.cancelRequested) throw new PluginCapabilityError("plugin_cancel_requested", "\u5DF2\u8BF7\u6C42\u53D6\u6D88");
  if (count !== filteredRows) throw new PluginCapabilityError("plugin_query_failed", "\u5206\u9875\u7EDF\u8BA1\u4E0D\u5B8C\u6574");
  return {
    contract: "vibetable.plugin-result.v1",
    status: "success",
    summary: `\u7B5B\u9009\u540E\u5171\u6709 ${count} \u6761\u8BB0\u5F55`,
    metrics: [{ label: "\u8BB0\u5F55", value: count }],
    table: { data: { count, totalRows, filteredRows, nonNullCount, complete, fieldId: requested?.fieldId ?? null, sampleRows } },
    artifacts: [],
    refresh: { collections: [collection] },
    warnings: complete ? [] : ["\u90E8\u5206\u8BA1\u7B97\u503C\u6B63\u5728\u66F4\u65B0"]
  };
};
export {
  run
};
