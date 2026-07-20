import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import SettingsView from "./SettingsView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import { useWorkCalendarStore } from "@/stores/workCalendarStore";
import { formatDateKey } from "@/calendar/workCalendar";

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
});
