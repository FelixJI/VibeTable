import type { SurfaceCapabilities } from "@vibetable/plugin-sdk";

/** The same headless consumer runs in the product and offline contract tests. */
export async function runAudit(api: SurfaceCapabilities): Promise<string> {
  const context = await api.context.read();
  if (!context.collection) throw new Error("宿主没有活动数据表，未记录任何内容。");
  const description = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection });
  if (!("fields" in description)) throw new Error("授权元数据与预期不符。");
  const probe = await api.data.query({ contract: "vibetable.plugin-query.v2", collection: context.collection, fields: ["id"], pageSize: 1 });
  try {
    if (!probe.complete) throw new Error("权威计数不可用；未把不完整结果当作全量。");
  } finally { if (probe.nextCursor) await api.data.releaseCursor(probe.nextCursor); }
  await api.storage.set("auditSnapshot", { collection: context.collection, schemaRevision: description.schemaRevision, totalRows: probe.totalRows, recordedAt: new Date().toISOString() });
  return `已记录权威计数 ${probe.totalRows} 条（${description.fields.length} 个授权字段）。`;
}
