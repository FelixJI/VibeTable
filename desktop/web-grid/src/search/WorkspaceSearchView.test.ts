import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NInput, NInputNumber, NSelect } from "naive-ui";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { SearchHit, SearchStatus } from "@/contracts/generated/workbench";
import { setLocale } from "@/i18n";
import {
  setWorkspaceV2UiPort,
  type WorkspaceV2UiPort,
} from "@/services/workspaceV2UiPort";
import WorkspaceSearchView from "./WorkspaceSearchView.vue";
import { useWorkspaceSearchStore } from "./workspaceSearchStore";

const readyStatus: SearchStatus = {
  state: "ready",
  generation: 4,
  checkpoint: "42",
  processed: 12,
  total: 12,
  errorCode: null,
};

function hit(kind: SearchHit["kind"], id: string, metadata = false): SearchHit {
  return {
    contractVersion: "1.0",
    hitId: id,
    kind,
    canonicalId: `${kind}:${id}`,
    title: `${kind}-${id}`,
    snippet: id === "without-snippet" ? null : `snippet-${id}`,
    highlights: [],
    sourceRevision: `revision-${id}`,
    score: 0.875,
    revisionTime: "2026-08-12T08:00:00Z",
    metadata: metadata ? [{ key: "relativePath", value: `reports/${id}.pdf` }] : [],
    openTarget: {
      kind,
      tableId: kind === "file" ? null : "orders",
      recordId: kind === "file" ? null : id,
      fieldId: kind === "attachment" ? "invoice" : null,
      documentId: kind === "file" ? id : null,
    },
  };
}

describe("WorkspaceSearchView", () => {
  beforeEach(() => {
    const pinia = createPinia();
    setActivePinia(pinia);
    setLocale("zh-CN");
    setWorkspaceV2UiPort(null);
  });

  it("searches, renders all source kinds, opens a result, and appends the next page", async () => {
    let queryCount = 0;
    const request = vi.fn(async (action: { method: string }) => {
      if (action.method === "workspaceSearch.status") return readyStatus;
      if (action.method === "workspaceSearch.query") {
        queryCount += 1;
        return queryCount === 1
          ? {
              hits: [
                hit("record", "record-1"),
                hit("attachment", "attachment-1"),
                hit("file", "file-1", true),
              ],
              nextCursor: "next-page",
              generation: 4,
            }
          : {
              hits: [hit("file", "without-snippet")],
              nextCursor: null,
              generation: 4,
            };
      }
      throw new Error(`unexpected ${action.method}`);
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const wrapper = mount(WorkspaceSearchView, {
      attachTo: document.body,
      global: { plugins: [createPinia()] },
    });
    const store = useWorkspaceSearchStore();
    await flushPromises();

    expect(wrapper.get(".index-state").attributes("data-generation")).toBe("4");
    expect(wrapper.get(".empty-state--initial").text()).toContain("跨数据与文件查找");
    store.query = "季度报告";
    await wrapper.get("form").trigger("submit");
    await flushPromises();

    expect(wrapper.findAll(".result-card")).toHaveLength(3);
    expect(wrapper.text()).toContain("reports/file-1.pdf");
    await wrapper.findAll(".result-card")[1]!.trigger("click");
    expect(wrapper.emitted("open")?.[0]?.[0]).toMatchObject({ kind: "attachment" });

    await wrapper.get('[data-testid="workspace-search-more"]').trigger("click");
    await flushPromises();
    expect(wrapper.findAll(".result-card")).toHaveLength(4);
    expect(wrapper.find('[data-testid="workspace-search-more"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("re-resolves an Enter-opened stale hit when authority replaces the focused result", async () => {
    const stale = hit("record", "record-old");
    const refreshed = {
      ...stale,
      hitId: "record-current",
      title: "record-current",
      sourceRevision: "revision-current",
    };
    const request = vi.fn(async (action: { method: string }) => {
      if (action.method === "workspaceSearch.status") return readyStatus;
      if (action.method === "workspaceSearch.resolveHit") {
        return { status: "stale", hit: refreshed };
      }
      throw new Error(`unexpected ${action.method}`);
    });
    setWorkspaceV2UiPort({ request: request as WorkspaceV2UiPort["request"] });
    const pinia = createPinia();
    const notifyStale = vi.fn();
    const wrapper = mount(WorkspaceSearchView, {
      attachTo: document.body,
      global: { plugins: [pinia] },
      props: {
        onOpen: async (indexedHit: SearchHit) => {
          const store = useWorkspaceSearchStore();
          if (await store.resolveHit(indexedHit) === null) notifyStale();
        },
      },
    });
    const store = useWorkspaceSearchStore();
    store.hits = [stale];
    await wrapper.vm.$nextTick();
    const oldResult = wrapper.get<HTMLButtonElement>('[data-testid="workspace-search-result"]');
    oldResult.element.focus();

    const replaceOnAuthorityUpdate = () => { store.hits = [refreshed]; };
    document.addEventListener("keydown", replaceOnAuthorityUpdate, { once: true });
    await oldResult.trigger("keydown", { key: "Enter" });
    await flushPromises();

    expect(request).toHaveBeenCalledWith(expect.objectContaining({
      method: "workspaceSearch.resolveHit",
      params: expect.objectContaining({ hit: stale }),
    }));
    expect(notifyStale).toHaveBeenCalledOnce();
    expect(store.hits).toEqual([refreshed]);
    wrapper.unmount();
  });

  it("binds AND/OR, scope, sort, kind, metadata, number and UTC date filters", async () => {
    setWorkspaceV2UiPort({
      request: vi.fn(async () => readyStatus) as WorkspaceV2UiPort["request"],
    });
    const pinia = createPinia();
    const wrapper = mount(WorkspaceSearchView, { global: { plugins: [pinia] } });
    const store = useWorkspaceSearchStore();
    await flushPromises();

    await wrapper.findAll(".segmented button")[1]!.trigger("click");
    await wrapper.findAll('input[type="radio"]')[1]!.setValue(true);
    wrapper.findAllComponents(NSelect)[0]!.vm.$emit("update:value", "file");
    wrapper.findAllComponents(NSelect)[1]!.vm.$emit("update:value", "title");
    wrapper.findAllComponents(NInput)[1]!.vm.$emit("update:value", " orders ");
    wrapper.findAllComponents(NInput)[2]!.vm.$emit("update:value", " invoice ");
    wrapper.findAllComponents(NInput)[3]!.vm.$emit("update:value", " application/pdf ");
    wrapper.findAllComponents(NInput)[4]!.vm.$emit("update:value", " .pdf ");
    wrapper.findAllComponents(NInputNumber)[0]!.vm.$emit("update:value", 100);
    wrapper.findAllComponents(NInputNumber)[1]!.vm.$emit("update:value", 900);
    wrapper.findAllComponents(NSelect)[2]!.vm.$emit("update:value", "indexed");
    const dates = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]');
    await dates[0]!.setValue("2026-08-01T10:30");
    await dates[1]!.setValue("2026-08-12T18:45");

    expect(store.logic).toBe("or");
    expect(store.scope).toBe("history");
    expect(store.sorts).toEqual([{ field: "title", direction: "asc" }]);
    expect(store.filters).toEqual(expect.arrayContaining([
      { field: "kind", operator: "eq", value: "file" },
      { field: "tableId", operator: "contains", value: "orders" },
      { field: "fieldId", operator: "contains", value: "invoice" },
      { field: "mimeType", operator: "contains", value: "application/pdf" },
      { field: "extension", operator: "eq", value: "pdf" },
      { field: "sizeBytes", operator: "gte", value: 100 },
      { field: "sizeBytes", operator: "lte", value: 900 },
      { field: "revisionTime", operator: "after", value: "2026-08-01T10:30:00.000Z" },
      { field: "revisionTime", operator: "before", value: "2026-08-12T18:45:00.000Z" },
      { field: "status", operator: "eq", value: "indexed" },
    ]));

    wrapper.findAllComponents(NSelect)[0]!.vm.$emit("update:value", "all");
    expect(store.filterValue("kind", "eq")).toBeNull();
    wrapper.unmount();
  });

  it("shows progress, stable errors, empty results, and switches rebuild to cancel", async () => {
    setWorkspaceV2UiPort({
      request: vi.fn(async () => readyStatus) as WorkspaceV2UiPort["request"],
    });
    const pinia = createPinia();
    const wrapper = mount(WorkspaceSearchView, { global: { plugins: [pinia] } });
    const store = useWorkspaceSearchStore();
    await flushPromises();
    const rebuild = vi.spyOn(store, "rebuild").mockResolvedValue();
    const cancel = vi.spyOn(store, "cancelRebuild").mockResolvedValue();

    store.status = { ...readyStatus, state: "building", processed: 3, total: 4 };
    store.errorCode = "workspace_search.busy";
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[role="alert"]').text()).toContain("workspace_search.busy");
    expect(wrapper.find(".n-progress").exists()).toBe(true);

    await wrapper.get('[data-testid="workspace-search-rebuild"]').trigger("click");
    expect(rebuild).toHaveBeenCalledOnce();
    store.rebuilding = true;
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="workspace-search-rebuild"]').trigger("click");
    expect(cancel).toHaveBeenCalledOnce();

    store.rebuilding = false;
    store.status = { ...readyStatus, state: "ready", total: null };
    store.errorCode = null;
    store.query = "no match";
    await wrapper.vm.$nextTick();
    expect(wrapper.get(".empty-state").text()).toContain("没有匹配结果");
    wrapper.unmount();
  });
});
