import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import SettingsView from "./SettingsView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { formatDateKey } from "@/calendar/workCalendar";
import { useUiStore } from "@/stores/uiStore";
import WorkCalendarMonth from "@/components/calendar/WorkCalendarMonth.vue";
import MonthNavigator from "@/components/calendar/MonthNavigator.vue";
import { NSelect, NSwitch } from "naive-ui";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import changelog from "@/generated/changelog.json";

describe("SettingsView", () => {
  const backupRequest = vi.fn();

  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
    backupRequest.mockReset();
    let appPreferences = {
      minimizeToTrayOnClose: false,
      startWithWindows: false,
    };
    backupRequest.mockImplementation(async (type: string, payload: unknown) => {
      if (type === "appPreferences.get") return appPreferences;
      if (type === "appPreferences.update") {
        appPreferences = { ...appPreferences, ...(payload as object) };
        return appPreferences;
      }
      if (type === "diagnostics.get") {
        return {
          currentDirectory: "C:\\VibeTable",
          programDirectory: "C:\\VibeTable",
          dataDirectory: "C:\\VibeTable\\shell",
          operatingSystem: "Windows 11",
          programVersion: "0.1.0",
          dotnetVersion: "10.0.0",
          pocketBaseVersion: "0.39.9",
          memoryBytes: 64 * 1024 * 1024,
          dataServiceState: "ready",
        };
      }
      return { status: "restarting" };
    });
    setHostBridgeForTesting({
      request: backupRequest,
    } as unknown as HostBridge);
  });

  afterEach(() => setHostBridgeForTesting(null));

  it("organizes discoverable settings into seven sections", () => {
    const wrapper = mount(SettingsView);
    expect(wrapper.findAll(".settings-nav button")).toHaveLength(7);
    expect(wrapper.text()).toContain("界面语言");
    expect(wrapper.text()).toContain("启动时进入");
    expect(wrapper.text()).toContain("上次使用的工作区");
    expect(wrapper.text()).toContain("启动页面");
  });

  it("persists the device-level Workspace Center startup preference", async () => {
    const wrapper = mount(SettingsView);
    const startup = wrapper.findAllComponents(NSelect)
      .find((select) =>
        select.attributes("data-testid") === "workspace-startup-policy-select");
    expect(startup).toBeDefined();

    startup!.vm.$emit("update:value", "workspaceCenter");
    await wrapper.vm.$nextTick();

    const ui = useUiStore();
    expect(ui.workspaceStartupPolicy).toBe("workspaceCenter");
    expect(localStorage.getItem("vt:workspace-startup-policy")).toBe("workspaceCenter");
  });

  it("loads and updates native tray and Windows startup preferences", async () => {
    const wrapper = mount(SettingsView);
    await flushPromises();

    expect(backupRequest).toHaveBeenCalledWith("appPreferences.get", {});
    expect(wrapper.text()).toContain("关闭时最小化到托盘");
    expect(wrapper.text()).toContain("开机自启动");

    const minimize = wrapper.findAllComponents(NSwitch)
      .find((item) => item.attributes("data-testid") === "minimize-to-tray-switch");
    const startup = wrapper.findAllComponents(NSwitch)
      .find((item) => item.attributes("data-testid") === "start-with-windows-switch");
    expect(minimize).toBeDefined();
    expect(startup).toBeDefined();

    minimize!.vm.$emit("update:value", true);
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith(
      "appPreferences.update",
      { minimizeToTrayOnClose: true },
    );
    expect(minimize!.props("value")).toBe(true);

    startup!.vm.$emit("update:value", true);
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith(
      "appPreferences.update",
      { startWithWindows: true },
    );
    expect(startup!.props("value")).toBe(true);
  });

  it("shows the assembly version and generated changelog in About", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-about"]').trigger("click");
    await flushPromises();

    expect(backupRequest).toHaveBeenCalledWith("diagnostics.get", {});
    expect(wrapper.text()).toContain("v0.1.0");
    const firstEntry = changelog.entries[0];
    expect(firstEntry).toBeDefined();
    expect(wrapper.get('[data-testid="about-changelog"]').text())
      .toContain(firstEntry!.subject);
    expect(changelog.entries)
      .not.toContainEqual(expect.objectContaining({
        subject: expect.stringMatching(/^(merge|合并)(:|\s)/i),
      }));
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

  it("routes advanced schema and shortcuts through real emits", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-mapping"]').trigger("click");
    await wrapper.find(".mapping-footer .n-button").trigger("click");
    expect(wrapper.emitted("openAdmin")).toHaveLength(1);

    await wrapper.get('[data-testid="settings-nav-interaction"]').trigger("click");
    await wrapper.get(".setting-action").trigger("click");
    expect(wrapper.emitted("openHelp")).toHaveLength(1);

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
    expect(wrapper.get('[data-testid="quote-network-disclosure"]').text())
      .toContain("第三方");
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
