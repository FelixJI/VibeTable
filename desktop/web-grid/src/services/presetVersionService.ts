import type {
  PresetEntry,
  PresetsResult,
  PresetView,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { unwrapProductRpcResult } from "./productRpcResult";

export type PresetSaveTarget = Pick<PresetEntry, "id" | "revision">;

function operationId(): string {
  return crypto.randomUUID();
}

export function usePresetVersionService() {
  const bridge = useHostBridge();

  async function listPresets(collection: string): Promise<PresetsResult> {
    return unwrapProductRpcResult<PresetsResult>(
      await bridge.request("preset.list", { collection }),
    );
  }

  async function savePreset(
    collection: string,
    name: string,
    view: PresetView,
    target: PresetSaveTarget | null,
  ): Promise<PresetEntry> {
    return unwrapProductRpcResult<PresetEntry>(await bridge.request("preset.save", {
      collection,
      name,
      view,
      presetId: target?.id ?? null,
      expectedRevision: target?.revision ?? null,
      operationId: operationId(),
    }));
  }

  async function deletePreset(presetId: string, expectedRevision: string): Promise<void> {
    unwrapProductRpcResult(await bridge.request("preset.delete", {
      presetId, expectedRevision, operationId: operationId(),
    }));
  }

  return { listPresets, savePreset, deletePreset };
}
