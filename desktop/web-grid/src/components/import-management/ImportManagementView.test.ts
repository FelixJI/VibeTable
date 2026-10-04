import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import ImportManagementView from "./ImportManagementView.vue";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import type { ImportHistoryEntry } from "@/contracts/importManagement";

function entry(overrides: Partial<ImportHistoryEntry> = {}): ImportHistoryEntry {
  return {
    taskId: "task-1",
    collection: "orders",
    sourceType: "csv",
    sourceName: "orders.csv",
    state: "succeeded",
    commitState: "committed",
    createdCount: 3,
    updatedCount: 1,
    startedAt: "2026-10-01T08:00:00Z",
    finishedAt: "2026-10-01T08:00:05Z",
    sessionEpoch: 1,
    errorCode: null,
    ...overrides,
  };
}

function mountView(props: Partial<InstanceType<typeof ImportManagementView>["$props"]> = {}) {
  return mount(ImportManagementView, {
    props: {
      loading: false,
      error: null,
      loaded: true,
      items: [],
      activeTaskId: null,
      taskCancellable: false,
      cancellingTaskId: null,
      pendingSourceName: null,
      canStart: true,
      ...props,
    },
  });
}

describe("ImportManagementView", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    useWorkspaceStore().setOpened(
      [{ collection: "orders" }, { collection: "invoices" }],
      { orders: "订单", invoices: "发票" },
    );
  });

  it("offers local file sources and marks cloud sources explicitly unavailable", async () => {
    const wrapper = mountView();

    await wrapper.get('[data-testid="import-source-csv"]').trigger("click");
    await wrapper.get('[data-testid="import-source-xlsx"]').trigger("click");
    expect(wrapper.emitted("newImport")).toHaveLength(2);

    for (const cloud of ["feishu", "wps"]) {
      const card = wrapper.get(`[data-testid="import-source-${cloud}"]`);
      expect(card.attributes("aria-disabled")).toBe("true");
      expect(card.text()).toContain("暂不可用");
    }
    // Unavailable cloud cards must not emit import intent.
    expect(wrapper.emitted("newImport")).toHaveLength(2);
  });

  it("disables starting a new import while a data task is busy", () => {
    const wrapper = mountView({ canStart: false });
    expect((wrapper.get('[data-testid="import-source-csv"]').element as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows the target picker for a granted source and emits the chosen target", async () => {
    const wrapper = mountView({ pendingSourceName: "orders.xlsx" });

    expect(wrapper.get('[data-testid="import-target"]').text()).toContain("orders.xlsx");
    const options = wrapper.findAll('[data-testid="import-target-option"]');
    expect(options).toHaveLength(2);
    expect(options[0]!.attributes("data-collection")).toBe("orders");

    await options[1]!.trigger("click");
    expect(wrapper.emitted("chooseTarget")).toEqual([["invoices"]]);

    await wrapper.get('[data-testid="import-cancel-source"]').trigger("click");
    expect(wrapper.emitted("cancelSource")).toHaveLength(1);
  });

  it("renders an empty state before any history exists", () => {
    const wrapper = mountView({ loaded: false, items: [] });
    expect(wrapper.get('[data-testid="import-management"]').text()).toContain("还没有导入记录");
  });

  it("shows a load error instead of pretending the history is empty", () => {
    const wrapper = mountView({ loaded: false, error: "rpc unavailable" });
    expect(wrapper.get('[data-testid="import-history-error"]').text()).toContain("rpc unavailable");
    expect(wrapper.text()).not.toContain("还没有导入记录");
  });

  it("never renders unknown commit counts as zero and explains the outcome", async () => {
    const wrapper = mountView({
      items: [
        entry({ taskId: "unknown-1", state: "interrupted", commitState: "unknown", createdCount: null, updatedCount: null }),
        entry({ taskId: "known-1", createdCount: 5, updatedCount: 2 }),
      ],
    });

    const rows = wrapper.findAll('[data-testid="import-history-row"]');
    expect(rows).toHaveLength(2);
    const unknownRow = rows[0]!;
    expect(unknownRow.attributes("data-commit-state")).toBe("unknown");
    expect(unknownRow.text()).toContain("结果待核实");

    await wrapper.get('[data-testid="import-history-detail-unknown-1"]').trigger("click");
    expect(unknownRow.text()).toContain("Worker");
    expect(unknownRow.text()).toContain("已中断");
    // Unknown counts are never displayed as numbers.
    expect(wrapper.find('[data-testid="import-entry-unknown"]').exists()).toBe(true);
    expect(unknownRow.find("[data-testid='import-entry-counts']").exists()).toBe(false);

    await wrapper.get('[data-testid="import-history-detail-known-1"]').trigger("click");
    expect(wrapper.get('[data-testid="import-entry-counts"]').text()).toContain("新增 5 行");
    expect(wrapper.get('[data-testid="import-entry-counts"]').text()).toContain("更新 2 行");
  });

  it("offers cancellation only for current-session queued/running entries", async () => {
    const wrapper = mountView({
      items: [
        entry({ taskId: "running-1", state: "running" }),
        entry({ taskId: "done-1", state: "succeeded" }),
        entry({ taskId: "stopped-1", state: "interrupted", commitState: "unknown" }),
      ],
    });

    const runningRow = wrapper.get('[data-task-id="running-1"]');
    expect(runningRow.find('[data-testid="import-history-cancel"]').exists()).toBe(true);
    expect(wrapper.get('[data-task-id="done-1"]').find('[data-testid="import-history-cancel"]').exists()).toBe(false);
    expect(wrapper.get('[data-task-id="stopped-1"]').find('[data-testid="import-history-cancel"]').exists()).toBe(false);

    await runningRow.get('[data-testid="import-history-cancel"]').trigger("click");
    expect(wrapper.emitted("cancelTask")).toEqual([["running-1"]]);
  });

  it("emits the target jump intent with the physical collection", async () => {
    const wrapper = mountView({ items: [entry()] });

    await wrapper.get('[data-testid="import-history-target-task-1"]').trigger("click");
    expect(wrapper.emitted("openTarget")).toEqual([["orders"]]);
  });

  it("emits refresh and renders the running local task with cancel", async () => {
    const wrapper = mountView({ activeTaskId: "local-1", taskCancellable: true });

    const card = wrapper.get('[data-testid="import-active-task"]');
    expect(card.text()).toContain("local-1");
    await card.get('[data-testid="import-cancel-active-task"]').trigger("click");
    expect(wrapper.emitted("cancelTask")).toEqual([["local-1"]]);

    await wrapper.get('[data-testid="import-history-refresh"]').trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
  });
});
