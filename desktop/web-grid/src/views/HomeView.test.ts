import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import type { HostBridge } from "@/bridge/hostBridge";
import { nextTick } from "vue";
import { beforeEach, describe, expect, it } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { vi } from "vitest";
import HomeView from "./HomeView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { formatDateKey } from "@/calendar/workCalendar";
import { loadDailyQuote } from "@/services/dailyQuoteService";

vi.mock("@/services/dailyQuoteService", () => ({
  loadDailyQuote: vi.fn(async ({ fallback }) => fallback),
}));

describe("HomeView", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
    vi.mocked(loadDailyQuote).mockClear();
  });

  it("offers a useful three-step empty state with only real actions clickable", async () => {
    const wrapper = mount(HomeView);
    const steps = wrapper.findAll(".guide-steps > *");
    expect(steps).toHaveLength(3);
    expect(steps[1].element.tagName).toBe("DIV");
    await steps[0].trigger("click");
    await steps[2].trigger("click");
    expect(wrapper.emitted("newTable")).toHaveLength(1);
    expect(wrapper.emitted("openAdmin")).toHaveLength(1);
  });

  it("shows display names and emits only the physical key", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened(
      [{ collection: "vt_t_01", metadata: {} }],
      { vt_t_01: "客户清单" },
    );
    const wrapper = mount(HomeView);
    const row = wrapper.get('[data-testid="home-recent-table"]');
    expect(row.text()).toContain("客户清单");
    await row.trigger("click");
    expect(wrapper.emitted("openTable")?.[0]).toEqual(["vt_t_01"]);
  });

  it("respects the offline daily quote preference", async () => {
    const ui = useUiStore();
    const wrapper = mount(HomeView);
    expect(wrapper.find(".quote-card").exists()).toBe(true);
    ui.setShowDailyQuote(false);
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".quote-card").exists()).toBe(false);
  });

  it("respects the mini calendar preference", async () => {
    const ui = useUiStore();
    const wrapper = mount(HomeView);
    expect(wrapper.find(".calendar-card").exists()).toBe(true);
    ui.setShowMiniCalendar(false);
    await nextTick();
    expect(wrapper.find(".calendar-card").exists()).toBe(false);
  });

  it("falls back to viewport width when matchMedia is unavailable", async () => {
    expect(window.matchMedia).toBeUndefined();
    const wrapper = mount(HomeView);
    await nextTick();
    expect(wrapper.find(".home-main").exists()).toBe(true);
    wrapper.unmount();
  });

  it("refreshes the quote whenever Home is entered and on explicit request", async () => {
    const ui = useUiStore();
    const wrapper = mount(HomeView);
    await flushPromises();
    expect(loadDailyQuote).toHaveBeenCalledTimes(1);

    ui.navigate("settings");
    await nextTick();
    ui.navigate("home");
    await flushPromises();
    expect(loadDailyQuote).toHaveBeenCalledTimes(2);
    expect(loadDailyQuote).toHaveBeenLastCalledWith(expect.objectContaining({
      source: ui.dailyQuoteSource,
      style: ui.dailyQuoteStyle,
    }));

    await wrapper.get('[data-testid="quote-refresh"]').trigger("click");
    await flushPromises();
    expect(loadDailyQuote).toHaveBeenCalledTimes(3);
  });

  it("keeps the selected online source visible when offline fallback is used", async () => {
    const ui = useUiStore();
    ui.setDailyQuoteSource("hitokoto");
    const wrapper = mount(HomeView);
    await flushPromises();

    const footer = wrapper.get(".quote-card footer").text();
    expect(footer).toContain("一言");
    expect(footer).toContain("离线语录");
    expect(footer).not.toBe("内置精选");
  });

  it("shows the shared holiday and adjusted-workday markers", async () => {
    setHostBridgeForTesting({ request: vi.fn(async (type, payload) => type === "settings.readWorkCalendar" ? { overrides: [], revision: "" } : { ...payload, status: "applied", changeSetId: "change-1", emittedEvents: [], revision: "saved" }) } as unknown as HostBridge);
    useWorkspaceStore().phase = "opened";
    useWorkspaceSessionStore().activeWorkspaceId = "calendar-workspace";
    useWorkspaceSessionStore().sessionState = "openedWritable";
    useWorkspaceSessionStore().writable = true;
    const calendar = useWorkCalendarStore();
    await flushPromises();
    const today = formatDateKey(new Date());
    calendar.setOverride(today, "workday", "调休上班");
    await calendar.save();
    const wrapper = mount(HomeView);
    const cell = wrapper.get(`[data-date="${today}"]`);
    expect(cell.text()).toContain("班");
    expect(cell.attributes("title")).toContain("调休上班");
  });

  it("renders the mini calendar grid immediately instead of popping it in later", async () => {
    // 共享日历未加载完成时（无桥接、状态 unavailable），迷你日历也应立即
    // 呈现本地周末规则网格，避免首页从上到下逐块出现。
    const calendar = useWorkCalendarStore();
    expect(calendar.available).toBe(false);
    const wrapper = mount(HomeView);
    await nextTick();
    expect(calendar.available).toBe(false);
    expect(wrapper.find(".calendar-card [data-date]").exists()).toBe(true);
    // 加载中不显示“不可用”占位；只有失败才提示。
    expect(wrapper.find(".calendar-card").text())
      .not.toContain("共享工作日历暂不可用");
  });

  it("deep-links the recovery timeline into the settings versions section", async () => {
    const ui = useUiStore();
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "Home",
      selectedRoot: "D:\\Home",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: null,
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 1,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    const wrapper = mount(HomeView);
    const button = wrapper.get('[data-testid="home-open-timeline"]');
    expect(button.text()).toContain("查看恢复时间线");
    await button.trigger("click");
    expect(ui.activeView).toBe("settings");
    expect(ui.requestedSettingsSection).toBe("versions");
  });

  it("describes search hits with the local last-modified time instead of the physical id", async () => {
    const workspace = useWorkspaceStore();
    const ui = useUiStore();
    workspace.setOpened(
      [{ collection: "vt_t_01abc", metadata: {} }],
      { vt_t_01abc: "客户清单" },
    );
    ui.touchTable("vt_t_01abc");
    const wrapper = mount(HomeView);
    await wrapper.get('[data-testid="home-search"] input').setValue("客户");
    await nextTick();
    const hit = wrapper.get(".search-results button");
    expect(hit.text()).toContain("客户清单");
    expect(hit.text()).toContain("本机修改");
    expect(hit.text()).not.toContain("vt_t_01abc");
  });
});
