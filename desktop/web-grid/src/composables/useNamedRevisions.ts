import { computed, nextTick, reactive, watch, onScopeDispose, type Ref } from "vue";
import type { ContentVersionEntry, VersionCompareResult } from "@/contracts";
import type { NamedRevisionScope, useContentVersionService } from "@/services/contentVersionService";

import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

export type NamedRevisionAction = "reload" | "create" | "save" | "compare" | "promote" | "delete";
export function useNamedRevisions(
  scope: Ref<NamedRevisionScope | null>,
  service: ReturnType<typeof useContentVersionService>,
  restored: () => Promise<void>,
) {
  const session = useWorkspaceSessionStore();
  const ready = computed(() => !session.enabled || (session.hasOpenWorkspace && !session.isTransitioning));
  const state = reactive({
    versions: [] as ContentVersionEntry[], selectedId: "", name: "",
    comparison: null as VersionCompareResult | null, loading: false, error: "",
  });
  let generation = 0;
  onScopeDispose(() => { generation++; });
  const selected = computed(() => state.versions.find((entry) => entry.id === state.selectedId));
  function select(id: string) { state.selectedId = id; state.comparison = null; }
  async function dispatch(action: NamedRevisionAction): Promise<void> {
    const target = scope.value;
    if (!target || !ready.value || state.loading) return;
    const ticket = ++generation;
    const entry = selected.value;
    const comparison = state.comparison;
    state.loading = true;
    state.error = "";
    try {
      if (action === "compare") {
        if (!entry) return;
        const result = await service.compare(target, entry.id);
        if (ticket === generation && state.selectedId === entry.id) state.comparison = result;
        return;
      }
      if (action === "create") await service.create(target, state.name.trim());
      if (action === "save" && entry) await service.save(target, entry);
      if (action === "delete" && entry) await service.delete(target, entry);
      if (action === "promote" && comparison && comparison.versionId === entry?.id) {
        await service.promote(target, comparison);
        if (ticket === generation) await restored();
      }
      if (ticket !== generation) return;
      const result = await service.list(target);
      if (ticket !== generation) return;
      state.versions = [...result.versions];
      // Preserve an explicit selection; never replace it with a newly created entry.
      if (!state.versions.some((value) => value.id === state.selectedId)) state.selectedId = "";
      state.comparison = null;
      if (action === "create") state.name = "";
    } catch (error) {
      if (ticket === generation) state.error = error instanceof Error ? error.message : String(error);
    } finally {
      if (ticket === generation) state.loading = false;
    }
  }
  watch([scope, ready, () => session.activeWorkspaceId, () => session.sessionEpoch], () => {
    generation++;
    state.versions = []; state.selectedId = ""; state.comparison = null;
    state.error = ""; state.loading = false; state.name = "";
    // Reset immediately, but never issue I/O inside applySession's resetters.
    // The settled scope and readiness must still belong to this generation.
    const ticket = generation;
    void nextTick().then(() => {
      if (ticket === generation && scope.value && ready.value) void dispatch("reload");
    });
  }, { immediate: true, flush: "sync" });
  return { state, select, dispatch };
}
export type NamedRevisionState = ReturnType<typeof useNamedRevisions>["state"];
