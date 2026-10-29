import { PluginCapabilityError } from "./capabilities.js";
import type { DataQueryRequest } from "./data.js";
import type { JsonObject } from "./types.js";

export const SURFACE_DATASET_CONTRACT = "vibetable.plugin-dataset.v1";
export interface DatasetSource {
  readonly query: Omit<DataQueryRequest, "cursor" | "pageSize">;
  readonly groupBy: readonly string[];
  readonly sums: readonly { readonly field: string; readonly storage: "decimalText" | "integerUnits"; readonly scale: number; readonly digits: number }[];
}
export interface DatasetVersion {
  readonly schemaRevision: string;
  readonly dataRevision: number;
  readonly clockPeriod: string;
  readonly computed: Readonly<Record<string, { readonly definition: number; readonly watermark: string; readonly ready: boolean; readonly status: string }>> | null;
}
export interface DatasetInfo {
  readonly contract: typeof SURFACE_DATASET_CONTRACT;
  readonly datasetId: string;
  readonly versions: Readonly<Record<string, DatasetVersion>>;
  /** computedFields comes from authoritative schema, never inferred from a cell's object keys. */
  readonly sources: readonly { readonly collection: string; readonly fields: readonly string[]; readonly computedFields: readonly string[]; readonly filteredRows: number; readonly totalRows: number; readonly complete: boolean }[];
  readonly expiresAt: string;
}
export interface DatasetPage {
  readonly datasetId: string;
  readonly collection: string;
  readonly items: readonly JsonObject[];
  readonly nextOffset: number | null;
  readonly versions: DatasetInfo["versions"];
  /** Projected computed cells are fresh. Page exhaustion is nextOffset === null. */
  readonly complete: boolean;
}
export interface DatasetSum {
  readonly field: string;
  readonly scale: number;
  readonly value: string | null;
  readonly complete: boolean;
  readonly nullCount: number;
  readonly pendingCount: number;
  readonly unknownCount: number;
  readonly unallocatedCount: number;
  readonly zeroCount: number;
}
export interface DatasetSummary {
  readonly datasetId: string;
  readonly collection: string;
  readonly versions: DatasetInfo["versions"];
  /** Same as summary.complete: all projected computations and exact sums are known. */
  readonly complete: boolean;
  readonly summary: { readonly count: number; readonly complete: boolean; readonly sums: readonly DatasetSum[]; readonly groups: readonly { readonly key: readonly { readonly state: string; readonly value: unknown }[]; readonly count: number; readonly sums: readonly DatasetSum[] }[] };
}
export interface SurfaceDataset extends DatasetInfo {
  page(collection: string, offset?: number, pageSize?: number, signal?: AbortSignal): Promise<DatasetPage>;
  summary(collection: string, signal?: AbortSignal): Promise<DatasetSummary>;
  /** Native CSV save from these immutable rows; never silently opens a newer dataset. */
  export(collection: string, suggestedName: string, signal?: AbortSignal): Promise<{ readonly committed: true; readonly datasetId: string; readonly versions: DatasetInfo["versions"]; readonly rowsWritten: number } | null>;
  /** Consumes this dataset. Exhaustion, early break, cancellation and consumer errors release it. */
  pages(collection: string, signal?: AbortSignal): AsyncGenerator<DatasetPage, void>;
  release(): Promise<void>;
}
export interface SurfaceDatasets {
  open(request: { readonly contract: typeof SURFACE_DATASET_CONTRACT; readonly sources: readonly DatasetSource[] }, signal?: AbortSignal): Promise<SurfaceDataset>;
}

type Call = <T>(method: string, params: unknown, signal?: AbortSignal) => Promise<T>;
export function createSurfaceDatasets(call: Call, release: (method: string, params: unknown) => void): SurfaceDatasets & { dispose(): void } {
  const owned = new Map<string, { releasing?: Promise<void> }>();
  let closed = false;
  async function releaseDataset(datasetId: string): Promise<void> {
    const entry = owned.get(datasetId);
    if (!entry) return;
    if (entry.releasing) return entry.releasing;
    entry.releasing = call<{ released: boolean }>("dataset.release", { datasetId }).then(receipt => {
      if (receipt.released !== true) throw new PluginCapabilityError("plugin_transport_failed", "Dataset release was not acknowledged.");
      owned.delete(datasetId);
    }).catch(error => { delete entry.releasing; throw error; });
    return entry.releasing;
  }
  return {
    async open(request, signal) {
      if (closed) throw new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed.");
      const info = await call<DatasetInfo>("dataset.open", structuredClone(request), signal);
      if (closed || signal?.aborted) { release("dataset.release", { datasetId: info.datasetId }); throw new PluginCapabilityError("plugin_cancel_requested", "Dataset cancelled."); }
      if (info.contract !== SURFACE_DATASET_CONTRACT || !info.datasetId) throw new PluginCapabilityError("plugin_api_unsupported", "Dataset contract is unsupported.");
      owned.set(info.datasetId, {});
      const use = <T>(method: string, params: unknown, token?: AbortSignal): Promise<T> => {
        if (!owned.has(info.datasetId)) return Promise.reject(new PluginCapabilityError("plugin_dataset_stale", "Dataset released."));
        return call<T>(method, params, token);
      };
      const dataset: SurfaceDataset = {
        ...info,
        page: (collection, offset = 0, pageSize = 200, token) => use("dataset.page", { datasetId: info.datasetId, collection, offset, pageSize }, token),
        summary: (collection, token) => use("dataset.summary", { datasetId: info.datasetId, collection }, token),
        export: (collection, suggestedName, token) => use("dataset.export", { datasetId: info.datasetId, collection, suggestedName }, token),
        async *pages(collection, token) {
          let offset: number | null = 0;
          try { do { const page = await dataset.page(collection, offset, 200, token); offset = page.nextOffset; yield page; } while (offset !== null); }
          finally { await releaseDataset(info.datasetId); }
        },
        release: () => releaseDataset(info.datasetId),
      };
      return dataset;
    },
    dispose() {
      if (closed) return;
      closed = true;
      for (const [datasetId, entry] of owned) if (!entry.releasing) release("dataset.release", { datasetId });
      owned.clear();
    },
  };
}
