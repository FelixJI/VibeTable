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

/** Query-contract batch limit: ids requests carry at most 200 items. */
const ID_BATCH_LIMIT = 200;

function idBatches(ids: readonly string[]): string[][] {
  const batches: string[][] = [];
  for (let index = 0; index < ids.length; index += ID_BATCH_LIMIT) {
    batches.push(ids.slice(index, index + ID_BATCH_LIMIT));
  }
  return batches;
}

export interface NormalizationScan {
  /** One merged update per changed record; never two operations for one row. */
  readonly operations: readonly UpdateOperation[];
  readonly sampleRows: readonly { id: string; field: string; before: string; after: string }[];
  /** Total changed text values; independent of the per-record operation count. */
  readonly changedFields: number;
  readonly skipped: number;
  /** Selected rows actually returned by the authority, not scanned table pages. */
  readonly scanned: number;
}

function textFields(description: DataDescription): string[] {
  return description.fields
    .filter(field => !field.readonly && field.logicalType === "text")
    .map(field => field.fieldId);
}

/** Query exactly the selected records by id and build guarded merged updates. */
export async function collectNormalization(
  api: SurfaceCapabilities,
  collection: string,
  description: DataDescription,
  selected: ReadonlySet<string>,
): Promise<NormalizationScan> {
  const fields = textFields(description);
  if (fields.length === 0) throw new PluginCapabilityError("plugin_write_denied", "没有可写的文本字段");
  const operations: UpdateOperation[] = [];
  const sampleRows: { id: string; field: string; before: string; after: string }[] = [];
  const remaining = new Set(selected);
  let changedFields = 0;
  let skipped = 0;
  let scanned = 0;
  for (const batch of idBatches([...remaining])) {
    // Even an ids query may page: follow every continuation to null and keep
    // the outstanding handle released on any abnormal exit — a leaked cursor
    // would pin one of the 64 surface handles.
    let cursor: string | undefined;
    try {
      do {
        const page = await api.data.query({
          contract: "vibetable.plugin-query.v2", collection, fields: ["id", ...fields], ids: batch, pageSize: 200,
          ...(cursor === undefined ? {} : { cursor }),
        });
        cursor = page.nextCursor ?? undefined;
        for (const row of page.items) {
          const id = String(row.id);
          if (!remaining.delete(id)) continue;
          scanned += 1;
          let values: Record<string, string> | undefined;
          for (const field of fields) {
            const before = row[field];
            if (typeof before !== "string") { skipped += 1; continue; }
            const after = normalizeText(before);
            if (after === before) { skipped += 1; continue; }
            // The Go kernel applies operations sequentially: two updates carrying
            // the same original expectedDigest would conflict on the second one and
            // roll back the whole batch. All changed fields of one record therefore
            // merge into a single update.
            (values ??= {})[field] = after;
            changedFields += 1;
            if (sampleRows.length < 5) sampleRows.push({ id, field, before, after });
          }
          if (values === undefined) continue;
          const expectedDigest = page.rowGuards[id];
          if (!expectedDigest) throw new PluginCapabilityError("plugin_guard_unavailable", "宿主没有提供记录并发保护，请更新宿主");
          operations.push({ kind: "update", primaryKey: id, expectedDigest, values });
        }
      } while (cursor !== undefined);
    } finally {
      if (cursor !== undefined) await api.data.releaseCursor(cursor);
    }
  }
  if (remaining.size) throw new PluginCapabilityError("plugin_selection_stale", "选中记录已变化，请重新选择后执行");
  return { operations, sampleRows, changedFields, skipped, scanned };
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
  const { operations, sampleRows, changedFields, skipped, scanned } = await collectNormalization(
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
  return `已规范化 ${changedFields} 处文本，跳过 ${skipped} 项${sample}`;
}
