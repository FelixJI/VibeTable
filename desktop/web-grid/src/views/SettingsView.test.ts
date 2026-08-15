import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import SettingsView from "./SettingsView.vue";
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
      updateProxy: "direct",
      customUpdateProxyUrl: "",
    };
    backupRequest.mockImplementation(async (type: string, payload: unknown) => {
      if (type === "appPreferences.get") return appPreferences;
      if (type === "appPreferences.update") {
        appPreferences = { ...appPreferences, ...(payload as object) };
        return appPreferences;
      }
      if (type === "diagnostics.get") {
        return {
          bundleVersion: "1.0",
          generatedAt: "2026-08-12T00:00:00Z",
          operatingSystem: "Windows 11",
          programVersion: "0.1.0",
          dotnetVersion: "10.0.0",
          pocketBaseVersion: "0.39.9",
          memoryBytes: 64 * 1024 * 1024,
          components: [{ component: "sidecar", state: "ready" }],
          jobs: { queued: 0, running: 0, succeeded: 1, failed: 0, cancelled: 0 },
          index: { state: "ready", generation: 2, processed: 3, total: 3, errorCode: null },
          pendingMutationRevision: 0,
          recentErrorCounts: [],
          logs: [],
        };
      }
      if (type === "update.check") {
        return {
          currentVersion: "0.1.0",
          latestVersion: "0.3.0",
          updateAvailable: true,
          canInstall: true,
          installUnavailableReason: null,
          downloadBytes: 12 * 1024 * 1024,
          releaseUrl: "https://github.com/FelixJI/VibeTable/releases/tag/v0.3.0",
          notesTruncated: false,
          releases: [
            {
              version: "0.3.0",
              title: "0.3 功能更新",
              body: "新增安全自我更新",
              publishedAt: "2026-08-01T00:00:00Z",
              releaseUrl: "https://github.com/FelixJI/VibeTable/releases/tag/v0.3.0",
            },
            {
              version: "0.2.0",
              title: "0.2 稳定性更新",
              body: "改进工作区恢复",
              publishedAt: "2026-07-01T00:00:00Z",
              releaseUrl: "https://github.com/FelixJI/VibeTable/releases/tag/v0.2.0",
            },
          ],
        };
      }
      return { status: "restarting" };
    });
    setHostBridgeForTesting({
      request: backupRequest,
    } as unknown as HostBridge);
  });

  afterEach(() => setHostBridgeForTesting(null));

  it("organizes discoverable settings into six sections", () => {
    const wrapper = mount(SettingsView);
    expect(wrapper.findAll(".settings-nav button")).toHaveLength(6);
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
    const changelogPanel = wrapper.get('[data-testid="about-changelog"]');
    if (changelog.entries.length === 0) {
      expect(changelogPanel.get(".changelog-empty").text())
        .toBe("此版本暂无用户可见变更。");
    } else {
      expect(changelogPanel.findAll(".changelog-subject"))
        .toHaveLength(changelog.entries.length);
    }
    expect(changelog.entries)
      .not.toContainEqual(expect.objectContaining({
        subject: expect.stringMatching(/^(merge|合并)(:|\s)/i),
      }));
  });

  it("selects a GitHub proxy and shows release notes between two versions", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-about"]').trigger("click");
    await flushPromises();

    const proxy = wrapper.findAllComponents(NSelect)
      .find((select) => select.attributes("data-testid") === "update-proxy-select");
    expect(proxy).toBeDefined();
    proxy!.vm.$emit("update:value", "ghProxyCom");
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith(
      "appPreferences.update",
      { updateProxy: "ghProxyCom" },
    );

    await wrapper.get('[data-testid="check-update-button"]').trigger("click");
    await flushPromises();

    expect(backupRequest).toHaveBeenCalledWith("update.check", {});
    const notes = wrapper.get('[data-testid="between-version-release-notes"]').text();
    expect(notes).toContain("v0.3.0");
    expect(notes).toContain("新增安全自我更新");
    expect(notes).toContain("v0.2.0");
    expect(notes).toContain("改进工作区恢复");
    expect(wrapper.get('[data-testid="release-update-card"]').text())
      .toContain("同一通道下载发布包和 SHA-256 文件");
    expect(wrapper.get('[data-testid="release-update-card"]').text())
      .toContain("GitHub API 中的摘要交叉校验");

    await wrapper.get('[data-testid="install-update-button"]').trigger("click");
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith("update.install", {});
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

  it("routes shortcuts through real emits", async () => {
    const wrapper = mount(SettingsView);
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
