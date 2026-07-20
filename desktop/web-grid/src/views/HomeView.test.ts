import { nextTick } from "vue";
import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { vi } from "vitest";
import HomeView from "./HomeView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { formatDateKey } from "@/calendar/workCalendar";

vi.mock("@/services/dailyQuoteService", () => ({
  loadDailyQuote: vi.fn(async ({ fallback }) => fallback),
}));

describe("HomeView", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
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
      [{ collection: "vt_t_01", displayName: "客户清单", metadata: {} }],
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

  it("shows the shared holiday and adjusted-workday markers", async () => {
    const calendar = useWorkCalendarStore();
    const today = formatDateKey(new Date());
    calendar.setOverride(today, "workday", "调休上班");
    const wrapper = mount(HomeView);
    const cell = wrapper.get(`[data-date="${today}"]`);
    expect(cell.text()).toContain("班");
    expect(cell.attributes("title")).toContain("调休上班");
  });
});
