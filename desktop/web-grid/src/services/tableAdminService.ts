import { useHostBridge } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { collectionsFromChanged } from "./collectionsProjection";
import type { CollectionsChangedPayload } from "@/contracts";

/**
 * tableAdminService wires `tableAdminStore` to the host bridge:
 *   - outbound `tableAdmin.createRequested` / `tableAdmin.deleteRequested` /
 *     `admin.openRequested` carry the user's create/delete/open-admin intent,
 *   - the correlated create/delete replies are projected into the single
 *     shared catalog in `workspaceStore` through the same pure mapping the
 *     subscription owner (`workspaceService`) uses (#509).
 *
 * This service deliberately does NOT subscribe to `database.opened` or
 * `database.collectionsChanged`: the workspace service owns those broadcasts,
 * and the admin store keeps only the form/submission state it needs.
 *
 * Table creation carries only a display-name intent. The host assigns the
 * opaque table identity and creates an empty base table; the unified Schema
 * v2 field-settings drawer owns every subsequent field definition.
 */
export function useTableAdminService(): {
  init: (onTableCreated?: (tableId: string) => void | Promise<void>) => void;
  dispose: () => void;
  createTable: () => Promise<void>;
  deleteTable: (name: string) => Promise<void>;
  openAdmin: () => void;
} {
  const bridge = useHostBridge();
  const store = useTableAdminStore();
  const workspace = useWorkspaceStore();
  const ui = useUiStore();
  const session = useWorkspaceSessionStore();
  let createdCallback: ((tableId: string) => void | Promise<void>) | undefined;
  let operationSequence = 0;
  let disposed = false;

  function init(
    onTableCreated?: (tableId: string) => void | Promise<void>,
  ): void {
    // A repeated init only refreshes the callback; there is no broadcast
    // subscription to duplicate (the workspace service owns the catalog).
    if (disposed) return;
    createdCallback = onTableCreated;
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    // Bumping operationSequence retires continuations already awaiting a
    // host reply, so a late response cannot settle the shared store, close
    // the modal, write the shared catalog, or fire the retired
    // createdCallback for a newer owner.
    operationSequence += 1;
    createdCallback = undefined;
  }

  /**
   * Project a correlated create/delete reply into the shared workspace
   * catalog. The reply carries a full authoritative snapshot, so response and
   * the following broadcast converge through the identical projection; the
   * store keeps its array identity when the content did not change.
   */
  function applyCatalog(changed: CollectionsChangedPayload): void {
    workspace.setCollections(
      collectionsFromChanged(changed),
      changed.displayNames,
    );
  }

  /**
   * A create/delete reply belongs to the workspace/session that submitted
   * it. After a switch the new session owns the admin flow, so a late
   * terminal only retires locally: the host may still have committed, and
   * we never claim a rollback or resend.
   */
  function ownsSession(): () => boolean {
    const workspaceId = session.activeWorkspaceId;
    const epoch = session.sessionEpoch;
    const transitioning = session.isTransitioning;
    return () => session.activeWorkspaceId === workspaceId
      && session.sessionEpoch === epoch
      && session.isTransitioning === transitioning;
  }

  async function createTable(): Promise<void> {
    if (disposed || !store.canSubmit) return;
    store.beginSubmit();
    const sequence = ++operationSequence;
    const sessionIsCurrent = ownsSession();
    const displayName = store.form.name.trim();
    // The single shared catalog is the only readable collection source; the
    // snapshot is taken synchronously at submit time.
    const existingIds = new Set(
      workspace.collections.map((item) => item.collection),
    );
    try {
      // Table lifecycle is host-owned. The renderer submits one closed intent
      // and never receives access to the generic schema.validate/apply RPCs.
      const changed = await bridge.request("tableAdmin.createRequested", {
        displayName,
      }) as CollectionsChangedPayload;
      if (sequence !== operationSequence || store.phase !== "submitting" || !sessionIsCurrent()) return;
      applyCatalog(changed);
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
      if (sequence !== operationSequence || store.phase !== "submitting" || !sessionIsCurrent()) return;
      const mapped = error as Error & { readonly path?: string };
      store.fail(mapped.message || "创建数据表失败。");
    }
  }

  async function deleteTable(name: string): Promise<void> {
    if (disposed) return;
    store.requestDelete(name);
    const sequence = ++operationSequence;
    const sessionIsCurrent = ownsSession();
    try {
      // Wire contract is { collection }, NOT { name }.
      const changed = await bridge.request(
        "tableAdmin.deleteRequested",
        { collection: name },
      ) as CollectionsChangedPayload;
      if (sequence !== operationSequence || store.phase !== "deleting" || !sessionIsCurrent()) return;
      applyCatalog(changed);
      if (changed.deletedTableId !== name || changed.tables.includes(name)) {
        throw new Error("主机返回的集合目录仍包含待删除数据表。");
      }
      store.succeed();
      ui.closeDelete();
    } catch (error) {
      if (sequence !== operationSequence || store.phase !== "deleting" || !sessionIsCurrent()) return;
      const mapped = error as Error;
      store.fail(mapped.message || "删除数据表失败。");
    }
  }

  function openAdmin(): void {
    if (disposed) return;
    bridge.notify("admin.openRequested", {
      floatingButtonEnabled: ui.adminFloatingButton,
      confirmClose: ui.adminConfirmClose,
      releaseWhenIdle: ui.adminReleaseWhenIdle,
    });
  }

  return { init, dispose, createTable, deleteTable, openAdmin };
}
