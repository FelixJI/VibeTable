import { useHostBridge } from "./bridgeContext";
import type {
  ApplyImportResult,
  DataTaskStatus,
  ExportResult,
  ImportPlan,
  SessionPathGrant,
} from "@/contracts";
import { computed, ref } from "vue";

export function useDataIoService() {
  const bridge = useHostBridge();
  const activeTaskId = ref<string | null>(null);
  const busy = computed(() => activeTaskId.value !== null);

  async function runTask(
    kind: "data.import" | "data.export",
    params: Readonly<Record<string, unknown>>,
  ): Promise<unknown> {
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

  async function importData(
    collection: string,
    schemaRevision: string,
  ): Promise<ApplyImportResult | null> {
    const grant = await bridge.request("data.importSourceRequested", {
      accept: [".xlsx", ".xls", ".csv"],
    }) as SessionPathGrant;
    const plan = await bridge.request("data.previewImport", {
      grantId: grant.grantId,
      collection,
      schemaRevision,
      mode: "create_only",
      columnMapping: [],
    }) as ImportPlan;
    if (plan.summary.errorRows > 0) {
      throw new Error(
        `Import preview found ${plan.summary.errorRows} invalid row(s).`,
      );
    }
    if (!window.confirm(
      `Import ${plan.summary.validRows} validated row(s) from ${grant.displayName}?`,
    )) {
      return null;
    }
    return await runTask("data.import", {
      grantId: grant.grantId,
      collection,
      token: plan.token.token,
      mode: "create_only",
      chunkSize: 500,
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

  return { activeTaskId, busy, importData, exportData, cancelActive };
}
