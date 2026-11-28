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
  /** Binds the active collection Host-side for a surface no click promoted.
   * Resolves true only when the Host confirmed the application for the foreground. */
  promote(key: string): Promise<boolean>;
  /** The Host refused a promotion of the visible application: leave the dead surface with a recoverable notice. */
  rollback(key: string): void;
}

/**
 * The single funnel every entry point that opens an application goes through (sidebar, toolbar, row and
 * column menus, plugin center): one click = one intent = one describe.
 */
export interface PluginApplicationOpen {
  /**
   * Registers the click on `key` before its describe runs and resolves the finalizer for when the click
   * path settles. A hidden frame that never signalled application readiness is closed first (bounded to
   * once), so the click's own catalog read opens a fresh surface through the normal path.
   *
   * - `settle(true)`: the describe confirmed the application; the hidden surface it promoted on the same
   *   token becomes the visible frame without a reload or a second describe.
   * - `settle(false)`: the navigation was discarded (cancel, superseded click, refused describe). The
   *   describe may already have promoted the session, so the token the intent captured is retired and
   *   closed at once, without waiting for a catalog read. A newer click on the same key supersedes the
   *   intent: its finalizer is a no-op and never touches the successor's surface.
   *
   * `current` is re-checked after the bounded cold close: a click the user already moved on from registers nothing.
   */
  begin(key: string, current?: () => boolean): Promise<(opened: boolean) => void>;
}

/** `token` always identifies the surface the state refers to; a new token starts over as `background`. */
type SlotState =
  | { readonly kind: "background"; readonly token: string; readonly ready: boolean }
  | { readonly kind: "promoting"; readonly token: string }
  | { readonly kind: "foreground"; readonly token: string }
  | { readonly kind: "retired"; readonly token: string };

/** One user click on an application. `token` is the hidden surface the click may have promoted: captured from the
 * catalog while the intent is pending (null until one showed up). */
interface OpenIntent {
  readonly key: string;
  token: string | null;
}

/**
 * Slot state machine for lifecycle-declaring applications (manifest
 * `compatibility.surfaceLifecycle`). Undeclared plugins never enter it.
 *
 * - A frame is mounted hidden only while the Host authority says so: the
 *   catalog entry reports `surfacePhase: "background"` for that very token.
 *   At the background cap, on a Host that projects no phase, or once the
 *   token was promoted, the entry is a normal manual entry (no slot).
 * - `background → foreground` happens through a click intent (its describe
 *   promoted the Host session: the same element is adopted) or an explicit
 *   `promote` for an activation no click announced. The frame stays hidden
 *   while a promotion is in flight.
 * - The same token without its background grant was promoted: the Host never
 *   demotes a session and never lists a retired token again. The active
 *   application confirms its promotion; any other hidden frame would keep
 *   foreground authority, so it is retired right away.
 * - Leaving the foreground, a discarded click or a refused promotion retires
 *   the token deterministically, without a catalog read: the Host session is
 *   foreground-only, so hiding it would keep write authority alive. The
 *   catalog is refreshed afterwards and a fresh background surface takes over.
 * - A hidden frame that has not signalled `readyToActivate` is never promoted
 *   in place (that would show a blank page): the click closes it once and its
 *   catalog read opens a fresh surface through the normal path.
 * - A background surface that closes itself stays retired for that token, so
 *   the Host's replacement token does not create a remount loop.
 */
export function createPluginAppPrewarm(port: PluginAppPrewarmPort) {
  const states = new Map<string, SlotState>();
  /** The newest click per key. An older finalizer is a no-op, so it can never close the successor's surface. */
  const latest = new Map<string, OpenIntent>();
  const slots: Ref<readonly PluginAppPrewarmSlot[]> = ref([]);
  let refreshScheduled = false;
  let refreshNeeded = false;

  function scheduleRefresh(): void {
    if (refreshScheduled) return;
    refreshScheduled = true;
    // Runs after the pending flush, so a frame retired above has already posted its close and
    // the Host issues a replacement token instead of handing the retired one back.
    void nextTick(() => { refreshScheduled = false; port.refresh(); });
  }

  /** Retires the token; the catalog read follows in `reconcile`, after the flush that posts the frame close. */
  function retire(key: string, token: string): void {
    states.set(key, { kind: "retired", token });
    refreshNeeded = true;
  }

  function reconcile(): void {
    const activeKey = port.activeKey.value;
    const next: PluginAppPrewarmSlot[] = [];
    const live = new Set<string>();
    for (const entry of port.entries.value) {
      if (!entry.declared) continue;
      live.add(entry.key);
      const granted = entry.surfacePhase === "background";
      const intent = latest.get(entry.key);
      if (intent) intent.token = entry.surfaceToken;
      let state = states.get(entry.key);
      if (state && state.token === entry.surfaceToken) {
        if (state.kind === "retired") continue;
        if (!granted && state.kind !== "foreground" && !intent) {
          // The same token without its background grant was promoted: the Host never demotes a session
          // and never lists a retired token again. The visible one confirms its promotion; any other
          // hidden frame would keep foreground authority, so it is retired right away. A pending click
          // owns its token until it settles: the describe may be the very promotion.
          if (entry.key === activeKey) state = { kind: "foreground", token: entry.surfaceToken };
          else { retire(entry.key, entry.surfaceToken); continue; }
        }
      } else {
        if (!granted) continue;
        state = { kind: "background", token: entry.surfaceToken, ready: false };
      }
      if (entry.key === activeKey) {
        if (state.kind === "background") {
          if (intent) state = { kind: "foreground", token: entry.surfaceToken };
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
    // Scheduled only now: the flush above posts the closes before the read goes out.
    if (refreshNeeded) { refreshNeeded = false; scheduleRefresh(); }
  }

  async function promote(key: string, token: string): Promise<void> {
    let opened = false;
    try { opened = await port.promote(key); } catch { opened = false; }
    const state = states.get(key);
    if (opened) {
      // The click path's own intent may already have adopted the surface, or the catalog confirmed the promotion.
      if (state?.kind === "promoting" && state.token === token) { states.set(key, { kind: "foreground", token }); reconcile(); }
      return;
    }
    // The user left, the catalog replaced the token or a newer promotion owns the slot meanwhile.
    if (state?.kind === "promoting" && state.token === token) { retire(key, token); reconcile(); }
    if (!state || state.kind === "foreground" || port.activeKey.value !== key) return;
    port.rollback(key);
  }

  function settle(intent: OpenIntent, opened: boolean): void {
    if (latest.get(intent.key) !== intent) return;
    latest.delete(intent.key);
    const state = states.get(intent.key);
    if (!state || state.kind === "retired") return;
    if (opened) {
      // The describe confirmed the application: the hidden surface of this key is the one it promoted.
      if (state.kind !== "foreground" && port.activeKey.value === intent.key) {
        states.set(intent.key, { kind: "foreground", token: state.token });
        reconcile();
      }
      return;
    }
    if (state.kind === "foreground") return;
    // An explicit promotion owns the slot; its own settlement retires it with the rollback notice.
    if (state.kind === "promoting") return;
    // Fail closed: the describe may already have promoted this session. The captured token goes now, a
    // successor token (or a successor click) is never touched.
    if (intent.token === null || state.token !== intent.token) return;
    retire(intent.key, state.token);
    reconcile();
  }

  watch(() => [port.entries.value, port.activeKey.value] as const, reconcile, { flush: "sync", immediate: true });

  /** The hidden frame has not signalled `readyToActivate` (still loading or failed to start). */
  function isCold(key: string): boolean {
    const state = states.get(key);
    return state?.kind === "background" && !state.ready;
  }
  /** Closes a cold hidden frame once; the catalog read of the click that follows opens a fresh surface. */
  function drop(key: string): void {
    const state = states.get(key);
    if (state?.kind !== "background") return;
    states.set(key, { kind: "retired", token: state.token });
    reconcile();
  }

  return {
    slots,
    async begin(key: string, current?: () => boolean): Promise<(opened: boolean) => void> {
      if (isCold(key)) {
        drop(key);
        await nextTick();
      }
      const superseded = (): void => undefined;
      if (current && !current()) return superseded;
      const state = states.get(key);
      const intent: OpenIntent = { key, token: state && state.kind !== "retired" ? state.token : null };
      latest.set(key, intent);
      return opened => settle(intent, opened);
    },
    isCold,
    drop,
    /** The application signalled `readyToActivate`: the next click promotes this very element. */
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

export type PluginAppPrewarm = ReturnType<typeof createPluginAppPrewarm>;
