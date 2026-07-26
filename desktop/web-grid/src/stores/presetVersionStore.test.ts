import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { PresetEntry } from "@/contracts";
import { usePresetVersionStore } from "./presetVersionStore";

function preset(id: string, isDefault = false): PresetEntry {
  return {
    id,
    collection: "orders",
    name: id,
    scope: "personal",
    view: {
      kind: "table",
      layout: "table",
      filters: [],
      sorts: [],
      search: "",
      visibleFields: [],
      isDefault,
    },
    revision: `revision-${id}`,
    emittedEvents: [],
  };
}

describe("presetVersionStore table views", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("selects the collection default and falls back after deletion", () => {
    const store = usePresetVersionStore();
    store.receivePresets({
      collection: "orders",
      presets: [preset("all"), preset("pending", true)],
    });
    expect(store.activePresetId).toBe("pending");
    store.removePreset("pending");
    expect(store.activePresetId).toBe("all");
  });

  it("keeps the in-memory all-records view when a collection has no presets", () => {
    const store = usePresetVersionStore();
    store.receivePresets({ collection: "orders", presets: [] });
    expect(store.activePresetId).toBeNull();
    expect(store.presets).toEqual([]);
  });

  it("marks only a persisted active view dirty", () => {
    const store = usePresetVersionStore();
    store.markDirty();
    expect(store.dirty).toBe(false);
    store.receivePresets({ collection: "orders", presets: [preset("all", true)] });
    store.markDirty();
    expect(store.dirty).toBe(true);
    store.markSaved();
    expect(store.dirty).toBe(false);
  });
});
