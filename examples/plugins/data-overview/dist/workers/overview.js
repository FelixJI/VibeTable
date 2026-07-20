// src/overview.ts
var run = async ({ collection }, capabilities, signal) => {
  signal.throwIfAborted();
  const page = await capabilities.data.read({ collection, fields: ["*"], pageSize: 100 });
  return {
    contract: "vibetable.plugin-result.v1",
    status: "success",
    summary: `\u5DF2\u8BFB\u53D6 ${page.items.length} \u6761\u6837\u672C`,
    metrics: [{ label: "\u6837\u672C", value: page.items.length }],
    table: { data: { intent: "refresh" } },
    artifacts: [],
    refresh: { collections: [collection] },
    warnings: []
  };
};
export {
  run
};
