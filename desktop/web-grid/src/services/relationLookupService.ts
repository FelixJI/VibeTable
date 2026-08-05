import type {
  LookupListResult,
  LookupQueryParams,
  LookupQueryResult,
  RelationCreateTargetResult,
  RelationDelta,
  RelationDeltaPreview,
  RelationDeltaResult,
  RelationSearchParams,
  RelationSearchResult,
  RelationSingleUpdateResult,
  RelationTargetRef,
  SchemaDescribeResult,
  SchemaSnapshot,
} from "@/contracts";
import { useRelationLookupStore, targetKey } from "@/stores/relationLookupStore";
import { useHostBridge } from "./bridgeContext";

const ACCEPTS = [
  "vibetable.relation-capabilities.v1",
  "vibetable.lookup-query.v1",
] as const;

export function useRelationLookupService() {
  const bridge = useHostBridge();
  const store = useRelationLookupStore();
  let unsubscribe: (() => void) | null = null;
  let invalidateAllCollections = false;

  function init(onDataInvalidated?: () => void): void {
    if (unsubscribe) return;
    unsubscribe = bridge.on("data.changed", (change) => {
      const active = store.collection;
      const snapshot = store.schema;
      if (!active) return;
      // A Lookup may traverse an arbitrary number of collections and may use
      // another Lookup at its endpoint. The current schema snapshot only
      // contains the first-hop relation descriptors, so it cannot prove that
      // a seemingly unrelated collection is irrelevant. While this context
      // has Lookups, conservatively invalidate on every product data event.
      // This may refresh more often, but it cannot leave a deep Lookup stale.
      const hasLookup = invalidateAllCollections || store.lookups.length > 0;
      if (hasLookup) invalidateAllCollections = true;
      if (!hasLookup) {
        if (!snapshot) return;
        const relatedCollections = new Set(snapshot.normalizedRelations.flatMap((relation) => [
          relation.relatedCollection,
          ...relation.allowedCollections,
          relation.junction?.collection,
        ].filter((item): item is string => !!item)));
        if (change.tableId !== active && !relatedCollections.has(change.tableId)) return;
      }
      // Related writes can invalidate realtime Lookup values. Let the
      // integration layer refresh authoritative rows, then renegotiate all
      // three revisions; never patch values from the visible page.
      onDataInvalidated?.();
      void loadContext(active);
    });
  }

  function dispose(): void {
    unsubscribe?.();
    unsubscribe = null;
    invalidateAllCollections = false;
  }

  async function loadContext(collection: string): Promise<boolean> {
    const requestGeneration = store.beginContext(collection);
    try {
      const [described, listed] = await Promise.all([
        bridge.request("schema.describe", { collection, requestGeneration, accepts: ACCEPTS }),
        bridge.request("lookup.list", { collection }),
      ]);
      const schemaResult = described as SchemaDescribeResult;
      const lookupResult = listed as LookupListResult;
      if (
        schemaResult.contract !== "vibetable.schema-describe.v1"
        || schemaResult.collection !== collection
        || schemaResult.requestGeneration !== requestGeneration
        || lookupResult.collection !== collection
      ) {
        throw new Error("关系上下文响应与当前数据表不匹配");
      }
      if (lookupResult.lookupRevision !== schemaResult.schema.lookupRevision) {
        throw new Error("Lookup 定义已变化，请重试");
      }
      const accepted = store.acceptContext(
        requestGeneration,
        schemaResult.schema,
        lookupResult.definitions,
        schemaResult.capabilities,
      );
      if (accepted) invalidateAllCollections = lookupResult.definitions.length > 0;
      return accepted;
    } catch (error) {
      store.rejectContext(
        requestGeneration,
        collection,
        error instanceof Error ? error.message : String(error),
      );
      return false;
    }
  }

  async function searchTargets(params: RelationSearchParams): Promise<RelationSearchResult> {
    const { query, ...rest } = params;
    const normalizedQuery = query?.trim();
    const request = normalizedQuery
      ? { ...rest, query: normalizedQuery }
      : rest;
    return await bridge.request("relation.searchTargets", request) as RelationSearchResult;
  }

  async function createTarget(
    relationId: string,
    label: string,
    collection?: string | null,
  ): Promise<RelationCreateTargetResult> {
    requireRelationEdit();
    return await bridge.request("relation.createTarget", {
      relationId,
      label: label.trim(),
      collection,
      idempotencyKey: requestId(),
    }) as RelationCreateTargetResult;
  }

  async function describeCollection(collection: string): Promise<SchemaSnapshot> {
    const requestGeneration = store.generation;
    const result = await bridge.request("schema.describe", {
      collection,
      requestGeneration,
      accepts: ACCEPTS,
    }) as SchemaDescribeResult;
    if (
      result.contract !== "vibetable.schema-describe.v1"
      || result.collection !== collection
      || result.requestGeneration !== requestGeneration
    ) throw new Error("关系路径 schema 响应与请求不匹配");
    return result.schema;
  }

  async function listCollectionLookups(collection: string): Promise<LookupListResult> {
    const result = await bridge.request("lookup.list", { collection }) as LookupListResult;
    if (result.collection !== collection) throw new Error("Lookup 列表响应与目标集合不匹配");
    return result;
  }

  async function updateSingle(
    relationId: string,
    sourceItemId: string,
    target: RelationTargetRef | null,
    expectedDateUpdated?: string | null,
  ): Promise<RelationSingleUpdateResult> {
    const schema = requireSchema();
    requireRelationEdit();
    return await bridge.request("relation.updateSingle", {
      relationId,
      sourceItemId,
      target,
      expectedSchemaRevision: schema.schemaRevision,
      expectedDateUpdated,
      idempotencyKey: requestId(),
    }) as RelationSingleUpdateResult;
  }

  function buildDraftDelta(expectedDateUpdated?: string | null): RelationDelta {
    const schema = requireSchema();
    const draft = store.draft;
    if (!draft) throw new Error("没有待提交的关系修改");
    const before = new Map(draft.original.map((target) => [targetKey(target), target]));
    const after = new Map(draft.selected.map((target) => [targetKey(target), target]));
    const adds = [...after].filter(([key]) => !before.has(key)).map(([, target]) => ({ target }));
    const removes = [...before].filter(([key]) => !after.has(key)).map(([, target]) => ({
      target,
      expectedRevision: target.junctionRevision,
    }));
    const updates = [...after].flatMap(([key, target]) => {
      const previous = before.get(key);
      if (!previous?.junctionId || sameRecord(previous.junctionValues, target.junctionValues)) return [];
      return [{
        junctionId: previous.junctionId,
        values: target.junctionValues,
        expectedRevision: previous.junctionRevision,
      }];
    });
    return {
      relationId: draft.relationId,
      sourceItemId: draft.sourceItemId,
      expectedSchemaRevision: schema.schemaRevision,
      expectedDateUpdated,
      adds,
      updates,
      removes,
      idempotencyKey: requestId(),
    };
  }

  async function loadDraft(
    relationId: string,
    sourceItemId: string,
    expectedDateUpdated?: string | null,
  ): Promise<RelationDeltaPreview> {
    const schema = requireSchema();
    requireRelationEdit();
    const delta: RelationDelta = {
      relationId,
      sourceItemId,
      expectedSchemaRevision: schema.schemaRevision,
      expectedDateUpdated,
      adds: [],
      updates: [],
      removes: [],
      idempotencyKey: requestId(),
    };
    const preview = await bridge.request("relation.previewDelta", delta) as RelationDeltaPreview;
    if (!preview.canApply) {
      const reason = preview.diagnostics.map((item) => item.message).join("；");
      throw new Error(reason || "关系数据无法加载");
    }
    store.openDraft(relationId, sourceItemId, preview.current);
    return preview;
  }

  async function applyDraft(expectedDateUpdated?: string | null): Promise<RelationDeltaResult> {
    requireRelationEdit();
    const delta = buildDraftDelta(expectedDateUpdated);
    const preview = await bridge.request("relation.previewDelta", delta) as import("@/contracts").RelationDeltaPreview;
    if (!preview.canApply) {
      const reason = preview.diagnostics.map((item) => item.message).join("；");
      throw new Error(reason || "关系修改无法应用");
    }
    const result = await bridge.request("relation.applyDelta", delta) as RelationDeltaResult;
    if (result.outcome === "committed") store.closeDraft();
    return result;
  }

  async function queryLookups(params: Omit<LookupQueryParams, "contract" | "requestGeneration" | "schemaRevision" | "permissionRevision" | "lookupRevision">): Promise<LookupQueryResult> {
    const schema = requireSchema();
    if (!store.capabilities?.lookupQueryV1) {
      // Deliberately no current-page fallback: it would make sort/filter/group/export incorrect.
      throw new Error(store.lookupUnavailableReason ?? "Lookup 权威查询不可用");
    }
    const generation = store.generation;
    const request: LookupQueryParams = {
      ...params,
      contract: "vibetable.lookup-query.v1",
      requestGeneration: generation,
      schemaRevision: schema.schemaRevision,
      permissionRevision: schema.permissionRevision,
      lookupRevision: schema.lookupRevision,
    };
    const result = await bridge.request("lookup.query", request) as LookupQueryResult;
    if (!store.acceptLookup(result)) throw new Error("Lookup 响应已过期");
    return result;
  }

  async function queryDataset(
    params: Omit<LookupQueryParams, "contract" | "requestGeneration" | "schemaRevision" | "permissionRevision" | "lookupRevision" | "query"> & {
      readonly query: Omit<LookupQueryParams["query"], "offset" | "limit">;
    },
  ): Promise<LookupQueryResult> {
    // The authoritative backend intentionally caps one query page at 500.
    // Fetch the complete client dataset through bounded pages instead of
    // sending an oversized request that the closed RPC contract rejects.
    const pageSize = 500;
    const first = await queryLookups({
      ...params,
      query: { ...params.query, offset: 0, limit: pageSize },
    });
    if (first.rows.length >= first.filteredRows) return first;
    const rows = [...first.rows];
    for (let offset = pageSize; offset < first.filteredRows; offset += pageSize) {
      const next = await queryLookups({
        ...params,
        query: { ...params.query, offset, limit: pageSize },
      });
      rows.push(...next.rows);
    }
    return { ...first, rows, offset: 0, limit: rows.length };
  }

  function requireSchema() {
    if (!store.schema) throw new Error("关系结构尚未加载");
    return store.schema;
  }
  function requireRelationEdit(): void {
    if (!store.capabilities?.relationEditV1) throw new Error("当前环境不支持关系编辑");
  }

  return {
    init,
    dispose,
    loadContext,
    describeCollection,
    listCollectionLookups,
    searchTargets,
    createTarget,
    updateSingle,
    loadDraft,
    buildDraftDelta,
    applyDraft,
    queryLookups,
    queryDataset,
  };
}

function requestId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `relation-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function sameRecord(left: Readonly<Record<string, unknown>>, right: Readonly<Record<string, unknown>>): boolean {
  const leftKeys = Object.keys(left).sort();
  const rightKeys = Object.keys(right).sort();
  return leftKeys.length === rightKeys.length
    && leftKeys.every((key, index) => key === rightKeys[index] && Object.is(left[key], right[key]));
}
