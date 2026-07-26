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

function operationId(): string {
  return crypto.randomUUID();
}

export function usePresetVersionService() {
  const bridge = useHostBridge();
  const store = usePresetVersionStore();

  async function listPresets(collection: string): Promise<PresetsResult> {
    return await bridge.request("preset.list", { collection }) as PresetsResult;
  }

  async function savePreset(
    collection: string,
    name: string,
    view: PresetView,
    presetId?: string | null,
  ): Promise<PresetEntry> {
    return await bridge.request("preset.save", {
      collection, name, view, presetId, operationId: operationId(),
    }) as PresetEntry;
  }

  async function deletePreset(presetId: string, expectedRevision: string): Promise<void> {
    await bridge.request("preset.delete", {
      presetId, expectedRevision, operationId: operationId(),
    });
  }

  async function listVersions(collection: string, itemId: string): Promise<void> {
    store.begin();
    try {
      store.receiveVersions(
        await bridge.request("version.list", { collection, itemId }) as VersionsResult,
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
      const created = await bridge.request("version.create", {
        collection, itemId, key, name, operationId: operationId(),
      }) as ContentVersionEntry;
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
      await bridge.request("version.save", {
        collection, itemId, versionId, values: {}, operationId: operationId(),
      });
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
      const result = await bridge.request(
        "version.compare",
        { collection, itemId, versionId },
      ) as VersionCompareResult;
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
      await bridge.request("version.promote", {
        collection, itemId, versionId, mainHash, operationId: operationId(),
      });
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
      await bridge.request("version.delete", {
        collection, itemId, versionId, expectedRevision, operationId: operationId(),
      });
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
