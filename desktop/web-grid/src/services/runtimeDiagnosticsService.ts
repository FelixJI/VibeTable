import type {
  RuntimeDiagnostics,
} from "@/contracts/runtimeDiagnosticsContracts";
import { useHostBridge } from "./bridgeContext";

export function useRuntimeDiagnosticsService() {
  const bridge = useHostBridge();

  async function getDiagnostics(): Promise<RuntimeDiagnostics> {
    return bridge.request("diagnostics.get", {});
  }

  return { getDiagnostics };
}
