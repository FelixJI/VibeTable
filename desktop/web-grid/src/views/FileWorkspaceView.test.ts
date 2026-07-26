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
    await menu.findAll('[role="menuitem"]')[0].trigger("click");
    expect(wrapper.emitted("intent")?.at(-1)).toEqual([{
      type: "document.relinkRequested",
      handle: "missing",
    }]);
  });

  it("exposes import and forwards dropped DOM Files without reading them", async () => {
    const wrapper = mount(FileWorkspaceView);
    await wrapper.get('[data-testid="document-import"]').trigger("click");
    expect(wrapper.emitted("intent")?.at(-1)).toEqual([{
      type: "document.importRequested",
      scope: { kind: "global" },
    }]);

    const droppedFile = new File(["not-read-by-web"], "private-name.txt", { type: "text/plain" });
    const dataTransfer = {
      types: ["Files"],
      dropEffect: "none",
      files: [droppedFile],
    };
    await wrapper.get('[data-testid="file-workspace"]').trigger("dragenter", { dataTransfer });
    expect(wrapper.find('[data-testid="external-drop-zone"]').exists()).toBe(true);
    await wrapper.get('[data-testid="file-workspace"]').trigger("drop", { dataTransfer });
    const dropIntent = wrapper.emitted("intent")?.at(-1)?.[0] as {
      type: string;
      scope: { kind: string };
      files: readonly File[];
    };
    expect(dropIntent.type).toBe("document.externalDropRequested");
    expect(dropIntent.scope).toEqual({ kind: "global" });
    expect(dropIntent.files).toEqual([droppedFile]);
    expect(wrapper.find('[data-testid="drop-feedback"]').exists()).toBe(true);
  });

  it("turns an eligible row drag gesture into an opaque drag-out intent", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([
      { entryHandle: "drag-1", documentId: "3", displayName: "brief.pdf", authority: "workspace", availability: "available", capabilities: ["open", "dragOut"] },
    ]);
    const wrapper = mount(FileWorkspaceView);
    const row = wrapper.get('[data-testid="document-row-drag-1"]');
    expect(row.attributes("draggable")).toBe("true");
    await row.trigger("dragstart", { dataTransfer: { types: [], dropEffect: "none" } });
    expect(wrapper.emitted("intent")?.at(-1)).toEqual([{
      type: "document.dragOutRequested",
      handle: "drag-1",
    }]);
  });

  it("shows only the local workspace source without a second placeholder source", () => {
    const wrapper = mount(FileWorkspaceView);
    expect(wrapper.find(".authority-switch").exists()).toBe(false);
  });
});
