import { BridgeOperationError } from "@/bridge/hostBridge";
import type {
  FieldApplyReceiptV2,
  FieldChangeActionV2,
  FormulaDraftValidationResult,
  LogicalTypeV2,
  ProductFieldDefinition,
  ProductTableDefinition,
  SchemaDescribeResult,
  SchemaSnapshot,
} from "@/contracts";
import {
  parseFieldApplyReceiptV2,
  parseFieldChangePlanV2,
  parseFieldMigrationStatusV2,
  parseFieldRecycleBinResultV2,
  parseFieldSettingsDescribeResultV2,
} from "@/contracts";
import { useHostBridge } from "@/services/bridgeContext";
import { useFieldSettingsStore } from "./store";
import { buildFieldChangeIntent } from "./model";
import { useWorkspaceStore } from "@/stores/workspaceStore";
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

function isProductTableDefinition(
  value: unknown,
  tableId: string,
): value is ProductTableDefinition {
  return !!value && typeof value === "object"
    && (value as { tableId?: unknown }).tableId === tableId
    && Array.isArray((value as { fields?: unknown }).fields);
}

function formulaPreviewType(resultType: LogicalTypeV2): {
  readonly dataType: NonNullable<ProductFieldDefinition["formula"]>["resultType"];
  readonly storageType: ProductFieldDefinition["storageType"];
} {
  switch (resultType) {
    case "text": return { dataType: "shortText", storageType: "text" };
    case "number": return { dataType: "float", storageType: "number" };
    case "bool": return { dataType: "boolean", storageType: "bool" };
    case "date": return { dataType: "date", storageType: "date" };
    case "dateTime": return { dataType: "dateTime", storageType: "date" };
    case "time": return { dataType: "time", storageType: "text" };
    default: throw new Error(`公式预览不支持结果类型 ${resultType}`);
  }
}

function buildFormulaPreviewDefinition(
  definition: ProductTableDefinition,
  validation: FormulaDraftValidationResult,
  existingFieldId: string | undefined,
  displayName: string,
): { readonly definition: ProductTableDefinition; readonly field: ProductFieldDefinition } {
  const mapped = formulaPreviewType(validation.resultType);
  const fieldId = existingFieldId ?? "fld_formula_preview";
  const existing = definition.fields.find(field => field.fieldId === fieldId);
  const field: ProductFieldDefinition = {
    ...(existing ?? {
      fieldId,
      physicalName: "f_formula_preview",
      displayName: displayName || "公式预览",
      nullable: true,
      defaultValue: null,
      constraints: [],
      editor: { kind: "formula", config: {} },
      relation: null,
      lookup: null,
      attachmentPolicy: null,
    }),
    kind: "formula",
    dataType: "formula",
    storageType: mapped.storageType,
    readOnly: true,
    formula: {
      language: "cel-v1",
      source: validation.canonicalSource,
      resultType: mapped.dataType,
      version: 1,
      status: "ready",
    },
  };
  const fields = existing
    ? definition.fields.map(item => item.fieldId === fieldId ? field : item)
    : [...definition.fields, field];
  return { definition: { ...definition, fields }, field };
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
  loadFormulaCatalog: () => Promise<void>;
  validateFormulaDraft: (displaySource: string) => Promise<void>;
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
  let formulaProductDefinition: ProductTableDefinition | null = null;

  async function describe(
    tableId: string,
    fieldId?: string,
    preferredType?: LogicalTypeV2,
  ): Promise<void> {
    const current = ++generation;
    formulaPreview.cancel();
    formulaProductDefinition = null;
    store.beginOpen();
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
    stopPolling();
    store.close();
    return true;
  }

  async function plan(nextAction?: FieldChangeActionV2): Promise<void> {
    if (!store.result) return;
    const action = nextAction ?? store.action;
    store.beginPlan(action);
    try {
      const intent = buildFieldChangeIntent({
        action,
        result: store.result,
        draft: ["retire", "restore", "purge"].includes(action) ? null : store.draft,
        conversionRule: store.conversionRule,
        confirmation: store.confirmation,
        backupReceipt: store.backupReceipt,
        relationPair: action === "create" && store.draft?.logicalType === "relation"
          ? store.relationPair
          : null,
      });
      store.setPlan(parseFieldChangePlanV2(
        unwrapFieldResult(await bridge.request("field.change.plan", intent)),
      ));
      frozenOperationId = null;
    } catch (error) {
      store.fail(error);
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
    const tableId = store.result.tableId;
    const tables = workspace.collections.map(item => ({
      tableId: item.collection,
      displayName: item.displayName ?? workspace.displayNames[item.collection] ?? item.collection,
    }));
    if (!tables.some(item => item.tableId === tableId)) {
      tables.unshift({ tableId, displayName: workspace.displayNames[tableId] ?? tableId });
    }
    store.setRelationTables(tables);
    if (store.relationPair && !store.relationPair.reciprocalDisplayName) {
      const source = tables.find(item => item.tableId === tableId);
      store.patchRelationPair({ reciprocalDisplayName: source?.displayName ?? "关联记录" });
    }
    try {
      store.beginRelationCatalog();
      const sourceSchema = await describeRelationTable(tableId);
      store.setRelationSchema("source", sourceSchema);
      if (store.relationPair && !store.relationPair.sourceDisplayFieldId) {
        store.patchRelationPair({
          sourceDisplayFieldId: sourceSchema.primaryDisplayFieldId
            || sourceSchema.columns.find(column => column.fieldId && column.kind !== "system")?.fieldId
            || "",
        });
      }
      const targetTableId = store.draft.relation?.targetTableId;
      if (targetTableId) await selectRelationTarget(targetTableId);
    } catch (error) {
      store.failRelationCatalog(error);
    }
  }

  async function selectRelationTarget(tableId: string): Promise<void> {
    if (!store.draft?.relation) return;
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
    await loadLookupSchemas(store.draft.lookup.path);
  }

  async function resolveLookupPath(
    path: readonly { readonly relationFieldId: string }[],
  ): Promise<void> {
    if (!store.result || store.draft?.logicalType !== "lookup" || !store.draft.lookup) return;
    await loadLookupSchemas(path);
  }

  async function loadLookupSchemas(
    path: readonly { readonly relationFieldId: string }[],
  ): Promise<void> {
    if (!store.result) return;
    try {
      store.beginLookupCatalog();
      const schemas: SchemaSnapshot[] = [await describeRelationTable(store.result.tableId)];
      for (const [index, step] of path.entries()) {
        if (!step.relationFieldId) break;
        const current = schemas[index]!;
        const relation = current.normalizedRelations.find(
          item => item.relationId === `${current.collection}.${step.relationFieldId}`,
        );
        if (!relation?.relatedCollection || relation.kind === "m2a" || relation.junction) {
          throw new Error(`第 ${index + 1} 跳不是可用于 Lookup 的直接关系`);
        }
        schemas.push(await describeRelationTable(relation.relatedCollection));
      }
      store.setLookupSchemas(schemas);
    } catch (error) {
      store.failLookupCatalog(error);
    }
  }

  async function loadFormulaCatalog(): Promise<void> {
    if (!store.result || store.draft?.logicalType !== "formula") return;
    try {
      store.beginFormulaCatalog();
      const [source, productDefinition] = await Promise.all([
        describeRelationTable(store.result.tableId),
        bridge.request("schema.getTable", { tableId: store.result.tableId }),
      ]);
      if (!isProductTableDefinition(productDefinition, store.result.tableId)) {
        throw new Error("公式预览表结构响应无效");
      }
      formulaProductDefinition = productDefinition;
      const relations = source.columns.flatMap(column => {
        if (!column.fieldId || column.kind !== "relation" || !column.relationId) return [];
        const descriptor = source.normalizedRelations.find(
          item => item.relationId === column.relationId,
        );
        if (!descriptor?.relatedCollection || descriptor.kind === "m2a" || descriptor.junction) {
          return [];
        }
        return [{ fieldId: column.fieldId, tableId: descriptor.relatedCollection }];
      });
      const resolved = await Promise.all(relations.map(async relation => ({
        fieldId: relation.fieldId,
        schema: await describeRelationTable(relation.tableId),
      })));
      store.setFormulaCatalog(source, Object.fromEntries(
        resolved.map(item => [item.fieldId, item.schema]),
      ));
    } catch (error) {
      store.failFormulaCatalog(error);
    }
  }

  async function validateFormulaDraft(displaySource: string): Promise<void> {
    const tableId = store.result?.tableId;
    if (!tableId || store.draft?.logicalType !== "formula") return;
    const current = ++formulaValidationGeneration;
    formulaPreview.cancel();
    store.resetFormulaPreview();
    store.beginFormulaValidation(displaySource);
    try {
      const result = unwrapFieldResult(await bridge.request("formula.draft.validate", {
        tableId,
        displaySource,
      }));
      if (!isFormulaDraftValidation(result)) {
        throw new Error("公式校验返回了无效结果");
      }
      if (current === formulaValidationGeneration) {
        store.setFormulaValidation(displaySource, result);
        scheduleFormulaPreview(result);
      }
    } catch (error) {
      if (current === formulaValidationGeneration) {
        store.failFormulaValidation(displaySource, error);
      }
    }
  }

  function scheduleFormulaPreview(validation: FormulaDraftValidationResult): void {
    const definition = formulaProductDefinition;
    const row = tableStore.allRows[0];
    if (!definition) {
      store.setFormulaPreviewNote("正在等待权威表结构，完成后可预览样例结果");
      return;
    }
    if (!row) {
      store.setFormulaPreviewNote("当前表没有可用于预览的样例记录");
      return;
    }
    const preview = buildFormulaPreviewDefinition(
      definition,
      validation,
      store.result?.definition?.identity.fieldId,
      store.draft?.displayName ?? "公式预览",
    );
    const physicalNames = new Set(preview.definition.fields.map(item => item.physicalName));
    const sample = Object.fromEntries(
      Object.entries(row).filter(([key]) => physicalNames.has(key) || key === "id"),
    );
    store.beginFormulaPreview();
    formulaPreview.schedule({
      definition: preview.definition,
      row: sample,
      changedFieldIds: [],
    }, {
      onResult: result => store.setFormulaPreview(result.values[preview.field.physicalName]),
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
    generation += 1;
    formulaValidationGeneration += 1;
    formulaPreview.dispose();
    stopPolling();
    frozenOperationId = null;
  }

  return {
    openCreate, openEdit, requestClose, plan, apply, refreshMigration,
    cancelMigration, loadRecycleBin, restore, loadRelationCatalog,
    selectRelationTarget, loadLookupCatalog, resolveLookupPath,
    loadFormulaCatalog, validateFormulaDraft, dispose,
  };
}

function isFormulaDraftValidation(value: unknown): value is FormulaDraftValidationResult {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<FormulaDraftValidationResult>;
  return typeof candidate.canonicalSource === "string"
    && typeof candidate.resultType === "string"
    && Array.isArray(candidate.dependencies)
    && Array.isArray(candidate.relationAggregatePaths);
}
