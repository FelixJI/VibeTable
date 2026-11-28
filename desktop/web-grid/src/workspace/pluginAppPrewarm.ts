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
  | { readonly kind: "background"; readonly token: string; readonly ready: boolean }
  | { readonly kind: "promoting"; readonly token: string }
  | { readonly kind: "foreground"; readonly token: string }
  | { readonly kind: "retired"; readonly token: string };

/**
 * Slot state machine for lifecycle-declaring applications (manifest
 * `compatibility.surfaceLifecycle`). Undeclared plugins never enter it.
 *
 * - A frame is mounted hidden only while the Host authority says so: the
 *   catalog entry reports `surfacePhase: "background"` for that very token.
 *   At the background cap, on a Host that projects no phase, or once the
 *   token was promoted, the entry is a normal manual entry (no slot).
 * - `background → foreground` happens only through `expectOpen` (the click
 *   path's describe already promoted the Host session) or an explicit
 *   `promote`; the frame stays hidden while a promotion is in flight.
 * - Leaving the foreground retires the token: the Host session is
 *   foreground-only, so hiding it would keep write authority alive. The
 *   catalog is refreshed afterwards and a fresh background surface takes over.
 * - A token that lost its background grant while still hidden is retired the
 *   same way: a hidden frame never keeps foreground authority.
 * - A hidden frame that has not signalled `ready` is never promoted in place
 *   (that would show a blank page): the click closes it once through `drop`
 *   and its catalog read opens a fresh surface through the normal path.
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
    // Runs after the pending flush, so a frame retired above has already posted its close and
    // the Host issues a replacement token instead of handing the retired one back.
    void nextTick(() => { refreshScheduled = false; port.refresh(); });
  }

  function retire(key: string, token: string): void {
    states.set(key, { kind: "retired", token });
    scheduleRefresh();
  }

  function reconcile(): void {
    const activeKey = port.activeKey.value;
    const next: PluginAppPrewarmSlot[] = [];
    const live = new Set<string>();
    for (const entry of port.entries.value) {
      if (!entry.declared) continue;
      live.add(entry.key);
      const granted = entry.surfacePhase === "background";
      let state = states.get(entry.key);
      if (state && state.token === entry.surfaceToken) {
        if (state.kind === "retired") continue;
        if (!granted && state.kind !== "foreground") {
          retire(entry.key, entry.surfaceToken);
          continue;
        }
      } else {
        if (!granted) continue;
        state = { kind: "background", token: entry.surfaceToken, ready: false };
      }
      if (entry.key === activeKey) {
        if (state.kind === "background") {
          if (expected.delete(entry.key)) state = { kind: "foreground", token: entry.surfaceToken };
          else {
            state = { kind: "promoting", token: entry.surfaceToken };
            void promote(entry.key, entry.surfaceToken);
          }
        }
      } else if (state.kind !== "background") {
        retire(entry.key, entry.surfaceToken);
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
    /**
     * The click path: the following activation of `key` was already promoted by its describe call.
     * The returned finalizer runs when the click path settles: a slot that is still hidden may hold a
     * session its describe promoted although the view discarded the navigation, so the authority is
     * re-read and a token the Host no longer grants in background is retired.
     */
    expectOpen(key: string): () => void {
      expected.add(key);
      return () => {
        expected.delete(key);
        const state = states.get(key);
        if (state && state.kind !== "foreground") scheduleRefresh();
      };
    },
    /** The hidden frame has not signalled `ready` (still loading or failed to start). */
    isCold(key: string): boolean {
      const state = states.get(key);
      return state?.kind === "background" && !state.ready;
    },
    /** Closes a cold hidden frame once; the catalog read of the click that follows opens a fresh surface. */
    drop(key: string): void {
      const state = states.get(key);
      if (state?.kind !== "background") return;
      states.set(key, { kind: "retired", token: state.token });
      reconcile();
    },
    /** The frame completed the SDK `ready` handshake: the next click promotes this very element. */
    markReady(key: string): void {
      const state = states.get(key);
      if (state?.kind !== "background" || state.ready) return;
      states.set(key, { ...state, ready: true });
    },
    /** A mounted background surface closed itself; keep that token retired until the catalog moves on.
     * `token` identifies the frame that emitted the close, so a frame unmounted by a replacement never retires it. */
    selfClose(key: string, token: string): void {
      const state = states.get(key);
      if (!state || state.kind === "retired" || state.token !== token) return;
      states.set(key, { kind: "retired", token });
      reconcile();
    },
  };
}
