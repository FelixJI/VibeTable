import type { HostBridge } from "@/bridge/hostBridge";
import type {
  DataChangedEvent,
  FormulaTaskState,
  FormulaTaskTerminalEvent,
  ProductContractError,
  RealtimeRecoverySnapshot,
  TaskChangedEvent,
} from "@/contracts";

export type ReconcileAction = "none" | "refresh-data" | "reload-schema";

export const MAX_RECOVERY_ACTIVE_FORMULA_TASKS = 10_000;
export const MAX_RECOVERY_FRAME_BYTES = 4 * 1024 * 1024;

// The smallest valid TaskChangedEvent is over 128 bytes, so this bounded window
// covers every event ID that can fit in a 4 MiB recovery frame.
const MAX_REMEMBERED_TASK_EVENT_IDS = 32_768;

export interface RealtimeTaskRecoveryDelivery {
  readonly activeFormulaTasks: readonly FormulaTaskState[];
  readonly terminalNotifications: readonly FormulaTaskTerminalEvent[];
}

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
  private lifecycle = 0;

  constructor(
    private readonly port: RealtimeReconcilePort,
    public readonly actions: RealtimeActions,
  ) {}

  reset(): void {
    this.lifecycle += 1;
    this.generation += 1;
    this.seen.clear();
    this.inFlight.clear();
  }

  async handle(
    event: DataChangedEvent,
    schemaRevision: string,
    dataRevision: string,
  ): Promise<void> {
    if (this.seen.has(event.eventId)) return;
    const current = this.inFlight.get(event.eventId);
    if (current) return await current;

    const lifecycle = this.lifecycle;
    const generation = ++this.generation;
    const work = (async () => {
      try {
        const result = await this.port.reconcile({
          tableId: event.tableId,
          schemaRevision,
          dataRevision,
        });
        if (lifecycle !== this.lifecycle) return;
        this.remember(event.eventId);
        if (generation !== this.generation) return;
        if (result.action === "reload-schema") this.actions.reloadSchema();
        else if (result.action === "refresh-data") this.actions.refreshData();
      } catch (error) {
        if (lifecycle === this.lifecycle) throw error;
      }
    })();
    this.inFlight.set(event.eventId, work);
    try {
      await work;
    } finally {
      if (this.inFlight.get(event.eventId) === work) this.inFlight.delete(event.eventId);
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
    readonly taskType: TaskChangedEvent["taskType"];
    readonly sequence: number;
    readonly occurredAt: string;
  }>();

  reset(): void {
    this.seen.clear();
    this.latestByTask.clear();
  }

  accept(event: TaskChangedEvent): boolean {
    const previous = this.latestByTask.get(event.taskId);
    if (this.seen.has(event.eventId)) return false;
    if (
      previous
      && event.sequence <= previous.sequence
      && event.occurredAt <= previous.occurredAt
    ) return false;
    this.remember(event.eventId);
    this.latestByTask.set(event.taskId, {
      taskType: event.taskType,
      sequence: event.sequence,
      occurredAt: event.occurredAt,
    });
    return true;
  }

  /**
   * Validates a whole recovery frame before returning its distinct current and
   * historical projections. Callers replace the former atomically and deliver
   * the latter as notifications; neither is synthesized as a task.changed event.
   */
  acceptRecovery(frame: unknown): RealtimeTaskRecoveryDelivery {
    const snapshot = parseRecoverySnapshot(frame);
    for (const [taskId, previous] of this.latestByTask) {
      if (previous.taskType === "formulaBackfill") this.latestByTask.delete(taskId);
    }
    const terminalNotifications = snapshot.terminalNotifications.filter(
      (event) => !this.seen.has(event.eventId),
    );
    for (const event of terminalNotifications) this.remember(event.eventId);
    return {
      activeFormulaTasks: snapshot.activeFormulaTasks,
      terminalNotifications,
    };
  }

  private remember(eventId: string): void {
    this.seen.add(eventId);
    if (this.seen.size <= MAX_REMEMBERED_TASK_EVENT_IDS) return;
    const oldest = this.seen.values().next().value as string | undefined;
    if (oldest) this.seen.delete(oldest);
  }
}

function parseRecoverySnapshot(value: unknown): RealtimeRecoverySnapshot {
  assertRecoveryFrameSize(value);
  if (!isRecord(value) || !hasExactKeys(value, [
    "contractVersion", "topic", "activeFormulaTasks", "terminalNotifications",
  ])) throw new TypeError("invalid realtime recovery frame");
  if (value.contractVersion !== "2.0" || value.topic !== "realtime.recovered") {
    throw new TypeError("invalid realtime recovery frame identity");
  }
  if (!Array.isArray(value.activeFormulaTasks) || !Array.isArray(value.terminalNotifications)) {
    throw new TypeError("invalid realtime recovery frame collections");
  }
  if (value.activeFormulaTasks.length > MAX_RECOVERY_ACTIVE_FORMULA_TASKS) {
    throw new RangeError("realtime recovery active task limit exceeded");
  }

  const taskIds = new Set<string>();
  const activeFormulaTasks = value.activeFormulaTasks.map((task) => {
    const parsed = parseFormulaTaskState(task);
    if (taskIds.has(parsed.taskId)) throw new TypeError("duplicate recovery task ID");
    taskIds.add(parsed.taskId);
    return parsed;
  });
  const eventIds = new Set<string>();
  const terminalNotifications = value.terminalNotifications.map((event) => {
    const parsed = parseTerminalTaskEvent(event);
    if (eventIds.has(parsed.eventId)) throw new TypeError("duplicate recovery event ID");
    eventIds.add(parsed.eventId);
    return parsed;
  });
  return {
    contractVersion: "2.0",
    topic: "realtime.recovered",
    activeFormulaTasks,
    terminalNotifications,
  };
}

function parseFormulaTaskState(value: unknown): FormulaTaskState {
  if (!isRecord(value) || !hasExactKeys(value, ["taskId", "state", "progress", "cursor", "error"])) {
    throw new TypeError("invalid recovered formula task");
  }
  if (
    !isNonEmptyString(value.taskId)
    || (value.state !== "pending" && value.state !== "running")
    || !isProgress(value.progress)
    || !isNullableString(value.cursor)
  ) throw new TypeError("invalid recovered formula task");
  return {
    taskId: value.taskId,
    state: value.state,
    progress: value.progress,
    cursor: value.cursor,
    error: parseTaskError(value.error),
  };
}

function parseTerminalTaskEvent(value: unknown): FormulaTaskTerminalEvent {
  if (!isRecord(value) || !hasExactKeys(value, [
    "contractVersion", "topic", "eventId", "sequence", "occurredAt", "taskId", "taskType",
    "state", "progress", "cursor", "error",
  ])) throw new TypeError("invalid recovered terminal task event");
  if (
    value.contractVersion !== "2.0"
    || value.topic !== "task.changed"
    || !isNonEmptyString(value.eventId)
    || !isPositiveInteger(value.sequence)
    || !isDateTime(value.occurredAt)
    || !isNonEmptyString(value.taskId)
    || value.taskType !== "formulaBackfill"
    || !isTerminalTaskState(value.state)
    || !isProgress(value.progress)
    || !isNullableString(value.cursor)
  ) throw new TypeError("invalid recovered terminal task event");
  return {
    contractVersion: "2.0",
    topic: "task.changed",
    eventId: value.eventId,
    sequence: value.sequence,
    occurredAt: value.occurredAt,
    taskId: value.taskId,
    taskType: "formulaBackfill",
    state: value.state,
    progress: value.progress,
    cursor: value.cursor,
    error: parseTaskError(value.error),
  };
}

function parseTaskError(value: unknown): ProductContractError | null {
  if (value === null) return null;
  if (!isRecord(value) || !hasExactKeys(value, [
    "contractVersion", "code", "path", "message", "details", "retryable",
  ])) throw new TypeError("invalid product task error");
  if (
    value.contractVersion !== "2.0"
    || !isProductErrorCode(value.code)
    || !isNullableString(value.path)
    || !isNonEmptyString(value.message)
    || !isRecord(value.details)
    || typeof value.retryable !== "boolean"
    || !isJsonValue(value.details)
  ) throw new TypeError("invalid product task error");
  return {
    contractVersion: "2.0",
    code: value.code,
    path: value.path,
    message: value.message,
    details: value.details,
    retryable: value.retryable,
  };
}

function assertRecoveryFrameSize(value: unknown): void {
  let encoded: string;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new TypeError("realtime recovery frame is not JSON");
  }
  if (typeof encoded !== "string" || new TextEncoder().encode(encoded).byteLength > MAX_RECOVERY_FRAME_BYTES) {
    throw new RangeError("realtime recovery frame exceeds the wire budget");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function hasExactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const actual = Object.keys(value);
  return actual.length === keys.length && actual.every((key) => keys.includes(key));
}

function isJsonValue(value: unknown): boolean {
  if (value === null || typeof value === "string" || typeof value === "boolean") return true;
  if (typeof value === "number") return Number.isFinite(value);
  if (Array.isArray(value)) return value.every(isJsonValue);
  return isRecord(value) && Object.values(value).every(isJsonValue);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function isNullableString(value: unknown): value is string | null {
  return value === null || typeof value === "string";
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 1;
}

function isProgress(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 && value <= 1;
}

function isTerminalTaskState(value: unknown): value is FormulaTaskTerminalEvent["state"] {
  return value === "succeeded" || value === "failed" || value === "cancelled";
}

function isDateTime(value: unknown): value is string {
  if (typeof value !== "string"
    || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)
    || Number.isNaN(Date.parse(value))) return false;
  // Date.parse normalizes impossible month days and 24:00 instead of rejecting them.
  const localDate = value.slice(0, 10);
  return new Date(`${localDate}T00:00:00Z`).toISOString().slice(0, 10) === localDate
    && Number(value.slice(11, 13)) < 24;
}

function isProductErrorCode(value: unknown): value is string {
  return typeof value === "string" && /^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$/.test(value);
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
