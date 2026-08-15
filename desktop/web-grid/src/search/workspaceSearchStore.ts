import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type {
  SearchFilter,
  SearchHit,
  SearchRequest,
  SearchSort,
  SearchStatus,
} from "@/contracts/generated/workbench";
import { requestWorkspaceV2UiAction } from "@/services/workspaceV2UiPort";

const EMPTY_STATUS: SearchStatus = {
  state: "idle",
  generation: 0,
  checkpoint: null,
  processed: 0,
  total: null,
  errorCode: null,
};

export const useWorkspaceSearchStore = defineStore("workspace-search", () => {
  const query = ref("");
  const logic = ref<SearchRequest["logic"]>("and");
  const scope = ref<SearchRequest["scope"]>("current");
  const filters = ref<SearchFilter[]>([]);
  const sorts = ref<SearchSort[]>([{ field: "score", direction: "desc" }]);
  const hits = ref<SearchHit[]>([]);
  const nextCursor = ref<string | null>(null);
  const generation = ref(0);
  const status = ref<SearchStatus>({ ...EMPTY_STATUS });
  const searching = ref(false);
  const rebuilding = ref(false);
  const resolvingHitId = ref<string | null>(null);
  const errorCode = ref<string | null>(null);
  let requestEpoch = 0;
  let resolveEpoch = 0;

  const canSearch = computed(() => query.value.trim().length > 0 && !searching.value);

  function filterValue(
    field: SearchFilter["field"],
    operator: SearchFilter["operator"],
  ): SearchFilter["value"] {
    return filters.value.find((item) => item.field === field && item.operator === operator)?.value
      ?? null;
  }

  function setFilter(
    field: SearchFilter["field"],
    operator: SearchFilter["operator"],
    value: SearchFilter["value"],
  ): void {
    const retained = filters.value.filter(
      (item) => item.field !== field || item.operator !== operator,
    );
    if (value === null || (typeof value === "string" && value.trim() === "")) {
      filters.value = retained;
      return;
    }
    filters.value = [...retained, {
      field,
      operator,
      value: typeof value === "string" ? value.trim() : value,
    }];
  }

  function request(cursor: string | null): SearchRequest {
    return {
      contractVersion: "1.0",
      query: query.value.trim(),
      logic: logic.value,
      filters: filters.value,
      sorts: sorts.value,
      scope: scope.value,
      cursor,
      limit: 50,
    };
  }

  async function search(options: { append?: boolean } = {}): Promise<void> {
    const append = options.append === true;
    if (!query.value.trim() || searching.value || (append && nextCursor.value === null)) return;
    const epoch = ++requestEpoch;
    searching.value = true;
    errorCode.value = null;
    try {
      const result = await requestWorkspaceV2UiAction({
        method: "workspaceSearch.query",
        params: request(append ? nextCursor.value : null),
      });
      if (epoch !== requestEpoch) return;
      hits.value = append
        ? [...new Map([...hits.value, ...result.hits].map((hit) => [hit.hitId, hit])).values()]
        : [...result.hits];
      nextCursor.value = result.nextCursor;
      generation.value = result.generation;
    } catch (error) {
      if (epoch !== requestEpoch) return;
      errorCode.value = error instanceof Error ? error.message : String(error);
      if (!append) hits.value = [];
    } finally {
      if (epoch === requestEpoch) searching.value = false;
    }
  }

  async function refreshStatus(): Promise<void> {
    try {
      status.value = await requestWorkspaceV2UiAction({
        method: "workspaceSearch.status",
        params: {},
      });
    } catch (error) {
      status.value = {
        ...status.value,
        state: "degraded",
        errorCode: error instanceof Error ? error.message : String(error),
      };
    }
  }

  async function rebuild(): Promise<void> {
    if (rebuilding.value) return;
    rebuilding.value = true;
    errorCode.value = null;
    status.value = { ...status.value, state: "building", processed: 0, total: null };
    try {
      status.value = await requestWorkspaceV2UiAction({
        method: "workspaceSearch.rebuild",
        params: {},
      });
      while (status.value.state === "building") {
        await new Promise((resolve) => window.setTimeout(resolve, 250));
        await refreshStatus();
      }
      if (status.value.state === "ready") await search();
    } catch (error) {
      errorCode.value = error instanceof Error ? error.message : String(error);
      status.value = { ...status.value, state: "failed", errorCode: errorCode.value };
    } finally {
      rebuilding.value = false;
    }
  }

  async function cancelRebuild(): Promise<void> {
    if (!rebuilding.value && status.value.state !== "building") return;
    try {
      status.value = await requestWorkspaceV2UiAction({
        method: "workspaceSearch.cancel",
        params: {},
      });
    } catch (error) {
      errorCode.value = error instanceof Error ? error.message : String(error);
    }
  }

  async function resolveHit(hit: SearchHit): Promise<SearchHit | null> {
    const epoch = ++resolveEpoch;
    resolvingHitId.value = hit.hitId;
    errorCode.value = null;
    try {
      const result = await requestWorkspaceV2UiAction({
        method: "workspaceSearch.resolveHit",
        params: { contractVersion: "1.0", scope: scope.value, hit },
      });
      if (epoch !== resolveEpoch) return null;
      if (result.status === "stale") {
        hits.value = hits.value.map((candidate) => (
          candidate.hitId === hit.hitId ? result.hit : candidate
        ));
        return null;
      }
      return result.hit;
    } catch (error) {
      if (epoch !== resolveEpoch) return null;
      const code = error instanceof Error ? error.message : String(error);
      errorCode.value = code;
      if (code === "workspace_search.hit_missing") {
        hits.value = hits.value.filter((candidate) => candidate.hitId !== hit.hitId);
      }
      return null;
    } finally {
      if (epoch === resolveEpoch) resolvingHitId.value = null;
    }
  }

  function reset(): void {
    requestEpoch += 1;
    resolveEpoch += 1;
    query.value = "";
    logic.value = "and";
    scope.value = "current";
    filters.value = [];
    sorts.value = [{ field: "score", direction: "desc" }];
    hits.value = [];
    nextCursor.value = null;
    generation.value = 0;
    status.value = { ...EMPTY_STATUS };
    searching.value = false;
    rebuilding.value = false;
    resolvingHitId.value = null;
    errorCode.value = null;
  }

  return {
    query,
    logic,
    scope,
    filters,
    sorts,
    hits,
    nextCursor,
    generation,
    status,
    searching,
    rebuilding,
    resolvingHitId,
    errorCode,
    canSearch,
    filterValue,
    setFilter,
    search,
    refreshStatus,
    rebuild,
    cancelRebuild,
    resolveHit,
    reset,
  };
});
