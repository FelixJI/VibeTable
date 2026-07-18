import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { mount, flushPromises } from "@vue/test-utils";

import StatusBar from "./StatusBar.vue";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { setLocale, getLocale, type Locale } from "@/i18n";

function mountBar() {
  return mount(StatusBar);
}

/**
 * Save/restore the module-level i18n locale so the suite does not leak the
 * locale choice back into other test files.
 */
async function withLocale(locale: Locale, fn: () => Promise<void> | void): Promise<void> {
  const prev = getLocale();
  setLocale(locale);
  try {
    await fn();
  } finally {
    setLocale(prev);
  }
}

describe("StatusBar", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("shows the error message when the table store has an error", () => {
    withLocale("en-US", () => {
      const table = useTableStore();
      table.setError("disk full");
      const wrapper = mountBar();
      expect(wrapper.text()).toContain("disk full");
      expect(wrapper.find(".status-bar").exists()).toBe(true);
    });
  });

  it("shows the loading message while a table is loading", () => {
    withLocale("zh-CN", () => {
      const table = useTableStore();
      const workspace = useWorkspaceStore();
      table.beginLoad();
      workspace.selectTable("orders");
      const wrapper = mountBar();
      // The zh-CN template: 正在加载表「{name}」…
      expect(wrapper.text()).toContain("orders");
      expect(wrapper.text()).toContain("正在加载表");
    });
  });

  it("shows the loaded row count once the dataset is ready", async () => {
    await withLocale("zh-CN", async () => {
      const table = useTableStore();
      table.beginLoad();
      table.setDatasetReady({
        table: "orders",
        columns: [],
        rows: [{ id: 1 }, { id: 2 }, { id: 3 }],
        offset: 0,
        limit: 3,
        totalRows: 3,
        mode: "client",
        loadedRows: 3,
      });
      await flushPromises();
      const wrapper = mountBar();
      // zh-CN: 已加载 {count} 行
      expect(wrapper.text()).toContain("已加载");
      expect(wrapper.text()).toContain("3");
    });
  });

  it("falls back to a default status when nothing is loading or loaded", () => {
    withLocale("zh-CN", () => {
      // Fresh store: not loading, no dataset, no error -> ready/就绪.
      const wrapper = mountBar();
      expect(wrapper.text()).toContain("就绪");
    });
  });
});
