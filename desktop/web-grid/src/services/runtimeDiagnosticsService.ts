import type {
  RuntimeDiagnostics,
  RuntimeDiagnosticsHostPayloadMap,
  RuntimeDiagnosticsWebPayloadMap,
} from "@/contracts/runtimeDiagnosticsContracts";
import { useHostBridge } from "./bridgeContext";

interface RuntimeDiagnosticsBridge {
  request<K extends keyof RuntimeDiagnosticsWebPayloadMap>(
    type: K,
    payload: RuntimeDiagnosticsWebPayloadMap[K],
  ): Promise<RuntimeDiagnosticsHostPayloadMap[K]>;
}

export function useRuntimeDiagnosticsService() {
  const bridge = useHostBridge() as unknown as RuntimeDiagnosticsBridge;

  async function getDiagnostics(): Promise<RuntimeDiagnostics> {
    return bridge.request("diagnostics.get", {});
  }

  return { getDiagnostics };
}
