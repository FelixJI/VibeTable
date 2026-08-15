import { shallowReactive, shallowRef, watch, type Ref } from "vue";

import type {
  FieldDefinitionV2,
  InsertRowResult,
  NormalizedRelationDescriptor,
  RelationTargetRef,
  SchemaSnapshotV2,
} from "@/contracts";
import { normalizeTargets } from "@/grid/relationLookupRenderer";
import type { useRelationLookupService } from "@/services/relationLookupService";
import type { useRelationLookupStore } from "@/stores/relationLookupStore";
import type { useTableStore } from "@/stores/tableStore";
import type { useWorkspaceStore } from "@/stores/workspaceStore";

type RelationLookupService = ReturnType<typeof useRelationLookupService>;
type RelationLookupStore = ReturnType<typeof useRelationLookupStore>;
type TableStore = ReturnType<typeof useTableStore>;
type WorkspaceStore = ReturnType<typeof useWorkspaceStore>;

type RelationStorePort = Pick<RelationLookupStore,
  "capabilities" | "schema" | "draft" | "openDraft" | "closeDraft" | "toggleDraftTarget">;
type RelationTablePort = Pick<TableStore, "schema" | "applyRelationValue">;
type RelationWorkspacePort = Pick<WorkspaceStore, "currentTable">;
export type RelationEditorServicePort = Pick<RelationLookupService,
  | "describeCollection" | "searchTargets" | "loadDraft" | "updateSingle"
  | "createTarget" | "attachExistingTarget" | "applyDraft">;

export interface RelationEditorState {
  show: boolean;
  rowKey: string | number | null;
  field: string;
  fieldLabel: string;
  descriptor: NormalizedRelationDescriptor | null;
  candidates: readonly RelationTargetRef[];
  total: number;
  query: string;
  loading: boolean;
  applying: boolean;
  error: string | null;
  targetFields: readonly FieldDefinitionV2[];
  targetRelations: readonly NormalizedRelationDescriptor[];
  targetRelationOptions: Readonly<Record<string, readonly RelationTargetRef[]>>;
  targetRelationLoading: Readonly<Record<string, boolean>>;
  targetDisplayField: string | null;
  createSchemaLoading: boolean;
}

export interface PendingRelationCreation {
  readonly sourceCollection: string;
  readonly sourceItemId: string;
  readonly relationId: string;
  readonly relationKind: NormalizedRelationDescriptor["kind"];
  readonly relationLabel: string;
  readonly targetCollection: string;
  readonly targetDisplayField: string;
  readonly expectedSchemaRevision: string;
}

export type RelationEditorIntent =
  | {
    readonly type: "editor.open";
    readonly rowKey: string | number;
    readonly field: string;
    readonly descriptor: NormalizedRelationDescriptor;
    readonly value: unknown;
  }
  | { readonly type: "editor.close" }
  | { readonly type: "targets.search"; readonly query: string; readonly offset?: number }
  | { readonly type: "targets.loadMore" }
  | { readonly type: "target.select"; readonly target: RelationTargetRef }
  | { readonly type: "target.clear" }
  | { readonly type: "draft.apply" }
  | { readonly type: "target.create"; readonly label: string }
  | { readonly type: "target.createFull"; readonly values: Readonly<Record<string, unknown>> }
  | { readonly type: "target.searchNested"; readonly field: string; readonly query: string }
  | { readonly type: "target.openFullEditor" }
  | { readonly type: "target.open"; readonly target: RelationTargetRef }
  | { readonly type: "pending.complete"; readonly result: InsertRowResult }
  | { readonly type: "pending.cancel" };

export interface RelationEditorController {
  readonly state: RelationEditorState;
  readonly pendingCreation: Ref<PendingRelationCreation | null>;
  readonly selected: readonly RelationTargetRef[];
  dispatch(intent: RelationEditorIntent): Promise<void>;
  searchFilterTargets(relationId: string, query: string): Promise<readonly RelationTargetRef[]>;
}

export interface RelationEditorDependencies {
  readonly workspace: RelationWorkspacePort;
  readonly table: RelationTablePort;
  readonly relations: RelationStorePort;
  readonly service: RelationEditorServicePort;
  readonly getTableDefinition: (collection: string) => Promise<SchemaSnapshotV2>;
  readonly selectTable: (collection: string) => void;
  readonly navigateTables: () => void;
  readonly openTarget: (target: RelationTargetRef) => void;
  readonly reportInfo: (content: string) => void;
  readonly reportSuccess: (content: string) => void;
  readonly reportError: (content: string) => void;
  readonly unsupportedError: () => string;
  readonly changedError: () => string;
}

function initialState(): RelationEditorState {
  return {
    show: false,
    rowKey: null,
    field: "",
    fieldLabel: "",
    descriptor: null,
    candidates: [],
    total: 0,
    query: "",
    loading: false,
    applying: false,
    error: null,
    targetFields: [],
    targetRelations: [],
    targetRelationOptions: {},
    targetRelationLoading: {},
    targetDisplayField: null,
    createSchemaLoading: false,
  };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function createRelationEditorController(
  dependencies: RelationEditorDependencies,
): RelationEditorController {
  const state = shallowRef<RelationEditorState>(shallowReactive(initialState()));
  const pendingCreation = shallowRef<PendingRelationCreation | null>(null);
  let searchGeneration = 0;
  let schemaGeneration = 0;
  let editorEpoch = 0;
  let nestedSearchGeneration = 0;
  const nestedSearchGenerations = new Map<string, number>();

  const invalidateEditor = (closeDraft: boolean): void => {
    searchGeneration += 1;
    schemaGeneration += 1;
    editorEpoch += 1;
    nestedSearchGenerations.clear();
    state.value.show = false;
    if (closeDraft) dependencies.relations.closeDraft();
  };

  watch(
    () => dependencies.workspace.currentTable,
    () => invalidateEditor(true),
  );

  async function loadCreateSchema(collection: string): Promise<void> {
    const generation = ++schemaGeneration;
    state.value.createSchemaLoading = true;
    try {
      const [definition, snapshot] = await Promise.all([
        dependencies.getTableDefinition(collection),
        dependencies.service.describeCollection(collection),
      ]);
      if (generation !== schemaGeneration || !state.value.show) return;
      if (definition.tableId !== collection || !Array.isArray(definition.fields)) {
        throw new Error("目标表完整记录结构无效");
      }
      state.value.targetFields = definition.fields;
      state.value.targetRelations = snapshot.normalizedRelations;
      state.value.targetDisplayField = definition.fields.find(
        field => field.identity.fieldId === snapshot.primaryDisplayFieldId,
      )?.identity.physicalName ?? null;
      const writableRelations = definition.fields.flatMap(field => {
        if (
          definition.kind === "view"
          || field.lifecycle.state !== "active"
          || field.logicalType !== "relation"
        ) return [];
        const relation = snapshot.normalizedRelations.find(
          candidate => candidate.fieldRef === field.identity.physicalName,
        );
        return relation ? [{ field, relation }] : [];
      });
      const entries = await Promise.all(writableRelations.map(async ({ field, relation }) => {
        const result = await dependencies.service.searchTargets({
          relationId: relation.relationId,
          offset: 0,
          limit: 50,
        });
        return [field.identity.physicalName, result.items] as const;
      }));
      if (generation !== schemaGeneration || !state.value.show) return;
      state.value.targetRelationOptions = Object.fromEntries(entries);
    } catch (error) {
      if (generation === schemaGeneration) state.value.error = errorMessage(error);
    } finally {
      if (generation === schemaGeneration) state.value.createSchemaLoading = false;
    }
  }

  async function searchTargets(query: string, offset = 0): Promise<void> {
    const descriptor = state.value.descriptor;
    if (!descriptor) return;
    const generation = ++searchGeneration;
    state.value.query = query;
    state.value.loading = true;
    state.value.error = null;
    try {
      const result = await dependencies.service.searchTargets({
        relationId: descriptor.relationId,
        query,
        offset,
        limit: 50,
      });
      if (generation !== searchGeneration || !state.value.show) return;
      state.value.candidates = offset === 0
        ? result.items
        : [...state.value.candidates, ...result.items];
      state.value.total = result.total;
    } catch (error) {
      if (generation === searchGeneration) state.value.error = errorMessage(error);
    } finally {
      if (generation === searchGeneration) state.value.loading = false;
    }
  }

  async function open(intent: Extract<RelationEditorIntent, { type: "editor.open" }>): Promise<void> {
    if (!dependencies.relations.capabilities?.relationEditV1) {
      dependencies.reportError(dependencies.unsupportedError());
      return;
    }
    editorEpoch += 1;
    nestedSearchGenerations.clear();
    const current = normalizeTargets(intent.value).map(target => ({
      ...target,
      collection: target.collection || intent.descriptor.relatedCollection || "",
    }));
    Object.assign(state.value, {
      ...initialState(),
      show: true,
      rowKey: intent.rowKey,
      field: intent.field,
      fieldLabel: dependencies.table.schema?.find(
        column => column.name === intent.field,
      )?.title ?? "关联字段",
      descriptor: intent.descriptor,
    });
    if (intent.descriptor.relatedCollection) {
      void loadCreateSchema(intent.descriptor.relatedCollection);
    }
    if (intent.descriptor.kind === "m2o") {
      dependencies.relations.openDraft(intent.descriptor.relationId, String(intent.rowKey), current);
      await searchTargets("");
      return;
    }
    state.value.loading = true;
    const capturedEpoch = editorEpoch;
    const isCurrent = () => capturedEpoch === editorEpoch
      && state.value.show
      && state.value.descriptor?.relationId === intent.descriptor.relationId;
    try {
      await dependencies.service.loadDraft(
        intent.descriptor.relationId,
        String(intent.rowKey),
        undefined,
        isCurrent,
      );
      if (!isCurrent()) return;
      await searchTargets("");
    } catch (error) {
      if (!isCurrent()) return;
      state.value.loading = false;
      state.value.error = errorMessage(error);
    }
  }

  async function searchNested(field: string, query: string): Promise<void> {
    const relation = state.value.targetRelations.find(candidate => candidate.fieldRef === field);
    const editorRelationId = state.value.descriptor?.relationId;
    if (!relation || !editorRelationId) return;
    const capturedEpoch = editorEpoch;
    const nestedRelationId = relation.relationId;
    const generation = ++nestedSearchGeneration;
    nestedSearchGenerations.set(field, generation);
    const isCurrent = (): boolean => capturedEpoch === editorEpoch
      && state.value.show
      && state.value.descriptor?.relationId === editorRelationId
      && state.value.targetRelations.find(
        candidate => candidate.fieldRef === field,
      )?.relationId === nestedRelationId
      && nestedSearchGenerations.get(field) === generation;
    state.value.targetRelationLoading = { ...state.value.targetRelationLoading, [field]: true };
    try {
      const result = await dependencies.service.searchTargets({
        relationId: relation.relationId,
        query,
        offset: 0,
        limit: 50,
      });
      if (isCurrent()) {
        state.value.targetRelationOptions = {
          ...state.value.targetRelationOptions,
          [field]: result.items,
        };
      }
    } catch (error) {
      if (isCurrent()) state.value.error = errorMessage(error);
    } finally {
      if (isCurrent()) {
        state.value.targetRelationLoading = {
          ...state.value.targetRelationLoading,
          [field]: false,
        };
      }
    }
  }

  async function selectTarget(target: RelationTargetRef): Promise<void> {
    const descriptor = state.value.descriptor;
    const rowKey = state.value.rowKey;
    if (!descriptor || rowKey === null) return;
    if (descriptor.kind !== "m2o") {
      dependencies.relations.toggleDraftTarget(target);
      return;
    }
    state.value.applying = true;
    state.value.error = null;
    try {
      const result = await dependencies.service.updateSingle(
        descriptor.relationId,
        String(rowKey),
        target,
      );
      if (result.outcome !== "committed") throw new Error(dependencies.changedError());
      dependencies.table.applyRelationValue(rowKey, state.value.field, result.current);
      invalidateEditor(true);
    } catch (error) {
      state.value.error = errorMessage(error);
    } finally {
      state.value.applying = false;
    }
  }

  async function createTarget(label: string): Promise<void> {
    const descriptor = state.value.descriptor;
    if (!descriptor || !label.trim()) return;
    state.value.applying = true;
    state.value.error = null;
    try {
      const result = await dependencies.service.createTarget(descriptor.relationId, label);
      state.value.candidates = [
        result.target,
        ...state.value.candidates.filter(candidate => candidate.itemId !== result.target.itemId),
      ];
      state.value.total += 1;
      if (descriptor.kind === "m2o") {
        await selectTarget(result.target);
        return;
      }
      dependencies.relations.toggleDraftTarget(result.target);
      state.value.query = result.target.label;
    } catch (error) {
      state.value.error = errorMessage(error);
    } finally {
      state.value.applying = false;
    }
  }

  async function createFull(values: Readonly<Record<string, unknown>>): Promise<void> {
    const descriptor = state.value.descriptor;
    const displayField = state.value.targetDisplayField;
    if (!descriptor || !displayField) return;
    state.value.applying = true;
    state.value.error = null;
    try {
      const result = await dependencies.service.createTarget(
        descriptor.relationId,
        String(values[displayField] ?? ""),
        values,
      );
      state.value.candidates = [result.target, ...state.value.candidates];
      state.value.total += 1;
      await selectTarget(result.target);
    } catch (error) {
      state.value.error = errorMessage(error);
    } finally {
      state.value.applying = false;
    }
  }

  function openFullEditor(): void {
    const descriptor = state.value.descriptor;
    const sourceItemId = state.value.rowKey;
    const targetCollection = descriptor?.relatedCollection;
    const displayField = state.value.targetDisplayField;
    const schemaRevision = dependencies.relations.schema?.schemaRevision;
    if (
      !descriptor
      || sourceItemId === null
      || !targetCollection
      || !displayField
      || !schemaRevision
    ) return;
    pendingCreation.value = {
      sourceCollection: descriptor.sourceCollection,
      sourceItemId: String(sourceItemId),
      relationId: descriptor.relationId,
      relationKind: descriptor.kind,
      relationLabel: dependencies.table.schema?.find(
        column => column.name === state.value.field,
      )?.title ?? "关联字段",
      targetCollection,
      targetDisplayField: displayField,
      expectedSchemaRevision: schemaRevision,
    };
    invalidateEditor(true);
    dependencies.selectTable(targetCollection);
    dependencies.navigateTables();
    dependencies.reportInfo("已进入目标表完整编辑；下一条成功创建的记录会自动关联并返回原表。");
  }

  async function completePending(result: InsertRowResult): Promise<void> {
    const pending = pendingCreation.value;
    if (!pending || dependencies.workspace.currentTable !== pending.targetCollection) return;
    const label = String(result.row[pending.targetDisplayField] ?? result.rowKey);
    try {
      const attached = await dependencies.service.attachExistingTarget(
        pending.relationId,
        pending.sourceItemId,
        {
          collection: pending.targetCollection,
          itemId: String(result.rowKey),
          label,
        },
        pending.relationKind,
        pending.expectedSchemaRevision,
      );
      if (attached.outcome !== "committed") throw new Error("原记录已变化，自动关联未提交");
      pendingCreation.value = null;
      dependencies.selectTable(pending.sourceCollection);
      dependencies.reportSuccess(`已创建记录并写入“${pending.relationLabel}”`);
    } catch (error) {
      dependencies.reportError(errorMessage(error));
    }
  }

  async function clearSingle(): Promise<void> {
    const descriptor = state.value.descriptor;
    const rowKey = state.value.rowKey;
    if (!descriptor || rowKey === null) return;
    state.value.applying = true;
    state.value.error = null;
    try {
      const result = await dependencies.service.updateSingle(
        descriptor.relationId,
        String(rowKey),
        null,
      );
      if (result.outcome !== "committed") throw new Error(dependencies.changedError());
      dependencies.table.applyRelationValue(rowKey, state.value.field, null);
      invalidateEditor(true);
    } catch (error) {
      state.value.error = errorMessage(error);
    } finally {
      state.value.applying = false;
    }
  }

  async function applyDraft(): Promise<void> {
    const rowKey = state.value.rowKey;
    if (rowKey === null) return;
    state.value.applying = true;
    state.value.error = null;
    try {
      const result = await dependencies.service.applyDraft();
      if (result.outcome !== "committed") throw new Error(dependencies.changedError());
      dependencies.table.applyRelationValue(rowKey, state.value.field, result.current);
      invalidateEditor(true);
    } catch (error) {
      state.value.error = errorMessage(error);
    } finally {
      state.value.applying = false;
    }
  }

  async function dispatch(intent: RelationEditorIntent): Promise<void> {
    switch (intent.type) {
      case "editor.open": await open(intent); return;
      case "editor.close": invalidateEditor(true); return;
      case "targets.search": await searchTargets(intent.query, intent.offset); return;
      case "targets.loadMore":
        if (!state.value.loading && state.value.candidates.length < state.value.total) {
          await searchTargets(state.value.query, state.value.candidates.length);
        }
        return;
      case "target.select": await selectTarget(intent.target); return;
      case "target.clear": await clearSingle(); return;
      case "draft.apply": await applyDraft(); return;
      case "target.create": await createTarget(intent.label); return;
      case "target.createFull": await createFull(intent.values); return;
      case "target.searchNested": await searchNested(intent.field, intent.query); return;
      case "target.openFullEditor": openFullEditor(); return;
      case "target.open":
        invalidateEditor(true);
        dependencies.openTarget(intent.target);
        return;
      case "pending.complete": await completePending(intent.result); return;
      case "pending.cancel": {
        const sourceCollection = pendingCreation.value?.sourceCollection;
        pendingCreation.value = null;
        if (sourceCollection) dependencies.selectTable(sourceCollection);
      }
    }
  }

  async function searchFilterTargets(
    relationId: string,
    query: string,
  ): Promise<readonly RelationTargetRef[]> {
    const result = await dependencies.service.searchTargets({
      relationId,
      query,
      offset: 0,
      limit: 50,
    });
    return result.items;
  }

  const controller: RelationEditorController = {
    get state() {
      return state.value;
    },
    pendingCreation,
    get selected() {
      const selected: readonly RelationTargetRef[] = dependencies.relations.draft?.selected ?? [];
      return selected;
    },
    dispatch,
    searchFilterTargets,
  };
  return controller;
}
