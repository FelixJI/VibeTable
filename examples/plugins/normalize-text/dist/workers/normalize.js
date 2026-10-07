// ../../../sdk/plugin/src/capabilities.ts
function mutationPlan(collection, operations, preview) {
  return { contract: "vibetable.mutation-plan.v1", collection, operations, preview };
}
var PluginCapabilityError = class extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
    this.name = "PluginCapabilityError";
  }
  code;
};

// src/normalize.ts
function normalizeText(value, strategy) {
  if (strategy === "trim") return value.trim();
  if (strategy === "collapse-whitespace") return value.trim().replace(/\s+/gu, " ");
  if (strategy === "lowercase") return value.toLowerCase();
  if (strategy === "uppercase") return value.toUpperCase();
  throw new PluginCapabilityError("plugin_input_invalid", "\u4E0D\u652F\u6301\u7684\u89C4\u8303\u5316\u65B9\u5F0F");
}
var run = async ({ field, strategy }, capabilities) => {
  const context = await capabilities.context.read();
  const collection = context.collection;
  if (!collection || !context.selectedKeys.length || !field) {
    throw new PluginCapabilityError("plugin_input_invalid", "\u8BF7\u9009\u62E9\u8BB0\u5F55\u5E76\u6307\u5B9A\u6587\u672C\u5B57\u6BB5");
  }
  const selected = new Set(context.selectedKeys.map(String));
  const operations = [];
  const sampleRows = [];
  let cursor = null;
  let scanned = 0;
  let skipped = 0;
  do {
    const page = await capabilities.data.read({
      collection,
      fields: ["id", field],
      pageSize: 200,
      ...cursor === null ? {} : { cursor }
    });
    for (const row of page.items) {
      const id = String(row.id);
      if (!selected.delete(id)) continue;
      const before = row[field];
      if (typeof before !== "string") {
        skipped += 1;
        continue;
      }
      const after = normalizeText(before, strategy);
      if (after === before) {
        skipped += 1;
        continue;
      }
      const expectedDigest = page.rowGuards[id];
      if (!expectedDigest) throw new PluginCapabilityError("plugin_guard_unavailable", "\u5BBF\u4E3B\u6CA1\u6709\u63D0\u4F9B\u8BB0\u5F55\u5E76\u53D1\u4FDD\u62A4\uFF0C\u8BF7\u66F4\u65B0\u5BBF\u4E3B");
      operations.push({ kind: "update", primaryKey: id, expectedDigest, values: { [field]: after } });
      if (sampleRows.length < 5) sampleRows.push({ id, before, after });
    }
    scanned += page.items.length;
    const receipt = await capabilities.ui.reportProgress({
      current: scanned,
      total: 0,
      message: `\u5DF2\u751F\u6210 ${operations.length} \u6761\u5DEE\u5F02`,
      cancellable: true
    });
    if (receipt.cancelRequested) throw new PluginCapabilityError("plugin_cancel_requested", "\u5DF2\u8BF7\u6C42\u53D6\u6D88");
    cursor = page.nextCursor;
  } while (cursor !== null && selected.size > 0);
  if (selected.size) throw new PluginCapabilityError("plugin_selection_stale", "\u9009\u4E2D\u8BB0\u5F55\u5DF2\u53D8\u5316\uFF0C\u8BF7\u91CD\u65B0\u9009\u62E9\u540E\u6267\u884C");
  return mutationPlan(collection, operations, {
    affectedCount: operations.length,
    sampleRows,
    summary: [{ label: "\u66F4\u65B0", value: operations.length }, { label: "\u8DF3\u8FC7", value: skipped }]
  });
};
export {
  normalizeText,
  run
};
