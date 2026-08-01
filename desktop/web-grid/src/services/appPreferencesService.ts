import type {
  AppPreferences,
  AppPreferencesUpdate,
} from "@/contracts/appPreferencesContracts";
import { useHostBridge } from "./bridgeContext";

export function useAppPreferencesService() {
  const bridge = useHostBridge();

  function get(): Promise<AppPreferences> {
    return bridge.request("appPreferences.get", {});
  }

  function update(patch: AppPreferencesUpdate): Promise<AppPreferences> {
    return bridge.request("appPreferences.update", patch);
  }

  return { get, update };
}
