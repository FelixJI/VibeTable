import { computed, ref } from "vue";
import { BridgeOperationError } from "@/bridge/hostBridge";
import { useHostBridge } from "./bridgeContext";
import type { ImportHistoryEntry, ImportHistoryResult } from "@/contracts/importManagement";
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
  const loading = ref(false);
  const error = ref<string | null>(null);
  const loaded = ref(false);
  /** Queued/running entries exist only for the current session (Host contract). */
  const activeEntries = computed(() =>
    (items.value ?? []).filter((entry) => entry.state === "queued" || entry.state === "running"));
  let requestGeneration = 0;
  let resolveScope: () => ImportManagementScope = () => ({
    workspaceId: null,
    sessionEpoch: null,
  });

  /** WorkspaceView binds the live workspace/session identity after creation. */
  function bindScope(resolver: () => ImportManagementScope): void {
    resolveScope = resolver;
  }

  function validateHistory(result: unknown): readonly ImportHistoryEntry[] {
    if (typeof result !== "object" || result === null || !Array.isArray((result as ImportHistoryResult).items)) {
      throw new Error("items");
    }
    return (result as ImportHistoryResult).items.map(validateEntry);
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
      items.value = validateHistory(result);
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
    loading,
    error,
    loaded,
    activeEntries,
    bindScope,
    refresh,
    retire,
    cancelTask,
  };
}
