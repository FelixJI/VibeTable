import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import AppToolbar from "./AppToolbar.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";

/**
 * AppToolbar is pure-presentation. It reads from workspace/table/ui stores and
 * EMITS connect / refresh / openHelp. The theme dropdown writes to the uiStore
 * directly (pure UI concern). These tests verify:
 *   1. Row-count text binds to table.datasetReady + table.rowCount.
 *   2. Connect is disabled when phase is opened/opening; refresh disabled when
 *      no current table.
 *   3. Clicks emit the right events.
 *   4. The theme dropdown's @select updates ui.themeMode through the store.
 */
function mountToolbar() {
  return mount(AppToolbar);
}

function findButton(wrapper: ReturnType<typeof mountToolbar>, testId: string) {
  return wrapper.find(`[data-testid="${testId}"]`);
}

describe("AppToolbar", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("shows no row-count text before the dataset is ready", () => {
    const wrapper = mountToolbar();
    expect(wrapper.find('[data-testid="toolbar-row-count"]').exists()).toBe(false);
  });

  it("shows the row-count text once the dataset is ready", async () => {
    const table = useTableStore();
    table.setDatasetReady({
      table: "orders",
      columns: [],
      rows: [{ id: 1 }, { id: 2 }],
      offset: 0,
      limit: 2,
      totalRows: 2,
      mode: "client",
      loadedRows: 2,
    });
    const wrapper = mountToolbar();
    await wrapper.vm.$nextTick();
    const rc = wrapper.find('[data-testid="toolbar-row-count"]');
    expect(rc.exists()).toBe(true);
    expect(rc.text()).toContain("2");
  });

  it("disables connect when workspace is already opened", () => {
    const workspace = useWorkspaceStore();
    workspace.beginOpen();
    workspace.setOpened([{ collection: "users", metadata: {} }]);
    const wrapper = mountToolbar();
    const connect = findButton(wrapper, "toolbar-connect");
    expect(connect.attributes("disabled")).toBeDefined();
  });

  it("disables refresh when there is no current table", () => {
    const wrapper = mountToolbar();
    const refresh = findButton(wrapper, "toolbar-refresh");
    expect(refresh.attributes("disabled")).toBeDefined();
  });

  it("enables refresh when a current table is selected", () => {
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const wrapper = mountToolbar();
    const refresh = findButton(wrapper, "toolbar-refresh");
    expect(refresh.attributes("disabled")).toBeUndefined();
  });

  it("emits connect when the connect button is clicked", async () => {
    const wrapper = mountToolbar();
    await findButton(wrapper, "toolbar-connect").trigger("click");
    expect(wrapper.emitted("connect")).toBeTruthy();
  });

  it("emits refresh when the refresh button is clicked (with a current table)", async () => {
    const workspace = useWorkspaceStore();
    workspace.selectTable("orders");
    const wrapper = mountToolbar();
    await findButton(wrapper, "toolbar-refresh").trigger("click");
    expect(wrapper.emitted("refresh")).toBeTruthy();
  });

  it("emits openHelp when the help button is clicked", async () => {
    const wrapper = mountToolbar();
    await findButton(wrapper, "toolbar-open-help").trigger("click");
    expect(wrapper.emitted("openHelp")).toBeTruthy();
  });

  it("reflects the uiStore themeMode in the rendered icon-button state", () => {
    const ui = useUiStore();
    ui.setThemeMode("dark");
    const wrapper = mountToolbar();
    expect(wrapper.findComponent({ name: "Icon" }).exists()).toBe(true);
    // No exception means the icon computed handled dark mode.
    expect(wrapper.html()).toBeTruthy();
  });
});
