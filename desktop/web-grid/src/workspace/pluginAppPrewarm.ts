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
  /** Advances on every platform navigation, including same-view and null→null departures. */
  readonly navigation: () => number;
  /** Re-reads the catalog so the Host replaces a retired token with a fresh background surface. */
  refresh(): void;
  /** Binds the active collection Host-side for a surface no click promoted.
   * Resolves true only when the Host confirmed the application for the foreground. */
  promote(key: string): Promise<boolean>;
  /** The Host refused a promotion of the visible application: leave the dead surface with a recoverable notice. */
  rollback(key: string): void;
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
   * the moment the user leaves, even when the application was never open (`activeKey` null → null), the
   * intent ends and a describe it already posted may have promoted its confirmed token, which is retired
   * and closed at once without waiting for a catalog read. A navigation that makes the key active is the
   * click's own open: the confirmed surface is adopted as the visible frame.
   */
  begin(key: string, current?: () => boolean): Promise<PluginApplicationIntent>;
}

/** `token` always identifies the surface the state refers to; a new token starts over as `background`. */
type SlotState =
  | { readonly kind: "background"; readonly token: string; readonly ready: boolean }
  | { readonly kind: "promoting"; readonly token: string }
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
 * - `background → foreground` happens only through a click intent whose
 *   confirmed token is the slot's token (its describe promoted the Host
 *   session: the same element is adopted) or an explicit `promote` for an
 *   activation no click announced. The frame stays hidden while a promotion
 *   is in flight.
 * - The same token without its background grant was promoted: the Host never
 *   demotes a session and never lists a retired token again. The visible one
 *   confirms its promotion; any other hidden frame would keep foreground
 *   authority, so it is retired right away. A pending click owns its key's
 *   token until it settles: its own catalog read may be the very promotion.
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
  let reconciling = false;
  let reconcileAgain = false;
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
      withReconcile(() => {
        for (const intent of [...latest.values()]) {
          if (!intent.current || intent.current()) continue;
          // The user left (the key is not active: its confirmed token goes at once) or this is the click's own open.
          settleState(intent, port.activeKey.value === intent.key);
        }
      });
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
    if (opened) {
      // The describe confirmed the application it promoted: that very surface is the visible frame. A slot on
      // another token (the catalog moved on) is not the one the Host promoted and is adopted by its own click.
      if ((state?.kind === "background" || state?.kind === "promoting") && state.token === intent.token && port.activeKey.value === intent.key) {
        states.set(intent.key, { kind: "foreground", token: state.token });
      }
      return;
    }
    if (!state || state.kind === "retired" || state.kind === "foreground") return;
    // An explicit promotion owns the slot; its own settlement retires it with the rollback notice.
    if (state.kind === "promoting") return;
    // A describe may already have promoted the token even if the newest same-key click has yet to post its own.
    if (state.token !== (intent.token ?? intent.inheritedToken)) return;
    retire(intent.key, state.token);
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
      // The intent adopts only the token it confirmed: the describe promoted that session and no other.
      const confirmed = intent !== undefined && intent.token !== null && intent.token === entry.surfaceToken;
      let state = states.get(entry.key);
      if (state && state.token === entry.surfaceToken) {
        if (state.kind === "retired") continue;
        if (!granted && state.kind !== "foreground" && intent === undefined) {
          // The same token without its background grant was promoted: the Host never demotes a session
          // and never lists a retired token again. The visible one confirms its promotion; any other
          // hidden frame would keep foreground authority, so it is retired right away. A pending click
          // owns its key's token until it settles: its own catalog read may be the very promotion.
          if (entry.key === activeKey) state = { kind: "foreground", token: entry.surfaceToken };
          else { retire(entry.key, entry.surfaceToken); continue; }
        }
      } else {
        if (!granted) {
          states.delete(entry.key);
          continue;
        }
        state = { kind: "background", token: entry.surfaceToken, ready: false };
      }
      if (entry.key === activeKey) {
        if (state.kind === "background") {
          if (confirmed) state = { kind: "foreground", token: entry.surfaceToken };
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

  /** Runs `work` and reconciles once; a reconcile requested from inside one (a synchronous navigation) runs after it. */
  function withReconcile(work: () => void): void {
    reconcileAgain = true;
    if (reconciling) return;
    reconciling = true;
    try { work(); while (reconcileAgain) { reconcileAgain = false; reconcile(); } }
    finally { reconciling = false; }
  }

  async function promote(key: string, token: string): Promise<void> {
    let opened = false;
    try { opened = await port.promote(key); } catch { opened = false; }
    const state = states.get(key);
    if (state?.kind !== "promoting" || state.token !== token || port.activeKey.value !== key) return;
    if (opened) {
      withReconcile(() => states.set(key, { kind: "foreground", token }));
      return;
    }
    withReconcile(() => retire(key, token));
    port.rollback(key);
  }

  watch(
    () => [port.entries.value, port.activeKey.value, port.navigation()] as const,
    () => { endStaleIntents(); withReconcile(() => undefined); },
    { flush: "sync", immediate: true },
  );

  /** The hidden frame has not signalled `readyToActivate` (still loading or failed to start). */
  function isCold(key: string): boolean {
    const state = states.get(key);
    return state?.kind === "background" && !state.ready;
  }
  /** Closes a cold hidden frame once; the catalog read of the click that follows opens a fresh surface. */
  function drop(key: string): void {
    const state = states.get(key);
    if (state?.kind !== "background") return;
    withReconcile(() => states.set(key, { kind: "retired", token: state.token }));
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
        settle(opened) { withReconcile(() => settleState(intent, opened)); },
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
      return state?.kind === "foreground" && state.token === token && port.activeKey.value === key;
    },
    /** A mounted background surface closed itself; keep that token retired until the catalog moves on.
     * `token` identifies the frame that emitted the close, so a frame unmounted by a replacement never retires it. */
    selfClose(key: string, token: string): void {
      const state = states.get(key);
      if (!state || state.kind === "retired" || state.token !== token) return;
      withReconcile(() => states.set(key, { kind: "retired", token }));
    },
  };
}

export type PluginAppPrewarm = ReturnType<typeof createPluginAppPrewarm>;
