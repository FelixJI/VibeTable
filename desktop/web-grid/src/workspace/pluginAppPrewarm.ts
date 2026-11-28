import { nextTick, ref, watch, type ComputedRef, type Ref } from "vue";
import type { PluginSurfacePhase } from "@/contracts";
import type { PluginAppEntry } from "./pluginAppNavigation";

/**
 * One mounted plugin application surface inside the pre-warm layer.
 * `background` surfaces are loaded and hidden: the Host admits read-only
 * requests only, and they never receive focus, pointer input or visibility.
 */
export interface PluginAppPrewarmSlot {
  readonly key: string;
  readonly entry: PluginAppEntry;
  readonly phase: PluginSurfacePhase;
}

export interface PluginAppPrewarmPort {
  /** Enabled catalog applications with a live Host token. */
  readonly entries: ComputedRef<readonly PluginAppEntry[]>;
  /** The application the user has open, or null (platform navigation, workspace center, native chrome). */
  readonly activeKey: ComputedRef<string | null>;
  /** Re-reads the catalog so the Host replaces a retired token with a fresh background surface. */
  refresh(): void;
  /** Binds the active collection Host-side for a surface the click path did not already promote.
   * Resolves true only when the Host confirmed the application for the foreground. */
  promote(key: string): Promise<boolean>;
  /** The Host refused a promotion: leave the dead surface with a recoverable notice. */
  rollback(key: string): void;
}

/** `token` always identifies the surface the state refers to; a new token starts over as `background`. */
type SlotState =
  | { readonly kind: "background"; readonly token: string }
  | { readonly kind: "promoting"; readonly token: string }
  | { readonly kind: "foreground"; readonly token: string }
  | { readonly kind: "retired"; readonly token: string };

/**
 * Slot state machine for lifecycle-declaring applications (manifest
 * `compatibility.surfaceLifecycle`). Undeclared plugins never enter it.
 *
 * - Every declared entry with a live token keeps a hidden `background` frame,
 *   so the user's click reuses the loaded surface instead of mounting one.
 * - `background → foreground` happens only through `expectOpen` (the click
 *   path's describe already promoted the Host session) or an explicit
 *   `promote`; the frame stays hidden while a promotion is in flight.
 * - Leaving the foreground retires the token: the Host session is
 *   foreground-only, so hiding it would keep write authority alive. The
 *   catalog is refreshed afterwards and a fresh background surface takes over.
 * - A background surface that closes itself stays retired for that token, so
 *   the Host's replacement token does not create a remount loop.
 */
export function createPluginAppPrewarm(port: PluginAppPrewarmPort) {
  const states = new Map<string, SlotState>();
  const expected = new Set<string>();
  const slots: Ref<readonly PluginAppPrewarmSlot[]> = ref([]);
  let refreshScheduled = false;

  function scheduleRefresh(): void {
    if (refreshScheduled) return;
    refreshScheduled = true;
    void nextTick(() => { refreshScheduled = false; port.refresh(); });
  }

  function reconcile(): void {
    const activeKey = port.activeKey.value;
    const next: PluginAppPrewarmSlot[] = [];
    const live = new Set<string>();
    for (const entry of port.entries.value) {
      if (!entry.prewarm) continue;
      live.add(entry.key);
      let state = states.get(entry.key);
      if (!state || state.token !== entry.surfaceToken) state = { kind: "background", token: entry.surfaceToken };
      else if (state.kind === "retired") continue;
      if (entry.key === activeKey) {
        if (state.kind === "background") {
          if (expected.delete(entry.key)) state = { kind: "foreground", token: entry.surfaceToken };
          else {
            state = { kind: "promoting", token: entry.surfaceToken };
            void promote(entry.key, entry.surfaceToken);
          }
        }
      } else if (state.kind !== "background") {
        states.set(entry.key, { kind: "retired", token: entry.surfaceToken });
        scheduleRefresh();
        continue;
      }
      states.set(entry.key, state);
      next.push({ key: entry.key, entry, phase: state.kind === "foreground" ? "foreground" : "background" });
    }
    for (const key of states.keys()) if (!live.has(key)) states.delete(key);
    slots.value = next;
  }

  async function promote(key: string, token: string): Promise<void> {
    let opened = false;
    try { opened = await port.promote(key); } catch { opened = false; }
    const state = states.get(key);
    // The user left, the catalog replaced the token or a newer promotion owns the slot meanwhile.
    if (state?.kind !== "promoting" || state.token !== token) return;
    if (opened) {
      states.set(key, { kind: "foreground", token });
      reconcile();
      return;
    }
    states.set(key, { kind: "retired", token });
    reconcile();
    port.rollback(key);
    scheduleRefresh();
  }

  watch(() => [port.entries.value, port.activeKey.value] as const, reconcile, { flush: "sync", immediate: true });

  return {
    slots,
    /** The click path: the following activation of `key` was already promoted by its describe call. */
    expectOpen(key: string): () => void {
      expected.add(key);
      return () => { expected.delete(key); };
    },
    /** A mounted background surface closed itself; keep that token retired until the catalog moves on. */
    selfClose(key: string): void {
      const state = states.get(key);
      if (!state || state.kind === "retired") return;
      states.set(key, { kind: "retired", token: state.token });
      reconcile();
    },
  };
}
