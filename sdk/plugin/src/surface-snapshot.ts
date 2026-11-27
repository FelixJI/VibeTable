import { PluginCapabilityError } from "./capabilities.js";
import type { DataQueryRequest } from "./data.js";
import { DATA_VERSIONS_LIMITS, isDataVersion, sameDataVersion } from "./surface-versions.js";
import type { DataVersion } from "./surface-versions.js";
import type { JsonObject } from "./types.js";

/**
 * `data.openSnapshot` (#590): one bounded read point over many authorized
 * sources. The Host materializes every source inside one Go transaction (same
 * evaluation instant, same #589 version vector), closes the transaction, and
 * only then hands out a handle. Rows are served from that immutable snapshot
 * in two shapes, each tied to its own authorization round trip: one page of one
 * source (snapshot.page) or one bounded batch that crosses sources
 * (snapshot.readBatch). Before either the Host re-checks the handle, the
 * session, the current authorization and every source's schema revision
 * exactly once (snapshot.validate), then materializes that page or batch at
 * that authorization point. The SDK never serves rows from a handle the Host
 * stopped endorsing and never invents pages from its own cache. A plain data
 * write never invalidates a handle and never leaks a newer row.
 */
export const SURFACE_SNAPSHOT_CONTRACT = "vibetable.plugin-snapshot.v1";
/** Bounded request/handle shape; the Host rejects anything larger before any read. */
export const SNAPSHOT_LIMITS = Object.freeze({
  sources: DATA_VERSIONS_LIMITS.sources,
  fieldsPerSource: DATA_VERSIONS_LIMITS.fieldsPerSource,
  keyBytes: DATA_VERSIONS_LIMITS.keyBytes,
  idsPerSource: 200,
  rows: 4096,
  bytes: 512 * 1024,
  handlesPerSurface: 2,
  handlesTotal: 8,
  seconds: 300,
  /** Fixed page size the handle walks a single source with. */
  pageRows: 200,
  /** One readBatch crosses sources but carries at most this many rows; empty sources ride along. */
  batchRows: 200,
  /** Bridge-encoded budget of one batch across source boundaries: every chunk's containers and fixed members plus every row's item, guard and separators (the whole reply also stays inside the frame); only the batch's very first chunk and very first row are unconditional. */
  batchBytes: 256 * 1024,
});

export interface DataSnapshotSource {
  /** An ordinary data.query request without cursor/pageSize: the snapshot reads
   * exactly this projection, ids, filters and sorts in one bounded transaction. */
  readonly query: Omit<DataQueryRequest, "cursor" | "pageSize">;
}
export interface DataSnapshotRequest {
  readonly contract: typeof SURFACE_SNAPSHOT_CONTRACT;
  /** 1-64 distinct collections (no `$` aliases); every source is read or none is. */
  readonly sources: readonly DataSnapshotSource[];
}
export type DataSnapshotVersion = DataVersion;
export interface DataSnapshotSourceInfo {
  readonly collection: string;
  readonly fields: readonly string[];
  /** From authoritative schema, never inferred from a cell's object keys. */
  readonly computedFields: readonly string[];
  readonly filteredRows: number;
  readonly totalRows: number;
  readonly complete: boolean;
}
export interface DataSnapshotInfo {
  readonly contract: typeof SURFACE_SNAPSHOT_CONTRACT;
  readonly snapshotId: string;
  /** Keyed by requested collection; identical to the data.versions vector for the same projections at the read point. */
  readonly versions: Readonly<Record<string, DataSnapshotVersion>>;
  readonly sources: readonly DataSnapshotSourceInfo[];
  /** Absolute ISO expiry; the Host releases the handle at that instant. */
  readonly expiresAt: string;
}
/** Rows of one source sliced out of the immutable snapshot. */
export interface DataSnapshotChunk {
  readonly collection: string;
  /** Rows in request order; every item carries exactly the projected fields. */
  readonly items: readonly JsonObject[];
  /** Opaque per-row digest keyed by row id (same contract as data.query): present for every
   * served row while the authority still lets the plugin read `id`, whether or not `id` is
   * projected; `{}` once that read is revoked. Items never gain an unprojected `id`. */
  readonly rowGuards: Readonly<Record<string, unknown>>;
  /** Offset of this source's next row inside the snapshot; null once the source is exhausted (a
   * non-null offset always points at a row still to serve). */
  readonly nextOffset: number | null;
  /** Projected computed cells are fresh. Source exhaustion is nextOffset === null. */
  readonly complete: boolean;
}
export interface DataSnapshotPage extends DataSnapshotChunk {
  readonly snapshotId: string;
  /** The full version vector of the read point, relayed unchanged on every page. */
  readonly versions: DataSnapshotInfo["versions"];
}
/** Explicit walk position: the first row (collection, offset) the next batch must serve. */
export interface DataSnapshotPosition {
  readonly collection: string;
  readonly offset: number;
}
/** One authorized cross-source read: several consecutive source chunks and the read point's whole version vector. */
export interface DataSnapshotBatch {
  readonly snapshotId: string;
  /** Consecutive source chunks in request order from the requested position; empty sources appear with zero rows. */
  readonly chunks: readonly DataSnapshotChunk[];
  /** Position the next batch continues from; null once every source is exhausted. */
  readonly next: DataSnapshotPosition | null;
  /** The full version vector of the read point, relayed unchanged on every batch. */
  readonly versions: DataSnapshotInfo["versions"];
}
export interface DataSnapshot extends DataSnapshotInfo {
  /** One page of one source from the immutable snapshot; one authorization round trip per page. */
  page(collection: string, offset?: number, pageSize?: number, signal?: AbortSignal): Promise<DataSnapshotPage>;
  /**
   * One authorized cross-source batch: at most batchRows rows and batchBytes of bridge-encoded
   * content (chunk containers, items, guards, separators) from `next` (default: the first source) in
   * request order, empty sources included; one authorization round trip per batch. The handle stays
   * open; the reply's `next` is the argument of the following call. Every delivered batch belongs
   * to the caller as a whole.
   */
  readBatch(next?: DataSnapshotPosition | null, signal?: AbortSignal): Promise<DataSnapshotBatch>;
  /** Every page of every source in request order through page(): each yielded page is one
   * authorized network page. Exhaustion, early break, cancellation and consumer errors close
   * the handle. */
  pages(signal?: AbortSignal): AsyncGenerator<DataSnapshotPage, void>;
  /** Idempotent; a failed release keeps the handle for a retry, a stale one drops it. */
  close(): Promise<void>;
}
export interface SurfaceDataSnapshot {
  /** Old Hosts answer `plugin_api_unsupported`; nothing is emulated locally. */
  open(request: DataSnapshotRequest, signal?: AbortSignal): Promise<DataSnapshot>;
}

type Call = <T>(method: string, params: unknown, signal?: AbortSignal) => Promise<T>;
function object(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object") return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
function exactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const names = Object.keys(value);
  return names.length === keys.length && names.every(name => keys.includes(name));
}
function count(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}
function position(value: unknown): value is DataSnapshotPosition {
  return object(value) && exactKeys(value, ["collection", "offset"]) && typeof value.collection === "string" && count(value.offset);
}
function samePosition(left: unknown, right: DataSnapshotPosition | null): boolean {
  return right === null ? left === null : position(left) && left.collection === right.collection && left.offset === right.offset;
}
function isStale(error: unknown): boolean {
  return error instanceof PluginCapabilityError && error.code === "plugin_snapshot_stale";
}

export function createSurfaceSnapshot(call: Call, release: (method: string, params: unknown) => void): SurfaceDataSnapshot & { dispose(): void } {
  const owned = new Map<string, { closing?: Promise<void> }>();
  let closed = false;
  const closeHandle = (snapshotId: string): Promise<void> => {
    const entry = owned.get(snapshotId);
    if (!entry) return Promise.resolve();
    if (entry.closing) return entry.closing;
    entry.closing = call<{ released: boolean }>("snapshot.release", { snapshotId }).then(receipt => {
      if (receipt.released !== true) throw new PluginCapabilityError("plugin_transport_failed", "Snapshot release was not acknowledged.");
      owned.delete(snapshotId);
    }).catch(error => {
      delete entry.closing;
      // The Host no longer knows this handle (released, expired, retired):
      // retrying is pointless, so drop the local ownership too.
      if (isStale(error)) owned.delete(snapshotId);
      throw error;
    });
    return entry.closing;
  };
  return {
    async open(request, signal) {
      if (closed) throw new PluginCapabilityError("plugin_surface_closed", "Plugin surface closed.");
      // Strict input, same line as Go: a snapshot query is an ordinary data.query request
      // without cursor/pageSize, and the mere presence of either member (null or not) or an
      // explicit null for ids/filters/sorts is plugin_capability_invalid. Answered here before
      // any round trip, so no handle is ever created for such a request.
      if (!Array.isArray(request.sources)) throw new PluginCapabilityError("plugin_capability_invalid", "Snapshot request is invalid.");
      for (const source of request.sources) {
        const query: unknown = object(source) ? source.query : undefined;
        if (!object(query) || Object.hasOwn(query, "cursor") || Object.hasOwn(query, "pageSize")
          || ["ids", "filters", "sorts"].some(key => Object.hasOwn(query, key) && query[key] === null)) {
          throw new PluginCapabilityError("plugin_capability_invalid", "Snapshot query is invalid.");
        }
      }
      // The authority sees and the reply is checked against one stable copy of the request: the
      // caller's object may change while the round trip is in flight without changing what was sent.
      const stable = structuredClone(request);
      const info = await call<DataSnapshotInfo>("snapshot.open", stable, signal);
      const invalid = () => new PluginCapabilityError("plugin_transport_failed", "Incomplete or inconsistent snapshot reply.");
      // A handle the Host created for an open this surface will not keep is released only once its
      // id is confirmable and not one this surface already owns (a colliding or malformed reply
      // never releases somebody else's handle).
      const discard = (): void => {
        if (!object(info) || typeof info.snapshotId !== "string" || !info.snapshotId || owned.has(info.snapshotId)) return;
        release("snapshot.release", { snapshotId: info.snapshotId });
      };
      const refused = (): never => {
        discard();
        throw invalid();
      };
      if (closed || signal?.aborted) {
        discard();
        throw new PluginCapabilityError("plugin_cancel_requested", "Snapshot cancelled.");
      }
      if (!object(info) || !exactKeys(info, ["contract", "snapshotId", "versions", "sources", "expiresAt"]) || info.contract !== SURFACE_SNAPSHOT_CONTRACT) throw refused();
      if (typeof info.snapshotId !== "string" || !info.snapshotId || !Array.isArray(info.sources) || !object(info.versions) ||
          typeof info.expiresAt !== "string" || !Number.isFinite(Date.parse(info.expiresAt))) throw refused();
      const requested = stable.sources.map(source => source.query);
      if (info.sources.length !== requested.length || Object.keys(info.versions).length !== requested.length) throw refused();
      for (const [index, source] of requested.entries()) {
        const meta = info.sources[index];
        if (!object(meta) || !exactKeys(meta, ["collection", "fields", "computedFields", "filteredRows", "totalRows", "complete"])) throw refused();
        if (meta.collection !== source.collection || !Array.isArray(meta.fields) || meta.fields.length !== source.fields.length || !meta.fields.every((field, i) => field === source.fields[i])) throw refused();
        if (!Array.isArray(meta.computedFields) || !meta.computedFields.every(field => typeof field === "string" && source.fields.includes(field))) throw refused();
        if (!count(meta.filteredRows) || !count(meta.totalRows) || meta.filteredRows > meta.totalRows || typeof meta.complete !== "boolean") throw refused();
        if (!Object.hasOwn(info.versions, source.collection) || !isDataVersion(info.versions[source.collection], source.fields)) throw refused();
      }
      const snapshotId = info.snapshotId;
      const metas = info.sources as DataSnapshotSourceInfo[];
      owned.set(snapshotId, {});
      const stale = () => new PluginCapabilityError("plugin_snapshot_stale", "Snapshot closed or released.");
      const shape = () => new PluginCapabilityError("plugin_capability_invalid", "Snapshot position is invalid.");
      // Every reply relays the read point's whole version vector, exactly: the same keys as the
      // requested collections and the same version per key. Anything else is not this snapshot.
      const relayVersions = (versions: unknown): boolean => object(versions) && Object.keys(versions).length === metas.length
        && metas.every(({ collection }) => sameDataVersion(info.versions[collection], versions[collection] as DataVersion));
      // Row guards follow the data.query contract: string digests keyed by row id, at most one per
      // served row, and whenever `id` is projected every key is the id of a served row. The map is a
      // subset (or `{}`) once the authority stops endorsing row identity.
      const checkGuards = (guards: Record<string, unknown>, items: readonly unknown[], meta: DataSnapshotSourceInfo): boolean => {
        const keys = Object.keys(guards);
        if (keys.length > items.length || keys.some(key => !key || typeof guards[key] !== "string")) return false;
        if (!meta.fields.includes("id")) return true;
        const served = new Set(items.map(item => (item as Record<string, unknown>).id));
        return keys.every(key => served.has(key));
      };
      // Slices tile their source exactly: nextOffset steps by the rows served, a slice that
      // stops short of filteredRows must have served rows, a non-null nextOffset always points at a
      // row still to serve, and only the final slice of a source reaches filteredRows with none.
      const checkSlice = (slice: Record<string, unknown>, meta: DataSnapshotSourceInfo, start: number): slice is DataSnapshotChunk & Record<string, unknown> => {
        if (slice.collection !== meta.collection || !Array.isArray(slice.items) || !object(slice.rowGuards) || typeof slice.complete !== "boolean") return false;
        const items: unknown[] = slice.items;
        if (!items.every(item => object(item) && exactKeys(item, meta.fields)) || !checkGuards(slice.rowGuards, items, meta)) return false;
        const next = slice.nextOffset;
        const reached = start + items.length;
        return next === null ? reached === meta.filteredRows : count(next) && next === reached && items.length > 0 && next < meta.filteredRows;
      };
      const use = async <T>(method: string, params: unknown, token?: AbortSignal): Promise<T> => {
        if (!owned.has(snapshotId)) throw stale();
        try { return await call<T>(method, params, token); }
        catch (error) {
          // The Host released this handle (schema/grant/session/expiry): never serve from it again.
          if (isStale(error)) owned.delete(snapshotId);
          throw error;
        }
      };
      // Nothing lands after the caller stopped waiting or the surface closed, and the handle it
      // came from ends right there. Checked synchronously in the turn the reply resolves, never a
      // microtask later, so no callback queued by the caller can run in between.
      const deliver = <T>(result: T, token?: AbortSignal): T => {
        if (!closed && !token?.aborted) return result;
        owned.delete(snapshotId);
        if (!closed) release("snapshot.release", { snapshotId });
        throw new PluginCapabilityError(closed ? "plugin_surface_closed" : "plugin_cancel_requested", closed ? "Plugin surface closed." : "Snapshot cancelled.");
      };
      const page = async (collection: string, offset = 0, pageSize = SNAPSHOT_LIMITS.pageRows, token?: AbortSignal): Promise<DataSnapshotPage> => {
        const meta = metas.find(source => source.collection === collection);
        if (!meta) throw new PluginCapabilityError("plugin_capability_invalid", "Collection is not part of this snapshot.");
        if (!count(offset) || !count(pageSize) || pageSize < 1 || pageSize > SNAPSHOT_LIMITS.pageRows) throw new PluginCapabilityError("plugin_capability_invalid", "Snapshot page bounds are invalid.");
        const result = deliver(await use<DataSnapshotPage>("snapshot.page", { snapshotId, collection, offset, pageSize }, token), token);
        if (!object(result) || !exactKeys(result, ["snapshotId", "collection", "items", "rowGuards", "nextOffset", "versions", "complete"])) throw invalid();
        if (result.snapshotId !== snapshotId || !checkSlice(result, meta, offset) || result.items.length > pageSize || !relayVersions(result.versions)) throw invalid();
        return result;
      };
      const readBatch = async (from: DataSnapshotPosition | null | undefined, token?: AbortSignal): Promise<DataSnapshotBatch> => {
        let index = 0, offset = 0;
        if (from !== null && from !== undefined) {
          if (!position(from)) throw shape();
          index = metas.findIndex(source => source.collection === from.collection);
          const meta = metas[index];
          if (!meta) throw new PluginCapabilityError("plugin_capability_invalid", "Collection is not part of this snapshot.");
          if (from.offset > meta.filteredRows) throw shape();
          offset = from.offset;
        }
        const result = deliver(await use<DataSnapshotBatch>("snapshot.readBatch", { snapshotId, next: from ?? null }, token), token);
        if (!object(result) || !exactKeys(result, ["snapshotId", "chunks", "next", "versions"])) throw invalid();
        if (result.snapshotId !== snapshotId || !Array.isArray(result.chunks) || !relayVersions(result.versions)) throw invalid();
        // Chunks tile the sources consecutively from the requested position: no source is
        // skipped (empty ones included), a chunk that stops short of its source ends the
        // batch, and the batch never exceeds the row budget.
        const chunks: unknown[] = result.chunks;
        if (chunks.length < 1) throw invalid();
        let rows = 0, lastMeta: DataSnapshotSourceInfo | undefined, lastNext: number | null = null;
        for (const [at, chunk] of chunks.entries()) {
          const meta = metas[index + at];
          if (!meta || !object(chunk) || !exactKeys(chunk, ["collection", "items", "rowGuards", "nextOffset", "complete"]) || !checkSlice(chunk, meta, at === 0 ? offset : 0)) throw invalid();
          if (chunk.nextOffset !== null && at !== chunks.length - 1) throw invalid();
          rows += chunk.items.length;
          lastMeta = meta;
          lastNext = chunk.nextOffset;
        }
        if (rows > SNAPSHOT_LIMITS.batchRows || !lastMeta) throw invalid();
        const following = metas[index + chunks.length];
        const expected: DataSnapshotPosition | null = lastNext !== null ? { collection: lastMeta.collection, offset: lastNext } : following ? { collection: following.collection, offset: 0 } : null;
        if (!samePosition(result.next, expected)) throw invalid();
        return result;
      };
      const snapshot: DataSnapshot = {
        ...info,
        page,
        readBatch,
        async *pages(token) {
          try {
            for (const { collection } of metas) {
              let offset: number | null = 0;
              do {
                const current = await page(collection, offset ?? 0, SNAPSHOT_LIMITS.pageRows, token);
                // The generator resumes a turn after page() resolved: re-check right before the yield.
                yield deliver(current, token);
                offset = current.nextOffset;
              } while (offset !== null);
            }
          } finally { await snapshot.close(); }
        },
        close: () => closeHandle(snapshotId),
      };
      return snapshot;
    },
    dispose() {
      if (closed) return;
      closed = true;
      for (const [snapshotId, entry] of owned) if (!entry.closing) release("snapshot.release", { snapshotId });
      owned.clear();
    },
  };
}
