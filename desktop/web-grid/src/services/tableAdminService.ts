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
  deleteTable: (name: string) => Promise<void>;
  openAdmin: () => void;
} {
  const bridge = useHostBridge();
  const store = useTableAdminStore();
  const ui = useUiStore();
  let createdCallback: ((tableId: string) => void | Promise<void>) | undefined;
  let operationSequence = 0;

  function init(
    onTableCreated?: (tableId: string) => void | Promise<void>,
  ): void {
    createdCallback = onTableCreated;
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      const collections = toCollections(payload);
      store.setCollections(collections);
    });
    bridge.on("database.collectionsChanged", applyCollectionsChanged);
  }

  function applyCollectionsChanged(payload: CollectionsChangedPayload): void {
    const collections = toCollectionsFromChanged(payload);
    store.setCollections(collections);
  }

  async function createTable(): Promise<void> {
    if (!store.canSubmit) return;
    store.beginSubmit();
    const sequence = ++operationSequence;
    const displayName = store.form.name.trim();
    const existingIds = new Set(
      store.collections.map((item) => item.collection),
    );
    try {
      // Table lifecycle is host-owned. The renderer submits one closed intent
      // and never receives access to the generic schema.validate/apply RPCs.
      const changed = await bridge.request("tableAdmin.createRequested", {
        displayName,
      }) as CollectionsChangedPayload;
      if (sequence !== operationSequence || store.phase !== "submitting") return;
      applyCollectionsChanged(changed);
      const createdTableId = changed.createdTableId;
      if (!createdTableId
        || existingIds.has(createdTableId)
        || !changed.tables.includes(createdTableId)
        || changed.displayNames[createdTableId] !== displayName) {
        throw new Error("主机未返回新建数据表的权威标识。");
      }
      store.succeed();
      ui.closeCreate();
      void createdCallback?.(createdTableId);
    } catch (error) {
      if (sequence !== operationSequence || store.phase !== "submitting") return;
      const mapped = error as Error & { readonly path?: string };
      store.fail(mapped.message || "创建数据表失败。");
    }
  }

  async function deleteTable(name: string): Promise<void> {
    store.requestDelete(name);
    const sequence = ++operationSequence;
    try {
      // Wire contract is { collection }, NOT { name }.
      const changed = await bridge.request(
        "tableAdmin.deleteRequested",
        { collection: name },
      ) as CollectionsChangedPayload;
      if (sequence !== operationSequence || store.phase !== "deleting") return;
      applyCollectionsChanged(changed);
      if (changed.deletedTableId !== name || changed.tables.includes(name)) {
        throw new Error("主机返回的集合目录仍包含待删除数据表。");
      }
      store.succeed();
      ui.closeDelete();
    } catch (error) {
      if (sequence !== operationSequence || store.phase !== "deleting") return;
      const mapped = error as Error;
      store.fail(mapped.message || "删除数据表失败。");
    }
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
