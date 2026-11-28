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
  /** The current visible application's confirmed token, or null when the view is retired. */
  readonly activeToken: ComputedRef<string | null>;
  /** Advances on every platform navigation, including same-view and null→null departures. */
  readonly navigation: () => number;
  /** Re-reads the catalog so the Host replaces a retired token with a fresh background surface. */
  refresh(): void;
}

/**
 * One user click on an application, as seen by the describe that opens it.
 *
 * The intent confirms exactly one token: the one the catalog projects for the key right before the
 * click's describe is posted. The Host is asked to promote that session and no other, so a session the
 * catalog issued later for the same key is never promoted by an older click. The confirmation is also
 * the record of what the click did: before it nothing was sent and a discarded click retires nothing;
 * after it a discarded click retires exactly that token, and a successor token or a successor click is
 * never touched.
 */
export interface PluginApplicationIntent {
  /** Still the newest click on its key: a superseded or ended intent is inert. */
  readonly active: boolean;
  /** The confirmed token, or null while the click's describe has not been posted. */
  readonly token: string | null;
  /** Called right before the describe is posted, with the token it names. Ignored once the click was superseded. */
  confirm(token: string | null): void;
  /** `true`: the describe confirmed the application and the visible frame is the one it promoted. `false`: the click was discarded. */
  settle(opened: boolean): void;
}

/**
 * Sidebar, toolbar, row context menu and plugin center share this open path.
 * Each click has one intent and sends at most one describe.
 */
export interface PluginApplicationOpen {
  /**
   * Registers the click on `key` before its describe. A hidden frame that
   * never signalled application readiness is closed first (bounded to once), so the click's own catalog
   * read opens a fresh surface through the normal path and the intent confirms that fresh token.
   *
   * `current` is the click's navigation. It is re-checked after the bounded cold close (a click the user
   * already moved on from registers nothing) and on every platform navigation while the click is pending:
   * the moment the user leaves, even when no application became visible, the
   * intent ends and a describe it already posted may have promoted its confirmed token, which is retired
   * and closed at once without waiting for a catalog read.
   */
  begin(key: string, current?: () => boolean): Promise<PluginApplicationIntent>;
}

/** `token` always identifies the surface the state refers to; a new token starts over as `background`. */
type SlotState =
  | { readonly kind: "background"; readonly token: string; readonly ready: boolean }
  | { readonly kind: "foreground"; readonly token: string }
  | { readonly kind: "retired"; readonly token: string };

/** One user click on an application. `token` is set once, by `confirm`, and never rewritten by the catalog. */
interface OpenIntent {
  readonly key: string;
  token: string | null;
  /** A same-key predecessor already described this session, which still needs an owner until the new click posts. */
  readonly inheritedToken: string | null;
  readonly current?: () => boolean;
}

/**
 * Slot state machine for lifecycle-declaring applications (manifest
 * `compatibility.surfaceLifecycle`). Undeclared plugins never enter it.
 *
 * - A frame is mounted hidden only while the Host authority says so: the
 *   catalog entry reports `surfacePhase: "background"` for that very token.
 *   At the background cap, on a Host that projects no phase, or once the
 *   token was promoted, the entry is a normal manual entry (no slot).
 * - A successful click adopts exactly the frame its describe promoted. The
 *   same catalog key can later receive another token and prepare a new
 *   background frame. Only a fresh click can open that replacement.
 * - Leaving the foreground or discarding a click retires
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
  let staleCheckQueued = false;

  /** Ends every pending click whose navigation is over, then reconciles. Runs in the microtask after the
   * navigation: the controller registers the click that caused it (a second click on the same key supersedes
   * the pending one, which retires nothing) before this check, and no Host reply can settle an ended click
   * first. The user's departure still ends the intent at once, with no catalog read. */
  function endStaleIntents(): void {
    if (staleCheckQueued) return;
    staleCheckQueued = true;
    queueMicrotask(() => {
      staleCheckQueued = false;
      for (const intent of latest.values()) {
        if (intent.current && !intent.current()) settleState(intent, false);
      }
      reconcile();
    });
  }

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

  /** Applies the intent's outcome to the slot state; the caller reconciles. */
  function settleState(intent: OpenIntent, opened: boolean): void {
    if (latest.get(intent.key) !== intent) return;
    latest.delete(intent.key);
    const state = states.get(intent.key);
    if (state?.kind !== "background") return;
    if (opened) {
      if (state.token === intent.token && port.activeToken.value === state.token) {
        states.set(intent.key, { kind: "foreground", token: state.token });
      }
      return;
    }
    // A describe may already have promoted the token even if the newest same-key click has yet to post its own.
    if (state.token !== (intent.token ?? intent.inheritedToken)) return;
    retire(intent.key, state.token);
  }

  function reconcile(): void {
    const activeToken = port.activeToken.value;
    const next: PluginAppPrewarmSlot[] = [];
    const live = new Set<string>();
    for (const entry of port.entries.value) {
      if (!entry.declared) continue;
      live.add(entry.key);
      const granted = entry.surfacePhase === "background";
      let state = states.get(entry.key);
      if (state && state.token === entry.surfaceToken) {
        if (state.kind === "retired") continue;
        // A hidden frame that lost its background grant can only be held until the pending click settles.
        if ((state.kind === "foreground" && activeToken !== state.token)
          || (state.kind === "background" && !granted && !latest.has(entry.key))) {
          retire(entry.key, state.token);
          continue;
        }
      } else {
        if (!granted) {
          states.delete(entry.key);
          continue;
        }
        state = { kind: "background", token: entry.surfaceToken, ready: false };
      }
      states.set(entry.key, state);
      next.push({ key: entry.key, entry, phase: state.kind === "foreground" ? "foreground" : "background" });
    }
    for (const key of states.keys()) if (!live.has(key)) states.delete(key);
    slots.value = next;
    // Scheduled only now: the flush above posts the closes before the read goes out.
    if (refreshNeeded) { refreshNeeded = false; scheduleRefresh(); }
  }

  watch(
    () => [port.entries.value, port.activeToken.value] as const,
    reconcile,
    { flush: "sync", immediate: true },
  );
  watch(port.navigation, endStaleIntents, { flush: "sync" });

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
    async begin(key: string, current?: () => boolean): Promise<PluginApplicationIntent> {
      if (isCold(key)) {
        drop(key);
        await nextTick();
      }
      // A click the user already moved on from registers nothing, so it never replaces a newer click.
      if (current && !current()) return { active: false, token: null, confirm: () => undefined, settle: () => undefined };
      const previous = latest.get(key);
      const intent: OpenIntent = { key, token: null, inheritedToken: previous?.token ?? previous?.inheritedToken ?? null, current };
      latest.set(key, intent);
      return {
        get active() { return latest.get(key) === intent; },
        get token() { return intent.token; },
        confirm(token) {
          // Only the newest click on the key speaks for it, and only for the describe it is about to post.
          if (latest.get(key) !== intent || intent.token !== null) return;
          intent.token = token;
        },
        settle(opened) { settleState(intent, opened); reconcile(); },
      };
    },
    isCold,
    drop,
    /** The application signalled `readyToActivate` for `token`: the next click promotes this very element. */
    markReady(key: string, token: string): void {
      const state = states.get(key);
      // A late signal from a replaced frame never vouches for its successor's token.
      if (state?.kind !== "background" || state.token !== token || state.ready) return;
      states.set(key, { ...state, ready: true });
    },
    /** The visible application is exactly this token: a close from any other token (a replaced frame) is not the user leaving it. */
    isActive(key: string, token: string): boolean {
      const state = states.get(key);
      return state?.kind === "foreground" && state.token === token && port.activeToken.value === token;
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
