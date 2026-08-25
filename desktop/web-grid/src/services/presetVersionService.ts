import type {
  ContentVersionEntry,
  PresetEntry,
  PresetsResult,
  PresetView,
  VersionCompareResult,
  VersionsResult,
} from "@/contracts";
import { usePresetVersionStore } from "@/stores/presetVersionStore";
import { useHostBridge } from "./bridgeContext";
import { unwrapProductRpcResult } from "./productRpcResult";

export type PresetSaveTarget = Pick<PresetEntry, "id" | "revision">;

function operationId(): string {
  return crypto.randomUUID();
}

export function usePresetVersionService() {
  const bridge = useHostBridge();
  const store = usePresetVersionStore();

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

  async function listVersions(collection: string, itemId: string): Promise<void> {
    store.begin();
    try {
      store.receiveVersions(
        unwrapProductRpcResult<VersionsResult>(
          await bridge.request("version.list", { collection, itemId }),
        ),
      );
    } catch (error) {
      store.fail(error);
    }
  }

  async function createVersion(
    collection: string,
    itemId: string,
    key: string,
    name: string,
  ): Promise<ContentVersionEntry> {
    store.begin();
    try {
      const created = unwrapProductRpcResult<ContentVersionEntry>(await bridge.request("version.create", {
        collection, itemId, key, name, operationId: operationId(),
      }));
      store.upsertVersion(created);
      return created;
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  async function saveVersion(
    collection: string,
    itemId: string,
    versionId: string,
  ): Promise<void> {
    store.begin();
    try {
      unwrapProductRpcResult(await bridge.request("version.save", {
        collection, itemId, versionId, values: {}, operationId: operationId(),
      }));
      await listVersions(collection, itemId);
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  async function compareVersion(
    collection: string,
    itemId: string,
    versionId: string,
  ): Promise<VersionCompareResult> {
    store.begin();
    try {
      const result = unwrapProductRpcResult<VersionCompareResult>(await bridge.request(
        "version.compare",
        { collection, itemId, versionId },
      ));
      store.receiveComparison(result);
      return result;
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  async function promoteVersion(
    collection: string,
    itemId: string,
    versionId: string,
    mainHash: string,
  ): Promise<void> {
    store.begin();
    try {
      unwrapProductRpcResult(await bridge.request("version.promote", {
        collection, itemId, versionId, mainHash, operationId: operationId(),
      }));
      await listVersions(collection, itemId);
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  async function deleteVersion(
    collection: string,
    itemId: string,
    versionId: string,
    expectedRevision: string,
  ): Promise<void> {
    store.begin();
    try {
      unwrapProductRpcResult(await bridge.request("version.delete", {
        collection, itemId, versionId, expectedRevision, operationId: operationId(),
      }));
      store.removeVersion(versionId);
    } catch (error) {
      store.fail(error);
      throw error;
    }
  }

  return {
    listPresets,
    savePreset,
    deletePreset,
    listVersions,
    createVersion,
    saveVersion,
    compareVersion,
    promoteVersion,
    deleteVersion,
  };
}
