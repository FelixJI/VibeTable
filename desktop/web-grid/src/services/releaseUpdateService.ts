import type {
  ReleaseUpdateCheckResult,
  ReleaseUpdateInstallResult,
} from "@/contracts/releaseUpdateContracts";
import { useHostBridge } from "./bridgeContext";

export function useReleaseUpdateService() {
  const bridge = useHostBridge();

  function check(): Promise<ReleaseUpdateCheckResult> {
    return bridge.request("update.check", {});
  }

  function install(): Promise<ReleaseUpdateInstallResult> {
    return bridge.request("update.install", {});
  }

  return { check, install };
}
