import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useUiStore } from "./uiStore";

describe("uiStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts with all panels closed and theme=system", () => {
    const s = useUiStore();
    expect(s.createModalOpen).toBe(false);
    expect(s.deleteModalOpen).toBe(false);
    expect(s.pastePanelOpen).toBe(false);
    expect(s.shortcutsOpen).toBe(false);
    expect(s.themeMode).toBe("system");
  });

  it("openCreate/closeCreate toggles createModalOpen", () => {
    const s = useUiStore();
    s.openCreate();
    expect(s.createModalOpen).toBe(true);
    s.closeCreate();
    expect(s.createModalOpen).toBe(false);
  });

  it("openDelete/closeDelete toggles deleteModalOpen", () => {
    const s = useUiStore();
    s.openDelete("users");
    expect(s.deleteModalOpen).toBe(true);
    expect(s.deleteTarget).toBe("users");
    s.closeDelete();
    expect(s.deleteModalOpen).toBe(false);
  });

  it("openShortcuts/closeShortcuts toggles shortcutsOpen", () => {
    const s = useUiStore();
    s.openShortcuts();
    expect(s.shortcutsOpen).toBe(true);
    s.closeShortcuts();
    expect(s.shortcutsOpen).toBe(false);
  });

  it("setThemeMode persists to localStorage", () => {
    const s = useUiStore();
    s.setThemeMode("dark");
    expect(s.themeMode).toBe("dark");
    expect(localStorage.getItem("vt:theme")).toBe("dark");
  });
});
