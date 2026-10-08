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

  it("同步退役完整创建状态，阻止新 workspace 的插入结果沿用旧关联", async () => {
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
      attachExistingTarget: vi.fn(),
    });
    const {
      controller,
      selectTable,
      navigateTables,
      reportInfo,
      reportSuccess,
      reportError,
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
    expect(controller.pendingCreation.value).not.toBeNull();
    vi.clearAllMocks();

    const retirement = controller.dispatch({ type: "scope.retire" });
    expect(controller.pendingCreation.value).toBeNull();
    expect(controller.state).toEqual(expect.objectContaining({
      show: false,
      rowKey: null,
      descriptor: null,
      candidates: [],
      loading: false,
      applying: false,
    }));
    expect(selectTable).not.toHaveBeenCalled();
    expect(navigateTables).not.toHaveBeenCalled();
    expect(reportInfo).not.toHaveBeenCalled();
    await retirement;

    await controller.dispatch({
      type: "pending.complete",
      result: {
        rowKey: "customer-from-workspace-b",
        row: { name: "Workspace B customer" },
        revision: {
          databaseSessionId: "workspace-b",
          schemaRevision: "schema-customers",
          dataRevision: 1,
        },
      },
    });
    expect(service.attachExistingTarget).not.toHaveBeenCalled();
    expect(selectTable).not.toHaveBeenCalled();
    expect(reportSuccess).not.toHaveBeenCalled();
    expect(reportError).not.toHaveBeenCalled();
    scope.stop();
  });

  it("退役后忽略已经在途的旧 workspace 自动关联结果", async () => {
    const related = { ...descriptor, relatedCollection: "customers" };
    const pendingAttach = deferred<{
      outcome: "committed";
      current: RelationTargetRef;
      schemaRevision: string;
      requestId: string;
    }>();
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
      attachExistingTarget: vi.fn(() => pendingAttach.promise),
    });
    const {
      controller,
      selectTable,
      reportSuccess,
      reportError,
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
    vi.clearAllMocks();

    const completion = controller.dispatch({
      type: "pending.complete",
      result: {
        rowKey: "customer-2",
        row: { name: "客户二" },
        revision: {
          databaseSessionId: "workspace-a",
          schemaRevision: "schema-customers",
          dataRevision: 2,
        },
      },
    });
    await vi.waitFor(() => expect(service.attachExistingTarget).toHaveBeenCalledOnce());
    await controller.dispatch({ type: "scope.retire" });
    pendingAttach.resolve({
      outcome: "committed",
      current: { collection: "customers", itemId: "customer-2", label: "客户二" },
      schemaRevision: "schema",
      requestId: "attach-request",
    });
    await completion;

    expect(controller.pendingCreation.value).toBeNull();
    expect(selectTable).not.toHaveBeenCalled();
    expect(reportSuccess).not.toHaveBeenCalled();
    expect(reportError).not.toHaveBeenCalled();
    scope.stop();
  });

  it("旧 workspace 创建目标晚到时不得对新 editor 发起关联写入", async () => {
    const pendingCreate = deferred<{
      outcome: "committed";
      target: RelationTargetRef;
      requestId: string;
    }>();
    const service = servicePort({
      searchTargets: vi.fn(async () => ({ items: [], total: 0 })),
      createTarget: vi.fn(() => pendingCreate.promise),
      updateSingle: vi.fn(),
    });
    const { controller, scope } = setup(service);
    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor,
      value: null,
    });
    const oldCreation = controller.dispatch({ type: "target.create", label: "Workspace A" });
    await vi.waitFor(() => expect(service.createTarget).toHaveBeenCalledOnce());

    await controller.dispatch({ type: "scope.retire" });
    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-2",
      field: "customer",
      descriptor,
      value: null,
    });
    pendingCreate.resolve({
      outcome: "committed",
      target: { collection: "customers", itemId: "customer-a", label: "Workspace A" },
      requestId: "create-a",
    });
    await oldCreation;

    expect(service.updateSingle).not.toHaveBeenCalled();
    expect(controller.state).toEqual(expect.objectContaining({
      show: true,
      rowKey: "row-2",
      field: "customer",
      applying: false,
      candidates: [],
    }));
    scope.stop();
  });
});

describe("relationEditorController targets.refresh", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("目标写入后按已选ID刷新标签（不依赖当前搜索候选），保留未提交暂选集合", async () => {
    const stale: RelationTargetRef = {
      collection: "customers", itemId: "customer0000001", label: "旧名称",
      secondaryLabel: "CT-001", displayValue: "旧名称", secondaryValue: "CT-001",
    };
    const fresh: RelationTargetRef = {
      collection: "customers", itemId: "customer0000001", label: "新名称",
      secondaryLabel: "CT-001", displayValue: "新名称", secondaryValue: "CT-001",
    };
    const unrelated: RelationTargetRef = {
      collection: "customers", itemId: "customer0000002", label: "无关候选",
    };
    const searchTargets = vi.fn()
      .mockResolvedValueOnce({ items: [stale], total: 1 })
      .mockResolvedValueOnce({ items: [unrelated], total: 1 })
      .mockResolvedValueOnce({ items: [unrelated], total: 1 })
      .mockResolvedValueOnce({ items: [fresh], total: 1 });
    const service = servicePort({
      searchTargets,
      describeCollection: vi.fn(async () => ({
        ...useRelationLookupStore().schema!, normalizedRelations: [],
      })),
      loadDraft: vi.fn(async () => ({
        delta: {}, current: [stale], diagnostics: [], canApply: true,
      }) as unknown as import("@/contracts").RelationDeltaPreview),
    });
    const { controller, relations } = setup(service);

    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor: { ...descriptor, kind: "m2m", relatedCollection: "customers" },
      value: [{ collection: "customers", itemId: "customer0000001", label: "customer0000001" }],
    });
    await flushPromises();
    relations.openDraft("orders.customer", "row-1", [stale]);
    expect(relations.draft?.selected.map(target => target.label)).toEqual(["旧名称"]);
    // 搜索无关词：当前候选不再包含已选目标。
    await controller.dispatch({ type: "targets.search", query: "无关词" });
    await flushPromises();
    // 用户在刷新前暂选（toggle 掉既有项）：
    controller.dispatch({ type: "target.select", target: relations.draft!.selected[0] });
    await flushPromises();
    expect(relations.draft?.selected).toHaveLength(0);

    await controller.dispatch({ type: "targets.refresh" });
    await flushPromises();
    // 暂选集合（空）未被替换；已选/原始标签按ID批量刷新，不静默沿用旧标签。
    expect(relations.draft?.selected).toHaveLength(0);
    expect(relations.draft?.original[0]).toMatchObject({ itemId: "customer0000001", label: "新名称" });
    // 候选仍由关键词搜索结果提供，未被 ID 刷新覆盖。
    expect(searchTargets).toHaveBeenCalledTimes(4);
    expect(searchTargets.mock.calls[3][0]).toMatchObject({
      relationId: "orders.customer",
      targetItemIds: ["customer0000001"],
    });
  });

  it("显示字段配置变化后 picker 采用新渲染契约", async () => {
    const updatedDescriptor: NormalizedRelationDescriptor = {
      ...descriptor, kind: "m2m", relatedCollection: "customers",
      displayFieldId: "fld_amount",
      displayFieldInfo: { fieldId: "fld_amount", dataType: "decimal", display: null },
    };
    const service = servicePort({
      searchTargets: vi.fn(async () => ({ items: [], total: 0 })),
      describeCollection: vi.fn(async () => ({
        collection: "orders",
        primaryKey: "id",
        primaryDisplayFieldId: "",
        columns: [],
        normalizedRelations: [updatedDescriptor],
        schemaRevision: "schema-2",
        permissionRevision: "permission",
        capabilityHash: "capability",
        lookupRevision: "lookup",
      })),
    });
    const { controller } = setup(service);
    await controller.dispatch({
      type: "editor.open",
      rowKey: "row-1",
      field: "customer",
      descriptor: { ...descriptor, kind: "m2m", relatedCollection: "customers" },
      value: [],
    });
    await flushPromises();
    await controller.dispatch({ type: "targets.refresh" });
    await flushPromises();
    expect(controller.state.descriptor).toBe(updatedDescriptor);
    expect(service.describeCollection).toHaveBeenLastCalledWith("orders");
  });

  it("关键词变化不取消已选标签刷新，关闭重开拒绝迟到刷新", async () => {
    const pending = deferred<RelationSearchResult>();
    const stale = { collection: "customers", itemId: "CT001", label: "旧名称" };
    const searchTargets = vi.fn(async (request: import("@/contracts").RelationSearchParams): Promise<RelationSearchResult> =>
      request.targetItemIds ? { items: [stale], total: 1 } : { items: [], total: 0 });
    const service = servicePort({
      searchTargets,
      describeCollection: vi.fn(async () => ({ ...useRelationLookupStore().schema!, normalizedRelations: [] })),
    });
    const { controller, relations } = setup(service);
    const open = () => controller.dispatch({
      type: "editor.open", rowKey: "row-1", field: "customer", descriptor, value: [stale],
    });
    await open();
    searchTargets.mockImplementation(async request => request.targetItemIds ? pending.promise : { items: [], total: 0 });
    const refresh = controller.dispatch({ type: "targets.refresh" });
    await flushPromises();
    await controller.dispatch({ type: "targets.search", query: "无关词" });
    pending.resolve({ items: [{ ...stale, label: "城轨一期" }], total: 1 });
    await refresh;
    expect(relations.draft?.selected[0]?.label).toBe("城轨一期");
    expect(controller.state.query).toBe("无关词");
    const late = deferred<RelationSearchResult>();
    searchTargets.mockImplementation(async request => request.targetItemIds ? late.promise : { items: [], total: 0 });
    const retired = controller.dispatch({ type: "targets.refresh" });
    await flushPromises();
    await controller.dispatch({ type: "editor.close" });
    searchTargets.mockImplementation(async request => request.targetItemIds ? { items: [stale], total: 1 } : { items: [], total: 0 });
    await open();
    late.resolve({ items: [{ ...stale, label: "迟到名称" }], total: 1 });
    await retired;
    expect(relations.draft?.selected[0]?.label).toBe("旧名称");
  });

  it("已选目标被删除后保留暂选身份并退回ID，不保留旧标签", async () => {
    const service = servicePort({ searchTargets: vi.fn(async () => ({ items: [], total: 0 })) });
    const { controller, relations } = setup(service);
    await controller.dispatch({ type: "editor.open", rowKey: "row-1", field: "customer", descriptor,
      value: [{ collection: "customers", itemId: "missing", label: "已删除的旧名称" }],
    });
    expect(relations.draft?.selected[0]).toMatchObject({ itemId: "missing", label: "missing" });
  });

  it("显示契约刷新不使正在提交的同一关系回包失效", async () => {
    const pending = deferred<import("@/contracts").RelationSingleUpdateResult>();
    const target = { collection: "customers", itemId: "CT001", label: "城轨一期" };
    const service = servicePort({
      searchTargets: vi.fn(async () => ({ items: [target], total: 1 })),
      updateSingle: vi.fn(() => pending.promise),
      describeCollection: vi.fn(async () => ({ ...useRelationLookupStore().schema!, normalizedRelations: [{ ...descriptor, displayFieldId: "name" }] })),
    });
    const { controller, table } = setup(service);
    await controller.dispatch({ type: "editor.open", rowKey: "row-1", field: "customer", descriptor, value: null });
    const applying = controller.dispatch({ type: "target.select", target });
    await controller.dispatch({ type: "targets.refresh" });
    pending.resolve({ outcome: "committed", current: target, schemaRevision: "schema", requestId: "commit" });
    await applying;
    expect(table.allRows[0]?.customer).toEqual(target);
    expect(controller.state.show).toBe(false);
  });

  it("初次打开的迟到已选标签不能覆盖后来的目标刷新", async () => {
    const initial = deferred<RelationSearchResult>();
    const target = { collection: "customers", itemId: "CT001", label: "城轨一期更新" };
    const related = { ...descriptor, relatedCollection: "customers" };
    let idReads = 0;
    const service = servicePort({
      searchTargets: vi.fn(async request => request.targetItemIds
        ? (++idReads === 1 ? initial.promise : { items: [target], total: 1 })
        : { items: [], total: 0 }),
      describeCollection: vi.fn(async () => ({ ...useRelationLookupStore().schema!, normalizedRelations: [related] })),
    });
    const { controller, relations } = setup(service);
    const opening = controller.dispatch({ type: "editor.open", rowKey: "row-1", field: "customer", descriptor: related, value: [target.itemId] });
    await flushPromises();
    await controller.dispatch({ type: "targets.refresh" });
    initial.resolve({ items: [{ ...target, label: "迟到旧名称" }], total: 1 });
    await opening;
    expect(relations.draft?.selected[0]?.label).toBe("城轨一期更新");
  });

  it("编辑器未打开时 refresh 不发起搜索", async () => {
    const service = servicePort({ searchTargets: vi.fn() });
    const { controller } = setup(service);
    await controller.dispatch({ type: "targets.refresh" });
    expect(service.searchTargets).not.toHaveBeenCalled();
  });
});
