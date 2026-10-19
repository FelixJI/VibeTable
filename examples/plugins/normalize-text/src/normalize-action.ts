import { PluginCapabilityError, type DataDescription, type SurfaceCapabilities } from "@vibetable/plugin-sdk";

/** The single migration strategy this headless action applies. */
export function normalizeText(value: string): string {
  return value.trim().replace(/\s+/gu, " ");
}

interface UpdateOperation {
  readonly kind: "update";
  readonly primaryKey: string;
  readonly expectedDigest: string;
  readonly values: Readonly<Record<string, string>>;
}

export interface NormalizationResult {
  readonly summary: string;
  readonly changedFields: number;
  readonly skippedValues: number;
  readonly scannedRows: number;
}

function textFields(description: DataDescription): string[] {
  return description.fields
    .filter(field => !field.readonly && field.logicalType === "text")
    .map(field => field.fieldId);
}

/** Query selected rows across pages and return guarded update operations. */
export async function collectNormalization(
  api: SurfaceCapabilities,
  collection: string,
  description: DataDescription,
  selected: ReadonlySet<string>,
): Promise<{ operations: UpdateOperation[]; sampleRows: { id: string; field: string; before: string; after: string }[]; skipped: number; scanned: number }> {
  const fields = textFields(description);
  if (fields.length === 0) throw new PluginCapabilityError("plugin_write_denied", "没有可写的文本字段");
  const operations: UpdateOperation[] = [];
  const sampleRows: { id: string; field: string; before: string; after: string }[] = [];
  const remaining = new Set(selected);
  let skipped = 0;
  let scanned = 0;
  let cursor: string | undefined;
  try {
    do {
      const page = await api.data.query({
        contract: "vibetable.plugin-query.v2", collection, fields: ["id", ...fields], pageSize: 200,
        ...(cursor === undefined ? {} : { cursor }),
      });
      for (const row of page.items) {
        const id = String(row.id);
        if (!remaining.delete(id)) continue;
        for (const field of fields) {
          const before = row[field];
          if (typeof before !== "string") { skipped += 1; continue; }
          const after = normalizeText(before);
          if (after === before) { skipped += 1; continue; }
          const expectedDigest = page.rowGuards[id];
          if (!expectedDigest) throw new PluginCapabilityError("plugin_guard_unavailable", "宿主没有提供记录并发保护，请更新宿主");
          operations.push({ kind: "update", primaryKey: id, expectedDigest, values: { [field]: after } });
          if (sampleRows.length < 5) sampleRows.push({ id, field, before, after });
        }
      }
      scanned += page.items.length;
      cursor = page.nextCursor ?? undefined;
    } while (cursor !== undefined && remaining.size > 0);
  } finally {
    // Stopping early keeps the continuation handle owned; release it explicitly.
    if (cursor !== undefined) await api.data.releaseCursor(cursor);
  }
  if (remaining.size) throw new PluginCapabilityError("plugin_selection_stale", "选中记录已变化，请重新选择后执行");
  return { operations, sampleRows, skipped, scanned };
}

/** One headless run: guarded preview, native Host confirmation, single commit. */
export async function normalizeSelection(api: SurfaceCapabilities): Promise<string> {
  const context = await api.context.read();
  const collection = context.collection;
  if (!collection || context.selectedKeys.length === 0) {
    throw new PluginCapabilityError("plugin_input_invalid", "请先在数据表中选择记录");
  }
  const description = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection });
  if (!("fields" in description)) throw new PluginCapabilityError("plugin_capability_invalid", "授权元数据与预期不符");
  const { operations, sampleRows, skipped, scanned } = await collectNormalization(
    api, collection, description, new Set(context.selectedKeys.map(String)),
  );
  if (operations.length === 0) return `扫描 ${scanned} 条选中记录：没有需要规范化的文本。`;
  const preview = await api.mutation.preview({
    contract: "vibetable.surface-mutation-plan.v1", collection,
    schemaRevision: description.schemaRevision, operations,
  });
  const outcome = await api.mutation.submit(preview.planId);
  if (outcome.status === "rejected") {
    throw new PluginCapabilityError("plugin_mutation_rejected", "用户拒绝：未写入任何记录");
  }
  const sample = sampleRows[0] ? `（示例：${sampleRows[0].before} → ${sampleRows[0].after}）` : "";
  return `已规范化 ${operations.length} 处文本，跳过 ${skipped} 项${sample}`;
}
