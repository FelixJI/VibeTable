import { useHostBridge } from "./bridgeContext";
import type {
  ApplyImportResult,
  DataTaskStatus,
  ExportResult,
  ImportPlan,
  SessionPathGrant,
} from "@/contracts";
import { computed, ref } from "vue";

export interface ImportPreviewSession {
  readonly grant: SessionPathGrant;
  readonly plan: ImportPlan;
  readonly mode: "create_only";
}

export function useDataIoService() {
  const bridge = useHostBridge();
  const activeTaskId = ref<string | null>(null);
  const busy = computed(() => activeTaskId.value !== null);

  async function runTask(
    kind: "data.import" | "data.export",
    params: Readonly<Record<string, unknown>>,
  ): Promise<unknown> {
    if (activeTaskId.value) {
      throw new Error("A data task is already running.");
    }
    let status = await bridge.request("task.create", { kind, params }) as DataTaskStatus;
    activeTaskId.value = status.taskId;
    try {
      while (status.state === "queued" || status.state === "running") {
        await new Promise((resolve) => window.setTimeout(resolve, 100));
        status = await bridge.request("task.status", {
          taskId: status.taskId,
        }) as DataTaskStatus;
      }
      if (status.state !== "succeeded") {
        throw new Error(status.error ?? `Data task ended as ${status.state}.`);
      }
      if (kind === "data.import"
          && status.result
          && typeof status.result === "object"
          && Array.isArray((status.result as { failedRows?: unknown }).failedRows)
          && ((status.result as { failedRows: unknown[] }).failedRows.length > 0)) {
        throw new Error(
          status.progress?.message
          ?? `Import failed for ${(status.result as { failedRows: unknown[] }).failedRows.length} row(s).`,
        );
      }
      return status.result;
    } finally {
      activeTaskId.value = null;
    }
  }

  async function cancelActive(): Promise<void> {
    const taskId = activeTaskId.value;
    if (taskId) {
      await bridge.request("task.cancel", { taskId });
    }
  }

  async function previewImport(
    collection: string,
    schemaRevision: string,
  ): Promise<ImportPreviewSession> {
    const grant = await bridge.request("data.importSourceRequested", {
      accept: [".xlsx", ".xlsm", ".csv"],
    }) as SessionPathGrant;
    const plan = await bridge.request("data.previewImport", {
      grantId: grant.grantId,
      collection,
      schemaRevision,
      mode: "create_only",
      columnMapping: [],
    }) as ImportPlan;
    return { grant, plan, mode: "create_only" };
  }

  async function applyImport(
    session: ImportPreviewSession,
  ): Promise<ApplyImportResult> {
    return await runTask("data.import", {
      grantId: session.grant.grantId,
      collection: session.plan.collection,
      token: session.plan.token.token,
      mode: session.mode,
      idempotencyPrefix: crypto.randomUUID(),
    }) as ApplyImportResult;
  }

  async function exportData(
    collection: string,
    query: Readonly<Record<string, unknown>>,
    format: "csv" | "xlsx" = "csv",
  ): Promise<ExportResult> {
    const grant = await bridge.request("data.exportTargetRequested", {
      defaultName: `${collection}-export.${format}`,
      format,
    }) as SessionPathGrant;
    return await runTask("data.export", {
      grantId: grant.grantId,
      collection,
      query,
      format,
      includeRelations: true,
      lookupIds: [],
    }) as ExportResult;
  }

  return {
    activeTaskId,
    busy,
    previewImport,
    applyImport,
    exportData,
    cancelActive,
  };
}
