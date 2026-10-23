import type {
  PresetEntry,
  PresetsResult,
  PresetView,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { decodePresetEntry, decodePresetsResult, decodeDeletePresetVersionResult, snapshotPresetView } from "./productRpcDecode";
import { unwrapProductRpcResult } from "./productRpcResult";
import { derivePresetId } from "./uuidV5";

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
    // Freeze every submission scalar before dispatch; the payload is sent
    // synchronously with the request and the derived id is awaited after the
    // reply, so caller mutations cannot change what was sent or expected.
    const operation = operationId();
    const submittedView = snapshotPresetView(view);
    const targetId = target?.id ?? null;
    const expectedRevision = target?.revision ?? null;
    const result = await bridge.request("preset.save", {
      collection,
      name,
      view,
      presetId: targetId,
      expectedRevision,
      operationId: operation,
    });
    const expectedId = targetId ?? (await derivePresetId(operation));
    return unwrapProductRpcResult(
      result,
      value => decodePresetEntry(value, { collection, presetId: expectedId, name, submittedView }, { writeReceipt: true }),
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
