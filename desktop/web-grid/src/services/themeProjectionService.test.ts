import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import {
  createThemeProjectionService,
  legacyThemeStoragePorts,
  snapshotFromPreferences,
  type HostThemeSnapshot,
  type ThemeProjectionDeps,
} from "./themeProjectionService";
import { useUiStore } from "@/stores/uiStore";
import type {
  AppPreferences,
  AppPreferencesUpdate,
} from "@/contracts/appPreferencesContracts";

function preferences(overrides: Partial<AppPreferences> = {}): AppPreferences {
  return {
    minimizeToTrayOnClose: false,
    startWithWindows: false,
    updateProxy: "direct",
    customUpdateProxyUrl: "",
    theme: null,
    effectiveTheme: "light",
    highContrast: false,
    ...overrides,
  };
}

function snapshot(overrides: Partial<HostThemeSnapshot> = {}): HostThemeSnapshot {
  return { preference: null, effective: "light", highContrast: false, ...overrides };
}

interface FakeDepsOptions {
  pulled?: AppPreferences;
  pullError?: Error;
  updateResult?: AppPreferences;
  updateError?: Error;
}

function fakeDeps(options: FakeDepsOptions = {}) {
  const events: string[] = [];
  const updates: AppPreferencesUpdate[] = [];
  const handlers = new Set<(snapshot: HostThemeSnapshot) => void>();
  const deps: ThemeProjectionDeps = {
    getPreferences: () => {
      events.push("pull");
      if (options.pullError) return Promise.reject(options.pullError);
      return Promise.resolve(options.pulled ?? preferences());
    },
    updatePreferences: (patch) => {
      events.push(`update:${JSON.stringify(patch)}`);
      updates.push(patch);
      if (options.updateError) return Promise.reject(options.updateError);
      return Promise.resolve(options.updateResult ?? preferences());
    },
    onAppearanceSnapshot: (handler) => {
      events.push("subscribe");
      handlers.add(handler);
      return () => handlers.delete(handler);
    },
  };
  return {
    deps,
    events,
    updates,
    push: (pushed: HostThemeSnapshot) => {
      for (const handler of handlers) handler(pushed);
    },
    subscriberCount: () => handlers.size,
  };
}

describe("themeProjectionService", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    localStorage.clear();
  });

  it("subscribes before pulling and applies the snapshot", async () => {
    const fake = fakeDeps({
      pulled: preferences({ theme: "dark", effectiveTheme: "dark" }),
    });
    const service = createThemeProjectionService(fake.deps);

    await service.initialize();

    expect(fake.events.slice(0, 2)).toEqual(["subscribe", "pull"]);
    const ui = useUiStore();
    expect(ui.themeMode).toBe("dark");
    expect(ui.systemTheme).toBe("dark");
    expect(ui.hostThemeSynced).toBe(true);
  });

  it("applies Host pushes directly and dispose recycles the subscription", async () => {
    const fake = fakeDeps();
    const service = createThemeProjectionService(fake.deps);
    service.attach();
    expect(fake.subscriberCount()).toBe(1);
    service.attach(); // Idempotent.
    expect(fake.subscriberCount()).toBe(1);

    fake.push(snapshot({ preference: "system", effective: "dark", highContrast: true }));
    const ui = useUiStore();
    expect(ui.themeMode).toBe("system");
    expect(ui.systemTheme).toBe("dark");
    expect(ui.highContrast).toBe(true);

    service.dispose();
    expect(fake.subscriberCount()).toBe(0);
    fake.push(snapshot({ preference: "dark", effective: "dark" }));
    expect(ui.themeMode).toBe("system"); // No stale handler ran.
  });

  it("adopts legacy theme after the shell snapshot without a workspace runtime", async () => {
    localStorage.setItem("vt:theme", "dark");
    const fake = fakeDeps({ updateResult: preferences({ theme: "dark", effectiveTheme: "dark" }) });
    const service = createThemeProjectionService(fake.deps);
    service.attach();
    expect(fake.events).toEqual(["subscribe"]);
    fake.push(snapshot());
    await vi.waitFor(() => expect(localStorage.getItem("vt:theme")).toBeNull());
    expect(fake.updates).toEqual([{ adoptLegacyTheme: "dark" }]);
    expect(useUiStore().themeMode).toBe("dark");
    service.dispose();
  });

  it("adopts a valid legacy value only when the Host is unconfigured", async () => {
    localStorage.setItem("vt:theme", "dark");
    const fake = fakeDeps({
      pulled: preferences({ theme: null, effectiveTheme: "light" }),
      updateResult: preferences({ theme: "dark", effectiveTheme: "dark" }),
    });
    const service = createThemeProjectionService(fake.deps);

    await service.initialize();

    expect(fake.updates).toEqual([{ adoptLegacyTheme: "dark" }]);
    // The legacy value is deleted only because the Host reply confirmed the
    // persisted theme equals the adopted value.
    expect(localStorage.getItem("vt:theme")).toBeNull();
    expect(useUiStore().themeMode).toBe("dark");
  });

  it("keeps the legacy value when the Host write fails", async () => {
    localStorage.setItem("vt:theme", "dark");
    const diagnostics: string[] = [];
    const fake = fakeDeps({
      pulled: preferences({ theme: null }),
      updateError: new Error("disk full"),
    });
    const service = createThemeProjectionService(fake.deps, {
      onDiagnostic: (message) => diagnostics.push(message),
    });

    await expect(service.initialize()).resolves.toBeUndefined();

    expect(localStorage.getItem("vt:theme")).toBe("dark");
    expect(diagnostics).toHaveLength(1);
  });

  it("keeps the legacy value when the Host already has an explicit choice", async () => {
    localStorage.setItem("vt:theme", "dark");
    const fake = fakeDeps({
      pulled: preferences({ theme: "system", effectiveTheme: "light" }),
    });
    const service = createThemeProjectionService(fake.deps);

    await service.initialize();

    // Host compare-unset rejected the adoption: no update at all.
    expect(fake.updates).toEqual([]);
    expect(localStorage.getItem("vt:theme")).toBe("dark");
    expect(useUiStore().themeMode).toBe("system");
  });

  it("keeps the legacy value when the adoption did not land", async () => {
    localStorage.setItem("vt:theme", "dark");
    // The Host was concurrently configured to something else before the
    // adoption applied: the reply no longer equals the legacy value.
    const fake = fakeDeps({
      pulled: preferences({ theme: null }),
      updateResult: preferences({ theme: "light", effectiveTheme: "light" }),
    });
    const service = createThemeProjectionService(fake.deps);

    await service.initialize();

    expect(fake.updates).toEqual([{ adoptLegacyTheme: "dark" }]);
    expect(localStorage.getItem("vt:theme")).toBe("dark");
    expect(useUiStore().themeMode).toBe("light");
  });

  it("ignores invalid legacy values without writing or clearing", async () => {
    localStorage.setItem("vt:theme", "sepia");
    const fake = fakeDeps({ pulled: preferences({ theme: null }) });
    const service = createThemeProjectionService(fake.deps);

    await service.initialize();

    expect(fake.updates).toEqual([]);
    expect(localStorage.getItem("vt:theme")).toBe("sepia");
  });

  it("skips migration when the snapshot cannot be pulled", async () => {
    localStorage.setItem("vt:theme", "dark");
    const diagnostics: string[] = [];
    const fake = fakeDeps({ pullError: new Error("bridge down") });
    const service = createThemeProjectionService(fake.deps, {
      onDiagnostic: (message) => diagnostics.push(message),
    });

    await expect(service.initialize()).resolves.toBeUndefined();

    expect(fake.updates).toEqual([]);
    expect(useUiStore().hostThemeSynced).toBe(false);
    expect(diagnostics).toHaveLength(1);
  });

  it("setTheme persists through the Host and applies the reply", async () => {
    const fake = fakeDeps({
      pulled: preferences({ theme: "dark", effectiveTheme: "dark" }),
      updateResult: preferences({ theme: "light", effectiveTheme: "dark" }),
    });
    const service = createThemeProjectionService(fake.deps);
    await service.initialize();

    await service.setTheme("light");

    expect(fake.updates).toEqual([{ theme: "light" }]);
    const ui = useUiStore();
    expect(ui.themeMode).toBe("light");
    expect(ui.systemTheme).toBe("dark");
  });

  it("setTheme rolls back and rethrows when persistence fails", async () => {
    const fake = fakeDeps({
      pulled: preferences({ theme: "dark", effectiveTheme: "dark" }),
      updateError: new Error("write denied"),
    });
    const service = createThemeProjectionService(fake.deps);
    await service.initialize();

    await expect(service.setTheme("light")).rejects.toThrow("write denied");

    const ui = useUiStore();
    expect(ui.themeMode).toBe("dark");
    expect(ui.hostThemeSynced).toBe(true);
  });

  it("legacy storage ports read and clear the vt:theme key", () => {
    expect(legacyThemeStoragePorts.readLegacyTheme()).toBeNull();
    localStorage.setItem("vt:theme", "system");
    expect(legacyThemeStoragePorts.readLegacyTheme()).toBe("system");
    localStorage.setItem("vt:theme", "bogus");
    expect(legacyThemeStoragePorts.readLegacyTheme()).toBeNull();
    localStorage.setItem("vt:theme", "dark");
    legacyThemeStoragePorts.clearLegacyTheme();
    expect(localStorage.getItem("vt:theme")).toBeNull();
  });

  it("snapshotFromPreferences maps the Host preference contract", () => {
    expect(snapshotFromPreferences(preferences())).toEqual(snapshot());
    expect(snapshotFromPreferences(
      preferences({ theme: "system", effectiveTheme: "dark", highContrast: true })),
    ).toEqual(snapshot({ preference: "system", effective: "dark", highContrast: true }));
  });
});
