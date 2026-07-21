import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import SettingsView from "./SettingsView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { formatDateKey } from "@/calendar/workCalendar";
import { useUiStore } from "@/stores/uiStore";
import ConnectionPill from "@/components/feedback/ConnectionPill.vue";
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";
import { NPopconfirm, NSelect } from "naive-ui";

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

  it("aligns the interface density control with the other general settings", () => {
    const wrapper = mount(SettingsView);
    const densityRow = wrapper
      .findAll(".setting-row")
      .find((row) => row.text().includes("界面密度"));
    expect(densityRow, "density setting row should render").toBeTruthy();
    // The radio group must opt into the fixed-width column AND the radio row
    // layout (flex-direction: row), otherwise `.setting-row > div` forces the
    // buttons into a vertical stack.
    const group = densityRow!.find(".setting-control--radio");
    expect(group.exists()).toBe(true);
    expect(group.classes()).toContain("n-radio-group");
  });

  it("keeps the Directus connection pill on a single line in the source row", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-source"]').trigger("click");
    // The pill's intrinsic width exceeds the space `space-between` leaves it;
    // wrapping it in a non-shrinking container keeps its content on one line.
    const sourceRow = wrapper
      .findAll(".setting-row")
      .find((row) => row.text().includes("Directus"));
    expect(sourceRow, "Directus source row should render").toBeTruthy();
    expect(sourceRow!.find(".setting-control--pill").exists()).toBe(true);
    expect(sourceRow!.findComponent(ConnectionPill).exists()).toBe(true);
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

  it("offers row delete only for orphaned/deleted mappings", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-mapping"]').trigger("click");
    const store = useIdentifierMappingStore();
    store.succeed({ mappings: [
      {
        id: "m-active", entityKind: "collection", physicalName: "vt_t_01",
        displayName: "活动表", locale: "zh-CN", aliases: [],
        origin: "vibetable", status: "active",
      },
      {
        id: "m-orphan", entityKind: "collection", physicalName: "vt_t_gone",
        displayName: "孤立表", locale: "zh-CN", aliases: [],
        origin: "vibetable", status: "orphaned",
      },
    ] });
    await wrapper.vm.$nextTick();

    const rows = wrapper.findAll(".mapping-item");
    // Active row has no delete control; orphaned row exposes one.
    expect(rows[0].find(".mapping-delete").exists()).toBe(false);
    expect(rows[1].find(".mapping-delete").exists()).toBe(true);

    // Confirms the per-row popconfirm by emitting its positive-click event —
    // the same event NPopconfirm fires on user confirmation.
    await rows[1].findComponent(NPopconfirm).vm.$emit("positive-click");
    expect(wrapper.emitted("deleteMapping")).toEqual([["m-orphan"]]);
  });

  it("enables the purge button when removable rows exist and emits on confirm", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-mapping"]').trigger("click");
    const store = useIdentifierMappingStore();
    store.succeed({ mappings: [{
      id: "m-del", entityKind: "collection", physicalName: "vt_t_gone",
      displayName: "已删表", locale: "zh-CN", aliases: [],
      origin: "vibetable", status: "deleted",
    }] });
    await wrapper.vm.$nextTick();

    const purgeButton = wrapper
      .findAll(".mapping-toolbar .n-button")
      .find((btn) => btn.find(".mapping-delete, [aria-label='一键清理孤立/已删除映射']").exists()
        || btn.attributes("aria-label") === "一键清理孤立/已删除映射");
    expect(purgeButton, "purge button should render").toBeTruthy();
    expect(purgeButton!.attributes("disabled")).toBeUndefined();

    await wrapper.findAllComponents(NPopconfirm)[0].vm.$emit("positive-click");
    expect(wrapper.emitted("purgeMappings")).toHaveLength(1);
  });

  it("disables the purge button when every mapping is active", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-mapping"]').trigger("click");
    const store = useIdentifierMappingStore();
    store.succeed({ mappings: [{
      id: "m-active", entityKind: "collection", physicalName: "vt_t_01",
      displayName: "活动表", locale: "zh-CN", aliases: [],
      origin: "vibetable", status: "active",
    }] });
    await wrapper.vm.$nextTick();

    const purgeButton = wrapper
      .findAll(".mapping-toolbar .n-button")
      .find((btn) => btn.attributes("aria-label") === "一键清理孤立/已删除映射");
    expect(purgeButton, "purge button should render").toBeTruthy();
    expect(purgeButton!.attributes("disabled")).toBeDefined();
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
    // 点今日按钮（用 data-testid 定位，与文件中既有约定一致）
    const todayBtn = wrapper.get('[data-testid="calendar-today"]');
    await todayBtn.trigger("click");
    const currentMonthKey = `${new Date().getFullYear()}-${String(new Date().getMonth() + 1).padStart(2, "0")}`;
    expect(wrapper.findComponent(WorkCalendarMonth).props("monthKey")).toBe(currentMonthKey);
  });

  it("renders the overrides count in the calendar toolbar", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-calendar"]').trigger("click");
    const store = useWorkCalendarStore();
    // store.setOverride 签名为 (date, kind, name)——位置参数，非对象
    store.setOverride(formatDateKey(new Date()), "holiday", "测试假日");
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("1 个特殊日期");
  });
});
