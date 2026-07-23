import { computed, ref } from "vue";
import { defineStore } from "pinia";
import type {
  LookupDefinition,
  LookupQueryResult,
  NormalizedRelationDescriptor,
  RelationLookupCapabilities,
  RelationTargetRef,
  SchemaSnapshot,
} from "@/contracts";

export interface RelationDraft {
  readonly relationId: string;
  readonly sourceItemId: string;
  readonly original: readonly RelationTargetRef[];
  readonly selected: readonly RelationTargetRef[];
}

export const useRelationLookupStore = defineStore("relationLookup", () => {
  const generation = ref(0);
  const collection = ref<string | null>(null);
  const loading = ref(false);
  const error = ref<string | null>(null);
  const schema = ref<SchemaSnapshot | null>(null);
  const lookups = ref<readonly LookupDefinition[]>([]);
  const capabilities = ref<RelationLookupCapabilities | null>(null);
  const lookupResult = ref<LookupQueryResult | null>(null);
  const draft = ref<RelationDraft | null>(null);

  const relationsById = computed(() => new Map(
    (schema.value?.normalizedRelations ?? []).map((relation) => [relation.relationId, relation]),
  ));
  const lookupsById = computed(() => new Map(
    lookups.value.map((lookup) => [lookup.lookupId, lookup]),
  ));
  const lookupUnavailableReason = computed(() => {
    const current = capabilities.value;
    if (!current || current.lookupQueryV1) return null;
    if (current.reason === "extension_missing") return "Lookup 扩展未安装";
    if (current.reason === "permission_denied") return "无权读取 Lookup";
    return "Lookup 查询能力不兼容";
  });

  function beginContext(nextCollection: string): number {
    generation.value += 1;
    collection.value = nextCollection;
    loading.value = true;
    error.value = null;
    schema.value = null;
    lookups.value = [];
    capabilities.value = null;
    lookupResult.value = null;
    draft.value = null;
    return generation.value;
  }

  function isCurrent(requestGeneration: number, expectedCollection: string): boolean {
    return generation.value === requestGeneration && collection.value === expectedCollection;
  }

  function acceptContext(
    requestGeneration: number,
    nextSchema: SchemaSnapshot,
    nextLookups: readonly LookupDefinition[],
    nextCapabilities: RelationLookupCapabilities,
  ): boolean {
    if (!isCurrent(requestGeneration, nextSchema.collection)) return false;
    schema.value = nextSchema;
    lookups.value = nextLookups;
    capabilities.value = nextCapabilities;
    loading.value = false;
    error.value = null;
    return true;
  }

  function rejectContext(requestGeneration: number, nextCollection: string, message: string): boolean {
    if (!isCurrent(requestGeneration, nextCollection)) return false;
    loading.value = false;
    error.value = message;
    return true;
  }

  function acceptLookup(result: LookupQueryResult): boolean {
    const current = schema.value;
    if (!current || !isCurrent(result.requestGeneration, result.collection)) return false;
    if (
      result.schemaRevision !== current.schemaRevision
      || result.permissionRevision !== current.permissionRevision
      || result.lookupRevision !== current.lookupRevision
    ) return false;
    lookupResult.value = result;
    return true;
  }

  function relation(relationId: string): NormalizedRelationDescriptor | undefined {
    return relationsById.value.get(relationId);
  }

  function openDraft(
    relationId: string,
    sourceItemId: string,
    current: readonly RelationTargetRef[],
  ): void {
    draft.value = {
      relationId,
      sourceItemId,
      original: current.map(cloneTarget),
      selected: current.map(cloneTarget),
    };
  }

  function toggleDraftTarget(target: RelationTargetRef): void {
    if (!draft.value) return;
    const key = targetKey(target);
    const found = draft.value.selected.some((item) => targetKey(item) === key);
    draft.value = {
      ...draft.value,
      selected: found
        ? draft.value.selected.filter((item) => targetKey(item) !== key)
        : [...draft.value.selected, cloneTarget(target)],
    };
  }

  function patchDraftJunction(target: RelationTargetRef, values: Readonly<Record<string, unknown>>): void {
    if (!draft.value) return;
    const key = targetKey(target);
    draft.value = {
      ...draft.value,
      selected: draft.value.selected.map((item) =>
        targetKey(item) === key ? { ...item, junctionValues: { ...values } } : item,
      ),
    };
  }

  function closeDraft(): void {
    draft.value = null;
  }

  function reset(): void {
    generation.value += 1;
    collection.value = null;
    loading.value = false;
    error.value = null;
    schema.value = null;
    lookups.value = [];
    capabilities.value = null;
    lookupResult.value = null;
    draft.value = null;
  }

  return {
    generation,
    collection,
    loading,
    error,
    schema,
    lookups,
    capabilities,
    lookupResult,
    draft,
    relationsById,
    lookupsById,
    lookupUnavailableReason,
    beginContext,
    isCurrent,
    acceptContext,
    rejectContext,
    acceptLookup,
    relation,
    openDraft,
    toggleDraftTarget,
    patchDraftJunction,
    closeDraft,
    reset,
  };
});

export function targetKey(target: RelationTargetRef): string {
  // A target is selected at most once even when the persisted value carries a
  // junction id and a fresh search result does not.
  return `${target.collection}\u0000${target.itemId}`;
}

function cloneTarget(target: RelationTargetRef): RelationTargetRef {
  return { ...target, junctionValues: { ...target.junctionValues } };
}
