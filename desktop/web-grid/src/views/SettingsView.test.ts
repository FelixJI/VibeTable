import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import SettingsView from "./SettingsView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { formatDateKey } from "@/calendar/workCalendar";
import { useUiStore } from "@/stores/uiStore";
import { NSelect } from "naive-ui";
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";

describe("SettingsView", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("organizes discoverable settings into six sections", () => {
    const wrapper = mount(SettingsView);
    expect(wrapper.findAll(".settings-nav button")).toHaveLength(6);
    expect(wrapper.text()).toContain("界面语言");
    expect(wrapper.text()).toContain("启动页面");
  });

  it("manages manual holidays and adjusted workdays from the shared calendar", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    const today = formatDateKey(new Date());
    await wrapper.get(`[data-date="${today}"]`).trigger("click");
    const holiday = wrapper.get<HTMLInputElement>('.calendar-rule-options input[value="holiday"]');
    await holiday.setValue(true);
    expect(useWorkCalendarStore().day(today).kind).toBe("holiday");
    expect(wrapper.get(`[data-date="${today}"]`).text()).toContain("休");
  });

  it("shows searchable mapping tools while keeping physical keys read-only", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "vt_t_01" }], { vt_t_01: "客户清单" });
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-mapping"]').trigger("click");
    const store = useIdentifierMappingStore();
    store.succeed({ mappings: [{
      id: "m1", entityKind: "collection", physicalName: "vt_t_01",
      displayName: "客户清单", locale: "zh-CN", aliases: ["客户"],
      origin: "vibetable", status: "active",
    }] });
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("客户清单");
    expect(wrapper.text()).toContain("物理键只读");
    expect(wrapper.find('input[placeholder*="物理键"]').exists()).toBe(true);
    expect(wrapper.emitted("loadMappings")).toHaveLength(1);
  });

  it("routes advanced schema, shortcuts, and reconnect through real emits", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-mapping"]').trigger("click");
    await wrapper.find(".mapping-footer .n-button").trigger("click");
    expect(wrapper.emitted("openAdmin")).toHaveLength(1);

    await wrapper.get('[data-testid="settings-nav-interaction"]').trigger("click");
    await wrapper.get(".setting-action").trigger("click");
    expect(wrapper.emitted("openHelp")).toHaveLength(1);

    await wrapper.get('[data-testid="settings-nav-source"]').trigger("click");
    await wrapper.get('[data-testid="connection-retry"]').trigger("click");
    expect(wrapper.emitted("reconnect")).toHaveLength(1);
  });

  it("offers persistent source and compatible style controls for daily quotes", async () => {
    const wrapper = mount(SettingsView);
    const selects = wrapper.findAllComponents(NSelect);
    const source = selects.find((select) =>
      (select.props("options") as Array<{ value: string }>).some((option) => option.value === "jinrishici"),
    );
    if (!source) throw new Error("quote source select not found");
    source.vm.$emit("update:value", "jinrishici");
    await wrapper.vm.$nextTick();

    const ui = useUiStore();
    expect(ui.dailyQuoteSource).toBe("jinrishici");
    expect(ui.dailyQuoteStyle).toBe("poetry");
    const style = wrapper.findAllComponents(NSelect).find((select) => {
      const options = select.props("options") as Array<{ value: string }>;
      return options.length === 1 && options[0]?.value === "poetry";
    });
    if (!style) throw new Error("quote style select not found");
    expect(style.props("disabled")).toBe(true);
  });

  it("jumps to a picked month via the month navigator", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    // MonthNavigator 渲染在工具栏
    const navigator = wrapper.findComponent(MonthNavigator);
    expect(navigator.exists(), "MonthNavigator should render in toolbar").toBe(true);
    // 模拟用户在面板选了 2025-12
    await navigator.vm.$emit("update:monthKey", "2025-12");
    // WorkCalendarMonth 的 monthKey prop 已更新
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe("2025-12");
  });

  it("jumps back to the current month via the Today button", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    // 先切到一个非当月
    const navigator = wrapper.findComponent(MonthNavigator);
    await navigator.vm.$emit("update:monthKey", "2025-01");
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe("2025-01");
    // 点今日按钮（用 aria-label 定位，避免和箭头按钮混淆）
    const todayBtn = wrapper
      .findAll(".calendar-toolbar .n-button")
      .find((btn) => btn.attributes("aria-label") === "今日");
    expect(todayBtn, "today button should render").toBeTruthy();
    await todayBtn!.trigger("click");
    const currentMonthKey = `${new Date().getFullYear()}-${String(new Date().getMonth() + 1).padStart(2, "0")}`;
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe(currentMonthKey);
  });

  it("renders the overrides count between the navigator and today button", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    const store = useWorkCalendarStore();
    // store.setOverride 签名为 (date, kind, name)——位置参数，非对象
    store.setOverride(formatDateKey(new Date()), "holiday", "测试假日");
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("1 个特殊日期");
  });
});
