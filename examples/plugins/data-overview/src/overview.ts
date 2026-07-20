import type { PluginAction } from "@vibetable/plugin-sdk";

export const run: PluginAction<{ collection: string }, { intent: "refresh" }> =
  async ({ collection }, capabilities, signal) => {
    signal.throwIfAborted();
    const page = await capabilities.data.read({ collection, fields: ["*"], pageSize: 100 });
    return {
      contract: "vibetable.plugin-result.v1",
      status: "success",
      summary: `已读取 ${page.items.length} 条样本`,
      metrics: [{ label: "样本", value: page.items.length }],
      table: { data: { intent: "refresh" } },
      artifacts: [],
      refresh: { collections: [collection] },
      warnings: [],
    };
  };
