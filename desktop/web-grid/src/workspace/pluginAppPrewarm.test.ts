import { computed, nextTick, ref } from "vue";
import { describe, expect, it, vi } from "vitest";
import type { PluginSurfacePhase } from "@/contracts";
import type { PluginAppEntry } from "./pluginAppNavigation";
import { createPluginAppPrewarm } from "./pluginAppPrewarm";

/** `phase` is the Host projection for this token: `background` (granted), `null` (foreground / cap / old Host). */
function entry(key: string, token: string, options: { declared?: boolean; phase?: PluginSurfacePhase | null } = {}): PluginAppEntry {
  const [pluginId, actionId] = key.split("/");
  const declared = options.declared ?? true;
  const surfacePhase = options.phase === undefined ? "background" : options.phase;
  return { key, pluginId: pluginId!, actionId: actionId!, label: key, src: `https://${token}.plugins.vibetable.local/index.html`,
    surfaceToken: token, revision: 1, declared, surfacePhase, prewarm: declared && surfacePhase === "background" };
}
function setup(initial: readonly PluginAppEntry[]) {
  const entries = ref<readonly PluginAppEntry[]>(initial);
  const activeKey = ref<string | null>(null);
  const navigation = ref(0);
  const refresh = vi.fn();
  const promote = vi.fn<(key: string) => Promise<boolean>>(() => new Promise<boolean>(() => undefined));
  const rollback = vi.fn();
  const prewarm = createPluginAppPrewarm({ entries: computed(() => entries.value), activeKey: computed(() => activeKey.value),
    navigation: () => navigation.value, refresh, promote, rollback });
  const view = () => prewarm.slots.value.map(slot => [slot.key, slot.entry.surfaceToken, slot.phase]);
  return { prewarm, entries, activeKey, navigation, refresh, promote, rollback, view };
}
const settleAfter = async () => { await Promise.resolve(); await Promise.resolve(); };
const A = "com.acme.a/open";
const B = "com.acme.b/open";

describe("createPluginAppPrewarm", () => {
  it("mounts one hidden slot per declared application the Host granted in background; every other entry stays manual", () => {
    const { view, promote, refresh } = setup([
      entry(A, "a-1"), entry(B, "b-1"), entry("com.acme.c/open", "c-1"), entry("com.acme.d/open", "d-1"),
      // Declared, but beyond the background cap: the Host opened it foreground (projected as null).
      entry("com.acme.e/open", "e-1", { phase: null }), entry("com.acme.f/open", "f-1", { phase: "foreground" }),
      // Declared on a Host that projects no phase at all.
      entry("com.acme.old/open", "old-1", { phase: null }),
      // Undeclared, even if a Host were to project background.
      entry("com.acme.legacy/open", "legacy-1", { declared: false }),
    ]);
    expect(view()).toEqual([
      [A, "a-1", "background"], [B, "b-1", "background"],
      ["com.acme.c/open", "c-1", "background"], ["com.acme.d/open", "d-1", "background"],
    ]);
    expect(promote).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("lets the click adopt exactly the token its describe promoted, confirmed once right before the describe, without a second describe", async () => {
    const { prewarm, activeKey, promote, refresh, view, entries } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    expect(intent.active).toBe(true);
    expect(intent.token).toBeNull();
    intent.confirm("a-1"); // the click's catalog read, right before its describe is posted
    expect(intent.token).toBe("a-1");
    intent.confirm("a-2"); // a confirmation is a single set point
    expect(intent.token).toBe("a-1");
    activeKey.value = A; // the describe promoted that session and the controller opened the application
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(promote).not.toHaveBeenCalled();
    intent.settle(true);
    intent.settle(false);
    await nextTick();
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
    expect(intent.active).toBe(false);
    // The catalog later confirms the promotion (no phase for the token): nothing changes.
    entries.value = [entry(A, "a-1", { phase: null })];
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
  });

  it("never adopts a token the click did not confirm: an activation without one goes through the explicit promotion", async () => {
    const { prewarm, activeKey, promote, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(promote).toHaveBeenCalledWith(A);
    intent.settle(true); // nothing was confirmed: the click's settlement adopts nothing
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(promote).toHaveBeenCalledTimes(1);
  });

  it("keeps an activation no click announced hidden until the Host promotes it, and rolls back when it refuses", async () => {
    const { activeKey, promote, rollback, refresh, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    let refuse!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { refuse = resolve; }));
    activeKey.value = A;
    expect(promote).toHaveBeenCalledWith(A);
    expect(view()).toEqual([[A, "a-1", "background"], [B, "b-1", "background"]]);
    refuse(true); await settleAfter();
    expect(view()).toEqual([[A, "a-1", "foreground"], [B, "b-1", "background"]]);
    expect(rollback).not.toHaveBeenCalled();
    // Leaving the foreground retires the host-side foreground session at once and refreshes the catalog.
    activeKey.value = null;
    expect(view()).toEqual([[B, "b-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
    // The refused case: a dead surface is discarded with a notice and a fresh catalog read.
    let refuseB!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { refuseB = resolve; }));
    activeKey.value = B;
    expect(promote).toHaveBeenCalledWith(B);
    expect(view()).toEqual([[B, "b-1", "background"]]);
    refuseB(false); await settleAfter();
    expect(rollback).toHaveBeenCalledWith(B);
    expect(view()).toEqual([]);
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("confirms an explicit promotion from the catalog: the same token without its grant was promoted, never retired", async () => {
    const { entries, activeKey, promote, rollback, refresh, view } = setup([entry(A, "a-1")]);
    // No click announced this activation: the slot waits hidden for the Host.
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(promote).toHaveBeenCalledTimes(1);
    // The promotion's own catalog read projects the promoted session as `null` for the very same token.
    entries.value = [entry(A, "a-1", { phase: null })];
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(rollback).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("re-pre-warms on a fresh token after the user left and keeps a self-closed token retired", async () => {
    const { prewarm, entries, activeKey, refresh, promote, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    intent.confirm("a-1");
    activeKey.value = A; intent.settle(true);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(prewarm.isActive(A, "a-1")).toBe(true);
    expect(prewarm.isActive(A, "a-0")).toBe(false);
    activeKey.value = null; await nextTick();
    expect(view()).toEqual([]);
    expect(refresh).toHaveBeenCalledTimes(1);
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([[A, "a-2", "background"]]);
    // A background plugin that closes itself must not trigger a remount loop.
    prewarm.selfClose(A, "a-2");
    expect(view()).toEqual([]);
    expect(refresh).toHaveBeenCalledTimes(1);
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([]);
    expect(promote).not.toHaveBeenCalled();
    // Only the Host's replacement token brings the surface back; a stale close from the old frame cannot retire it.
    entries.value = [entry(A, "a-3")];
    expect(view()).toEqual([[A, "a-3", "background"]]);
    prewarm.selfClose(A, "a-2");
    expect(view()).toEqual([[A, "a-3", "background"]]);
    // Nor can a late readiness signal from the replaced frame vouch for its successor.
    expect(prewarm.isCold(A)).toBe(true);
    prewarm.markReady(A, "a-2");
    expect(prewarm.isCold(A)).toBe(true);
    prewarm.markReady(A, "a-3");
    expect(prewarm.isCold(A)).toBe(false);
  });

  it("promotes a replaced token of the active application and drops retired catalog entries", async () => {
    const { prewarm, entries, activeKey, promote, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    intent.confirm("a-1");
    activeKey.value = A; intent.settle(true);
    let admit!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { admit = resolve; }));
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([[A, "a-2", "background"]]);
    expect(promote).toHaveBeenCalledWith(A);
    admit(true); await settleAfter();
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    // Disable / uninstall: the catalog drops the entry and the shell leaves the app; a later
    // reinstall starts cold without re-promoting the stale activation.
    entries.value = [];
    expect(view()).toEqual([]);
    activeKey.value = null;
    entries.value = [entry(A, "a-3")];
    expect(view()).toEqual([[A, "a-3", "background"]]);
    expect(promote).toHaveBeenCalledTimes(1);
    expect(refresh).toHaveBeenCalledTimes(0);
  });

  it("ignores a refused promotion after the active application gets a replacement token", async () => {
    const { entries, activeKey, promote, rollback, refresh, view } = setup([entry(A, "a-1")]);
    let refuseOld!: (ok: boolean) => void;
    let admitNew!: (ok: boolean) => void;
    promote
      .mockImplementationOnce(() => new Promise<boolean>(resolve => { refuseOld = resolve; }))
      .mockImplementationOnce(() => new Promise<boolean>(resolve => { admitNew = resolve; }));
    activeKey.value = A;
    entries.value = [entry(A, "a-2")];
    expect(promote).toHaveBeenCalledTimes(2);
    refuseOld(false); await settleAfter();
    expect(rollback).not.toHaveBeenCalled();
    expect(view()).toEqual([[A, "a-2", "background"]]);
    admitNew(true); await settleAfter();
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("ignores a promotion that settles after the user already left", async () => {
    const { activeKey, promote, rollback, view } = setup([entry(A, "a-1")]);
    let admit!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { admit = resolve; }));
    activeKey.value = A;
    activeKey.value = null;
    expect(view()).toEqual([]);
    admit(true); await settleAfter();
    expect(view()).toEqual([]);
    expect(rollback).not.toHaveBeenCalled();
  });

  it("reclaims a discarded click at once: the token its describe may have promoted is retired and closed without a catalog read", async () => {
    const { prewarm, entries, refresh, promote, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    // The user navigated away while the describe was in flight: the app never opened, but the Host
    // session may already be foreground. The settlement retires the confirmed token deterministically.
    intent.confirm("a-1");
    expect(view()).toEqual([[A, "a-1", "background"], [B, "b-1", "background"]]);
    intent.settle(false);
    expect(view()).toEqual([[B, "b-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
    // The catalog may still project the promoted session: it is already retired, nothing further happens.
    entries.value = [entry(A, "a-1", { phase: null }), entry(B, "b-1")];
    expect(view()).toEqual([[B, "b-1", "background"]]);
    expect(promote).not.toHaveBeenCalled();
    // The frame unmounted (its close retires the Host session) and the replacement token is pre-warmed.
    entries.value = [entry(A, "a-2"), entry(B, "b-1")];
    expect(view()).toEqual([[A, "a-2", "background"], [B, "b-1", "background"]]);
  });

  it("a discarded click whose describe never left retires nothing", async () => {
    const { prewarm, refresh, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    expect(intent.token).toBeNull();
    intent.settle(false); // the entry vanished before any describe was posted
    expect(view()).toEqual([[A, "a-1", "background"], [B, "b-1", "background"]]);
    await nextTick();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("a late rollback applies to the original token only: a successor token the catalog issued meanwhile is never touched", async () => {
    const { prewarm, entries, activeKey, refresh, promote, view } = setup([entry(A, "a-1")]);
    const intent = await prewarm.begin(A);
    intent.confirm("a-1");
    // The frame closed itself after the describe was posted: the Host issued a replacement.
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([[A, "a-2", "background"]]);
    intent.settle(false); // the original describe comes back refused
    expect(view()).toEqual([[A, "a-2", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    // The successor is opened by its own click, never promoted on the strength of the old one.
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-2", "background"]]);
    expect(promote).toHaveBeenCalledWith(A);
    const fresh = await prewarm.begin(A);
    expect(fresh.token).toBeNull();
  });

  it("ends a pending click the moment the user navigates away, even when the application was never open", async () => {
    const { prewarm, navigation, activeKey, refresh, promote, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    prewarm.markReady(A, "a-1"); prewarm.markReady(B, "b-1");
    const captured = navigation.value;
    const intent = await prewarm.begin(A, () => navigation.value === captured);
    intent.confirm("a-1");
    // A platform navigation while the describe is in flight: `activeKey` stays null → null, so only the
    // navigation revision tells. The intent ends in the same tick, before any Host reply could settle it.
    navigation.value++;
    await Promise.resolve();
    expect(intent.active).toBe(false);
    expect(view()).toEqual([[B, "b-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
    intent.settle(true); // the controller's late settlement is inert
    expect(view()).toEqual([[B, "b-1", "background"]]);
    expect(promote).not.toHaveBeenCalled();
    // A navigation that makes the key active is the click's own open: its confirmed surface is adopted.
    const capturedB = navigation.value;
    const second = await prewarm.begin(B, () => navigation.value === capturedB);
    second.confirm("b-1");
    navigation.value++; activeKey.value = B;
    await Promise.resolve();
    expect(view()).toEqual([[B, "b-1", "foreground"]]);
    expect(second.active).toBe(false);
    expect(promote).not.toHaveBeenCalled();
    second.settle(false);
    expect(view()).toEqual([[B, "b-1", "foreground"]]);
  });

  it("a second click on the same key supersedes the pending one without retiring the surface it may have promoted", async () => {
    const { prewarm, navigation, activeKey, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const captured = navigation.value;
    const first = await prewarm.begin(A, () => navigation.value === captured);
    first.confirm("a-1");
    // The second click's own navigation, then its intent (registered before the deferred check runs).
    navigation.value++;
    const second = await prewarm.begin(A, () => navigation.value === navigation.value);
    await Promise.resolve();
    expect(first.active).toBe(false);
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    // The second describe promotes the very same session: the first click's late settlement is inert.
    second.confirm("a-1");
    navigation.value++; activeKey.value = A;
    await Promise.resolve();
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    first.settle(false); second.settle(true); first.settle(false);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("reclaims the first click's token if the second click is abandoned before sending its describe", async () => {
    const { prewarm, navigation, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const originalNavigation = navigation.value;
    const first = await prewarm.begin(A, () => navigation.value === originalNavigation);
    first.confirm("a-1");
    const secondNavigation = ++navigation.value;
    const second = await prewarm.begin(A, () => navigation.value === secondNavigation);
    expect(first.active).toBe(false);
    expect(second.token).toBeNull();
    expect(view()).toEqual([[A, "a-1", "background"]]);
    navigation.value++;
    await Promise.resolve();
    expect(second.active).toBe(false);
    expect(view()).toEqual([]);
    await nextTick();
    expect(refresh).toHaveBeenCalledOnce();
  });

  it("a superseded click never touches the successor: the older settlement is a no-op and the newer click opens the app", async () => {
    const { prewarm, activeKey, promote, rollback, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const first = await prewarm.begin(A);
    const second = await prewarm.begin(A);
    expect(first.active).toBe(false);
    first.confirm("a-1"); // an older click's describe is already discarded: it confirms nothing
    expect(first.token).toBeNull();
    second.confirm("a-1");
    first.settle(false);
    expect(view()).toEqual([[A, "a-1", "background"]]);
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    second.settle(true);
    first.settle(false); second.settle(false);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(rollback).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(promote).not.toHaveBeenCalled();
  });

  it("switching to another application reclaims the first click's token and leaves the second application alone", async () => {
    const { prewarm, activeKey, refresh, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    prewarm.markReady(A, "a-1"); prewarm.markReady(B, "b-1");
    const clickA = await prewarm.begin(A);
    const clickB = await prewarm.begin(B);
    clickA.confirm("a-1"); clickB.confirm("b-1");
    clickA.settle(false); // A's describe was superseded by the click on B
    expect(view()).toEqual([[B, "b-1", "background"]]);
    activeKey.value = B;
    clickB.settle(true);
    expect(view()).toEqual([[B, "b-1", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("never promotes a cold frame in place: the click closes it once before its own catalog read and the fresh token opens normally", async () => {
    const { prewarm, entries, activeKey, refresh, promote, view } = setup([entry(A, "a-1")]);
    expect(prewarm.isCold(A)).toBe(true);
    const opening = prewarm.begin(A);
    expect(view()).toEqual([]);
    const intent = await opening;
    await nextTick();
    expect(refresh).not.toHaveBeenCalled(); // The click's own catalog read follows: no second read.
    // The Host retired the closed session and issued a replacement, which the click's describe promotes directly.
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([[A, "a-2", "background"]]);
    intent.confirm("a-2");
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    expect(promote).not.toHaveBeenCalled();
    intent.settle(true);
    expect(refresh).not.toHaveBeenCalled();
    prewarm.markReady(A, "a-2");
    expect(prewarm.isCold(A)).toBe(false);
  });

  it("a click the user moved on from during the cold close registers nothing, so the newer click keeps the replacement", async () => {
    const { prewarm, entries, activeKey, refresh, promote, view } = setup([entry(A, "a-1")]);
    let current = true;
    const stale = prewarm.begin(A, () => current);
    current = false; // The user navigated again while the cold frame was closing
    const staleIntent = await stale;
    expect(staleIntent.active).toBe(false);
    expect(view()).toEqual([]);
    entries.value = [entry(A, "a-2")];
    prewarm.markReady(A, "a-2");
    const fresh = await prewarm.begin(A);
    fresh.confirm("a-2");
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    expect(promote).not.toHaveBeenCalled();
    staleIntent.confirm("a-2"); staleIntent.settle(false); fresh.settle(true); staleIntent.settle(false);
    expect(fresh.token).toBe("a-2");
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("a click during an explicit promotion settles it on the same token without another describe", async () => {
    const { prewarm, activeKey, promote, view } = setup([entry(A, "a-1")]);
    activeKey.value = A;
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(promote).toHaveBeenCalledTimes(1);
    const intent = await prewarm.begin(A);
    // The click's describe confirmed the application while the explicit promotion is still in flight.
    intent.confirm("a-1");
    intent.settle(true);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(promote).toHaveBeenCalledTimes(1);
  });

  it("a user leaving during an explicit promotion ends the click at once and the refused promotion never rolls back another view", async () => {
    const { prewarm, navigation, activeKey, promote, rollback, refresh, view } = setup([entry(A, "a-1")]);
    let admit!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { admit = resolve; }));
    prewarm.markReady(A, "a-1");
    activeKey.value = A;
    const captured = navigation.value;
    const intent = await prewarm.begin(A, () => navigation.value === captured);
    intent.confirm("a-1");
    navigation.value++; activeKey.value = null; // 返回平台 while the describe is in flight
    await Promise.resolve();
    expect(intent.active).toBe(false);
    expect(view()).toEqual([]); // leaving the foreground retires the promoting token at once
    expect(refresh).toHaveBeenCalledTimes(1); // scheduled by that retirement, before any reply
    admit(false); await settleAfter(); // the explicit promotion's own refusal
    expect(view()).toEqual([]);
    expect(rollback).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("retires a token the catalog shows promoted while no click owns it, hidden authority never lingers", () => {
    const { entries, refresh, view } = setup([entry(A, "a-1")]);
    // A surface promoted by some other path (or a click whose settlement already ran) shows `null` for its token.
    entries.value = [entry(A, "a-1", { phase: null })];
    expect(view()).toEqual([]);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("a pending click protects its key's token between its own catalog read and its describe, and only until it settles", async () => {
    const { prewarm, entries, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    // The click's own read may be looking at the very promotion its describe is about to confirm.
    entries.value = [entry(A, "a-1", { phase: null })];
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    // Discarded without a describe: nothing is promoted on its behalf, and the lingering authority goes.
    intent.settle(false);
    expect(view()).toEqual([]);
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
  });
});
