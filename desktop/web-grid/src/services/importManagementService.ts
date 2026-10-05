import { computed, ref } from "vue";
import { BridgeOperationError } from "@/bridge/hostBridge";
import { useHostBridge } from "./bridgeContext";
import {
  SOURCE_IMPORT_CONTRACT,
  type ImportHistoryEntry,
  type ImportHistoryResult,
  type SourceImportDiagnostic,
  type SourceImportEntry,
  type SourceImportReadWindow,
  type SourceImportState,
} from "@/contracts/importManagement";
import { t } from "@/i18n";

/**
 * Session identity captured before each history read. A late reply that no
 * longer matches the live scope is dropped instead of polluting the workspace
 * that is now open.
 */
export interface ImportManagementScope {
  readonly workspaceId: string | null;
  readonly sessionEpoch: number | string | null;
}

function scopeMatches(captured: ImportManagementScope, current: ImportManagementScope): boolean {
  return captured.workspaceId === current.workspaceId
    && captured.sessionEpoch === current.sessionEpoch;
}

const HISTORY_STATES: readonly ImportHistoryEntry["state"][] = [
  "queued", "running", "interrupted", "succeeded", "failed", "cancelled", "aborted",
];

const SOURCE_STATES: readonly SourceImportState[] = [
  "queued", "running", "aborted", "interrupted", "succeeded", "failed", "cancelled", "unknown",
];

const SOURCE_POLICIES = ["native", "snapshot", "skip"] as const;

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function isCount(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

/** Small structural guard: only the invariants the UI depends on, no JSON-schema framework. */
function validateEntry(entry: unknown, index: number): ImportHistoryEntry {
  if (typeof entry !== "object" || entry === null) throw new Error(`entry ${index}`);
  const candidate = entry as Readonly<Record<string, unknown>>;
  if (!isNonEmptyString(candidate.taskId)
    || !isNonEmptyString(candidate.collection)
    || !isNonEmptyString(candidate.sourceName)
    || !isNonEmptyString(candidate.startedAt)
    || !isCount(candidate.sessionEpoch) || candidate.sessionEpoch === 0
    || (candidate.sourceType !== "csv" && candidate.sourceType !== "xlsx")
    || !HISTORY_STATES.includes(candidate.state as ImportHistoryEntry["state"])
    || (candidate.commitState !== "unknown" && candidate.commitState !== "committed")
    || (candidate.createdCount !== null && !isCount(candidate.createdCount))
    || (candidate.updatedCount !== null && !isCount(candidate.updatedCount))
    || (candidate.finishedAt !== null && !isNonEmptyString(candidate.finishedAt))
    || (candidate.errorCode !== null && !isNonEmptyString(candidate.errorCode))) {
    throw new Error(`entry ${index}`);
  }
  if (candidate.commitState === "committed"
    ? candidate.state !== "succeeded" || candidate.createdCount === null
      || candidate.updatedCount === null || candidate.finishedAt === null
    : candidate.state === "succeeded" || candidate.createdCount !== null
      || candidate.updatedCount !== null) {
    throw new Error(`entry ${index}`);
  }
  return entry as ImportHistoryEntry;
}

/**
 * Structural guard for durable source-migration receipts. Only the
 * invariants the UI depends on: identity, contract, the strict count split
 * (created + notSubmitted + unknownRecords === total) and terminal honesty —
 * a "succeeded" receipt must be terminal with nothing unsubmitted/unknown.
 * Raw field provenance (definitions/identities) is reduced to policy counts.
 */
function validateSourceEntry(candidate: unknown, index: number): SourceImportEntry {
  if (typeof candidate !== "object" || candidate === null) throw new Error(`migration ${index}`);
  const raw = candidate as Readonly<Record<string, unknown>>;
  const targets = validateArray(raw.targets, `migration ${index} targets`)
    .map((target, targetIndex) => {
      if (typeof target !== "object" || target === null) throw new Error(`migration ${index} target ${targetIndex}`);
      const item = target as Readonly<Record<string, unknown>>;
      const collection = item.collection;
      // A present-but-empty collection is a broken target projection; the
      // frontend must not fall back to guessing a physical name.
      if ((collection !== undefined && collection !== null)
        && (typeof collection !== "string" || collection.length === 0)) {
        throw new Error(`migration ${index} target ${targetIndex} collection`);
      }
      if (!isNonEmptyString(item.sourceTableId)
        || !isNonEmptyString(item.tableId)
        || !isNonEmptyString(item.name)) {
        throw new Error(`migration ${index} target ${targetIndex}`);
      }
      return {
        sourceTableId: item.sourceTableId as string,
        tableId: item.tableId as string,
        name: item.name as string,
        ...(collection === undefined || collection === null ? {} : { collection: collection as string }),
      };
    });
  const batches = validateArray(raw.batches, `migration ${index} batches`)
    .map((batch, batchIndex) => {
      if (typeof batch !== "object" || batch === null) throw new Error(`migration ${index} batch ${batchIndex}`);
      const item = batch as Readonly<Record<string, unknown>>;
      if (!isNonEmptyString(item.jobId) || !isNonEmptyString(item.batchId)
        || !isNonEmptyString(item.stage) || !isNonEmptyString(item.tableId)
        || !isCount(item.created) || !isCount(item.relationWrites) || !isCount(item.attachmentWrites)) {
        throw new Error(`migration ${index} batch ${batchIndex}`);
      }
      return item as unknown as {
        jobId: string; batchId: string; stage: string; tableId: string;
        created: number; relationWrites: number; attachmentWrites: number;
      };
    });
  const diagnostics = validateArray(raw.diagnostics, `migration ${index} diagnostics`)
    .map((diagnostic, diagnosticIndex) => {
      if (typeof diagnostic !== "object" || diagnostic === null
        || !isNonEmptyString((diagnostic as Record<string, unknown>).code)) {
        throw new Error(`migration ${index} diagnostic ${diagnosticIndex}`);
      }
      return {
        code: (diagnostic as Record<string, unknown>).code as string,
        blocking: (diagnostic as Record<string, unknown>).blocking === true,
      } satisfies SourceImportDiagnostic;
    });
  let snapshotFieldCount = 0;
  let skippedFieldCount = 0;
  // Field provenance carries source definitions/identities; only the policy
  // distribution is projected so the UI can disclose degradation honestly.
  for (const [fieldIndex, field] of validateArray(raw.fields, `migration ${index} fields`).entries()) {
    if (typeof field !== "object" || field === null) throw new Error(`migration ${index} field ${fieldIndex}`);
    const policy = (field as Record<string, unknown>).policy;
    if (policy === "snapshot") snapshotFieldCount += 1;
    else if (policy === "skip") skippedFieldCount += 1;
    else if (!SOURCE_POLICIES.includes(policy as (typeof SOURCE_POLICIES)[number])) {
      throw new Error(`migration ${index} field ${fieldIndex} policy`);
    }
  }
  let readWindow: SourceImportReadWindow | null = null;
  if (raw.readWindow !== null && raw.readWindow !== undefined) {
    if (typeof raw.readWindow !== "object"
      || !isNonEmptyString((raw.readWindow as Record<string, unknown>).startedAt)
      || !isNonEmptyString((raw.readWindow as Record<string, unknown>).finishedAt)
      || !isNonEmptyString((raw.readWindow as Record<string, unknown>).consistency)) {
      throw new Error(`migration ${index} readWindow`);
    }
    const consistency = (raw.readWindow as Record<string, unknown>).consistency;
    if (consistency !== "snapshot" && consistency !== "window") {
      throw new Error(`migration ${index} readWindow consistency`);
    }
    readWindow = {
      startedAt: (raw.readWindow as Record<string, unknown>).startedAt as string,
      finishedAt: (raw.readWindow as Record<string, unknown>).finishedAt as string,
      consistency,
    };
  }
  const state = raw.state as SourceImportState;
  if (raw.contract !== SOURCE_IMPORT_CONTRACT
    || !isNonEmptyString(raw.jobId) || !isNonEmptyString(raw.provider)
    || !isNonEmptyString(raw.containerId) || !isNonEmptyString(raw.sourceName)
    || !isNonEmptyString(raw.stage) || !isNonEmptyString(raw.startedAt)
    || !isCount(raw.sessionEpoch) || raw.sessionEpoch === 0
    || !SOURCE_STATES.includes(state)
    || !isCount(raw.created) || !isCount(raw.total)
    || !isCount(raw.notSubmitted) || !isCount(raw.unknownRecords)
    || (raw.unknownBatch !== null && raw.unknownBatch !== undefined && !isNonEmptyString(raw.unknownBatch))
    || (raw.finishedAt !== null && raw.finishedAt !== undefined && !isNonEmptyString(raw.finishedAt))) {
    throw new Error(`migration ${index}`);
  }
  const unknownBatch = raw.unknownBatch === undefined ? null : raw.unknownBatch as string | null;
  const finishedAt = raw.finishedAt === undefined ? null : raw.finishedAt as string | null;
  // Strict committed/unsubmitted/pending split; nothing may hide in the sum.
  if (raw.created + raw.notSubmitted + raw.unknownRecords !== raw.total) {
    throw new Error(`migration ${index} counts`);
  }
  // A succeeded receipt must be terminal with no unsubmitted or unknown work.
  if (state === "succeeded"
    && (finishedAt === null || raw.notSubmitted !== 0 || raw.unknownRecords !== 0 || unknownBatch !== null)) {
    throw new Error(`migration ${index} succeeded`);
  }
  // Success means every required stage ran: constraints, relations and
  // attachments included. Full counts on an unsettled stage never pass.
  if (state === "succeeded" && raw.stage !== "settled") {
    throw new Error(`migration ${index} succeeded stage`);
  }
  // Unknown rows must be attributable to a concrete batch for review.
  if (raw.unknownRecords > 0 && unknownBatch === null) {
    throw new Error(`migration ${index} unknown batch`);
  }
  return {
    jobId: raw.jobId as string,
    provider: raw.provider as string,
    containerId: raw.containerId as string,
    sourceName: raw.sourceName as string,
    state,
    stage: raw.stage as string,
    created: raw.created as number,
    total: raw.total as number,
    notSubmitted: raw.notSubmitted as number,
    unknownRecords: raw.unknownRecords as number,
    unknownBatch,
    targets,
    batches,
    diagnostics,
    startedAt: raw.startedAt as string,
    finishedAt,
    sessionEpoch: raw.sessionEpoch as number,
    readWindow,
    snapshotFieldCount,
    skippedFieldCount,
  };
}

function validateArray(value: unknown, label: string): readonly unknown[] {
  if (!Array.isArray(value)) throw new Error(label);
  return value;
}

/**
 * Fixed localized load-failure text. Raw cause messages may embed transport
 * details such as token URLs; only a stable locating code is surfaced.
 */
function safeLoadError(cause: unknown): string {
  const code = cause instanceof BridgeOperationError && cause.code
    ? cause.code
    : "IMPORT_HISTORY_UNAVAILABLE";
  return t("importManagement.history.loadFailed", { code });
}

/**
 * Read-side import-management service. It owns ONLY the projected history from
 * the read-only `data.importHistory` RPC (Host -> Go; no Python involved) plus
 * task cancellation through the existing `task.cancel` bridge method. Live
 * execution stays with dataIoService/Host — this module never builds a second
 * queue and never persists credentials or runtime state in localStorage.
 */
export function useImportManagementService() {
  const bridge = useHostBridge();
  const items = ref<readonly ImportHistoryEntry[] | null>(null);
  const sourceEntries = ref<readonly SourceImportEntry[] | null>(null);
  const loading = ref(false);
  const error = ref<string | null>(null);
  const loaded = ref(false);
  /** Queued/running entries exist only for the current session (Host contract). */
  const activeEntries = computed(() =>
    (items.value ?? []).filter((entry) => entry.state === "queued" || entry.state === "running"));
  /** Current-session source-migration jobs (Host overlay of the live job). */
  const activeSourceEntries = computed(() =>
    (sourceEntries.value ?? []).filter((entry) => entry.state === "queued" || entry.state === "running"));
  let requestGeneration = 0;
  let resolveScope: () => ImportManagementScope = () => ({
    workspaceId: null,
    sessionEpoch: null,
  });

  /** WorkspaceView binds the live workspace/session identity after creation. */
  function bindScope(resolver: () => ImportManagementScope): void {
    resolveScope = resolver;
  }

  function validateHistory(result: unknown): {
    items: readonly ImportHistoryEntry[];
    migrations: readonly SourceImportEntry[];
  } {
    if (typeof result !== "object" || result === null || !Array.isArray((result as ImportHistoryResult).items)) {
      throw new Error("items");
    }
    const candidate = result as ImportHistoryResult;
    // Legacy hosts predate the source-migration extension: an absent field is
    // an empty migration history. A PRESENT-but-invalid field is a broken
    // payload and must fail the load instead of rendering empty history.
    const migrations = candidate.migrations === undefined
      ? []
      : validateArray(candidate.migrations, "migrations").map(validateSourceEntry);
    return { items: candidate.items.map(validateEntry), migrations };
  }

  async function refresh(): Promise<void> {
    const captured = resolveScope();
    const generation = ++requestGeneration;
    loading.value = true;
    try {
      const result = await bridge.request("data.importHistory", {});
      // A reply from a retired workspace/session must never render as the
      // current workspace's history.
      if (generation !== requestGeneration || !scopeMatches(captured, resolveScope())) return;
      const history = validateHistory(result);
      items.value = history.items;
      sourceEntries.value = history.migrations;
      error.value = null;
      loaded.value = true;
    } catch (cause) {
      if (generation !== requestGeneration || !scopeMatches(captured, resolveScope())) return;
      // A failed load/refresh is surfaced as a fixed localized error with a
      // locating code; it must never echo raw transport text (token URLs) nor
      // render as a successful (empty) history.
      error.value = safeLoadError(cause);
    } finally {
      if (generation === requestGeneration) loading.value = false;
    }
  }

  /** Drop all projections when the workspace session is retired/switched. */
  function retire(): void {
    requestGeneration += 1;
    items.value = null;
    sourceEntries.value = null;
    error.value = null;
    loading.value = false;
    loaded.value = false;
  }

  /** Cancels one projected task through the existing bridge cancellation RPC. */
  async function cancelTask(taskId: string): Promise<void> {
    await bridge.request("task.cancel", { taskId });
  }

  return {
    items,
    sourceEntries,
    loading,
    error,
    loaded,
    activeEntries,
    activeSourceEntries,
    bindScope,
    refresh,
    retire,
    cancelTask,
  };
}
