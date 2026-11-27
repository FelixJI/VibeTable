import { PluginCapabilityError } from "./capabilities.js";
import type { DatasetVersion } from "./surface-dataset.js";

/**
 * `data.versions` (#589): the exact canonical schema/data version vector of up to
 * 64 authorized sources, read by Go in one authoritative transaction together with
 * the current installation and every source's permissions. No business row,
 * dataset page or cursor is read; an unchanged batch costs no extra reads.
 */
export const SURFACE_DATA_VERSIONS_CONTRACT = "vibetable.plugin-data-versions.v1";
/** Bounded batch shape; the Host rejects anything larger before any read. */
export const DATA_VERSIONS_LIMITS = Object.freeze({ sources: 64, fieldsPerSource: 64, keyBytes: 128, dataRevision: Number.MAX_SAFE_INTEGER });

export interface DataVersionSource {
  /** Canonical single-table collection ID. */
  readonly collection: string;
  /** Explicit canonical field IDs of the projection the plugin reuses (no relation
   * paths, no duplicates). The projection is part of the version check: a narrowed
   * grant fails the whole batch instead of letting previously authorized columns
   * pass as unchanged. */
  readonly fields: readonly string[];
}
export interface DataVersionsRequest {
  readonly contract: typeof SURFACE_DATA_VERSIONS_CONTRACT;
  /** 1-64 distinct sources. */
  readonly sources: readonly DataVersionSource[];
  /** Prior versions keyed by exactly the requested collections, passed back exactly
   * as returned (every key present, never null: a missing dataRevision is not 0).
   * Omit to only read. When present the reply also carries `stale`. */
  readonly versions?: Readonly<Record<string, DataVersion>>;
}
/** Versions returned by data.versions use canonical computed field IDs. */
export type DataVersion = DatasetVersion & { readonly computed: NonNullable<DatasetVersion["computed"]> };
type ComputedRevisions = NonNullable<DatasetVersion["computed"]>;
export interface DataVersions {
  readonly contract: typeof SURFACE_DATA_VERSIONS_CONTRACT;
  /** Keyed by requested collection; `computed` is keyed by canonical field ID and holds only projected computed fields. */
  readonly versions: Readonly<Record<string, DataVersion>>;
  /** Requested collections whose current version differs from the prior one. Only present when the request carried `versions`. */
  readonly stale?: readonly string[];
}
export type SurfaceDataVersions = (request: DataVersionsRequest, signal?: AbortSignal) => Promise<DataVersions>;

/** Exact comparison of two version vectors; an absent side is never equal. */
export function sameDataVersion(left: DatasetVersion | undefined, right: DatasetVersion | undefined): boolean {
  if (!left || !right || left.schemaRevision !== right.schemaRevision || left.dataRevision !== right.dataRevision || left.clockPeriod !== right.clockPeriod) return false;
  if (!validVersion(left) || !validVersion(right)) return false;
  const a: ComputedRevisions = left.computed, b: ComputedRevisions = right.computed;
  const keys = Object.keys(a);
  if (keys.length !== Object.keys(b).length) return false;
  return keys.every(key => {
    const x = a[key], y = b[key];
    return !!x && !!y && x.definition === y.definition && x.watermark === y.watermark && x.ready === y.ready && x.status === y.status;
  });
}

type Call = <T>(method: string, params: unknown, signal?: AbortSignal) => Promise<T>;
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function validVersion(value: unknown): value is DataVersion {
  return object(value) && typeof value.schemaRevision === "string" && value.schemaRevision.length > 0 &&
    typeof value.dataRevision === "number" && Number.isSafeInteger(value.dataRevision) && value.dataRevision >= 0 &&
    typeof value.clockPeriod === "string" && object(value.computed) &&
    Object.entries(value.computed).every(([field, entry]) => field.length > 0 && object(entry) &&
      Number.isSafeInteger(entry.definition) && typeof entry.watermark === "string" &&
      typeof entry.ready === "boolean" && typeof entry.status === "string");
}
export function createSurfaceVersions(call: Call): SurfaceDataVersions {
  return async (request, signal) => {
    // Exactly one ordinary bridge request carries the projections and prior
    // versions verbatim; nothing is resolved, split, cached or retried locally.
    request = structuredClone(request);
    const result = await call<DataVersions>("data.versions", request, signal);
    // Old Hosts reject the method explicitly; a reply that is not the versions
    // contract is never reinterpreted as another capability or a partial result.
    const invalid = () => new PluginCapabilityError("plugin_transport_failed", "Incomplete or inconsistent data versions reply.");
    if (!result || result.contract !== SURFACE_DATA_VERSIONS_CONTRACT || !object(result.versions) ||
        Object.keys(result.versions).length !== request.sources.length) throw invalid();
    for (const { collection } of request.sources) {
      if (!Object.hasOwn(result.versions, collection) || !validVersion(result.versions[collection])) throw invalid();
    }
    if (request.versions === undefined) {
      if (result.stale !== undefined) throw invalid();
    } else {
      const expected = request.sources.filter(({ collection }) =>
        !sameDataVersion(request.versions?.[collection], result.versions[collection])).map(source => source.collection);
      if (!Array.isArray(result.stale) || result.stale.length !== expected.length ||
          !expected.every(collection => result.stale!.includes(collection))) throw invalid();
    }
    return result;
  };
}
