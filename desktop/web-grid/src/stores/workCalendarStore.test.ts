import { useWorkspaceStore } from "@/stores/workspaceStore";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises } from "@vue/test-utils";
import { useWorkCalendarStore, WORK_CALENDAR_STORAGE_KEY } from "./workCalendarStore";
import { useWorkspaceSessionStore } from "./workspaceSessionStore";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/services/bridgeContext", () => ({ useHostBridge: () => ({ request }) }));

describe("workCalendarStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
    useWorkspaceStore().phase = "opened";
    request.mockReset();
    request.mockResolvedValue({ overrides: [], revision: "" });
    const session = useWorkspaceSessionStore();
    session.activeWorkspaceId = "workspace-a";
    session.writable = true;
  });

  it("persists confirmed overrides and restores the default rule through the authority", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "调休上班");
    expect(store.day("2026-07-18").kind).toBe("weekend");
    request.mockResolvedValueOnce({ overrides: store.draft, revision: "revision-a" });
    await store.save();
    expect(store.day("2026-07-18")).toMatchObject({ kind: "workday", name: "调休上班" });
    expect(localStorage.getItem(WORK_CALENDAR_STORAGE_KEY)).toBeNull();
    expect(request).toHaveBeenLastCalledWith("settings.commitWorkCalendar", expect.objectContaining({ expectedRevision: "", overrides: [{ date: "2026-07-18", kind: "workday", name: "调休上班" }] }));
    store.clearOverride("2026-07-18");
    request.mockResolvedValueOnce({ overrides: [], revision: "revision-clear" });
    await store.save();
    expect(store.day("2026-07-18").kind).toBe("weekend");
    expect(store.revision).toBe("revision-clear");
  });

  it("never imports unscoped legacy storage or applies a retired workspace response", async () => {
    localStorage.setItem(WORK_CALENDAR_STORAGE_KEY, JSON.stringify([{ date: "2026-07-18", kind: "workday", name: "旧全局" }]));
    let finish!: (value: unknown) => void;
    request.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
    const store = useWorkCalendarStore();
    const session = useWorkspaceSessionStore();
    session.activeWorkspaceId = "workspace-b";
    await flushPromises();
    expect(store.overrides).toEqual([]);
    finish({ overrides: [{ date: "2026-07-18", kind: "workday", name: "旧工作区" }], revision: "old" });
    await flushPromises();
    expect(store.overrides).toEqual([]);
    expect(store.revision).toBe("");
  });

  it("retains the draft on a CAS conflict and requires explicit reload", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "我的编辑");
    request.mockResolvedValueOnce({ error: { code: "settings.calendar.revision_conflict", path: "expectedRevision", message: "reload", retryable: false } });
    await store.save();
    expect(store.status).toBe("conflict");
    expect(store.draft[0]?.name).toBe("我的编辑");
    expect(store.overrides).toEqual([]);
    const count = request.mock.calls.length;
    await store.save();
    expect(request).toHaveBeenCalledTimes(count);
    request.mockResolvedValueOnce({ overrides: [], revision: "current" });
    await store.load();
    expect(store.revision).toBe("current");
    expect(store.status).toBe("ready");
  });

  it("does not silently turn corrupt authority data into defaults", async () => {
    request.mockResolvedValueOnce({ overrides: [{ date: "bad", kind: "holiday", name: "x" }], revision: "bad" });
    const store = useWorkCalendarStore();
    await flushPromises();
    expect(store.status).toBe("error");
    expect(store.available).toBe(false);
    store.setOverride("2026-07-18", "workday", "blocked");
    await store.save();
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("waits for database.opened and reads only the completed workspace epoch", async () => {
    useWorkspaceStore().phase = "opening";
    const session = useWorkspaceSessionStore();
    const store = useWorkCalendarStore();
    await flushPromises();
    expect(request).not.toHaveBeenCalled();
    const scopes: Array<[string | null, number]> = [];
    request.mockImplementation(async () => {
      scopes.push([session.activeWorkspaceId, session.sessionEpoch]);
      return { overrides: [], revision: "" };
    });
    session.activeWorkspaceId = "workspace-b";
    session.sessionEpoch = 9;
    useWorkspaceStore().phase = "opened";
    await flushPromises();
    expect(scopes).toEqual([["workspace-b", 9]]);
    expect(store.status).toBe("ready");
  });

});
