import { defineStore } from "pinia";
import { ref } from "vue";
import type { StartupLogEntry, StartupPhase, StartupStatePayload } from "@/contracts";

/**
 * Host-authoritative startup projection. Credentials and OTPs deliberately do
 * not exist in this store; form components keep them in short-lived refs.
 */
export const useStartupStore = defineStore("startup", () => {
  const phase = ref<StartupPhase>("starting");
  const stage = ref<string | null>(null);
  const detail = ref<string | null>(null);
  const email = ref("");
  const rememberPassword = ref(false);
  const autoLogin = ref(false);
  const canRetry = ref(false);
  const canCancel = ref(false);
  const logs = ref<readonly StartupLogEntry[]>([]);

  function applyHostState(payload: StartupStatePayload): void {
    phase.value = payload.phase;
    stage.value = payload.stage?.trim() || null;
    detail.value = payload.detail?.trim() || null;
    email.value = payload.email ?? "";
    rememberPassword.value = payload.rememberPassword ?? false;
    autoLogin.value = payload.autoLogin ?? false;
    canRetry.value = payload.canRetry ?? false;
    canCancel.value = payload.canCancel ?? false;
    logs.value = (payload.logs ?? []).slice(-24);
  }

  return {
    phase,
    stage,
    detail,
    email,
    rememberPassword,
    autoLogin,
    canRetry,
    canCancel,
    logs,
    applyHostState,
  };
});
