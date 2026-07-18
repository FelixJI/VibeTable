import { useHostBridge } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import type { CollectionSummary } from "@/stores/workspaceStore";
import type {
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
} from "@/contracts";

/**
 * Translate the wire-level `database.opened` payload (separate `tables`/
 * `views` lists) into the store's `CollectionSummary[]` shape. Mirrors
 * `workspaceService.toCollections` — both stores maintain their own view of
 * the collection list (see the duplication note in the task-9 report).
 */
function toCollections(
  payload: DatabaseOpenedPayload,
): readonly CollectionSummary[] {
  const tables = payload.tables.map((t) => ({ collection: t, metadata: {} }));
  const views = payload.views.map((v) => ({
    collection: v,
    metadata: { kind: "view" } as const,
  }));
  return [...tables, ...views];
}

/**
 * Translate a `database.collectionsChanged` payload (carrying `tables` and
 * optional `capabilityHashes`) into `CollectionSummary[]`.
 */
function toCollectionsFromChanged(
  payload: CollectionsChangedPayload,
): readonly CollectionSummary[] {
  const hashes = payload.capabilityHashes ?? {};
  return payload.tables.map((t) => ({
    collection: t,
    metadata: t in hashes ? { capabilityHash: hashes[t] } : {},
  }));
}

/**
 * tableAdminService wires `tableAdminStore` to the host bridge:
 *   - inbound `database.opened` / `database.collectionsChanged` populate the
 *     store's collection list (the sidebar reads this to list tables),
 *   - outbound `tableAdmin.createRequested` / `tableAdmin.deleteRequested` /
 *     `admin.openRequested` carry the user's create/delete/open-admin intent.
 *
 * Note on field naming: the store's `FieldRow.name` is mapped to the wire
 * `TableAdminFieldInput.key` here, keeping the host's `key`-based contract
 * boundary clean while the UI/store uses the legacy `name` label.
 */
export function useTableAdminService(): {
  init: () => void;
  createTable: () => void;
  deleteTable: (name: string) => void;
  openAdmin: () => void;
} {
  const bridge = useHostBridge();
  const store = useTableAdminStore();

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setCollections(toCollections(payload));
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(toCollectionsFromChanged(payload));
    });
  }

  function createTable(): void {
    if (!store.canSubmit) return;
    store.beginSubmit();
    bridge.notify("tableAdmin.createRequested", {
      name: store.form.name,
      fields: store.form.fields.map((f) => ({ key: f.name, type: f.type })),
    });
  }

  function deleteTable(name: string): void {
    store.requestDelete(name);
    // Wire contract is { collection }, NOT { name }.
    bridge.notify("tableAdmin.deleteRequested", { collection: name });
  }

  function openAdmin(): void {
    bridge.notify("admin.openRequested", {});
  }

  return { init, createTable, deleteTable, openAdmin };
}
