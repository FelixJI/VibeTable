import { createHostBridge } from "@/bridge/hostBridge";
import type { HostBridge } from "@/bridge/hostBridge";

// Module-level singleton. In production there is exactly one WebView2 host,
// so one bridge is correct. Tests call setHostBridgeForTesting() to inject a
// fake built via createHostBridge({ webview: shim }).
let singleton: HostBridge | null = null;

export function useHostBridge(): HostBridge {
  if (!singleton) {
    // Native file hashing, backup restore, and plugin package verification can
    // legitimately exceed the bridge's unit-test-oriented 10 second default
    // on a cold Windows/antivirus path. Keep production requests bounded while
    // allowing those host-owned operations to complete.
    singleton = createHostBridge({ timeoutMs: 30_000 });
  }
  return singleton;
}

/** Test-only: inject a pre-built bridge (real or shim). */
export function setHostBridgeForTesting(bridge: HostBridge | null): void {
  singleton = bridge;
}
