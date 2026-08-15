import { flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { effectScope } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  FieldDefinitionV2,
  LogicalTypeV2,
  NormalizedRelationDescriptor,
  RelationDeltaPreview,
  RelationTargetRef,
  RelationSearchResult,
  SchemaSnapshotV2,
} from "@/contracts";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import {
  createRelationEditorController,
  type RelationEditorServicePort,
} from "./relationEditorController";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

const descriptor: NormalizedRelationDescriptor = {
  relationId: "orders.customer",
  fieldRef: "customer",
  sourceCollection: "orders",
  kind: "m2o",
  relatedCollection: null,
  unique: false,
  nullable: true,
  onDelete: "nullify",
  preset: "standard",
  selfRelation: false,
  managed: true,
  state: "valid",
  diagnostics: [],
};

function field(
  fieldId: string,
  physicalName: string,
  displayName: string,
  logicalType: LogicalTypeV2,
): FieldDefinitionV2 {
  return {
    contract: "vibetable.schema.v2",
    identity: { fieldId, physicalName, providerFieldId: `pb_${fieldId}` },
    displayName,
    help: "",
    logicalType,
    lifecycle: { state: "active", retiredAt: null },
    value: {
      required: false,
      default: { enabled: false, value: null, source: "recommended", defaultsVersion: 1 },
      presence: { mode: "native" },
    },
    constraints: {
      unique: { enabled: false, blankPolicy: "ignoreMissing" },
      range: { min: null, max: null },
      length: { min: null, max: null },
      pattern: { enabled: false, value: "" },
      domains: { only: [], except: [] },
      selection: { min: 0, max: null },
    },
    storage: {
      kind: "pocketbase-text",
      options: { onlyInt: false, maxSize: 0, convertURLs: false, presentable: true },
    },
    display: {
      kind: "text",
      preset: "default",
      displayScale: 0,
      scaleMode: "fixed",
      trimTrailingZeros: false,
      useGrouping: false,
      currency: "",
      percentStorage: "ratio",
      unit: null,
      precision: "exact",
      timezone: "local",
      mode: "plain",
      trueLabel: "是",
      falseLabel: "否",
    },
  };
}

const customerDefinition: SchemaSnapshotV2 = {
  contract: "vibetable.schema.v2",
  tableId: "customers",
  displayName: "客户",
  kind: "base",
  schemaRevision: "schema-customers",
  dataRevision: 1,
  archivePolicy: { mode: "none", fieldId: null, archivedValue: null },
  fields: [field("customer-name", "name", "名称", "text")],
  capabilities: [],
};

function servicePort(
  overrides: Partial<RelationEditorServicePort>,
): RelationEditorServicePort {
  const unexpected = async (): Promise<never> => { throw new Error("unexpected relation call"); };
  return {
    describeCollection: vi.fn(unexpected),
    searchTargets: vi.fn(unexpected),
    loadDraft: vi.fn(unexpected),
    updateSingle: vi.fn(unexpected),
    createTarget: vi.fn(unexpected),
    attachExistingTarget: vi.fn(unexpected),
    applyDraft: vi.fn(unexpected),
    ...overrides,
  };
}

type SetupOverrides = Partial<Pick<
  Parameters<typeof createRelationEditorController>[0],
  | "getTableDefinition" | "selectTable" | "navigateTables"
  | "openTarget" | "reportInfo" | "reportSuccess" | "reportError"
>>;

function setup(service: RelationEditorServicePort, overrides: SetupOverrides = {}) {
  const workspace = useWorkspaceStore();
  const table = useTableStore();
  const relations = useRelationLookupStore();
  workspace.selectTable("orders");
  table.setDatasetReady({
    table: "orders",
    columns: [{
      name: "customer",
      title: "客户",
      dataType: "json",
      editable: true,
      nullable: true,
      kind: "relation",
      relationId: descriptor.relationId,
    }],
    rows: [{ rowKey: "row-1", customer: null }],
    offset: 0,
    limit: 100,
    totalRows: 1,
    mode: "remote",
    revision: { databaseSessionId: "session", schemaRevision: "schema", dataRevision: 1 },
  });
  relations.capabilities = {
    contract: "vibetable.relation-capabilities.v1",
    relationReadV1: true,
    relationEditV1: true,
    lookupQueryV1: true,
  };
  relations.schema = {
    collection: "orders",
    primaryKey: "id",
    primaryDisplayFieldId: "order-name",
    columns: [],
    normalizedRelations: [],
    schemaRevision: "schema",
    permissionRevision: "permission",
    capabilityHash: "capability",
    lookupRevision: "lookup",
  };
  const selectTable = overrides.selectTable ?? vi.fn(collection => workspace.selectTable(collection));
  const navigateTables = overrides.navigateTables ?? vi.fn();
  const reportInfo = overrides.reportInfo ?? vi.fn();
  const reportSuccess = overrides.reportSuccess ?? vi.fn();
  const reportError = overrides.reportError ?? vi.fn();
  const scope = effectScope();
  const controller = scope.run(() => createRelationEditorController({
    workspace,
    table,
    relations,
    service,
    getTableDefinition: overrides.getTableDefinition ?? vi.fn(),
    selectTable,
    navigateTables,
    openTarget: overrides.openTarget ?? vi.fn(),
    reportInfo,
    reportSuccess,
    reportError,
    unsupportedError: () => "unsupported",
    changedError: () => "changed",
  }))!;
  return {
    controller,
    table,
    relations,
    workspace,
    selectTable,
    navigateTables,
    reportInfo,
    reportSuccess,
    reportError,
    scope,
  };
}

describe("relationEditorController", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("关闭编辑器后丢弃尚未完成的目标搜索响应", async () => {
    const pending = deferred<RelationSearchResult>();
    const service = servicePort({
      searchTargets: vi.fn(() => pending.promise),
    });
    const { controller, scope } = setup(service);

    const opening = controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor,
      value: null,
    });
    await flushPromises();
    await controller.dispatch({ type: "editor.close" });
    pending.resolve({ items: [{ collection: "customers", itemId: "c1", label: "客户一" }], total: 1 });
    await opening;

    expect(controller.state.show).toBe(false);
    expect(controller.state.candidates).toEqual([]);
    scope.stop();
  });

  it("A 的草稿晚到时不得覆盖关闭后打开的 B 编辑器草稿", async () => {
    const pending = deferred<void>();
    const relations = useRelationLookupStore();
    const first = { ...descriptor, relationId: "orders.tags", fieldRef: "tags", kind: "m2m" as const };
    const secondTarget: RelationTargetRef = {
      collection: "customers",
      itemId: "c2",
      label: "客户二",
    };
    const service = servicePort({
      loadDraft: vi.fn(async (
        relationId: string,
        sourceItemId: string,
        _expectedDateUpdated?: string | null,
        isCurrent: () => boolean = () => true,
      ) => {
        await pending.promise;
        if (isCurrent()) relations.openDraft(relationId, sourceItemId, []);
        const preview: RelationDeltaPreview = {
          delta: {
            relationId,
            sourceItemId,
            expectedSchemaRevision: "schema",
            adds: [],
            removes: [],
            idempotencyKey: "operation",
          },
          current: [],
          diagnostics: [],
          canApply: true,
        };
        return preview;
      }),
      searchTargets: vi.fn(async () => ({ items: [], total: 0 })),
    });
    const { controller, scope } = setup(service);

    const openingFirst = controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "tags",
      descriptor: first,
      value: [],
    });
    await flushPromises();
    await controller.dispatch({ type: "editor.close" });
    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-2",
      field: "customer",
      descriptor,
      value: secondTarget,
    });
    expect(relations.draft?.relationId).toBe(descriptor.relationId);

    pending.resolve();
    await openingFirst;

    expect(relations.draft).toEqual(expect.objectContaining({
      relationId: descriptor.relationId,
      sourceItemId: "row-2",
    }));
    scope.stop();
  });

  it("通过单一意图提交 m2o，并只应用服务端返回的规范值", async () => {
    const target: RelationTargetRef = {
      collection: "customers",
      itemId: "c1",
      label: "客户一",
    };
    const canonical = { ...target, label: "客户 001" };
    const service = servicePort({
      searchTargets: vi.fn(async () => ({ items: [target], total: 1 })),
      updateSingle: vi.fn(async () => ({
        outcome: "committed" as const,
        current: canonical,
        schemaRevision: "schema",
        requestId: "request-1",
      })),
    });
    const { controller, table, scope } = setup(service);

    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor,
      value: null,
    });
    await controller.dispatch({ type: "target.select", target });

    expect(service.updateSingle).toHaveBeenCalledWith(descriptor.relationId, "row-1", target);
    expect(table.allRows[0]?.customer).toEqual(canonical);
    expect(controller.state.show).toBe(false);
    scope.stop();
  });

  it("通过完整创建接口写入目标字段并把规范关联值应用回源记录", async () => {
    const related = { ...descriptor, relatedCollection: "customers" };
    const target: RelationTargetRef = {
      collection: "customers",
      itemId: "customer-1",
      label: "客户一",
    };
    const service = servicePort({
      describeCollection: vi.fn(async () => ({
        collection: "customers",
        primaryKey: "id",
        primaryDisplayFieldId: "customer-name",
        columns: [],
        normalizedRelations: [],
        schemaRevision: "schema-customers",
        permissionRevision: "permission",
        capabilityHash: "capability",
        lookupRevision: "lookup",
      })),
      searchTargets: vi.fn(async () => ({ items: [], total: 0 })),
      createTarget: vi.fn(async () => ({
        outcome: "committed" as const,
        target,
        requestId: "create-request",
      })),
      updateSingle: vi.fn(async () => ({
        outcome: "committed" as const,
        current: target,
        schemaRevision: "schema",
        requestId: "attach-request",
      })),
    });
    const { controller, table, scope } = setup(service, {
      getTableDefinition: vi.fn(async () => customerDefinition),
    });

    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor: related,
      value: null,
    });
    await flushPromises();
    await controller.dispatch({ type: "target.createFull", values: { name: "客户一" } });

    expect(service.createTarget).toHaveBeenCalledWith(
      related.relationId,
      "客户一",
      { name: "客户一" },
    );
    expect(service.updateSingle).toHaveBeenCalledWith(related.relationId, "row-1", target);
    expect(table.allRows[0]?.customer).toEqual(target);
    expect(controller.state.show).toBe(false);
    scope.stop();
  });

  it("完整编辑创建成功后自动关联并返回源表，同时清除 pending", async () => {
    const related = { ...descriptor, relatedCollection: "customers" };
    const service = servicePort({
      describeCollection: vi.fn(async () => ({
        collection: "customers",
        primaryKey: "id",
        primaryDisplayFieldId: "customer-name",
        columns: [],
        normalizedRelations: [],
        schemaRevision: "schema-customers",
        permissionRevision: "permission",
        capabilityHash: "capability",
        lookupRevision: "lookup",
      })),
      searchTargets: vi.fn(async () => ({ items: [], total: 0 })),
      attachExistingTarget: vi.fn(async () => ({
        outcome: "committed" as const,
        current: { collection: "customers", itemId: "customer-2", label: "客户二" },
        schemaRevision: "schema",
        requestId: "attach-request",
      })),
    });
    const {
      controller,
      workspace,
      selectTable,
      navigateTables,
      reportSuccess,
      scope,
    } = setup(service, { getTableDefinition: vi.fn(async () => customerDefinition) });

    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor: related,
      value: null,
    });
    await flushPromises();
    await controller.dispatch({ type: "target.openFullEditor" });

    expect(controller.pendingCreation.value).toEqual(expect.objectContaining({
      sourceCollection: "orders",
      sourceItemId: "row-1",
      targetCollection: "customers",
      targetDisplayField: "name",
    }));
    expect(workspace.currentTable).toBe("customers");
    expect(navigateTables).toHaveBeenCalledOnce();

    await controller.dispatch({
      type: "pending.complete",
      result: {
        rowKey: "customer-2",
        row: { name: "客户二" },
        revision: {
          databaseSessionId: "session",
          schemaRevision: "schema-customers",
          dataRevision: 2,
        },
      },
    });

    expect(service.attachExistingTarget).toHaveBeenCalledWith(
      related.relationId,
      "row-1",
      { collection: "customers", itemId: "customer-2", label: "客户二" },
      "m2o",
      "schema",
    );
    expect(controller.pendingCreation.value).toBeNull();
    expect(selectTable).toHaveBeenLastCalledWith("orders");
    expect(reportSuccess).toHaveBeenCalledWith("已创建记录并写入“客户”");
    scope.stop();
  });
});
