import { useWorkspaceStore } from "@/stores/workspaceStore";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises } from "@vue/test-utils";
import { useWorkCalendarStore, WORK_CALENDAR_STORAGE_KEY } from "./workCalendarStore";
import { useWorkspaceSessionStore } from "./workspaceSessionStore";
import { formatDateKey, type WorkCalendarOverride } from "@/calendar/workCalendar";

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
    session.sessionState = "openedWritable";
    session.writable = true;
  });

  it("does not reload a late database projection during workspace retirement", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.sessionState = "openedWritable";
    const store = useWorkCalendarStore();
    await flushPromises();
    request.mockClear();
    expect(session.beginSwitch("workspace-b")).toBe(true);
    useWorkspaceStore().phase = "opening";
    await flushPromises();
    useWorkspaceStore().phase = "opened";
    await flushPromises();
    await store.load();
    expect(request).not.toHaveBeenCalled();
    session.sessionState = "openedWritable";
    session.sessionPhase = "idle";
    await flushPromises();
    expect(request).toHaveBeenCalledExactlyOnceWith("settings.readWorkCalendar", {});
    expect(store.status).toBe("ready");
  });

  it.each(["failed", "closed"] as const)("does not read the calendar from a %s session", async (state) => {
    const store = useWorkCalendarStore();
    await flushPromises();
    request.mockClear();
    useWorkspaceSessionStore().sessionState = state;
    await flushPromises();
    await store.load();
    expect(request).not.toHaveBeenCalled();
    expect(store.status).toBe("unavailable");
  });

  it("persists confirmed overrides and restores the default rule through the authority", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "调休上班");
    expect(store.day("2026-07-18").kind).toBe("weekend");
    request.mockResolvedValueOnce({ status: "applied", changeSetId: "change-1", emittedEvents: [], overrides: store.draft, revision: "revision-a" });
    await store.save();
    expect(store.day("2026-07-18")).toMatchObject({ kind: "workday", name: "调休上班" });
    expect(localStorage.getItem(WORK_CALENDAR_STORAGE_KEY)).toBeNull();
    expect(request).toHaveBeenLastCalledWith("settings.commitWorkCalendar", expect.objectContaining({ expectedRevision: "", overrides: [{ date: "2026-07-18", kind: "workday", name: "调休上班" }] }));
    store.clearOverride("2026-07-18");
    request.mockResolvedValueOnce({ status: "replayed", changeSetId: "change-2", emittedEvents: [], overrides: [], revision: "revision-clear" });
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

  it("reports a protocol error for a malformed error envelope without inventing calendar state", async () => {
    request.mockResolvedValueOnce({ error: { code: "authority.failed" } });
    const store = useWorkCalendarStore();
    await flushPromises();
    expect(store.status).toBe("error");
    expect(store.overrides).toEqual([]);
    expect(store.error).toContain("settings.readWorkCalendar");
    // A save whose receipt cannot be verified must neither claim success nor auto-resubmit.
    request.mockResolvedValueOnce({ overrides: [], revision: "current" });
    await store.load();
    expect(store.status).toBe("ready");
    store.setOverride("2026-07-18", "workday", "未知回执");
    request.mockClear();
    request.mockResolvedValueOnce({ error: "not-an-envelope" });
    await store.save();
    expect(store.status).toBe("error");
    expect(store.error).toContain("settings.commitWorkCalendar");
    expect(store.draft[0]?.name).toBe("未知回执");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("keeps the draft and error state when a commit receipt is truncated", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "待确认");
    const count = request.mock.calls.length;
    request.mockResolvedValueOnce({ overrides: store.draft, revision: "revision-a" });
    await store.save();
    expect(store.status).toBe("error");
    expect(store.available).toBe(false);
    expect(store.draft[0]?.name).toBe("待确认");
    expect(store.overrides).toEqual([]);
    expect(request).toHaveBeenCalledTimes(count + 1);
  });

  it("rejects a decorated commit receipt whose revision is empty", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "空回执");
    const count = request.mock.calls.length;
    // Reads may legally return "" before the first commit; a commit receipt
    // echoes the persisted item's revision, which is never empty.
    request.mockResolvedValueOnce({ status: "applied", changeSetId: "change-1", emittedEvents: [], overrides: store.draft, revision: "" });
    await store.save();
    expect(store.status).toBe("error");
    expect(store.draft[0]?.name).toBe("空回执");
    expect(request).toHaveBeenCalledTimes(count + 1);
    expect(store.revision).toBe("");
  });

  it("accepts a fully decorated commit receipt and keeps reads on the basic result", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "确认");
    request.mockResolvedValueOnce({
      status: "applied", changeSetId: "change-1", emittedEvents: ["event-1"],
      overrides: store.draft, revision: "revision-b",
    });
    await store.save();
    expect(store.status).toBe("ready");
    expect(store.revision).toBe("revision-b");
    request.mockClear();
    request.mockResolvedValueOnce({ overrides: store.overrides, revision: "revision-b" });
    await store.load();
    expect(store.status).toBe("ready");
    expect(request).toHaveBeenCalledExactlyOnceWith("settings.readWorkCalendar", {});
  });

  it.each(["read", "commit"])("enforces the producer override limit on %s results", async (method) => {
    const overrides: WorkCalendarOverride[] = Array.from({ length: 3661 }, (_, index) => ({
      date: formatDateKey(new Date(2026, 0, index + 1)), kind: "holiday", name: "",
    }));
    const store = useWorkCalendarStore();
    await flushPromises();
    const accepted = overrides.slice(0, 3660);
    request.mockResolvedValueOnce({ overrides: accepted, revision: "current" });
    await store.load();
    expect(store.status).toBe("ready");
    expect(store.overrideCount).toBe(3660);
    store.setOverride(accepted[0]!.date, "workday", "unsaved");
    const draft = store.draft.map(item => ({ ...item }));
    request.mockClear();
    request.mockResolvedValueOnce({
      status: "applied", changeSetId: "change-1", emittedEvents: [],
      overrides, revision: "invalid",
    });
    if (method === "read") await store.load();
    else await store.save();
    expect(store.status).toBe("error");
    expect(store.error).toContain(method === "read" ? "settings.readWorkCalendar" : "settings.commitWorkCalendar");
    expect(store.overrides).toEqual(accepted);
    expect(store.draft).toEqual(draft);
    expect(store.revision).toBe("current");
    await store.save();
    expect(request).toHaveBeenCalledTimes(1);
  });

  it("rejects a fully valid receipt whose overrides contradict the submission", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "我提交的");
    const count = request.mock.calls.length;
    // Structurally legal, fully decorated — but the authority echoes a
    // different override set than the one submitted this time.
    request.mockResolvedValueOnce({
      status: "applied", changeSetId: "change-1", emittedEvents: [],
      overrides: [{ date: "2026-07-19", kind: "holiday", name: "别的会话" }], revision: "revision-x",
    });
    await store.save();
    expect(store.status).toBe("error");
    expect(store.draft[0]?.name).toBe("我提交的");
    expect(store.overrides).toEqual([]);
    expect(store.revision).toBe("");
    expect(request).toHaveBeenCalledTimes(count + 1);
  });

  it("binds the commit receipt to the dispatch-time snapshot during a real in-flight mutation", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "提交内容");
    const submitted: Array<unknown> = [];
    request.mockImplementationOnce(async (_type: string, payload: Record<string, unknown>) => {
      submitted.push(payload.overrides);
      return new Promise(resolve => { setTimeout(() => resolve({
        status: "applied", changeSetId: "change-1", emittedEvents: [],
        overrides: payload.overrides, revision: "revision-s",
      }), 0); });
    });
    const saving = store.save();
    // Mutate the draft while the request is genuinely in flight.
    store.draft = [{ date: "2026-07-19", kind: "holiday", name: "飞行中修改" }];
    await saving;
    // The receipt echoes the submitted snapshot, not the mutated draft.
    expect(store.status).toBe("ready");
    expect(store.overrides).toEqual([{ date: "2026-07-18", kind: "workday", name: "提交内容" }]);
    expect(submitted[0]).toEqual([{ date: "2026-07-18", kind: "workday", name: "提交内容" }]);

    // A fully legal receipt that matches neither the snapshot nor the draft
    // stays a protocol failure and keeps the (mutated) draft untouched.
    let finish!: (value: unknown) => void;
    request.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
    store.draft = [{ date: "2026-07-18", kind: "workday", name: "第二轮" }];
    const failing = store.save();
    store.draft = [...store.draft, { date: "2026-07-20", kind: "holiday", name: "等待" }];
    finish({
      status: "applied", changeSetId: "change-2", emittedEvents: [],
      overrides: [{ date: "2026-07-21", kind: "workday", name: "别的会话" }], revision: "revision-o",
    });
    await failing;
    expect(store.status).toBe("error");
    expect(store.overrides).toEqual([{ date: "2026-07-18", kind: "workday", name: "提交内容" }]);
    expect(store.revision).toBe("revision-s");
    expect(store.draft[1]?.name).toBe("等待");
  });

  it("accepts the producer-normalized echo of the submitted overrides", async () => {
    const store = useWorkCalendarStore();
    await flushPromises();
    store.setOverride("2026-07-18", "workday", "　规范化名　");
    request.mockResolvedValueOnce({
      status: "applied", changeSetId: "change-1", emittedEvents: [],
      overrides: [{ date: "2026-07-18", kind: "workday", name: "规范化名" }], revision: "revision-n",
    });
    await store.save();
    expect(store.status).toBe("ready");
    expect(store.revision).toBe("revision-n");
    expect(store.day("2026-07-18")).toMatchObject({ name: "规范化名" });
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
