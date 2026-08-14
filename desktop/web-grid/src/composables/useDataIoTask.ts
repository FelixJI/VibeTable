import { computed, readonly, ref, type Ref } from "vue";
import type { ApplyImportResult, ExportResult } from "@/contracts";
import type { ImportPreviewSession } from "@/services/dataIoService";

export interface DataIoTaskPort {
  readonly busy: Readonly<Ref<boolean>>;
  previewImport(collection: string, schemaRevision: string): Promise<ImportPreviewSession>;
  applyImport(session: ImportPreviewSession): Promise<ApplyImportResult>;
  exportData(
    collection: string,
    query: Readonly<Record<string, unknown>>,
  ): Promise<ExportResult>;
  cancelActive(): Promise<void>;
}

interface DataIoTaskContext {
  readonly collection?: string | null;
  readonly schemaRevision?: string | null;
}

interface DataIoTaskOptions {
  readonly service: DataIoTaskPort;
  readonly resolveContext: () => DataIoTaskContext;
  readonly importSucceeded: (rowCount: number) => void;
  readonly exportSucceeded: (result: ExportResult) => void;
  readonly reportError: (message: string) => void;
  readonly refresh: () => void;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Owns the complete table data-task lifecycle. Views only bind its state and
 * forward user intent; task serialization, cancellation, and error placement
 * remain private to this module.
 */
export function useDataIoTask(options: DataIoTaskOptions) {
  const previewSession = ref<ImportPreviewSession | null>(null);
  const previewing = ref(false);
  const applying = ref(false);
  const cancelling = ref(false);
  const applyError = ref<string | null>(null);
  const locked = computed(() =>
    previewing.value
    || applying.value
    || previewSession.value !== null
    || options.service.busy.value,
  );

  async function previewImport(): Promise<void> {
    const { collection, schemaRevision } = options.resolveContext();
    if (!collection || !schemaRevision || locked.value) return;
    previewing.value = true;
    applyError.value = null;
    try {
      previewSession.value = await options.service.previewImport(collection, schemaRevision);
    } catch (error) {
      options.reportError(errorMessage(error));
    } finally {
      previewing.value = false;
    }
  }

  async function applyImport(): Promise<void> {
    const session = previewSession.value;
    if (!session || applying.value || options.service.busy.value) return;
    applying.value = true;
    applyError.value = null;
    try {
      const result = await options.service.applyImport(session);
      options.importSucceeded(result.createdCount + result.updatedCount);
      previewSession.value = null;
      options.refresh();
    } catch (error) {
      applyError.value = errorMessage(error);
    } finally {
      applying.value = false;
      cancelling.value = false;
    }
  }

  async function cancelImport(): Promise<void> {
    if (!applying.value || cancelling.value) return;
    cancelling.value = true;
    try {
      await options.service.cancelActive();
    } catch (error) {
      cancelling.value = false;
      applyError.value = errorMessage(error);
    }
  }

  function cancelActiveTask(): Promise<void> {
    return options.service.cancelActive();
  }

  function dismissPreview(): void {
    if (applying.value) return;
    previewSession.value = null;
    applyError.value = null;
  }

  async function exportData(): Promise<void> {
    const { collection } = options.resolveContext();
    if (!collection || locked.value) return;
    try {
      options.exportSucceeded(await options.service.exportData(collection, {}));
    } catch (error) {
      options.reportError(errorMessage(error));
    }
  }

  return {
    busy: options.service.busy,
    previewSession: readonly(previewSession),
    previewing: readonly(previewing),
    applying: readonly(applying),
    cancelling: readonly(cancelling),
    applyError: readonly(applyError),
    locked,
    previewImport,
    applyImport,
    cancelImport,
    cancelActiveTask,
    dismissPreview,
    exportData,
  };
}
