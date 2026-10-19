import { PluginCapabilityError, type DataDescription, type JsonObject, type SurfaceCapabilities } from "@vibetable/plugin-sdk";

export interface OverviewInput {
  readonly fieldId?: string;
  readonly contains?: string;
}

export interface OverviewStats {
  readonly count: number;
  readonly totalRows: number;
  readonly filteredRows: number;
  readonly nonNullCount: number;
  readonly complete: boolean;
  readonly fieldId: string | null;
  readonly sampleRows: readonly JsonObject[];
  readonly pagesRead: number;
}

/** Pick the first sortable non-id field as the default focus column. */
export function defaultField(description: DataDescription): string | null {
  return description.fields.find(item => item.fieldId !== "id" && item.sortable)?.fieldId ?? null;
}

/** Page through the whole authorized result and fold authoritative counters. */
export async function collectOverview(
  api: SurfaceCapabilities,
  input: OverviewInput,
): Promise<{ collection: string; description: DataDescription; stats: OverviewStats }> {
  const context = await api.context.read();
  const collection = context.collection;
  if (!collection) throw new PluginCapabilityError("plugin_input_invalid", "请选择数据表");
  const metadata = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection });
  if (!("fields" in metadata)) throw new PluginCapabilityError("plugin_capability_invalid", "授权元数据与预期不符");
  const requested = input.fieldId === undefined
    ? (defaultField(metadata) === null ? undefined : metadata.fields.find(item => item.fieldId === defaultField(metadata)))
    : metadata.fields.find(item => item.fieldId === input.fieldId);
  if (input.fieldId !== undefined && !requested) {
    throw new PluginCapabilityError("plugin_read_denied", "字段不可读取");
  }
  const query = {
    contract: "vibetable.plugin-query.v2" as const, collection,
    fields: requested ? ["id", requested.fieldId] : ["id"], pageSize: 200,
    filters: input.contains === undefined ? [] : [{ field: requested?.fieldId ?? "", operator: "contains" as const, value: input.contains }],
    sorts: requested?.sortable ? [{ field: requested.fieldId, direction: "asc" as const, nullsLast: true }] : [],
  };
  let cursor: string | undefined;
  let count = 0;
  let nonNullCount = 0;
  let complete = true;
  let filteredRows = 0;
  let totalRows = 0;
  let pagesRead = 0;
  const sampleRows: JsonObject[] = [];
  try {
    do {
      const page = await api.data.query(cursor === undefined ? query : { ...query, cursor });
      pagesRead += 1;
      count += page.items.length;
      filteredRows = page.filteredRows;
      totalRows = page.totalRows;
      complete &&= page.complete;
      for (const row of page.items) {
        if (requested && row[requested.fieldId] !== null && row[requested.fieldId] !== undefined) nonNullCount++;
        if (sampleRows.length < 5) sampleRows.push(row);
      }
      cursor = page.nextCursor ?? undefined;
    } while (cursor !== undefined);
  } finally {
    if (cursor !== undefined) await api.data.releaseCursor(cursor);
  }
  if (count !== filteredRows) throw new PluginCapabilityError("plugin_query_failed", "分页统计不完整");
  return {
    collection, description: metadata,
    stats: { count, totalRows, filteredRows, nonNullCount, complete, fieldId: requested?.fieldId ?? null, sampleRows, pagesRead },
  };
}
