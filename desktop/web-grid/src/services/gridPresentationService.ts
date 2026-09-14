import type { GridState, GridStateResult } from "@/contracts";
import { parseGridStateJson } from "@/contracts/gridStateJson";
import { useHostBridge } from "./bridgeContext";

export interface GridPresentationService {
  read(table: string): Promise<GridStateResult>;
  save(table: string, state: GridState, revision: string): Promise<GridStateResult>;
}

/** Host owns device-local presentation; these requests never enter Product RPC. */
export function useGridPresentationService(): GridPresentationService {
  const bridge = useHostBridge();
  return {
    async read(table) {
      return await bridge.request("gridState.get", { table });
    },
    async save(table, state, revision) {
      return await bridge.request("gridState.save", { table, state, revision });
    },
  };
}

/** Serial CAS saves coalesce only unsent snapshots; a conflict never overwrites the winner. */
export function createGridPresentationPersistence(
  service: GridPresentationService,
  identity: () => string | null,
  reportError: (error: Error) => void,
) {
  interface TableQueue {
    readonly table: string;
    readonly owner: string;
    revision: string | null;
    pending: GridState | null;
    running: Promise<void> | null;
    blocked: boolean;
    lastSnapshot: string;
  }
  const queues = new Map<string, TableQueue>();
  let selection = 0;
  let active: TableQueue | null = null;

  // A table selection retires only the UI. Its captured writes stay valid until
  // the workspace epoch changes, including when navigation uses a synchronous port.
  function retire(): void {
    selection++;
    active = null;
    for (const [table, queue] of queues) {
      if (queue.owner !== identity()) queues.delete(table);
    }
  }
  function current(queue: TableQueue): boolean {
    return queue.owner === identity() && queues.get(queue.table) === queue;
  }
  async function open(collection: string): Promise<GridState | null> {
    retire();
    const ticket = selection;
    const owner = identity();
    if (!owner) return null;
    const queue = queues.get(collection) ?? {
      table: collection, owner, revision: null, pending: null, running: null,
      blocked: false, lastSnapshot: "",
    };
    queues.set(collection, queue);
    try {
      // Reopening cannot read the intermediate snapshot of a still-running save.
      await drain(queue);
      if (ticket !== selection || !current(queue)) return null;
      const result = await service.read(collection);
      if (ticket !== selection || !current(queue)) return null;
      queue.revision = result.revision;
      queue.pending = null;
      queue.blocked = false;
      queue.lastSnapshot = "";
      active = queue;
      return result.state;
    } catch (error) {
      if (ticket === selection && current(queue)) {
        queue.blocked = true;
        reportError(error instanceof Error ? error : new Error(String(error)));
      }
      return null;
    }
  }
  async function drain(queue: TableQueue): Promise<void> {
    if (queue.running) {
      await queue.running;
      if (queue.pending && !queue.blocked) await drain(queue);
      return;
    }
    if (!queue.pending || queue.revision === null || queue.blocked || !current(queue)) return;
    queue.running = (async () => {
      while (queue.pending && current(queue) && !queue.blocked && queue.revision !== null) {
        const state = queue.pending;
        queue.pending = null;
        try {
          const result = await service.save(queue.table, state, queue.revision);
          if (!current(queue)) return;
          if (result.conflict) {
            queue.blocked = true;
            queue.pending ??= state;
            if (active === queue) reportError(new Error("Grid layout changed in another window. Reload before saving again."));
            return;
          }
          queue.revision = result.revision;
        } catch (error) {
          if (current(queue)) {
            queue.blocked = true;
            queue.pending ??= state;
            if (active === queue) reportError(error instanceof Error ? error : new Error(String(error)));
          }
          return;
        }
      }
    })();
    try { await queue.running; } finally { queue.running = null; }
  }
  function save(state: GridState): void {
    const queue = active;
    if (!queue || queue.revision === null || !current(queue)) return;
    const snapshot = JSON.stringify(state);
    if (snapshot === queue.lastSnapshot) return;
    queue.lastSnapshot = snapshot;
    queue.pending = parseGridStateJson(snapshot) as GridState;
    void drain(queue);
  }
  return { open, save, flush: async () => { if (active) await drain(active); }, retire };
}
