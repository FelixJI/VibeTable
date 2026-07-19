import { useHostBridge } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
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
  const labels = payload.displayNames ?? {};
  const tables = payload.tables.map((t) => ({
    collection: t,
    displayName: labels[t],
    metadata: {},
  }));
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
  const labels = payload.displayNames ?? {};
  return payload.tables.map((t) => ({
    collection: t,
    displayName: labels[t],
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
  const ui = useUiStore();

  /**
   * Resolve an in-flight create/delete when the host signals the collection
   * list changed (or a fresh `database.opened` arrived). The host emits
   * `database.collectionsChanged` for ANY collection change — including the
   * initial load — so we ONLY transition + close the modal when `phase` is
   * actually `submitting`/`deleting` (meaning the user kicked off an op and is
   * waiting on its round-trip). This restores the auto-close-on-success
   * behavior that the pre-rewrite `main.ts` had (the rewrite dropped it; see
   * issue I3 in the final review).
   */
  function resolveIfPending(): void {
    if (store.phase === "submitting") {
      store.succeed();
      ui.closeCreate();
    } else if (store.phase === "deleting") {
      store.succeed();
      ui.closeDelete();
    }
  }

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setCollections(toCollections(payload));
      // A `database.opened` after a create/delete also implies success: the
      // host re-announces the full collection list once the new schema lands.
      resolveIfPending();
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(toCollectionsFromChanged(payload));
      resolveIfPending();
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
    bridge.notify("admin.openRequested", {
      floatingButtonEnabled: ui.adminFloatingButton,
      confirmClose: ui.adminConfirmClose,
      releaseWhenIdle: ui.adminReleaseWhenIdle,
    });
  }

  return { init, createTable, deleteTable, openAdmin };
}
