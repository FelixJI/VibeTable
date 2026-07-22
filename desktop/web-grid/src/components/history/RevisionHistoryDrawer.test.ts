import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import RevisionHistoryDrawer from "./RevisionHistoryDrawer.vue";
import { useRevisionHistoryStore } from "@/stores/revisionHistoryStore";
import type { HistoryPage, RestorePreview } from "@/contracts";

const mounted: VueWrapper[] = [];

function mountDrawer() {
  const wrapper = mount(RevisionHistoryDrawer, {
    props: { fieldOptions: [{ label: "状态", value: "status" }] },
    attachTo: document.body,
    global: { stubs: { teleport: true } },
  });
  mounted.push(wrapper);
  return wrapper;
}

const page: HistoryPage = {
  collection: "orders",
  scope: "table",
  changeSets: [{
    rootRevisionId: "r2",
    activityId: "activity-1",
    revisionIds: ["r2", "r3"],
    affectedRecords: 2,
    action: "update",
    timestamp: "2026-07-22T08:00:00Z",
    actor: { userId: "u1", displayName: "林舟" },
    scalarChanges: [],
    relationChanges: [],
    recordChanges: [{
      revisionId: "r2",
      itemId: "42",
      recordLabel: "客户 A",
      action: "update",
      scalarChanges: [{ field: "status", before: "new", after: "done" }],
      relationChanges: [],
    }, {
      revisionId: "r3",
      itemId: "43",
      recordLabel: "客户 B",
      action: "update",
      scalarChanges: [{ field: "owner", before: "A", after: "B" }],
      relationChanges: [],
    }],
  }],
  total: 2,
  hasMore: true,
  capabilityHash: "cap",
  schemaRevision: "schema",
};

describe("RevisionHistoryDrawer", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setActivePinia(createPinia());
  });
  afterEach(() => {
    for (const wrapper of mounted.splice(0)) wrapper.unmount();
  });

  it("renders a dense Activity group with per-record adjacent diffs", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "table" });
    store.receivePage(page);
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.get('[data-testid="history-scope-label"]').text()).toContain("整表审计");
    expect(wrapper.text()).toContain("2 条记录");
    expect(wrapper.text()).toContain("林舟");
    expect(wrapper.text()).toContain("客户 A");
    expect(wrapper.text()).toContain("new");
    expect(wrapper.text()).toContain("done");
  });

  it("updates the unified server search on Enter and emits reload", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "table" });
    store.receivePage(page);
    const wrapper = mountDrawer();
    const input = wrapper.get('[data-testid="history-search"] input');
    await input.setValue("客户 A");
    await input.trigger("keyup", { key: "Enter" });
    expect(store.query.search).toBe("客户 A");
    expect(wrapper.emitted("reload")).toHaveLength(1);
  });

  it("emits an exact single-record restore target", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "table" });
    store.receivePage(page);
    const wrapper = mountDrawer();
    await wrapper.get('[data-testid="history-preview-r2"]').trigger("click");
    expect(wrapper.emitted("preview")?.[0]).toEqual([{ itemId: "42", revisionId: "r2", field: null }]);
  });

  it("does not disclose an unavailable relation target", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "row", itemId: "42" });
    store.receivePage({
      ...page,
      scope: "row",
      itemId: "42",
      changeSets: [{
        ...page.changeSets[0]!,
        affectedRecords: 1,
        recordChanges: [{
          revisionId: "r2",
          itemId: "42",
          recordLabel: "客户 A",
          action: "update",
          scalarChanges: [],
          relationChanges: [{
            field: "owner",
            kind: "m2o",
            relatedCollection: "users",
            relatedItemId: null,
            displayValue: null,
            beforeItemId: "visible-user",
            afterItemId: "secret-user-id",
            beforeDisplayValue: "原负责人",
            afterDisplayValue: null,
            targetAvailable: false,
          }],
        }],
      }],
      total: 1,
      hasMore: false,
    });
    const wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.text()).toContain("不可用");
    expect(wrapper.text()).not.toContain("secret-user-id");
  });

  it("marks the backend-identified pre-delete revision as the archived default", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "archived" });
    const older = {
      ...page.changeSets[0]!,
      rootRevisionId: "r1",
      activityId: "activity-older",
      timestamp: "2026-07-21T08:00:00Z",
      recordChanges: [{
        ...page.changeSets[0]!.recordChanges![0]!,
        revisionId: "r1",
      }],
    };
    store.receivePage({
      ...page,
      scope: "archived",
      changeSets: [page.changeSets[0]!, older],
      archivedDefaultRevisionIds: { "42": "r1" },
      hasMore: false,
    });
    const wrapper = mountDrawer();
    await flushPromises();

    expect(wrapper.find('[data-testid="history-default-r2"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="history-default-r1"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("默认 · 删除前版本");
  });

  it("shows empty, unavailable, and retryable error states", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "table" });
    store.receivePage({ ...page, changeSets: [], total: 0, hasMore: false });
    const wrapper = mountDrawer();
    expect(wrapper.find('[data-testid="history-empty"]').exists()).toBe(true);

    store.failLoad("未开启修订", "history_not_allowed");
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-testid="history-unavailable"]').exists()).toBe(true);
    expect(wrapper.text()).toContain("未开启修订");

    store.failLoad("网络异常", "transport_error");
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-testid="history-error"]').exists()).toBe(true);
  });

  it("shows current-to-target preview and requires explicit confirmation", async () => {
    const store = useRevisionHistoryStore();
    store.open({ scope: "cell", itemId: "42", field: "status" });
    store.beginPreview({ itemId: "42", revisionId: "r1", field: "status" });
    const preview: RestorePreview = {
      collection: "orders",
      itemId: "42",
      scope: "cell",
      field: "status",
      targetRevision: "r1",
      currentHash: "hash",
      schemaRevision: "schema",
      scalarChanges: [{ field: "status", before: "done", after: "new" }],
      relationChanges: [],
      diagnostics: [{
        field: "updated_at",
        classification: "readonly_system",
        severity: "warning",
        code: "system_field",
        message: "系统字段不会写回",
      }],
      canApply: true,
      restorableFields: ["status"],
      token: "token",
      expiresAt: "2026-07-22T09:00:00Z",
    };
    store.receivePreview(preview);
    const wrapper = mountDrawer();
    await flushPromises();
    expect(wrapper.get('[data-testid="restore-preview"]').text()).toContain("done");
    expect(wrapper.get('[data-testid="restore-preview"]').text()).toContain("系统字段不能写回");
    await wrapper.get('[data-testid="restore-confirm"]').trigger("click");
    expect(wrapper.emitted("apply")).toHaveLength(1);
  });
});
