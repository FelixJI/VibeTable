import type { StartupStatePayload } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { useStartupStore } from "@/stores/startupStore";

export interface StartupService {
  init(): void;
  retry(): void;
  cancel(): void;
}

export function useStartupService(): StartupService {
  const bridge = useHostBridge();
  const store = useStartupStore();
  let initialized = false;

  function init(): void {
    if (initialized) return;
    initialized = true;
    bridge.on("host.startupStateChanged", (payload: StartupStatePayload) => {
      store.applyHostState(payload);
    });
  }

  return {
    init,
    retry: () => bridge.notify("host.startupRetryRequested", {}),
    cancel: () => bridge.notify("host.startupCancelRequested", {}),
  };
}
