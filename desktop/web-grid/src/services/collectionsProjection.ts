import type { CollectionSummary } from "@/stores/workspaceStore";
import type {
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
} from "@/contracts";

/**
 * The single projection entry for the workspace collections catalog.
 *
 * Both `database.opened` and `database.collectionsChanged` (subscription and
 * correlated create/delete replies alike) describe the same catalog: tables
 * first, then views. Every consumer reads the projected result from
 * `workspaceStore.collections`; no second mapper or store copy may exist
 * (see #509). The two wire shapes stay distinct where the host contracts
 * differ: `opened` never carries capability hashes, `changed` carries
 * optional `views` and `capabilityHashes`.
 */

/** Project a `database.opened` payload (separate `tables`/`views` lists). */
export function collectionsFromOpened(
  payload: DatabaseOpenedPayload,
): readonly CollectionSummary[] {
  const tables = payload.tables.map((t) => ({
    collection: t,
    metadata: {},
  }));
  const views = payload.views.map((v) => ({
    collection: v,
    metadata: { kind: "view" } as const,
  }));
  return [...tables, ...views];
}

/** Project a `database.collectionsChanged` payload (optional view metadata). */
export function collectionsFromChanged(
  payload: CollectionsChangedPayload,
): readonly CollectionSummary[] {
  const hashes = payload.capabilityHashes ?? {};
  const tables = payload.tables.map((t) => ({
    collection: t,
    metadata:
      t in hashes ? { capabilityHash: hashes[t] } : {},
  }));
  const views = (payload.views ?? []).map((view) => ({
    collection: view,
    metadata: { kind: "view", ...(view in hashes ? { capabilityHash: hashes[view] } : {}) },
  }));
  return [...tables, ...views];
}
