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
var run = async (_input, capabilities) => {
  const { collection } = await capabilities.context.read();
  if (!collection) throw new PluginCapabilityError("plugin_input_invalid", "\u8BF7\u9009\u62E9\u6570\u636E\u8868");
  const page = await capabilities.data.read({ collection, fields: ["id"], pageSize: 1 });
  const count = page.totalRows;
  const receipt = await capabilities.ui.reportProgress({
    current: count,
    total: count,
    message: `\u5DF2\u7EDF\u8BA1 ${count} \u6761\u8BB0\u5F55`,
    cancellable: true
  });
  if (receipt.cancelRequested) throw new PluginCapabilityError("plugin_cancel_requested", "\u5DF2\u8BF7\u6C42\u53D6\u6D88");
  return {
    contract: "vibetable.plugin-result.v1",
    status: "success",
    summary: `\u5171\u6709 ${count} \u6761\u8BB0\u5F55`,
    metrics: [{ label: "\u8BB0\u5F55", value: count }],
    table: { data: { count } },
    artifacts: [],
    refresh: { collections: [collection] },
    warnings: []
  };
};
export {
  run
};
