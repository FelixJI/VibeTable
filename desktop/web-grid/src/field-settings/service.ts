import { BridgeOperationError } from "@/bridge/hostBridge";
import type {
  FieldApplyReceiptV2,
  FieldChangeActionV2,
  LogicalTypeV2,
  SchemaDescribeResult,
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
  dispose: () => void;
} {
  const bridge = useHostBridge();
  const store = useFieldSettingsStore();
  const workspace = useWorkspaceStore();
  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let frozenOperationId: string | null = null;

  async function describe(
    tableId: string,
    fieldId?: string,
    preferredType?: LogicalTypeV2,
  ): Promise<void> {
    const current = ++generation;
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

  async function describeRelationTable(tableId: string) {
    const result = await bridge.request("schema.describe", {
      collection: tableId,
      requestGeneration: 0,
      accepts: RELATION_ACCEPTS,
    }) as SchemaDescribeResult;
    if (result.contract !== "vibetable.schema-describe.v1" || result.collection !== tableId) {
      throw new Error("关联字段目录响应与所选数据表不匹配");
    }
    return result.schema;
  }

  function dispose(): void {
    generation += 1;
    stopPolling();
    frozenOperationId = null;
  }

  return {
    openCreate, openEdit, requestClose, plan, apply, refreshMigration,
    cancelMigration, loadRecycleBin, restore, loadRelationCatalog,
    selectRelationTarget, dispose,
  };
}
