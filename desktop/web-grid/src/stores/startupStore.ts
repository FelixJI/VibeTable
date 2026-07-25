import { defineStore } from "pinia";
import { ref } from "vue";
import type { StartupLogEntry, StartupPhase, StartupStatePayload } from "@/contracts";

/** Host-authoritative startup projection for the local runtime readiness gate. */
export const useStartupStore = defineStore("startup", () => {
  const phase = ref<StartupPhase>("starting");
  const stage = ref<string | null>(null);
  const detail = ref<string | null>(null);
  const canRetry = ref(false);
  const canCancel = ref(false);
  const logs = ref<readonly StartupLogEntry[]>([]);

  function applyHostState(payload: StartupStatePayload): void {
    phase.value = payload.phase;
    stage.value = payload.stage?.trim() || null;
    detail.value = payload.detail?.trim() || null;
    canRetry.value = payload.canRetry ?? false;
    canCancel.value = payload.canCancel ?? false;
    logs.value = (payload.logs ?? []).slice(-24);
  }

  return {
    phase,
    stage,
    detail,
    canRetry,
    canCancel,
    logs,
    applyHostState,
  };
});
