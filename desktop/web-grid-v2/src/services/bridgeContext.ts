import { createHostBridge } from "@/bridge/hostBridge";
import type { HostBridge } from "@/bridge/hostBridge";

// Module-level singleton. In production there is exactly one WebView2 host,
// so one bridge is correct. Tests call setHostBridgeForTesting() to inject a
// fake built via createHostBridge({ webview: shim }).
let singleton: HostBridge | null = null;

export function useHostBridge(): HostBridge {
  if (!singleton) {
    singleton = createHostBridge();
  }
  return singleton;
}

/** Test-only: inject a pre-built bridge (real or shim). */
export function setHostBridgeForTesting(bridge: HostBridge | null): void {
  singleton = bridge;
}
