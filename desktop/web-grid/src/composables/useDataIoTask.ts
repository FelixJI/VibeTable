import { computed, readonly, ref, watch, type Ref } from "vue";
import type { ApplyImportResult, ExportFormat, ExportResult, ImportPlan, SessionPathGrant } from "@/contracts";
import type {
  DataTaskSessionState,
  ExportLookupContext,
  ExportLookupSelection,
  ImportColumnMappingPayload,
  ImportPreviewSession,
  LookupExportOption,
  RelationImportOption,
} from "@/services/dataIoService";
import { t } from "@/i18n";

/** One user-authored relation mapping row (stable identities only). */
export interface RelationMappingDraft {
  readonly sourceColumn: string;
  readonly relationId: string;
  readonly matchField: string;
}

/** Export confirmation panel state; only the latest epoch may render it. */
export interface ExportLookupPanelState {
  readonly format: ExportFormat;
  readonly collection: string;
  readonly loading: boolean;
  readonly error: string | null;
  readonly options: readonly LookupExportOption[];
  readonly lookupRevision: string | null;
}

export interface DataIoTaskPort {
  readonly busy: Readonly<Ref<boolean>>;
  previewImport(collection: string, schemaRevision: string, assertCurrent?: () => void): Promise<ImportPreviewSession>;
  previewImportWithGrant(
    grant: SessionPathGrant,
    collection: string,
    schemaRevision: string,
    columnMapping: readonly ImportColumnMappingPayload[],
  ): Promise<ImportPlan>;
  loadRelationImportOptions(collection: string, assertCurrent?: () => void): Promise<readonly RelationImportOption[]>;
  loadExportLookupContext(collection: string): Promise<ExportLookupContext>;
  applyImport(
    session: ImportPreviewSession,
    assertCurrent?: () => void,
    taskSession?: () => DataTaskSessionState,
  ): Promise<ApplyImportResult>;
  exportData(
    collection: string,
    query: Readonly<Record<string, unknown>>,
    format: ExportFormat,
    lookup?: ExportLookupSelection,
    assertCurrent?: () => void,
    taskSession?: () => DataTaskSessionState,
  ): Promise<ExportResult>;
  cancelActive(): Promise<void>;
}

interface DataIoTaskContext {
  readonly collection?: string | null;
  readonly schemaRevision?: string | null;
  readonly workspaceId?: string | null;
  readonly sessionEpoch?: string | number | null;
  readonly available?: boolean;
}

interface DataIoTaskOptions {
  readonly service: DataIoTaskPort;
  readonly resolveContext: () => DataIoTaskContext;
  readonly importSucceeded: (rowCount: number) => void;
  readonly exportSucceeded: (result: ExportResult) => void;
  readonly reportError: (message: string) => void;
  readonly refresh: () => void;
}

interface RequestScope {
  readonly epoch: number;
  readonly collection: string;
  readonly workspaceId: string | number | null;
  readonly sessionEpoch: string | number | null;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function mappingKey(rows: readonly RelationMappingDraft[]): string {
  return JSON.stringify(
    rows
      .map((row) => [row.sourceColumn, row.relationId, row.matchField])
      .sort((left, right) => left.join("\u0000").localeCompare(right.join("\u0000"))),
  );
}

/**
 * Owns the complete table data-task lifecycle. Views only bind its state and
 * forward user intent; task serialization, cancellation, and error placement
 * remain private to this module. Every in-flight request captures the current
 * workspace/session epoch, collection, and request generation; results from a
 * retired scope are dropped instead of reopening panels or overwriting state.
 */
export function useDataIoTask(options: DataIoTaskOptions) {
  const previewSession = ref<ImportPreviewSession | null>(null);
  const previewing = ref(false);
  const applying = ref(false);
  const exporting = ref(false);
  const cancelling = ref(false);
  const applyError = ref<string | null>(null);
  const relationOptions = ref<readonly RelationImportOption[] | null>(null);
  const relationOptionsLoading = ref(false);
  const relationOptionsError = ref<string | null>(null);
  const relationConfig = ref<readonly RelationMappingDraft[]>([]);
  const mappingDirty = ref(false);
  const schemaDrifted = ref(false);
  const repreviewing = ref(false);
  const exportPanel = ref<ExportLookupPanelState | null>(null);
  const exportLookupIds = ref<readonly string[]>([]);
  let requestEpoch = 0;
  let catalogRequest = 0;
  let previewedMappingKey = "[]";
  const taskLocked = computed(() =>
    previewing.value
    || applying.value
    || exporting.value
    || repreviewing.value
    || previewSession.value !== null
    || exportPanel.value !== null
    || options.service.busy.value,
  );

  function captureScope(collection: string): RequestScope {
    const { workspaceId, sessionEpoch } = options.resolveContext();
    return {
      epoch: requestEpoch,
      collection,
      workspaceId: workspaceId ?? null,
      sessionEpoch: sessionEpoch ?? null,
    };
  }

  function isLive(scope: RequestScope): boolean {
    const context = options.resolveContext();
    return context.available !== false
      && scope.epoch === requestEpoch
      && context.collection === scope.collection
      && (context.workspaceId ?? null) === scope.workspaceId
      && (context.sessionEpoch ?? null) === scope.sessionEpoch;
  }

  function taskSession(scope: RequestScope): DataTaskSessionState {
    const context = options.resolveContext();
    if ((context.workspaceId ?? null) !== scope.workspaceId
        || (context.sessionEpoch ?? null) !== scope.sessionEpoch) return "retired";
    return context.available === false ? "draining" : "active";
  }

  function assertCurrent(scope: RequestScope): void {
    if (!isLive(scope)) throw new Error(t("dataIo.operationRetired"));
  }

  function resolveImportContext(): { collection: string; schemaRevision: string } | null {
    const { collection, schemaRevision, available } = options.resolveContext();
    return available !== false && collection && schemaRevision && !taskLocked.value
      ? { collection, schemaRevision }
      : null;
  }

  function resolveRepreviewContext(): { collection: string; schemaRevision: string } | null {
    const { collection, schemaRevision, available } = options.resolveContext();
    const busy = applying.value || previewing.value || repreviewing.value || exporting.value
      || options.service.busy.value;
    return available !== false && collection && schemaRevision && !busy
      && !relationOptionsLoading.value && !relationOptionsError.value ? { collection, schemaRevision } : null;
  }

  function resolveExportCollection(): string | null {
    const { collection, available } = options.resolveContext();
    return available !== false && collection && !taskLocked.value ? collection : null;
  }

  const canPreviewImport = computed(() => resolveImportContext() !== null);
  const canExport = computed(() => resolveExportCollection() !== null);
  const canRepreview = computed(() => resolveRepreviewContext() !== null
    && previewSession.value !== null
    && (mappingDirty.value || schemaDrifted.value));

  function resetRelationCatalog(): void {
    catalogRequest += 1;
    relationOptions.value = null;
    relationOptionsError.value = null;
    relationOptionsLoading.value = false;
    relationConfig.value = [];
    mappingDirty.value = false;
    schemaDrifted.value = false;
    previewedMappingKey = "[]";
  }

  function retirePendingRequests(): void {
    requestEpoch += 1;
  }

  function buildMappingPayload(
    rows: readonly RelationMappingDraft[],
  ): ImportColumnMappingPayload[] {
    if (rows.length > 256) {
      throw new Error(t("dataIo.import.mapping.tooManyRows"));
    }
    const optionByTarget = new Map(
      (relationOptions.value ?? []).map((option) => [option.relationId, option]));
    const usedSources = new Set<string>();
    const usedTargets = new Set<string>();
    return rows.map((row) => {
      const option = optionByTarget.get(row.relationId);
      if (
        !option
        || !option.matchFields.some((field) => field.fieldId === row.matchField)
        || !row.sourceColumn.trim()
        || [...row.sourceColumn].length > 128
        || usedSources.has(row.sourceColumn)
        || usedTargets.has(row.relationId)
      ) {
        throw new Error(t("dataIo.import.mapping.invalid"));
      }
      usedSources.add(row.sourceColumn);
      usedTargets.add(row.relationId);
      return {
        sourceColumn: row.sourceColumn,
        targetField: option.targetField,
        relationId: option.relationId,
        matchField: row.matchField,
      };
    });
  }

  function setRelationConfig(rows: readonly RelationMappingDraft[]): void {
    if (applying.value || repreviewing.value || previewing.value) return;
    relationConfig.value = rows;
    mappingDirty.value = mappingKey(rows) !== previewedMappingKey;
  }

  async function loadRelationOptions(collection: string, scope: RequestScope): Promise<void> {
    if (!isLive(scope)) return;
    const generation = ++catalogRequest;
    relationOptionsLoading.value = true;
    relationOptionsError.value = null;
    try {
      const loaded = await options.service.loadRelationImportOptions(collection, () => assertCurrent(scope));
      if (!isLive(scope) || generation !== catalogRequest) return;
      relationOptions.value = loaded;
    } catch (error) {
      if (isLive(scope) && generation === catalogRequest) relationOptionsError.value = errorMessage(error);
    } finally {
      if (generation === catalogRequest) relationOptionsLoading.value = false;
    }
  }

  async function previewImport(): Promise<void> {
    const context = resolveImportContext();
    if (!context) return;
    const scope = captureScope(context.collection);
    previewing.value = true;
    applyError.value = null;
    resetRelationCatalog();
    try {
      const session = await options.service.previewImport(
        context.collection,
        context.schemaRevision,
        () => assertCurrent(scope),
      );
      if (!isLive(scope)) return;
      previewSession.value = session;
      previewedMappingKey = "[]";
    } catch (error) {
      if (isLive(scope)) options.reportError(errorMessage(error));
      return;
    } finally {
      previewing.value = false;
    }
    await loadRelationOptions(context.collection, scope);
  }

  async function repreviewImport(): Promise<void> {
    const session = previewSession.value;
    const context = resolveRepreviewContext();
    if (!session || !context) return;
    const scope = captureScope(context.collection);
    repreviewing.value = true;
    applyError.value = null;
    try {
      const payload = buildMappingPayload(relationConfig.value);
      const plan = await options.service.previewImportWithGrant(
        session.grant,
        context.collection,
        context.schemaRevision,
        payload,
      );
      if (!isLive(scope)) return;
      previewSession.value = { grant: session.grant, plan, mode: session.mode };
      previewedMappingKey = mappingKey(relationConfig.value);
      mappingDirty.value = false;
      schemaDrifted.value = false;
    } catch (error) {
      if (isLive(scope)) applyError.value = errorMessage(error);
    } finally {
      repreviewing.value = false;
    }
  }

  async function applyImport(): Promise<void> {
    const session = previewSession.value;
    if (!session || applying.value || options.service.busy.value) return;
    if (mappingDirty.value || schemaDrifted.value || repreviewing.value
        || relationOptionsLoading.value
        || session.plan.summary.errorRows > 0 || session.plan.summary.validRows === 0) return;
    const scope = captureScope(session.plan.collection);
    if (!isLive(scope)) return;
    applying.value = true;
    applyError.value = null;
    try {
      const result = await options.service.applyImport(
        session, () => assertCurrent(scope), () => taskSession(scope),
      );
      if (!isLive(scope)) return;
      options.importSucceeded(result.createdCount + result.updatedCount);
      previewSession.value = null;
      resetRelationCatalog();
      retirePendingRequests();
      options.refresh();
    } catch (error) {
      if (isLive(scope)) applyError.value = errorMessage(error);
    } finally {
      applying.value = false;
      cancelling.value = false;
      if (!isLive(scope)) {
        // The confirmed write itself stays server-owned; only its stale panel
        // and callbacks are dropped once the workspace/table scope retired.
        dismissPreview();
      }
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
    retirePendingRequests();
    previewSession.value = null;
    applyError.value = null;
    resetRelationCatalog();
  }

  async function exportData(format: ExportFormat): Promise<void> {
    const collection = resolveExportCollection();
    if (!collection || exportPanel.value) return;
    const scope = captureScope(collection);
    exportLookupIds.value = [];
    exportPanel.value = {
      format,
      collection,
      loading: true,
      error: null,
      options: [],
      lookupRevision: null,
    };
    try {
      const context = await options.service.loadExportLookupContext(collection);
      if (!isLive(scope) || !exportPanel.value || exportPanel.value.collection !== collection) {
        return;
      }
      exportPanel.value = {
        ...exportPanel.value,
        loading: false,
        options: context.options,
        lookupRevision: context.lookupRevision,
      };
    } catch (error) {
      if (isLive(scope) && exportPanel.value) {
        exportPanel.value = {
          ...exportPanel.value,
          loading: false,
          error: errorMessage(error),
        };
      }
    }
  }

  function setExportLookupIds(ids: readonly string[]): void {
    if (!exportPanel.value || exporting.value) return;
    const known = new Set(exportPanel.value.options.map((option) => option.lookupId));
    exportLookupIds.value = ids.filter((id) => known.has(id));
  }

  function cancelExportPanel(): void {
    if (exporting.value) return;
    retirePendingRequests();
    exportPanel.value = null;
    exportLookupIds.value = [];
  }

  async function confirmExportData(): Promise<void> {
    const panel = exportPanel.value;
    if (!panel || panel.loading || exporting.value || options.service.busy.value) return;
    const scope = captureScope(panel.collection);
    const ids = [...exportLookupIds.value];
    exporting.value = true;
    exportPanel.value = null;
    try {
      const selection = ids.length > 0 && panel.lookupRevision
        ? { lookupIds: ids, lookupRevision: panel.lookupRevision }
        : undefined;
      const result = await options.service.exportData(
        panel.collection,
        {},
        panel.format,
        selection,
        () => assertCurrent(scope),
        () => taskSession(scope),
      );
      if (isLive(scope)) options.exportSucceeded(result);
    } catch (error) {
      if (isLive(scope)) options.reportError(errorMessage(error));
    } finally {
      exporting.value = false;
    }
  }

  watch(
    () => {
      const context = options.resolveContext();
      return [
        context.workspaceId ?? null,
        context.sessionEpoch ?? null,
        context.collection ?? null,
        context.schemaRevision ?? null,
        context.available !== false,
      ] as const;
    },
    (next, previous) => {
      const [workspaceId, sessionEpoch, collection, schemaRevision, available] = next;
      const [prevWorkspaceId, prevSessionEpoch, prevCollection] = previous;
      const scopeRetired = workspaceId !== prevWorkspaceId
        || sessionEpoch !== prevSessionEpoch
        || collection !== prevCollection
        || !available;
      if (scopeRetired) {
        retirePendingRequests();
        if (exportPanel.value && !exporting.value) {
          exportPanel.value = null;
          exportLookupIds.value = [];
        }
        if (previewSession.value && !applying.value) {
          dismissPreview();
        }
        return;
      }
      if (schemaRevision !== previous[3]) {
        retirePendingRequests();
        if (previewSession.value && !applying.value) {
          schemaDrifted.value = true;
          if (collection) void loadRelationOptions(collection, captureScope(collection));
        }
        if (exportPanel.value && !exporting.value) {
          exportPanel.value = null;
          exportLookupIds.value = [];
        }
      }
    },
    { flush: "sync" },
  );

  return {
    busy: options.service.busy,
    previewSession: readonly(previewSession),
    previewing: readonly(previewing),
    applying: readonly(applying),
    cancelling: readonly(cancelling),
    applyError: readonly(applyError),
    canPreviewImport,
    canExport,
    relationOptions: readonly(relationOptions),
    relationOptionsLoading: readonly(relationOptionsLoading),
    relationOptionsError: readonly(relationOptionsError),
    relationConfig: readonly(relationConfig),
    mappingDirty: readonly(mappingDirty),
    schemaDrifted: readonly(schemaDrifted),
    repreviewing: readonly(repreviewing),
    canRepreview,
    exportPanel: readonly(exportPanel),
    exportLookupIds: readonly(exportLookupIds),
    previewImport,
    repreviewImport,
    setRelationConfig,
    applyImport,
    cancelImport,
    cancelActiveTask,
    dismissPreview,
    exportData,
    setExportLookupIds,
    cancelExportPanel,
    confirmExportData,
  };
}
