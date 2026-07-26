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
    activePresetId: null as string | null,
    dirty: false,
  }),
  actions: {
    begin() {
      this.loading = true;
      this.error = null;
    },
    clearPresets(collection = "") {
      this.collection = collection;
      this.presets = [];
      this.activePresetId = null;
      this.dirty = false;
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
      const current = this.presets.find((item) => item.id === this.activePresetId);
      this.activePresetId = current?.id
        ?? this.presets.find((item) => item.view.isDefault)?.id
        ?? this.presets[0]?.id
        ?? null;
      this.dirty = false;
    },
    upsertPreset(entry: PresetEntry) {
      this.loading = false;
      if (entry.view.isDefault) {
        this.presets = this.presets.map((item) => item.id === entry.id
          ? item
          : { ...item, view: { ...item.view, isDefault: false } });
      }
      const index = this.presets.findIndex((item) => item.id === entry.id);
      if (index < 0) this.presets.push(entry);
      else this.presets[index] = entry;
      this.activePresetId = entry.id;
      this.dirty = false;
    },
    removePreset(id: string) {
      this.loading = false;
      this.presets = this.presets.filter((item) => item.id !== id);
      if (this.activePresetId === id) {
        this.activePresetId = this.presets.find((item) => item.view.isDefault)?.id
          ?? this.presets[0]?.id
          ?? null;
      }
      this.dirty = false;
    },
    activatePreset(id: string | null) {
      this.activePresetId = id;
      this.dirty = false;
    },
    markDirty() {
      if (this.activePresetId) this.dirty = true;
    },
    markSaved() {
      this.dirty = false;
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
