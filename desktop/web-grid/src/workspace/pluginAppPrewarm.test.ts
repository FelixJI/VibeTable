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
    surfaceToken: token, installationId: null, revision: 1, declared, surfacePhase, prewarm: declared && surfacePhase === "background" };
}
function setup(initial: readonly PluginAppEntry[]) {
  const entries = ref<readonly PluginAppEntry[]>(initial);
  const activeToken = ref<string | null>(null);
  const navigation = ref(0);
  const refresh = vi.fn();
  const prewarm = createPluginAppPrewarm({ entries: computed(() => entries.value), activeToken: computed(() => activeToken.value),
    navigation: () => navigation.value, refresh });
  const view = () => prewarm.slots.value.map(slot => [slot.key, slot.entry.surfaceToken, slot.phase]);
  return { prewarm, entries, activeToken, navigation, refresh, view };
}
const A = "com.acme.a/open";
const B = "com.acme.b/open";

describe("createPluginAppPrewarm", () => {
  it("mounts one hidden slot per declared application the Host granted in background; every other entry stays manual", () => {
    const { view, refresh } = setup([
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
    expect(refresh).not.toHaveBeenCalled();
  });

  it("lets the click adopt exactly the token its describe promoted, confirmed once right before the describe, without a second describe", async () => {
    const { prewarm, activeToken, refresh, view, entries } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    expect(intent.active).toBe(true);
    expect(intent.token).toBeNull();
    intent.confirm("a-1"); // the click's catalog read, right before its describe is posted
    expect(intent.token).toBe("a-1");
    intent.confirm("a-2"); // a confirmation is a single set point
    expect(intent.token).toBe("a-1");
    activeToken.value = "a-1";
    intent.settle(true);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    intent.settle(false);
    await nextTick();
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
    expect(intent.active).toBe(false);
    // The catalog later confirms the promotion (no phase for the token): nothing changes.
    entries.value = [entry(A, "a-1", { phase: null })];
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
  });

  it("does not show a background frame until a click confirms its token and succeeds", async () => {
    const { prewarm, activeToken, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    activeToken.value = "a-1";
    expect(view()).toEqual([[A, "a-1", "background"]]);
    intent.settle(true);
    expect(view()).toEqual([[A, "a-1", "background"]]);
  });

  it("re-pre-warms on a fresh token after the user left and keeps a self-closed token retired", async () => {
    const { prewarm, entries, activeToken, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const intent = await prewarm.begin(A);
    intent.confirm("a-1");
    activeToken.value = "a-1"; intent.settle(true);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(prewarm.isActive(A, "a-1")).toBe(true);
    expect(prewarm.isActive(A, "a-0")).toBe(false);
    activeToken.value = null; await nextTick();
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

  it("keeps the old foreground authorization from opening a replacement token", async () => {
    const { prewarm, entries, activeToken, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const first = await prewarm.begin(A);
    first.confirm("a-1");
    activeToken.value = "a-1";
    first.settle(true);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([[A, "a-2", "background"]]);
    activeToken.value = null;
    expect(refresh).not.toHaveBeenCalled();

    prewarm.markReady(A, "a-2");
    const fresh = await prewarm.begin(A);
    fresh.confirm("a-2");
    activeToken.value = "a-2";
    fresh.settle(true);
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    entries.value = [];
    expect(view()).toEqual([]);
  });

  it("reclaims a discarded click at once: the token its describe may have promoted is retired and closed without a catalog read", async () => {
    const { prewarm, entries, refresh, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
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
    const { prewarm, entries, activeToken, refresh, view } = setup([entry(A, "a-1")]);
    const intent = await prewarm.begin(A);
    intent.confirm("a-1");
    // The frame closed itself after the describe was posted: the Host issued a replacement.
    entries.value = [entry(A, "a-2")];
    expect(view()).toEqual([[A, "a-2", "background"]]);
    intent.settle(false); // the original describe comes back refused
    expect(view()).toEqual([[A, "a-2", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    expect(activeToken.value).toBeNull();
  });

  it("ends a pending click the moment the user navigates away, even when the application was never open", async () => {
    const { prewarm, navigation, activeToken, refresh, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    prewarm.markReady(A, "a-1"); prewarm.markReady(B, "b-1");
    const captured = navigation.value;
    const intent = await prewarm.begin(A, () => navigation.value === captured);
    intent.confirm("a-1");
    // A platform navigation while the describe is in flight: the active token stays null → null, so only the
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
    // The controller commits a still-current click after the Host promotes it.
    const capturedB = navigation.value;
    const second = await prewarm.begin(B, () => navigation.value === capturedB);
    second.confirm("b-1");
    navigation.value++; activeToken.value = "b-1";
    second.settle(true);
    await Promise.resolve();
    expect(view()).toEqual([[B, "b-1", "foreground"]]);
    expect(second.active).toBe(false);
    second.settle(false);
    expect(view()).toEqual([[B, "b-1", "foreground"]]);
  });

  it("a second click on the same key supersedes the pending one without retiring the surface it may have promoted", async () => {
    const { prewarm, navigation, activeToken, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const captured = navigation.value;
    const first = await prewarm.begin(A, () => navigation.value === captured);
    first.confirm("a-1");
    // The second click's own navigation, then its intent (registered before the deferred check runs).
    navigation.value++;
    const secondNavigation = navigation.value;
    const second = await prewarm.begin(A, () => navigation.value === secondNavigation);
    await Promise.resolve();
    expect(first.active).toBe(false);
    expect(view()).toEqual([[A, "a-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    // The second describe promotes the very same session: the first click's late settlement is inert.
    second.confirm("a-1");
    navigation.value++; activeToken.value = "a-1";
    second.settle(true);
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
    const { prewarm, activeToken, refresh, view } = setup([entry(A, "a-1")]);
    prewarm.markReady(A, "a-1");
    const first = await prewarm.begin(A);
    const second = await prewarm.begin(A);
    expect(first.active).toBe(false);
    first.confirm("a-1"); // an older click's describe is already discarded: it confirms nothing
    expect(first.token).toBeNull();
    second.confirm("a-1");
    first.settle(false);
    expect(view()).toEqual([[A, "a-1", "background"]]);
    activeToken.value = "a-1";
    second.settle(true);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    first.settle(false); second.settle(false);
    expect(view()).toEqual([[A, "a-1", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("switching to another application reclaims the first click's token and leaves the second application alone", async () => {
    const { prewarm, activeToken, refresh, view } = setup([entry(A, "a-1"), entry(B, "b-1")]);
    prewarm.markReady(A, "a-1"); prewarm.markReady(B, "b-1");
    const clickA = await prewarm.begin(A);
    const clickB = await prewarm.begin(B);
    clickA.confirm("a-1"); clickB.confirm("b-1");
    clickA.settle(false); // A's describe was superseded by the click on B
    expect(view()).toEqual([[B, "b-1", "background"]]);
    activeToken.value = "b-1";
    clickB.settle(true);
    expect(view()).toEqual([[B, "b-1", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("never promotes a cold frame in place: the click closes it once before its own catalog read and the fresh token opens normally", async () => {
    const { prewarm, entries, activeToken, refresh, view } = setup([entry(A, "a-1")]);
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
    activeToken.value = "a-2";
    intent.settle(true);
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
    prewarm.markReady(A, "a-2");
    expect(prewarm.isCold(A)).toBe(false);
  });

  it("a click the user moved on from during the cold close registers nothing, so the newer click keeps the replacement", async () => {
    const { prewarm, entries, activeToken, refresh, view } = setup([entry(A, "a-1")]);
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
    activeToken.value = "a-2";
    fresh.settle(true);
    staleIntent.confirm("a-2"); staleIntent.settle(false);
    expect(fresh.token).toBe("a-2");
    expect(view()).toEqual([[A, "a-2", "foreground"]]);
    expect(refresh).not.toHaveBeenCalled();
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
