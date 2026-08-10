import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import FileWorkspaceView from "./FileWorkspaceView.vue";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";

describe("FileWorkspaceView", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps single click for selection and double-click for open", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([
      { documentId: "11111111-1111-4111-8111-111111111111", entryHandle: "doc-1", displayName: "方案.docx", authority: "workspace", availability: "available", mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", capabilities: ["open", "preview", "reveal", "history"] },
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
      { documentId: "22222222-2222-4222-8222-222222222222", entryHandle: "missing", displayName: "预算.xlsx", authority: "workspace", availability: "missing", capabilities: ["relink", "history"] },
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
      { documentId: "33333333-3333-4333-8333-333333333333", entryHandle: "drag-1", displayName: "brief.pdf", authority: "workspace", availability: "available", capabilities: ["open", "dragOut"] },
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

  it("forwards the busy diff cancel button to the closed host cancel intent", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([{
      documentId: "33333333-3333-4333-8333-333333333333",
      entryHandle: "diff-1",
      displayName: "compare.txt",
      authority: "workspace",
      availability: "available",
      effectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      capabilities: ["history", "diff"],
    }]);
    const wrapper = mount(FileWorkspaceView);
    store.selectAt(0);
    store.showInspector("history");
    store.beginDiff(
      "diff-1",
      "44444444-4444-4444-8444-444444444444",
      "55555555-5555-4555-8555-555555555555",
    );
    await wrapper.vm.$nextTick();

    await wrapper.get('[data-testid="diff-busy"] button').trigger("click");

    expect(wrapper.emitted("intent")?.at(-1)).toEqual([{
      type: "document.diffCancelRequested",
      entryHandle: "diff-1",
      operationId: expect.stringMatching(
        /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
      ),
    }]);
    expect(store.diffPhase).toBe("idle");
  });

  it("confirms a recoverable unlink with the current effective revision CAS", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([{
      documentId: "44444444-4444-4444-8444-444444444444",
      entryHandle: "unlink-1",
      displayName: "可恢复.txt",
      authority: "workspace",
      availability: "available",
      effectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      capabilities: ["open", "history", "unlink"],
    }]);
    const wrapper = mount(FileWorkspaceView, {
      global: {
        stubs: {
          teleport: true,
          transition: false,
        },
      },
    });
    await wrapper.get('[data-testid="document-row-unlink-1"]')
      .trigger("contextmenu", { clientX: 20, clientY: 30 });
    await wrapper.get('[data-testid="document-unlink"]').trigger("click");
    expect(wrapper.text()).toContain("版本历史会按保留策略保存");
    await wrapper.get('[data-testid="document-unlink-confirm"]').trigger("click");
    expect(wrapper.emitted("workspaceV2Action")?.at(-1)).toEqual([{
      method: "fileHistory.unlink",
      params: {
        documentId: "44444444-4444-4444-8444-444444444444",
        expectedEffectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      },
    }]);
  });

  it("shows only the local workspace source without a second placeholder source", () => {
    const wrapper = mount(FileWorkspaceView);
    expect(wrapper.find(".authority-switch").exists()).toBe(false);
  });

  it("requires an explicit identity decision for ambiguous external changes", async () => {
    const store = useDocumentWorkspaceStore();
    const protection = useWorkspaceProtectionStore();
    store.setEntries([{
      documentId: "11111111-1111-4111-8111-111111111111",
      entryHandle: "candidate-1",
      displayName: "原方案.docx",
      authority: "workspace",
      availability: "available",
      effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
      capabilities: ["open", "history"],
    }]);
    protection.setPendingFileChanges([{
      changeId: "33333333-3333-4333-8333-333333333333",
      relativePath: "归档/方案.docx",
      missing: false,
      observedHash: `sha256:${"ab".repeat(32)}`,
      observedSize: 1024,
      reason: "ambiguous identity",
      candidateDocumentIds: ["11111111-1111-4111-8111-111111111111"],
      createdAt: "2026-07-28T09:00:00Z",
      updatedAt: "2026-07-28T09:00:00Z",
    }]);
    const wrapper = mount(FileWorkspaceView, {
      global: {
        stubs: {
          teleport: true,
          transition: false,
        },
      },
    });

    expect(wrapper.get('[data-testid="pending-file-change-alert"]').text())
      .toContain("1");
    await wrapper.get('[data-testid="pending-file-change-alert"] button')
      .trigger("click");
    expect(wrapper.text()).toContain("VibeTable 不会猜测文件身份");
    await wrapper.get(
      '[data-testid="pending-move-33333333-3333-4333-8333-333333333333"]',
    ).trigger("click");

    expect(wrapper.emitted("workspaceV2Action")?.at(-1)).toEqual([{
      method: "fileHistory.applyPendingChange",
      params: {
        changeId: "33333333-3333-4333-8333-333333333333",
        action: "move",
        documentId: "11111111-1111-4111-8111-111111111111",
        expectedEffectiveRevisionId: "22222222-2222-4222-8222-222222222222",
      },
    }]);
  });
});
