import { defineStore } from "pinia";
import type {
  ContentVersionEntry,
  PresetEntry,
  PresetsResult,
  VersionCompareResult,
  VersionsResult,
} from "@/contracts";

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export const usePresetVersionStore = defineStore("preset-versions", {
  state: () => ({
    loading: false,
    error: null as string | null,
    collection: "",
    itemId: "",
    presets: [] as PresetEntry[],
    versions: [] as ContentVersionEntry[],
    comparison: null as VersionCompareResult | null,
  }),
  actions: {
    begin() {
      this.loading = true;
      this.error = null;
    },
    fail(error: unknown) {
      this.loading = false;
      this.error = message(error);
    },
    receivePresets(result: PresetsResult) {
      this.loading = false;
      this.collection = result.collection;
      this.presets = [...result.presets];
    },
    upsertPreset(entry: PresetEntry) {
      this.loading = false;
      const index = this.presets.findIndex((item) => item.id === entry.id);
      if (index < 0) this.presets.push(entry);
      else this.presets[index] = entry;
    },
    removePreset(id: string) {
      this.loading = false;
      this.presets = this.presets.filter((item) => item.id !== id);
    },
    receiveVersions(result: VersionsResult) {
      this.loading = false;
      this.collection = result.collection;
      this.itemId = result.itemId;
      this.versions = [...result.versions];
      this.comparison = null;
    },
    upsertVersion(entry: ContentVersionEntry) {
      this.loading = false;
      const index = this.versions.findIndex((item) => item.id === entry.id);
      if (index < 0) this.versions.push(entry);
      else this.versions[index] = entry;
    },
    removeVersion(id: string) {
      this.loading = false;
      this.versions = this.versions.filter((item) => item.id !== id);
      if (this.comparison?.versionId === id) this.comparison = null;
    },
    receiveComparison(result: VersionCompareResult) {
      this.loading = false;
      this.comparison = result;
      const version = this.versions.find((item) => item.id === result.versionId);
      if (version) {
        const index = this.versions.indexOf(version);
        this.versions[index] = { ...version, outdated: result.outdated, mainHash: result.mainHash };
      }
    },
  },
});
