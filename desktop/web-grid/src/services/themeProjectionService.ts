import { useHostBridge } from "@/services/bridgeContext";
import { useAppPreferencesService } from "@/services/appPreferencesService";
import { useUiStore } from "@/stores/uiStore";
import type { SystemBaseTheme, ThemeMode } from "@/stores/uiStore";
import type {
  AppPreferences,
  AppPreferencesUpdate,
  HostAppearancePayload,
} from "@/contracts/appPreferencesContracts";

/** Host-authoritative appearance snapshot. A null preference means the Host
 * has never persisted a theme — distinct from an explicit "system". */
export interface HostThemeSnapshot {
  readonly preference: ThemeMode | null;
  readonly effective: SystemBaseTheme;
  readonly highContrast: boolean;
}

/** Narrow transport seams so tests stay bridge-free; production binds them to
 * the existing appPreferences get/update requests and the appearance event. */
export interface ThemeProjectionDeps {
  readonly getPreferences: () => Promise<AppPreferences>;
  readonly updatePreferences: (patch: AppPreferencesUpdate) => Promise<AppPreferences>;
  readonly onAppearanceSnapshot: (
    handler: (snapshot: HostThemeSnapshot) => void,
  ) => () => void;
}

export interface ThemeProjectionOptions {
  readonly onDiagnostic?: (message: string) => void;
}

export interface ThemeProjectionService {
  /** Subscribe to Host pushes. Safe (and required) before `app.ready`; the
   * Host posts the initial snapshot right after the handshake. */
  attach(): void;
  /** Pull + apply the snapshot and run the one-time legacy migration. Call
   * only after the startup handshake is ready — the Host rejects requests
   * arriving before `app.ready`. Idempotent. */
  initialize(): Promise<void>;
  /** Persist a theme choice to the Host; rolls back the optimistic local
   * projection and rethrows when persistence fails. */
  setTheme(mode: ThemeMode): Promise<void>;
  /** Apply a Host snapshot to the UI store. */
  applySnapshot(snapshot: HostThemeSnapshot): void;
  /** Recycle the push subscription (tests / teardown). */
  dispose(): void;
}

export const LEGACY_THEME_STORAGE_KEY = "vt:theme";

export function isThemeMode(value: unknown): value is ThemeMode {
  return value === "light" || value === "dark" || value === "system";
}

/** Legacy localStorage access, isolated so the store stays storage-free. */
export const legacyThemeStoragePorts = {
  readLegacyTheme(): ThemeMode | null {
    try {
      const stored = localStorage.getItem(LEGACY_THEME_STORAGE_KEY);
      return isThemeMode(stored) ? stored : null;
    } catch {
      return null;
    }
  },
  clearLegacyTheme(): void {
    try {
      localStorage.removeItem(LEGACY_THEME_STORAGE_KEY);
    } catch {
      // Storage may be unavailable; the legacy value stays put and the next
      // launch retries the adoption.
    }
  },
};

export function snapshotFromPreferences(preferences: AppPreferences): HostThemeSnapshot {
  return {
    preference: preferences.theme ?? null,
    effective: preferences.effectiveTheme,
    highContrast: preferences.highContrast,
  };
}

export function snapshotFromAppearance(payload: HostAppearancePayload): HostThemeSnapshot {
  return {
    preference: payload.preference ?? null,
    effective: payload.effective,
    highContrast: payload.highContrast,
  };
}

export function createThemeProjectionService(
  deps: ThemeProjectionDeps,
  options: ThemeProjectionOptions = {},
): ThemeProjectionService {
  const onDiagnostic = options.onDiagnostic ?? (() => undefined);
  let unsubscribe: (() => void) | null = null;
  let lastSnapshot: HostThemeSnapshot | null = null;
  let initialized = false;

  function applySnapshot(snapshot: HostThemeSnapshot): void {
    lastSnapshot = snapshot;
    useUiStore().applyThemeProjection(snapshot);
  }

  function attach(): void {
    if (unsubscribe) return;
    unsubscribe = deps.onAppearanceSnapshot(applySnapshot);
  }

  async function initialize(): Promise<void> {
    if (initialized) return;
    initialized = true;
    attach();
    let snapshot: HostThemeSnapshot | null = null;
    try {
      snapshot = snapshotFromPreferences(await deps.getPreferences());
    } catch (error) {
      onDiagnostic(`theme snapshot request failed: ${describe(error)}`);
    }
    if (snapshot) {
      applySnapshot(snapshot);
    }
    await adoptLegacyTheme(snapshot);
  }

  /** Adopts the legacy renderer value only when the Host never persisted a
   * theme; the Host applies compare-unset semantics. The legacy value is
   * deleted only when the Host reply confirms the persisted theme actually
   * equals the legacy value — a failure or an already-explicit Host choice
   * (including explicit "system") keeps it for the next launch. */
  async function adoptLegacyTheme(snapshot: HostThemeSnapshot | null): Promise<void> {
    if (!snapshot || snapshot.preference !== null) {
      return;
    }
    const legacy = legacyThemeStoragePorts.readLegacyTheme();
    if (!legacy) {
      return;
    }
    try {
      const updated = await deps.updatePreferences({ adoptLegacyTheme: legacy });
      applySnapshot(snapshotFromPreferences(updated));
      if ((updated.theme ?? null) === legacy) {
        legacyThemeStoragePorts.clearLegacyTheme();
      }
    } catch (error) {
      // Migration failure keeps the legacy value and never blocks startup.
      onDiagnostic(`legacy theme adoption failed: ${describe(error)}`);
    }
  }

  async function setTheme(mode: ThemeMode): Promise<void> {
    const rollback = lastSnapshot;
    useUiStore().setThemeMode(mode);
    try {
      applySnapshot(snapshotFromPreferences(await deps.updatePreferences({ theme: mode })));
    } catch (error) {
      if (rollback) {
        applySnapshot(rollback);
      }
      throw error;
    }
  }

  function dispose(): void {
    unsubscribe?.();
    unsubscribe = null;
    initialized = false;
  }

  return { attach, initialize, setTheme, applySnapshot, dispose };
}

/** Production singleton bound to the shared host bridge: reuses the existing
 * `appPreferences.get` / `appPreferences.update` requests plus the
 * `appearance.changed` push — no separate transport. */
let singleton: ThemeProjectionService | null = null;

export function useThemeProjectionService(): ThemeProjectionService {
  if (!singleton) {
    const preferences = useAppPreferencesService();
    const bridge = useHostBridge();
    singleton = createThemeProjectionService({
      getPreferences: () => preferences.get(),
      updatePreferences: (patch) => preferences.update(patch),
      onAppearanceSnapshot: (handler) =>
        bridge.on("appearance.changed", (payload) =>
          handler(snapshotFromAppearance(payload))),
    });
  }
  return singleton;
}

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
