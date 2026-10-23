import type {
  PresetEntry,
  PresetsResult,
  PresetView,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { decodePresetEntry, decodePresetsResult, decodeDeletePresetVersionResult, snapshotPresetView } from "./productRpcDecode";
import { unwrapProductRpcResult } from "./productRpcResult";

export type PresetSaveTarget = Pick<PresetEntry, "id" | "revision">;

function operationId(): string {
  return crypto.randomUUID();
}

export function usePresetVersionService() {
  const bridge = useHostBridge();

  async function listPresets(collection: string): Promise<PresetsResult> {
    return unwrapProductRpcResult(
      await bridge.request("preset.list", { collection }),
      value => decodePresetsResult(value, { collection }),
      "preset.list",
    );
  }

  async function savePreset(
    collection: string,
    name: string,
    view: PresetView,
    target: PresetSaveTarget | null,
  ): Promise<PresetEntry> {
    // Capture the submission semantics before dispatch: the caller may mutate
    // the view (including nested opaque objects) while the request is in
    // flight, and the receipt must match what was sent, not the live object.
    const submittedView = snapshotPresetView(view);
    return unwrapProductRpcResult(
      await bridge.request("preset.save", {
        collection,
        name,
        view,
        presetId: target?.id ?? null,
        expectedRevision: target?.revision ?? null,
        operationId: operationId(),
      }),
      value => decodePresetEntry(value, { collection, presetId: target?.id, name, submittedView }, { writeReceipt: true }),
      "preset.save",
    );
  }

  async function deletePreset(presetId: string, expectedRevision: string): Promise<void> {
    unwrapProductRpcResult(
      await bridge.request("preset.delete", {
        presetId, expectedRevision, operationId: operationId(),
      }),
      value => decodeDeletePresetVersionResult(value, { id: presetId }),
      "preset.delete",
    );
  }

  return { listPresets, savePreset, deletePreset };
}
