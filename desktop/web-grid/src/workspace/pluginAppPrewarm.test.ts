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
  const refresh = vi.fn();
  const promote = vi.fn<(key: string) => Promise<boolean>>(() => new Promise<boolean>(() => undefined));
  const rollback = vi.fn();
  const prewarm = createPluginAppPrewarm({ entries: computed(() => entries.value), activeKey: computed(() => activeKey.value), refresh, promote, rollback });
  const view = () => prewarm.slots.value.map(slot => [slot.key, slot.entry.surfaceToken, slot.phase]);
  return { prewarm, entries, activeKey, refresh, promote, rollback, view };
}

describe("createPluginAppPrewarm", () => {
  it("mounts one hidden slot per declared application the Host granted in background; every other entry stays manual", () => {
    const { view, promote, refresh } = setup([
      entry("com.acme.a/open", "a-1"), entry("com.acme.b/open", "b-1"), entry("com.acme.c/open", "c-1"), entry("com.acme.d/open", "d-1"),
      // Declared, but beyond the background cap: the Host opened it foreground (projected as null).
      entry("com.acme.e/open", "e-1", { phase: null }), entry("com.acme.f/open", "f-1", { phase: "foreground" }),
      // Declared on a Host that projects no phase at all.
      entry("com.acme.old/open", "old-1", { phase: null }),
      // Undeclared, even if a Host were to project background.
      entry("com.acme.legacy/open", "legacy-1", { declared: false }),
    ]);
    expect(view()).toEqual([
      ["com.acme.a/open", "a-1", "background"], ["com.acme.b/open", "b-1", "background"],
      ["com.acme.c/open", "c-1", "background"], ["com.acme.d/open", "d-1", "background"],
    ]);
    expect(promote).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("lets the click path adopt the surface the describe call already promoted, on the same token", async () => {
    const { prewarm, activeKey, promote, refresh, view } = setup([entry("com.acme.a/open", "a-1")]);
    const opened = prewarm.expectOpen("com.acme.a/open");
    activeKey.value = "com.acme.a/open";
    expect(view()).toEqual([["com.acme.a/open", "a-1", "foreground"]]);
    expect(promote).not.toHaveBeenCalled();
    opened(); // The click path settled on the visible frame: nothing to re-read.
    await nextTick();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("keeps an unhandshaked activation hidden until the Host promotes it, and rolls back when it refuses", async () => {
    const { activeKey, promote, rollback, refresh, view } = setup([entry("com.acme.a/open", "a-1"), entry("com.acme.b/open", "b-1")]);
    let refuse!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { refuse = resolve; }));
    activeKey.value = "com.acme.a/open";
    expect(promote).toHaveBeenCalledWith("com.acme.a/open");
    expect(view()).toEqual([["com.acme.a/open", "a-1", "background"], ["com.acme.b/open", "b-1", "background"]]);
    refuse(true); await Promise.resolve(); await Promise.resolve();
    expect(view()).toEqual([["com.acme.a/open", "a-1", "foreground"], ["com.acme.b/open", "b-1", "background"]]);
    expect(rollback).not.toHaveBeenCalled();
    // Leaving the foreground retires the host-side foreground session and refreshes the catalog.
    activeKey.value = null;
    expect(view()).toEqual([["com.acme.b/open", "b-1", "background"]]);
    expect(refresh).not.toHaveBeenCalled();
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
    // The refused case: a dead surface is discarded with a notice and a fresh catalog read.
    let refuseB!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { refuseB = resolve; }));
    activeKey.value = "com.acme.b/open";
    expect(promote).toHaveBeenCalledWith("com.acme.b/open");
    expect(view()).toEqual([["com.acme.b/open", "b-1", "background"]]);
    refuseB(false); await Promise.resolve(); await Promise.resolve();
    expect(rollback).toHaveBeenCalledWith("com.acme.b/open");
    expect(view()).toEqual([]);
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("re-pre-warms on a fresh token after the user left and keeps a self-closed token retired", async () => {
    const { prewarm, entries, activeKey, refresh, promote, view } = setup([entry("com.acme.a/open", "a-1")]);
    const opened = prewarm.expectOpen("com.acme.a/open");
    activeKey.value = "com.acme.a/open"; opened();
    expect(view()).toEqual([["com.acme.a/open", "a-1", "foreground"]]);
    activeKey.value = null; await nextTick();
    expect(view()).toEqual([]);
    expect(refresh).toHaveBeenCalledTimes(1);
    entries.value = [entry("com.acme.a/open", "a-2")];
    expect(view()).toEqual([["com.acme.a/open", "a-2", "background"]]);
    // A background plugin that closes itself must not trigger a remount loop.
    prewarm.selfClose("com.acme.a/open", "a-2");
    expect(view()).toEqual([]);
    expect(refresh).toHaveBeenCalledTimes(1);
    entries.value = [entry("com.acme.a/open", "a-2")];
    expect(view()).toEqual([]);
    expect(promote).not.toHaveBeenCalled();
    // Only the Host's replacement token brings the surface back; a stale close from the old frame cannot retire it.
    entries.value = [entry("com.acme.a/open", "a-3")];
    expect(view()).toEqual([["com.acme.a/open", "a-3", "background"]]);
    prewarm.selfClose("com.acme.a/open", "a-2");
    expect(view()).toEqual([["com.acme.a/open", "a-3", "background"]]);
  });

  it("promotes a replaced token of the active application and drops retired catalog entries", async () => {
    const { prewarm, entries, activeKey, promote, refresh, view } = setup([entry("com.acme.a/open", "a-1")]);
    const opened = prewarm.expectOpen("com.acme.a/open");
    activeKey.value = "com.acme.a/open"; opened();
    let admit!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { admit = resolve; }));
    entries.value = [entry("com.acme.a/open", "a-2")];
    expect(view()).toEqual([["com.acme.a/open", "a-2", "background"]]);
    expect(promote).toHaveBeenCalledWith("com.acme.a/open");
    admit(true); await Promise.resolve(); await Promise.resolve();
    expect(view()).toEqual([["com.acme.a/open", "a-2", "foreground"]]);
    // Disable / uninstall: the catalog drops the entry and the shell leaves the app; a later
    // reinstall starts cold without re-promoting the stale activation.
    entries.value = [];
    expect(view()).toEqual([]);
    activeKey.value = null;
    entries.value = [entry("com.acme.a/open", "a-3")];
    expect(view()).toEqual([["com.acme.a/open", "a-3", "background"]]);
    expect(promote).toHaveBeenCalledTimes(1);
    expect(refresh).toHaveBeenCalledTimes(0);
  });

  it("ignores a promotion that settles after the user already left", async () => {
    const { activeKey, promote, rollback, view } = setup([entry("com.acme.a/open", "a-1")]);
    let admit!: (ok: boolean) => void;
    promote.mockImplementationOnce(() => new Promise<boolean>(resolve => { admit = resolve; }));
    activeKey.value = "com.acme.a/open";
    activeKey.value = null;
    expect(view()).toEqual([]);
    admit(true); await Promise.resolve(); await Promise.resolve();
    expect(view()).toEqual([]);
    expect(rollback).not.toHaveBeenCalled();
  });

  it("reclaims a hidden session a discarded click promoted: the click re-reads the authority and the ungranted token is retired", async () => {
    const { prewarm, entries, refresh, promote, view } = setup([entry("com.acme.a/open", "a-1")]);
    // The user navigated away while the describe was in flight: the app never opened, but the Host
    // session may already be foreground. The finalizer re-reads the catalog instead of trusting it.
    prewarm.expectOpen("com.acme.a/open")();
    expect(view()).toEqual([["com.acme.a/open", "a-1", "background"]]);
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(1);
    entries.value = [entry("com.acme.a/open", "a-1", { phase: null })];
    expect(view()).toEqual([]);
    await nextTick();
    expect(refresh).toHaveBeenCalledTimes(2);
    // The frame unmounted (its close retires the Host session) and the replacement token is pre-warmed.
    entries.value = [entry("com.acme.a/open", "a-2")];
    expect(view()).toEqual([["com.acme.a/open", "a-2", "background"]]);
    expect(promote).not.toHaveBeenCalled();
  });

  it("never promotes a cold frame in place: the click closes it once and the fresh token opens through the normal path", async () => {
    const { prewarm, entries, activeKey, refresh, promote, view } = setup([entry("com.acme.a/open", "a-1")]);
    expect(prewarm.isCold("com.acme.a/open")).toBe(true);
    prewarm.markReady("com.acme.a/open");
    expect(prewarm.isCold("com.acme.a/open")).toBe(false);
    expect(view()).toEqual([["com.acme.a/open", "a-1", "background"]]);
    prewarm.drop("com.acme.a/open");
    expect(view()).toEqual([]);
    await nextTick();
    expect(refresh).not.toHaveBeenCalled(); // The click's own catalog read follows: no second read.
    const opened = prewarm.expectOpen("com.acme.a/open");
    activeKey.value = "com.acme.a/open";
    expect(view()).toEqual([]);
    // The Host retired the closed session and issued a replacement, which the handshake promotes directly.
    entries.value = [entry("com.acme.a/open", "a-2")];
    expect(view()).toEqual([["com.acme.a/open", "a-2", "foreground"]]);
    expect(promote).not.toHaveBeenCalled();
    opened();
    expect(refresh).not.toHaveBeenCalled();
  });
});
