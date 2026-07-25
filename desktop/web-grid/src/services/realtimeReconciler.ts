import type { HostBridge } from "@/bridge/hostBridge";
import type { DataChangedEvent, TaskChangedEvent } from "@/contracts";

export type ReconcileAction = "none" | "refresh-data" | "reload-schema";

export interface RealtimeReconcilePort {
  reconcile(request: {
    readonly tableId: string;
    readonly schemaRevision: string;
    readonly dataRevision: string;
  }): Promise<{ readonly action: ReconcileAction }>;
}

export interface RealtimeActions {
  readonly refreshData: () => void;
  readonly reloadSchema: () => void;
}

/** Deduplicates SSE envelopes and lets the authoritative revision seam decide refresh scope. */
export class RealtimeReconciler {
  private readonly seen = new Set<string>();
  private readonly inFlight = new Map<string, Promise<void>>();
  private generation = 0;

  constructor(
    private readonly port: RealtimeReconcilePort,
    public readonly actions: RealtimeActions,
  ) {}

  async handle(
    event: DataChangedEvent,
    schemaRevision: string,
    dataRevision: string,
  ): Promise<void> {
    if (this.seen.has(event.eventId)) return;
    const current = this.inFlight.get(event.eventId);
    if (current) return await current;

    const generation = ++this.generation;
    const work = (async () => {
      const result = await this.port.reconcile({
        tableId: event.tableId,
        schemaRevision,
        dataRevision,
      });
      this.remember(event.eventId);
      if (generation !== this.generation) return;
      if (result.action === "reload-schema") this.actions.reloadSchema();
      else if (result.action === "refresh-data") this.actions.refreshData();
    })();
    this.inFlight.set(event.eventId, work);
    try {
      await work;
    } finally {
      this.inFlight.delete(event.eventId);
    }
  }

  private remember(eventId: string): void {
    this.seen.add(eventId);
    if (this.seen.size > 2048) {
      const oldest = this.seen.values().next().value as string | undefined;
      if (oldest) this.seen.delete(oldest);
    }
  }
}

/**
 * Product task notifications are complete snapshots. Event IDs provide strict
 * deduplication; sequence plus occurredAt rejects stale delivery while still
 * allowing a sidecar process restart to reset its in-memory sequence counter.
 */
export class RealtimeTaskTracker {
  private readonly seen = new Set<string>();
  private readonly latestByTask = new Map<string, {
    readonly sequence: number;
    readonly occurredAt: string;
  }>();

  accept(event: TaskChangedEvent): boolean {
    const previous = this.latestByTask.get(event.taskId);
    if (this.seen.has(event.eventId)) return false;
    if (
      previous
      && event.sequence <= previous.sequence
      && event.occurredAt <= previous.occurredAt
    ) return false;
    this.seen.add(event.eventId);
    this.latestByTask.set(event.taskId, {
      sequence: event.sequence,
      occurredAt: event.occurredAt,
    });
    if (this.seen.size > 2048) {
      const oldest = this.seen.values().next().value as string | undefined;
      if (oldest) this.seen.delete(oldest);
    }
    return true;
  }
}

export function createBridgeRealtimeReconcilePort(
  bridge: Pick<HostBridge, "request">,
): RealtimeReconcilePort {
  return {
    async reconcile(request) {
      return await bridge.request("events.reconcile", request) as {
        readonly action: ReconcileAction;
      };
    },
  };
}
