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
 * Table creation carries only a display-name intent. The host assigns the
 * opaque table identity and creates an empty base table; the unified Schema
 * v2 field-settings drawer owns every subsequent field definition.
 */
export function useTableAdminService(): {
  init: (onTableCreated?: (tableId: string) => void | Promise<void>) => void;
  createTable: () => Promise<void>;
  deleteTable: (name: string) => void;
  openAdmin: () => void;
} {
  const bridge = useHostBridge();
  const store = useTableAdminStore();
  const ui = useUiStore();
  let createdCallback: ((tableId: string) => void | Promise<void>) | undefined;
  let pendingDisplayName: string | null = null;
  let pendingExistingIds: ReadonlySet<string> | null = null;

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
  function resolveIfPending(
    collections: readonly CollectionSummary[],
    previousIds: ReadonlySet<string>,
    displayNames: Readonly<Record<string, string>>,
  ): void {
    if (store.phase === "submitting") {
      const baseline = pendingExistingIds ?? previousIds;
      const additions = collections.filter(
        (item) => !baseline.has(item.collection),
      );
      const created = additions.find(
        (item) => displayNames[item.collection] === pendingDisplayName,
      );
      if (!created) return;
      store.succeed();
      ui.closeCreate();
      pendingDisplayName = null;
      pendingExistingIds = null;
      void createdCallback?.(created.collection);
    } else if (store.phase === "deleting") {
      store.succeed();
      ui.closeDelete();
    }
  }

  function init(
    onTableCreated?: (tableId: string) => void | Promise<void>,
  ): void {
    createdCallback = onTableCreated;
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      const previousIds = new Set(store.collections.map((item) => item.collection));
      const collections = toCollections(payload);
      store.setCollections(collections);
      // A `database.opened` after a create/delete also implies success: the
      // host re-announces the full collection list once the new schema lands.
      resolveIfPending(collections, previousIds, payload.displayNames);
    });
    bridge.on("database.collectionsChanged", (payload) => {
      const previousIds = new Set(store.collections.map((item) => item.collection));
      const collections = toCollectionsFromChanged(payload);
      store.setCollections(collections);
      resolveIfPending(collections, previousIds, payload.displayNames);
    });
  }

  async function createTable(): Promise<void> {
    if (!store.canSubmit) return;
    store.beginSubmit();
    pendingDisplayName = store.form.name.trim();
    pendingExistingIds = new Set(
      store.collections.map((item) => item.collection),
    );
    try {
      // Table lifecycle is host-owned. The renderer submits one closed intent
      // and never receives access to the generic schema.validate/apply RPCs.
      bridge.notify("tableAdmin.createRequested", {
        displayName: pendingDisplayName,
      });
    } catch (error) {
      pendingExistingIds = null;
      const mapped = error as Error & { readonly path?: string };
      store.fail(mapped.message || "创建数据表失败。");
    }
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
