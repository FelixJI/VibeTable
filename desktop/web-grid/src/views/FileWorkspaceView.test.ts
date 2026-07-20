import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import FileWorkspaceView from "./FileWorkspaceView.vue";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";

describe("FileWorkspaceView", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps single click for selection and double-click for open", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([
      { entryHandle: "doc-1", documentId: "1", displayName: "方案.docx", authority: "workspace", availability: "available", mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", capabilities: ["open", "preview", "reveal", "history"] },
    ]);
    const wrapper = mount(FileWorkspaceView);
    expect(wrapper.emitted("intent")?.[0]).toEqual([{
      type: "document.listRequested",
      scope: { kind: "global" },
      authority: "workspace",
    }]);
    const row = wrapper.get('[data-testid="document-row-doc-1"]');
    const countBeforeClick = wrapper.emitted("intent")?.length ?? 0;
    await row.trigger("click");
    expect(store.selectedHandles).toEqual(["doc-1"]);
    expect(wrapper.emitted("intent")?.length).toBe(countBeforeClick);
    await row.trigger("dblclick");
    expect(wrapper.emitted("intent")?.at(-1)).toEqual([{ type: "document.openRequested", entryHandle: "doc-1" }]);
  });

  it("offers relink instead of reveal for a missing workspace document", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([
      { entryHandle: "missing", documentId: "2", displayName: "预算.xlsx", authority: "workspace", availability: "missing", capabilities: ["relink", "history"] },
    ]);
    const wrapper = mount(FileWorkspaceView);
    await wrapper.get('[data-testid="document-row-missing"]').trigger("contextmenu", { clientX: 20, clientY: 30 });
    const menu = wrapper.get('[data-testid="document-context-menu"]');
    expect(menu.text()).toContain("重新定位");
    expect(menu.text()).not.toContain("在资源管理器中显示");
  });

  it("marks cloud attachments as unavailable until the host scope is connected", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([
      { entryHandle: "local", documentId: "1", displayName: "local.pdf", authority: "workspace", availability: "available", capabilities: ["preview"] },
      { entryHandle: "cloud", documentId: "2", displayName: "cloud.pdf", authority: "cloud", availability: "remote", capabilities: ["preview"] },
    ]);
    const wrapper = mount(FileWorkspaceView);
    expect(wrapper.text()).toContain("local.pdf");
    const sourceTabs = wrapper.findAll(".authority-switch button");
    expect(sourceTabs[1].attributes("disabled")).toBeDefined();
    await sourceTabs[1].trigger("click");
    expect(wrapper.text()).toContain("local.pdf");
    expect(wrapper.text()).not.toContain("cloud.pdf");
  });
});
