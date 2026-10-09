import { watch } from "vue";
import { BridgeOperationError } from "@/bridge/hostBridge";
import type {
  FieldApplyReceiptV2,
  FieldChangeActionV2,
  FormulaDraftValidationResult,
  FormulaFunctionInfo,
  FieldDefinitionV2,
  FieldDraftV2,
  CapabilityV2,
  JsonValueV2,
  LogicalTypeV2,
  SchemaDescribeResult,
  SchemaSnapshot,
} from "@/contracts";
import type { FormulaAuthorDocument } from "@/contracts/generated/workbench";
import { ProductRpcError } from "@/services/productRpcResult";
import {
  parseFieldApplyReceiptV2,
  parseSchemaSnapshotV2,
  parseFieldChangePlanV2,
  parseFieldMigrationStatusV2,
  parseFieldRecycleBinResultV2,
  parseFieldSettingsDescribeResultV2,
} from "@/contracts";
import { useHostBridge } from "@/services/bridgeContext";
import { useFieldSettingsStore } from "./store";
import type { FormulaDraftDiagnostic, FormulaDraftValidateRequest } from "./formula/formulaDraftRequest";
import { isFormulaAuthorDocument, isFormulaTextRange } from "./formula/tokenDocument";
import { buildFieldChangeIntent, relationPairPatchFromDrafts } from "./model";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { collectionLabel } from "@/components/layout/collectionLabel";
import { useTableStore } from "@/stores/tableStore";
import {
  createBridgeFormulaPreviewPort,
  FormulaPreviewCoordinator,
} from "@/services/formulaPreviewCoordinator";

const RELATION_ACCEPTS = [
  "vibetable.relation-capabilities.v1",
  "vibetable.lookup-query.v1",
] as const;

function operationId(): string {
  return globalThis.crypto?.randomUUID?.()
    ?? `field-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function unwrapFieldResult(value: unknown): unknown {
  if (!value || typeof value !== "object" || !("error" in value)) return value;
  const error = (value as { readonly error?: unknown }).error;
  if (!error || typeof error !== "object") return value;
  const candidate = error as { readonly code?: unknown; readonly message?: unknown };
  if (typeof candidate.message !== "string") return value;
  throw new BridgeOperationError({
    message: candidate.message,
    ...(typeof candidate.code === "string" ? { code: candidate.code } : {}),
  });
}

interface FieldSettingsServiceOptions {
  readonly onCommitted?: (receipt: FieldApplyReceiptV2) => void | Promise<void>;
}

function buildFormulaPreviewField(
  recommended: CapabilityV2["recommended"],
  validation: FormulaDraftValidationResult,
  existing: FieldDefinitionV2 | null,
  displayName: string,
): FieldDefinitionV2 {
  return {
    contract: "vibetable.schema.v2",
    identity: existing?.identity ?? {
      fieldId: "fld_formula_preview",
      physicalName: "f_formula_preview",
      providerFieldId: "pb_formula_preview",
    },
    displayName: displayName || "公式预览",
    help: existing?.help ?? "",
    logicalType: "formula",
    lifecycle: existing?.lifecycle ?? { state: "active", retiredAt: null },
    value: existing?.value ?? recommended.value,
    constraints: existing?.constraints ?? recommended.constraints,
    storage: {
      ...(existing?.storage ?? recommended.storage),
      kind: "computed",
      options: {
        ...(existing?.storage ?? recommended.storage).options,
        // The fresh draft inference is authoritative for the preview field;
        // recommended defaults would force every number result to decimal.
        onlyInt: validation.onlyInt ?? false,
      },
    },
    display: existing?.display ?? recommended.display,
    formula: {
      language: validation.language ?? "cel-v1",
      source: validation.canonicalSource,
      resultType: validation.resultType,
      ...(validation.resultElementType ? { resultElementType: validation.resultElementType } : {}),
    },
  };
}

export function useFieldSettingsService(options: FieldSettingsServiceOptions = {}): {
  openCreate: (tableId: string, preferredType?: LogicalTypeV2) => Promise<void>;
  openEdit: (tableId: string, fieldId: string) => Promise<void>;
  requestClose: () => boolean;
  plan: (action?: FieldChangeActionV2) => Promise<void>;
  apply: () => Promise<void>;
  refreshMigration: () => Promise<void>;
  cancelMigration: () => Promise<void>;
  loadRecycleBin: () => Promise<void>;
  restore: (fieldId: string) => Promise<void>;
  loadRelationCatalog: () => Promise<void>;
  selectRelationTarget: (tableId: string) => Promise<void>;
  loadLookupCatalog: () => Promise<void>;
  resolveLookupPath: (path: readonly { readonly relationFieldId: string }[]) => Promise<void>;
  selectLookupSource: (tableId: string) => Promise<void>;
  previewLookupDraft: (draft: NonNullable<FieldDraftV2["lookup"]> | null) => void;
  loadFormulaCatalog: () => Promise<void>;
  selectFormulaSource: (tableId: string) => Promise<void>;
  validateFormulaDraft: (request: FormulaDraftValidateRequest) => Promise<void>;
  dispose: () => void;
} {
  const bridge = useHostBridge();
  const store = useFieldSettingsStore();
  const workspace = useWorkspaceStore();
  const tableStore = useTableStore();
  const formulaPreview = new FormulaPreviewCoordinator(
    createBridgeFormulaPreviewPort(bridge),
    0,
  );
  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let frozenOperationId: string | null = null;
  let formulaValidationGeneration = 0;
  let formulaCollectionGeneration = 0;
  let lookupCatalogGeneration = 0;
  let lookupPreviewGeneration = 0;
  let lookupPreviewTimer: ReturnType<typeof setTimeout> | null = null;
  let lookupDraft: NonNullable<FieldDraftV2["lookup"]> | null = null;
  function invalidateLookupPreview(): void {
    lookupPreviewGeneration += 1;
    if (lookupPreviewTimer !== null) clearTimeout(lookupPreviewTimer);
    lookupPreviewTimer = null;
    store.lookupPreview = { loading: false, ready: false, value: undefined, error: null };
  }
  const stopLookupWatch = watch(
    () => [workspace.phase, workspace.collections, store.result, tableStore.schemaRevision,
      store.open, store.draft?.logicalType] as const,
    () => { lookupCatalogGeneration += 1; lookupDraft = null; invalidateLookupPreview(); },
    { flush: "sync" },
  );

  function previewLookupDraft(draft: NonNullable<FieldDraftV2["lookup"]> | null): void {
    invalidateLookupPreview();
    lookupDraft = draft;
    const described = store.result;
    const source = draft?.condition ? store.lookupConditionSchema : store.lookupSchemas.at(-1);
    if (!draft || !described || !source) return;
    if (draft.condition && source.collection !== draft.condition.sourceTableId) return;
    const current = lookupPreviewGeneration;
    store.lookupPreview = { loading: true, ready: false, value: undefined, error: null };
    lookupPreviewTimer = setTimeout(async () => {
      lookupPreviewTimer = null;
      try {
        const result = await bridge.request("lookup.draft.preview", {
          tableId: described.tableId, schemaRevision: described.schemaRevision,
          sourceSchemaRevision: source.schemaRevision, lookup: draft,
        });
        if (current !== lookupPreviewGeneration || !store.open) return;
        unwrapFieldResult(result);
        store.lookupPreview = {
          loading: false, ready: result.cell !== null, value: result.cell?.value,
          error: result.cell === null ? "当前表没有可用于预览的记录" : null,
        };
      } catch (error) {
        if (current !== lookupPreviewGeneration || !store.open) return;
        store.lookupPreview = { loading: false, ready: false, value: undefined,
          error: error instanceof Error ? error.message : String(error) };
      }
    }, 250);
  }

  async function selectLookupSource(tableId: string): Promise<void> {
    invalidateLookupPreview();
    const current = ++lookupCatalogGeneration;
    store.lookupConditionSchema = null;
    store.lookupConditionFields = [];
    store.lookupCatalogLoading = false;
    store.lookupCatalogError = null;
    if (!tableId || !store.result) return;
    store.beginLookupCatalog();
    try {
      const [schema, raw] = await Promise.all([
        describeRelationTable(tableId), bridge.request("schema.getTable", { tableId }),
      ]);
      const definition = parseSchemaSnapshotV2(unwrapFieldResult(raw));
      if (current !== lookupCatalogGeneration || !store.open) return;
      if (definition.tableId !== tableId || definition.schemaRevision !== schema.schemaRevision) {
        throw new Error("来源字段目录已变化，请重新选择来源表");
      }
      store.lookupConditionSchema = schema;
      store.lookupConditionFields = definition.fields;
      store.lookupCatalogLoading = false;
      store.lookupCatalogError = null;
      if (lookupDraft?.condition?.sourceTableId === tableId) previewLookupDraft(lookupDraft);
    } catch (error) {
      if (current === lookupCatalogGeneration) store.failLookupCatalog(error);
    }
  }

  let formulaValidationAbort: AbortController | null = null;

  /** Workspace switches must retire every in-flight formula editor request. */
  const stopWorkspaceWatch = watch(
    () => [workspace.phase, workspace.collections] as const,
    () => {
      if (!store.open) return;
      abortFormulaValidation();
    },
    { flush: "sync" },
  );
  /** Field-schema changes must not let stale validations enable saving. */
  const stopSchemaWatch = watch(
    () => store.result?.schemaRevision ?? null,
    (next, previous) => {
      if (!store.open || previous === null || next === previous) return;
      abortFormulaValidation();
    },
    { flush: "sync" },
  );

  function abortFormulaValidation(): void {
    formulaValidationGeneration += 1;
    formulaValidationAbort?.abort();
    formulaValidationAbort = null;
    formulaPreview.cancel();
    store.invalidateFormulaDraft();
  }

  async function describe(
    tableId: string,
    fieldId?: string,
    preferredType?: LogicalTypeV2,
  ): Promise<void> {
    const current = ++generation;
    formulaValidationGeneration += 1;
    formulaValidationAbort?.abort();
    formulaValidationAbort = null;
    formulaPreview.cancel();
    store.beginOpen(fieldId ? { tableId, fieldId } : null);
    try {
      const result = parseFieldSettingsDescribeResultV2(unwrapFieldResult(
        await bridge.request("field.settings.describe", {
          tableId,
          ...(fieldId ? { fieldId } : {}),
        }),
      ));
      if (current === generation) {
        store.load(result);
        if (!result.definition && preferredType) store.changeType(preferredType);
      }
    } catch (error) {
      if (current === generation) store.fail(error);
    }
  }

  function openCreate(tableId: string, preferredType?: LogicalTypeV2): Promise<void> {
    return describe(tableId, undefined, preferredType);
  }

  function openEdit(tableId: string, fieldId: string): Promise<void> {
    return describe(tableId, fieldId);
  }

  function requestClose(): boolean {
    if (store.dirty && !globalThis.confirm("字段设置尚未保存，确定放弃更改吗？")) {
      return false;
    }
    generation += 1;
    abortFormulaValidation();
    stopPolling();
    store.close();
    return true;
  }

  async function plan(nextAction?: FieldChangeActionV2): Promise<void> {
    if (!store.result) return;
    const current = generation;
    const action = nextAction ?? store.action;
    store.beginPlan(action);
    try {
      const relationPairPatch = action === "update" && store.isPairedRelation
        && store.original && store.draft && store.originalRelationPair && store.relationPair
        ? relationPairPatchFromDrafts(
          store.original, store.draft, store.originalRelationPair, store.relationPair,
        ) : undefined;
      if (action === "update" && store.isPairedRelation
        && (!store.relationPair || store.relationCatalogLoading || store.relationCatalogError)) {
        throw new Error("请先加载完整的双向关联字段设置");
      }
      const intent = buildFieldChangeIntent({
        action,
        result: store.result,
        draft: relationPairPatch || ["retire", "restore", "purge"].includes(action) ? null : store.draft,
        relationPairPatch,
        conversionRule: store.conversionRule,
        confirmation: store.confirmation,
        backupReceipt: store.backupReceipt,
        relationPair: action === "create" && store.draft?.logicalType === "relation"
          ? store.relationPair
          : null,
      });
      const planned = parseFieldChangePlanV2(
        unwrapFieldResult(await bridge.request("field.change.plan", intent)),
      );
      if (current !== generation) return;
      store.setPlan(planned);
      frozenOperationId = null;
    } catch (error) {
      if (current === generation) store.fail(error);
    }
  }

  async function apply(): Promise<void> {
    if (!store.plan || !store.canApply) return;
    store.beginApply();
    try {
      const receipt = parseFieldApplyReceiptV2(unwrapFieldResult(
        await bridge.request("field.change.apply", {
          planId: store.plan.planId,
          planHash: store.plan.planHash,
          operationId: frozenOperationId ??= operationId(),
          actor: { id: "desktop-user", kind: "user" },
          confirmations: store.confirmations,
        }),
      ));
      store.setReceipt(receipt);
      if (receipt.migrationJobId) {
        schedulePoll();
      } else if (receipt.action === "purge") {
        frozenOperationId = null;
        await options.onCommitted?.(receipt);
        generation += 1;
        store.close();
      } else {
        frozenOperationId = null;
        await describe(receipt.tableId, receipt.fieldId);
        await options.onCommitted?.(receipt);
      }
    } catch (error) {
      store.fail(error);
    }
  }

  async function refreshMigration(): Promise<void> {
    const jobId = store.receipt?.migrationJobId ?? store.migration?.jobId;
    if (!jobId) return;
    try {
      const status = parseFieldMigrationStatusV2(unwrapFieldResult(
        await bridge.request("field.change.status", { jobId }),
      ));
      store.setMigration(status);
      if (!["completed", "cancelled", "failed", "rolled_back"].includes(status.phase)) {
        schedulePoll();
      } else {
        stopPolling();
        if (status.phase === "completed" && store.receipt) {
          const { tableId, fieldId } = store.receipt;
          const receipt = store.receipt;
          frozenOperationId = null;
          await describe(tableId, fieldId);
          await options.onCommitted?.(receipt);
        }
      }
    } catch (error) {
      store.fail(error);
      stopPolling();
    }
  }

  function schedulePoll(): void {
    stopPolling();
    pollTimer = setTimeout(() => void refreshMigration(), 750);
  }

  function stopPolling(): void {
    if (pollTimer !== null) clearTimeout(pollTimer);
    pollTimer = null;
  }

  async function cancelMigration(): Promise<void> {
    const jobId = store.receipt?.migrationJobId ?? store.migration?.jobId;
    if (!jobId) return;
    try {
      const status = parseFieldMigrationStatusV2(unwrapFieldResult(
        await bridge.request("field.change.cancel", { jobId }),
      ));
      store.setMigration(status);
      if (["cancelled", "failed", "rolled_back"].includes(status.phase)) {
        stopPolling();
      } else {
        schedulePoll();
      }
    } catch (error) {
      store.fail(error);
    }
  }

  async function loadRecycleBin(): Promise<void> {
    const tableId = store.result?.tableId;
    if (!tableId) return;
    try {
      const result = parseFieldRecycleBinResultV2(unwrapFieldResult(
        await bridge.request("field.recycleBin.list", { tableId }),
      ));
      store.setRecycled(result.fields);
    } catch (error) {
      store.fail(error);
    }
  }

  async function restore(fieldId: string): Promise<void> {
    const tableId = store.result?.tableId;
    if (!tableId) return;
    try {
      const described = parseFieldSettingsDescribeResultV2(unwrapFieldResult(
        await bridge.request("field.settings.describe", { tableId, fieldId }),
      ));
      store.load(described);
      await plan("restore");
    } catch (error) {
      store.fail(error instanceof BridgeOperationError ? error : error);
    }
  }

  async function loadRelationCatalog(): Promise<void> {
    if (!store.result || store.draft?.logicalType !== "relation") return;
    const current = generation;
    const described = store.result;
    const tableId = described.tableId;
    const tables = workspace.collections.map(item => ({
      tableId: item.collection,
      displayName: collectionLabel(item, workspace.displayNames),
    }));
    if (!tables.some(item => item.tableId === tableId)) {
      tables.unshift({
        tableId,
        displayName: collectionLabel({ collection: tableId }, workspace.displayNames),
      });
    }
    store.setRelationTables(tables);
    if (!store.isPairedRelation && store.relationPair && !store.relationPair.reciprocalDisplayName) {
      const source = tables.find(item => item.tableId === tableId);
      store.patchRelationPair({ reciprocalDisplayName: source?.displayName ?? "关联记录" });
    }
    try {
      store.beginRelationCatalog();
      const sourceSchema = await describeRelationTable(tableId);
      if (current !== generation || !store.open) return;
      store.setRelationSchema("source", sourceSchema);
      if (!store.isPairedRelation && store.relationPair && !store.relationPair.sourceDisplayFieldId) {
        store.patchRelationPair({
          sourceDisplayFieldId: sourceSchema.primaryDisplayFieldId
            || sourceSchema.columns.find(column => column.fieldId && column.kind !== "system")?.fieldId
            || "",
        });
      }
      const targetTableId = store.draft.relation?.targetTableId;
      if (targetTableId && described.definition) {
        const targetSchema = targetTableId === tableId
          ? sourceSchema : await describeRelationTable(targetTableId);
        if (current !== generation || !store.open) return;
        store.setRelationSchema("target", targetSchema);
        const relation = described.definition.relation;
        if (relation?.pairId) {
          if (!relation.reciprocalFieldId) {
            throw new Error("另一端关联字段不可用，请重新打开字段设置");
          }
          store.beginRelationCatalog();
          const reciprocal = parseFieldSettingsDescribeResultV2(unwrapFieldResult(
            await bridge.request("field.settings.describe", {
              tableId: targetTableId, fieldId: relation.reciprocalFieldId,
            }),
          ));
          if (current !== generation || !store.open) return;
          const field = reciprocal.definition;
          if (reciprocal.tableId !== targetTableId
            || field?.identity.fieldId !== relation.reciprocalFieldId
            || field.relation?.pairId !== relation.pairId
            || field.relation.targetTableId !== tableId
            || field.relation.reciprocalFieldId !== described.definition.identity.fieldId) {
            throw new Error("另一端关联字段不可用，请重新打开字段设置");
          }
          store.loadRelationPair({
            reciprocalDisplayName: field.displayName,
            reciprocalCardinality: field.relation.cardinality,
            sourceDisplayFieldId: field.relation.displayFieldId,
          });
          store.setRelationSchema("target", targetSchema);
        }
      } else if (targetTableId) await selectRelationTarget(targetTableId);
    } catch (error) {
      if (current === generation && store.open) store.failRelationCatalog(error);
    }
  }

  async function selectRelationTarget(tableId: string): Promise<void> {
    if (!store.draft?.relation || store.isExisting) return;
    store.patchDraft({
      relation: { ...store.draft.relation, targetTableId: tableId, displayFieldId: "" },
    });
    try {
      store.beginRelationCatalog();
      const schema = tableId === store.result?.tableId && store.relationSourceSchema
        ? store.relationSourceSchema
        : await describeRelationTable(tableId);
      store.setRelationSchema("target", schema);
      store.patchDraft({
        relation: {
          ...store.draft.relation,
          displayFieldId: schema.primaryDisplayFieldId
            || schema.columns.find(column => column.fieldId && column.kind !== "system")?.fieldId
            || "",
        },
      });
    } catch (error) {
      store.failRelationCatalog(error);
    }
  }

  async function loadLookupCatalog(): Promise<void> {
    if (!store.result || store.draft?.logicalType !== "lookup" || !store.draft.lookup) return;
    const tableId = store.result.tableId;
    const current = generation;
    const collections = workspace.collections;
    store.setRelationTables(workspace.collections.map(item => ({
      tableId: item.collection, displayName: collectionLabel(item, workspace.displayNames),
    })));
    try {
      const raw = await bridge.request("schema.getTable", { tableId });
      if (current !== generation || collections !== workspace.collections || !store.open) return;
      const schema = parseSchemaSnapshotV2(unwrapFieldResult(raw));
      if (schema.tableId !== tableId || schema.schemaRevision !== store.result.schemaRevision) {
        throw new Error("当前字段目录已变化，请重新打开字段设置");
      }
      store.lookupCurrentFields = schema.fields;
      await loadLookupSchemas(store.draft.lookup.path);
      if (current !== generation || collections !== workspace.collections || !store.open) return;
      if (store.draft.lookup.condition) await selectLookupSource(store.draft.lookup.condition.sourceTableId);
    } catch (error) {
      if (current === generation && collections === workspace.collections && store.open) {
        store.failLookupCatalog(error);
      }
    }
  }

  async function resolveLookupPath(
    path: readonly { readonly relationFieldId: string }[],
  ): Promise<void> {
    if (!store.result || store.draft?.logicalType !== "lookup" || !store.draft.lookup) return;
    invalidateLookupPreview();
    store.lookupConditionSchema = null;
    store.lookupConditionFields = [];
    await loadLookupSchemas(path);
  }

  async function loadLookupSchemas(
    path: readonly { readonly relationFieldId: string }[],
  ): Promise<void> {
    if (!store.result) return;
    const current = ++lookupCatalogGeneration;
    try {
      store.beginLookupCatalog();
      const schemas: SchemaSnapshot[] = [await describeRelationTable(store.result.tableId)];
      for (const [index, step] of path.entries()) {
        if (!step.relationFieldId) break;
        const current = schemas[index]!;
        const relation = current.normalizedRelations.find(
          item => item.relationId === `${current.collection}.${step.relationFieldId}`,
        );
        if (!relation?.relatedCollection) {
          throw new Error(`第 ${index + 1} 跳不是可用于 Lookup 的直接关系`);
        }
        schemas.push(await describeRelationTable(relation.relatedCollection));
      }
      if (current === lookupCatalogGeneration && store.open) store.setLookupSchemas(schemas);
    } catch (error) {
      if (current === lookupCatalogGeneration) store.failLookupCatalog(error);
    }
  }

  async function selectFormulaSource(tableId: string): Promise<void> {
    if (!store.open || store.draft?.logicalType !== "formula"
      || !store.relationTables.some(table => table.tableId === tableId)) return;
    const request = ++formulaCollectionGeneration;
    const current = generation;
    const collections = workspace.collections;
    const isLive = () => request === formulaCollectionGeneration && current === generation
      && collections === workspace.collections && store.open && store.draft?.logicalType === "formula";
    store.formulaCollectionSchema = null;
    store.formulaCollectionLoading = true;
    store.formulaCatalogError = null;
    try {
      const schema = await describeRelationTable(tableId);
      if (isLive()) store.formulaCollectionSchema = schema;
    } catch (error) {
      if (isLive()) store.failFormulaCatalog(error);
    } finally {
      if (isLive()) store.formulaCollectionLoading = false;
    }
  }

  async function loadFormulaCatalog(): Promise<void> {
    if (!store.result || store.draft?.logicalType !== "formula") return;
    const current = generation;
    const requestGeneration = formulaValidationGeneration;
    const collections = workspace.collections;
    const phase = workspace.phase;
    const schemaRevision = store.result.schemaRevision;
    const catalogIsLive = () => current === generation && store.open
      && collections === workspace.collections && phase === workspace.phase
      && schemaRevision === store.result?.schemaRevision;
    formulaCollectionGeneration += 1;
    store.formulaCollectionSchema = null;
    store.formulaCollectionLoading = false;
    store.setRelationTables(workspace.collections.map(item => ({
      tableId: item.collection,
      displayName: collectionLabel(item, workspace.displayNames),
    })));
    store.beginFormulaCatalog();
    try {
      const source = await describeRelationTable(store.result.tableId);
      if (!catalogIsLive()) return;
      const relations = source.columns.flatMap(column => {
        if (!column.fieldId || column.kind !== "relation" || !column.relationId) return [];
        const descriptor = source.normalizedRelations.find(
          item => item.relationId === column.relationId,
        );
        if (!descriptor?.relatedCollection) {
          return [];
        }
        return [{ fieldId: column.fieldId, tableId: descriptor.relatedCollection }];
      });
      const resolved = await Promise.all(relations.map(async relation => ({
        fieldId: relation.fieldId,
        schema: await describeRelationTable(relation.tableId),
      })));
      if (!catalogIsLive()) return;
      store.setFormulaCatalog(source, Object.fromEntries(
        resolved.map(item => [item.fieldId, item.schema]),
      ));
      if (requestGeneration !== formulaValidationGeneration) {
        // Consume the still-current validation for the deferred preview; restoring would overwrite newer user input.
        if (catalogIsLive() && store.draft?.logicalType === "formula"
          && store.formulaValidation) {
          scheduleFormulaPreview(store.formulaValidation);
        }
        return;
      }
      const persisted = store.draft?.formula?.source ?? "";
      if (persisted) {
        // Restore the persisted canonical text into a stable-token document.
        void validateFormulaDraft({
          kind: "restore",
          displaySource: persisted,
          adoptDocument: true,
        });
      } else {
        // Bootstrap the catalog for an empty formula without adopting "0".
        void validateFormulaDraft({
          kind: "restore",
          displaySource: "0",
          adoptDocument: false,
        });
      }
    } catch (error) {
      if (catalogIsLive()) store.failFormulaCatalog(error);
    }
  }

  async function validateFormulaDraft(request: FormulaDraftValidateRequest): Promise<void> {
    if (request.kind === "invalidate") {
      formulaValidationGeneration += 1;
      formulaValidationAbort?.abort();
      formulaValidationAbort = null;
      formulaPreview.cancel();
      store.invalidateFormulaDraft(request.discardDocument);
      return;
    }
    const tableId = store.result?.tableId;
    if (!tableId || store.draft?.logicalType !== "formula") return;
    const current = ++formulaValidationGeneration;
    const controller = new AbortController();
    formulaValidationAbort?.abort();
    formulaValidationAbort = controller;
    formulaPreview.cancel();
    const schemaRevision = store.result?.schemaRevision ?? "";
    const workspaceStamp = `${workspace.phase}:${workspace.collections.length}`;
    if (request.kind === "restore") {
      store.beginFormulaRestore();
    } else {
      store.beginFormulaValidation(
        request.displaySource,
        request.authorDocument.documentRevision,
      );
    }
    try {
      const raw = await Promise.race([
        bridge.request("formula.draft.validate", {
          tableId,
          displaySource: request.displaySource,
          ...(request.kind === "restore"
            ? { restoreSource: true }
            : { authorDocument: request.authorDocument }),
        }),
        abortedRequest(controller.signal),
      ]);
      if (!currentRequestIsLive(current, controller, schemaRevision, workspaceStamp)) return;
      const failure = productFailure(raw);
      if (failure) {
        failFormulaRequest(request, failure.message, failure.diagnostic, failure.restoredDocument);
        return;
      }
      const result = raw as FormulaDraftValidationResult;
      if (!isFormulaDraftValidation(result)
        || (request.kind === "restore" && !isFormulaAuthorDocument(result.authorDocument))) {
        failFormulaRequest(request, "公式校验返回了无效结果");
        return;
      }
      if (request.kind === "restore") {
        store.setFormulaRestored(result, request.adoptDocument);
      } else {
        store.setFormulaValidation(request.displaySource, result);
        scheduleFormulaPreview(result);
      }
    } catch (error) {
      if (isAbortError(error)) return;
      if (!currentRequestIsLive(current, controller, schemaRevision, workspaceStamp)) return;
      failFormulaRequest(request, errorMessage(error), diagnosticFromError(error));
    } finally {
      if (formulaValidationAbort === controller) formulaValidationAbort = null;
    }
  }

  function currentRequestIsLive(
    current: number,
    controller: AbortController,
    schemaRevision: string,
    workspaceStamp: string,
  ): boolean {
    if (current !== formulaValidationGeneration || controller.signal.aborted) return false;
    if (!store.open || store.draft?.logicalType !== "formula") return false;
    // Only results matching the current schema and workspace may be committed.
    if (store.result?.schemaRevision !== schemaRevision) return false;
    if (`${workspace.phase}:${workspace.collections.length}` !== workspaceStamp) return false;
    return true;
  }

  function failFormulaRequest(
    request: FormulaDraftValidateRequest,
    message: string,
    diagnostic?: FormulaDraftDiagnostic | null,
    restoredDocument?: FormulaAuthorDocument | null,
  ): void {
    if (request.kind === "restore") {
      store.failFormulaRestore(
        new Error(message),
        diagnostic ?? null,
        restoredDocument ?? null,
      );
    } else if (request.kind === "document") {
      store.failFormulaValidation(request.displaySource, new Error(message), diagnostic ?? null);
    }
  }

  function scheduleFormulaPreview(validation: FormulaDraftValidationResult): void {
    const tableId = store.result?.tableId;
    const formulaCapability = store.result?.capabilities.find(
      item => item.logicalType === "formula",
    );
    const row = tableStore.allRows[0];
    if (!tableId || !formulaCapability) {
      store.setFormulaPreviewNote("正在等待权威表结构，完成后可预览样例结果");
      return;
    }
    if (!row) {
      store.setFormulaPreviewNote("当前表没有可用于预览的样例记录");
      return;
    }
    const field = buildFormulaPreviewField(
      formulaCapability.recommended,
      validation,
      store.result?.definition ?? null,
      store.draft?.displayName ?? "公式预览",
    );
    const sourceSchema = store.formulaSourceSchema;
    if (!sourceSchema || store.formulaCatalogLoading || store.formulaCatalogError) {
      // Only an installed, current catalog yields a well-formed sample; loadFormulaCatalog reschedules after install.
      store.setFormulaPreviewNote(store.formulaCatalogError
        ? "公式字段目录加载失败，暂时无法预览样例结果"
        : "正在等待公式字段目录，完成后自动预览样例结果");
      return;
    }
    // The grid adds a primary-key column that is not a Schema V2 formula input.
    // Keep declared system fields such as AutoDate in the preview activation.
    const columns = sourceSchema.columns
      .filter(item => item.name !== sourceSchema.primaryKey);
    const sample: Record<string, JsonValueV2> = {};
    store.beginFormulaPreview();
    for (const column of columns) {
      if (!Object.hasOwn(row, column.name)) continue;
      const value = row[column.name];
      // Only authoritative Lookup columns carry cell status/provenance envelopes.
      // Bare scalar/list projections and ordinary user JSON retain their values.
      if (column.kind === "lookup" && value !== null
        && typeof value === "object" && !Array.isArray(value)) {
        const cell = value as Record<string, unknown>;
        if (cell.state !== "ok" || !Object.hasOwn(cell, "value")) {
          store.failFormulaPreview(new Error(
            `查找字段“${column.title}”的样例值尚不可用（${String(cell.state ?? "invalid")}）`,
          ));
          return;
        }
        sample[column.name] = cell.value as JsonValueV2;
      } else {
        sample[column.name] = value as JsonValueV2;
      }
    }
    formulaPreview.schedule({
      tableId,
      field,
      row: sample,
      changedFieldIds: [],
    }, {
      onResult: result => store.setFormulaPreview(result.values[field.identity.physicalName]),
      onError: error => store.failFormulaPreview(error),
    });
  }

  async function describeRelationTable(tableId: string) {
    const result = await bridge.request("schema.describe", {
      collection: tableId,
      requestGeneration: 0,
      accepts: RELATION_ACCEPTS,
    }) as SchemaDescribeResult;
    if (result.contract !== "vibetable.schema-describe.v1" || result.collection !== tableId) {
      throw new Error("关联字段目录响应与所选数据表不匹配");
    }
    store.setLookupMaxDepth(result.capabilities.lookupMaxDepth ?? 8);
    return result.schema;
  }

  function dispose(): void {
    stopLookupWatch();
    lookupCatalogGeneration += 1;
    invalidateLookupPreview();
    generation += 1;
    formulaValidationGeneration += 1;
    formulaValidationAbort?.abort();
    formulaValidationAbort = null;
    formulaPreview.dispose();
    stopPolling();
    frozenOperationId = null;
    stopWorkspaceWatch();
    stopSchemaWatch();
  }

  return {
    openCreate, openEdit, requestClose, plan, apply, refreshMigration,
    cancelMigration, loadRecycleBin, restore, loadRelationCatalog,
    selectRelationTarget, loadLookupCatalog, resolveLookupPath, selectLookupSource, previewLookupDraft,
    loadFormulaCatalog, selectFormulaSource, validateFormulaDraft, dispose,
  };
}

function isFormulaDraftValidation(value: unknown): value is FormulaDraftValidationResult {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<FormulaDraftValidationResult>;
  return typeof candidate.canonicalSource === "string"
    && typeof candidate.resultType === "string"
    && (candidate.language === undefined || candidate.language === "cel-v1" || candidate.language === "cel-v2")
    && (candidate.resultElementType === undefined
      ? !(candidate.language === "cel-v2" && candidate.resultType === "json")
      : candidate.language === "cel-v2" && candidate.resultType === "json"
        && ["number", "bool", "text", "dateTime"].includes(candidate.resultElementType))
    && (candidate.onlyInt === undefined || typeof candidate.onlyInt === "boolean")
    && Array.isArray(candidate.dependencies)
    && Array.isArray(candidate.relationAggregatePaths)
    && (candidate.authorDocument === undefined
      || isFormulaAuthorDocument(candidate.authorDocument))
    && (candidate.functions === undefined
      || Array.isArray(candidate.functions) && candidate.functions.every(isFormulaFunctionInfo));
}

function isFormulaFunctionInfo(value: unknown): value is FormulaFunctionInfo {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return ["name", "category", "signature", "description", "example"]
    .every(key => typeof candidate[key] === "string");
}

interface FormulaRpcFailure {
  readonly message: string;
  readonly diagnostic: FormulaDraftDiagnostic | null;
  /** Optional document carried by failed restores (#REF!/token retention). */
  readonly restoredDocument: FormulaAuthorDocument | null;
}

/** Keeps the structured UTF-16 diagnostic that unwrapFieldResult would drop. */
function productFailure(value: unknown): FormulaRpcFailure | null {
  if (!value || typeof value !== "object" || !("error" in value)) return null;
  const error = (value as { readonly error?: unknown }).error;
  if (!error || typeof error !== "object") return null;
  const candidate = error as Readonly<Record<string, unknown>>;
  if (typeof candidate.message !== "string") return null;
  const code = typeof candidate.code === "string" ? candidate.code : null;
  const details = candidate.details;
  const range = details && typeof details === "object" && "range" in details
    ? details.range
    : undefined;
  const restored = details && typeof details === "object" && "authorDocument" in details
    ? (details as { readonly authorDocument?: unknown }).authorDocument
    : undefined;
  return {
    message: candidate.message,
    diagnostic: {
      message: candidate.message,
      code,
      range: isFormulaTextRange(range) ? range : null,
    },
    restoredDocument: isFormulaAuthorDocument(restored) ? restored : null,
  };
}

function diagnosticFromError(error: unknown): FormulaDraftDiagnostic {
  if (error instanceof ProductRpcError) {
    return {
      message: error.message,
      code: error.code,
      range: isFormulaTextRange(error.details?.range) ? error.details.range : null,
    };
  }
  const candidate = error as { readonly code?: unknown };
  return {
    message: errorMessage(error),
    code: typeof candidate?.code === "string" ? candidate.code : null,
    range: null,
  };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function abortedRequest(signal: AbortSignal): Promise<never> {
  return new Promise((_resolve, reject) => {
    signal.addEventListener(
      "abort",
      () => reject(new DOMException("cancelled", "AbortError")),
      { once: true },
    );
  });
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}
