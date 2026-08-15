import type { HostBridge } from "@/bridge/hostBridge";
import type {
  DashboardPanelType,
  DashboardQueryResultPayload,
  SchemaDescribeResult,
} from "@/contracts";
import type {
  BindingCollectionSchema,
  BindingQueryExecutor,
  SchemaCatalog,
} from "@/dashboard/bindingRuntime";

const SCHEMA_ACCEPTS = [
  "vibetable.relation-capabilities.v1",
  "vibetable.lookup-query.v1",
] as const;

/** Host adapter for the read-only schema catalog used by surface builders. */
export class DashboardSchemaCatalog implements SchemaCatalog {
  private readonly cache = new Map<string, BindingCollectionSchema>();
  private generation = 0;

  constructor(private readonly bridge: HostBridge) {}

  async describe(collectionId: string, signal: AbortSignal): Promise<BindingCollectionSchema> {
    signal.throwIfAborted();
    const cached = this.cache.get(collectionId);
    if (cached) return cached;
    const requestGeneration = ++this.generation;
    const result = await this.bridge.request("schema.describe", {
      collection: collectionId,
      requestGeneration,
      accepts: SCHEMA_ACCEPTS,
    }) as SchemaDescribeResult;
    signal.throwIfAborted();
    if (result.collection !== collectionId || result.requestGeneration !== requestGeneration) {
      throw productError("binding.schema_stale", "SchemaCatalog returned a stale description.");
    }
    const schema: BindingCollectionSchema = {
      collectionId,
      revision: result.schema.schemaRevision,
      fields: result.schema.columns.map((column) => ({
        ref: column.name,
        fieldId: column.fieldId ?? column.name,
        label: column.title,
        dataType: column.dataType,
        filterOperators: column.filterOperators ?? [],
        groupable: column.groupable === true,
        summaryOperations: column.summaryOperations ?? [],
      })),
    };
    this.cache.set(collectionId, schema);
    return schema;
  }

  invalidate(collectionId?: string): void {
    if (collectionId) this.cache.delete(collectionId);
    else this.cache.clear();
  }
}

/** Host adapter that turns AbortSignal into the native dashboard cancellation contract. */
export class DashboardQueryExecutor implements BindingQueryExecutor {
  constructor(private readonly bridge: HostBridge) {}

  async execute(
    panelType: DashboardPanelType,
    query: Parameters<BindingQueryExecutor["execute"]>[1],
    signal: AbortSignal,
  ): Promise<DashboardQueryResultPayload> {
    signal.throwIfAborted();
    const handle = this.bridge.requestWithHandle("dashboard.queryRequested", { panelType, query });
    const cancel = () => this.bridge.notify("dashboard.cancelRequested", { targetRequestId: handle.requestId });
    signal.addEventListener("abort", cancel, { once: true });
    try {
      return await handle.promise as DashboardQueryResultPayload;
    } finally {
      signal.removeEventListener("abort", cancel);
    }
  }
}

function productError(code: string, message: string): Error & { code: string } {
  return Object.assign(new Error(message), { code });
}
