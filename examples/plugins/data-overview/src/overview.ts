import { PluginCapabilityError, type PluginReadAction } from "@vibetable/plugin-sdk";

export const run: PluginReadAction = async (_input, capabilities) => {
    const { collection } = await capabilities.context.read();
    if (!collection) throw new PluginCapabilityError("plugin_input_invalid", "请选择数据表");
    const page = await capabilities.data.read({ collection, fields: ["id"], pageSize: 1 });
    const count = page.totalRows;
    const receipt = await capabilities.ui.reportProgress({ current: count, total: count,
      message: `已统计 ${count} 条记录`, cancellable: true });
    if (receipt.cancelRequested) throw new PluginCapabilityError("plugin_cancel_requested", "已请求取消");
    return {
      contract: "vibetable.plugin-result.v1",
      status: "success",
      summary: `共有 ${count} 条记录`,
      metrics: [{ label: "记录", value: count }],
      table: { data: { count } },
      artifacts: [],
      refresh: { collections: [collection] },
      warnings: [],
    };
  };
