import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
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
import { NModal, NSelect } from "naive-ui";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";

describe("SettingsView", () => {
  const backupRequest = vi.fn();

  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
    backupRequest.mockReset();
    backupRequest.mockImplementation(async (type: string) => {
      if (type === "backup.list") {
        return {
          backups: [{
            name: "manual_20260724_101500.zip",
            size: 8192,
            modified: "2026-07-24T10:15:00Z",
            sha256: "a".repeat(64),
          }],
        };
      }
      if (type === "backup.create") {
        return {
          backup: {
            name: "manual_20260724_101501.zip",
            size: 9000,
            modified: "2026-07-24T10:15:01Z",
            sha256: "b".repeat(64),
          },
          integrityValid: true,
        };
      }
      if (type === "dataRoot.get") {
        return {
          dataRoot: "C:\\VibeTable\\VibeTableData",
          defaultDataRoot: "C:\\VibeTable\\VibeTableData",
          migrationPending: false,
          pendingDataRoot: null,
        };
      }
      if (type === "dataRoot.chooseMigrationRequested") {
        return {
          selected: true,
          targetDataRoot: "D:\\Data\\VibeTableData",
          requiresRestart: true,
        };
      }
      if (type === "diagnostics.get") {
        return {
          currentDirectory: "C:\\VibeTable",
          programDirectory: "C:\\VibeTable",
          dataDirectory: "C:\\VibeTable\\VibeTableData",
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
    expect(wrapper.text()).toContain("启动页面");
  });

  it("shows the assembly version and generated changelog in About", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-about"]').trigger("click");
    await flushPromises();

    expect(backupRequest).toHaveBeenCalledWith("diagnostics.get", {});
    expect(wrapper.text()).toContain("v0.1.0");
    expect(wrapper.get('[data-testid="about-changelog"]').text())
      .toContain("初始化项目");
    expect(wrapper.get('[data-testid="about-changelog"]').text())
      .not.toMatch(/\bMerge\b|合并/i);
  });

  it("lists and creates integrity-checked local backups", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-backup"]').trigger("click");
    await flushPromises();

    expect(backupRequest).toHaveBeenCalledWith("backup.list", {});
    expect(wrapper.text()).toContain("manual_20260724_101500.zip");

    await wrapper.get('[data-testid="backup-create"]').trigger("click");
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith(
      "backup.create",
      expect.objectContaining({ name: expect.stringMatching(/^manual_\d{8}_\d{6}\.zip$/) }),
    );
    expect(wrapper.get('[data-testid="backup-status"]').text()).toContain("备份");
  });

  it("requires explicit confirmation before starting a two-phase restore", async () => {
    const wrapper = mount(SettingsView, { attachTo: document.body });
    await wrapper.get('[data-testid="settings-nav-backup"]').trigger("click");
    await flushPromises();

    const restoreButton = wrapper.get(
      '[data-testid="backup-restore-manual_20260724_101500.zip"]',
    );
    (restoreButton.element as HTMLElement).focus();
    await restoreButton.trigger("click");
    expect(backupRequest).not.toHaveBeenCalledWith(
      "backup.restore",
      expect.anything(),
    );
    const modal = wrapper.findComponent(NModal);
    expect(modal.props("trapFocus")).toBe(true);
    expect(modal.props("maskClosable")).toBe(false);
    expect(
      document.body.querySelector('[data-testid="backup-restore-confirmation"]')
        ?.getAttribute("role"),
    ).toBe("dialog");

    document.body.querySelector<HTMLElement>('[data-testid="backup-restore-confirm"]')
      ?.click();
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith("backup.restore", {
      name: "manual_20260724_101500.zip",
      confirmed: true,
    });
    expect(wrapper.get('[data-testid="backup-status"]').text()).toContain("重启");
    wrapper.unmount();
  });

  it("closes restore confirmation on Esc intent and returns focus to its trigger", async () => {
    const wrapper = mount(SettingsView, { attachTo: document.body });
    await wrapper.get('[data-testid="settings-nav-backup"]').trigger("click");
    await flushPromises();
    const restoreButton = wrapper.get(
      '[data-testid="backup-restore-manual_20260724_101500.zip"]',
    );
    await restoreButton.trigger("click");

    wrapper.findComponent(NModal).vm.$emit("update:show", false);
    await flushPromises();
    await new Promise((resolve) => window.setTimeout(resolve, 0));

    expect(document.activeElement).toBe(restoreButton.element);
    expect(backupRequest).not.toHaveBeenCalledWith(
      "backup.restore",
      expect.anything(),
    );
    wrapper.unmount();
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

  it("keeps the data-service connection pill on a single line in the source row", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-source"]').trigger("click");
    // The pill's intrinsic width exceeds the space `space-between` leaves it;
    // wrapping it in a non-shrinking container keeps its content on one line.
    const sourceRow = wrapper
      .findAll(".setting-row")
      .find((row) => row.text().includes("本地数据服务"));
    expect(sourceRow, "data-service source row should render").toBeTruthy();
    expect(sourceRow!.find(".setting-control--pill").exists()).toBe(true);
    expect(sourceRow!.findComponent(ConnectionPill).exists()).toBe(true);
    expect(wrapper.find('[data-testid="preset-version-panel"]').exists()).toBe(false);
  });

  it("shows the active data root and schedules native migration for restart", async () => {
    const wrapper = mount(SettingsView);
    await wrapper.get('[data-testid="settings-nav-source"]').trigger("click");
    await flushPromises();

    expect(backupRequest).toHaveBeenCalledWith("dataRoot.get", {});
    expect(wrapper.get('[data-testid="data-root-path"]').text())
      .toContain("VibeTableData");

    await wrapper.get('[data-testid="data-root-migrate"]').trigger("click");
    await flushPromises();
    expect(backupRequest).toHaveBeenCalledWith(
      "dataRoot.chooseMigrationRequested",
      {},
    );
    expect(wrapper.get('[data-testid="data-root-pending"]').text())
      .toContain("重启");
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
