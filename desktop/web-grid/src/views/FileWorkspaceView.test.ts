import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import FileWorkspaceView from "./FileWorkspaceView.vue";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type { DocumentEntry } from "@/stores/documentWorkspaceStore";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import DocumentContextMenu from "@/components/files/DocumentContextMenu.vue";
import DocumentInspector from "@/components/files/DocumentInspector.vue";
import type { FileRevisionV2 } from "@/contracts/workspaceV2";

function entry(overrides: Partial<DocumentEntry> & Pick<DocumentEntry, "documentId" | "entryHandle" | "displayName">): DocumentEntry {
  return {
    authority: "workspace", availability: "available", capabilities: [],
    relativePath: overrides.displayName, extension: "txt", mimeType: "text/plain",
    sizeBytes: 1, effectiveRevisionCreatedAt: "2026-08-12T00:00:00Z",
    formalVersion: 1, status: "active", ...overrides,
  };
}

describe("FileWorkspaceView", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => vi.useRealTimers());

  it("keeps single click for selection and double-click for open", async () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries([
      entry({ documentId: "11111111-1111-4111-8111-111111111111", entryHandle: "doc-1", displayName: "方案.docx", extension: "docx", mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", capabilities: ["open", "preview", "reveal", "history"] }),
    ]);
    const wrapper = mount(FileWorkspaceView);
    expect(wrapper.emitted("intent")?.[0]).toEqual([{
      type: "document.listRequested",
      scope: { kind: "global" },
      authority: "workspace",
      query: expect.objectContaining({ logic: "and", limit: 100, cursor: null }),
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
      entry({ documentId: "22222222-2222-4222-8222-222222222222", entryHandle: "missing", displayName: "预算.xlsx", availability: "missing", capabilities: ["relink", "history"] }),
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
      entry({ documentId: "33333333-3333-4333-8333-333333333333", entryHandle: "drag-1", displayName: "brief.pdf", capabilities: ["open", "dragOut"] }),
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
    store.setEntries([entry({
      documentId: "33333333-3333-4333-8333-333333333333",
      entryHandle: "diff-1",
      displayName: "compare.txt",
      effectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      capabilities: ["history", "diff"],
    })]);
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
    store.setEntries([entry({
      documentId: "44444444-4444-4444-8444-444444444444",
      entryHandle: "unlink-1",
      displayName: "可恢复.txt",
      effectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      capabilities: ["open", "history", "unlink"],
    })]);
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
    store.setEntries([entry({
      documentId: "11111111-1111-4111-8111-111111111111",
      entryHandle: "candidate-1",
      displayName: "原方案.docx",
      effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
      capabilities: ["open", "history"],
    })]);
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

  it("builds typed metadata queries, debounces search, and preserves pagination cursors", async () => {
    vi.useFakeTimers();
    const store = useDocumentWorkspaceStore();
    const wrapper = mount(FileWorkspaceView);
    const countAfterMount = wrapper.emitted("intent")?.length ?? 0;
    const field = wrapper.getComponent('[data-testid="file-filter-field"]') as VueWrapper;
    const value = wrapper.getComponent('[data-testid="file-filter-value"]') as VueWrapper;
    field.vm.$emit("update:value", "sizeBytes");
    await wrapper.vm.$nextTick();
    (wrapper.getComponent('[data-testid="file-filter-operator"]') as VueWrapper)
      .vm.$emit("update:value", "gte");

    value.vm.$emit("update:value", "not-a-number");
    await wrapper.get('[data-testid="file-filter-add"]').trigger("click");
    expect(wrapper.emitted("intent")).toHaveLength(countAfterMount);

    value.vm.$emit("update:value", "2048");
    await wrapper.get('[data-testid="file-filter-add"]').trigger("click");
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({
      type: "document.listRequested",
      query: { logic: "and", filters: [{ field: "sizeBytes", operator: "gte", value: 2048 }] },
    });

    store.setQuery("  report  ");
    await wrapper.vm.$nextTick();
    vi.advanceTimersByTime(220);
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({
      query: { filters: [
        { field: "displayName", operator: "contains", value: "report" },
        { field: "sizeBytes", operator: "gte", value: 2048 },
      ] },
    });

    const logic = wrapper.getComponent('[data-testid="file-filter-logic"]') as VueWrapper;
    logic.vm.$emit("update:value", "or");
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({ query: { logic: "or" } });

    store.setPage([], "opaque-cursor", 9, false);
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-testid="document-load-more"]').trigger("click");
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({
      query: { cursor: "opaque-cursor" },
    });
    store.beginLoad();
    const beforeBusyPage = wrapper.emitted("intent")?.length;
    await wrapper.get('[data-testid="document-load-more"]').trigger("click");
    expect(wrapper.emitted("intent")).toHaveLength(beforeBusyPage!);

    store.setPage([], null, 10, false);
    await wrapper.get('[data-testid="file-filter-chip-0"]').trigger("click");
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({
      query: { filters: [{ field: "displayName", value: "report" }] },
    });
    wrapper.unmount();
  });

  it("ignores non-file drags and tracks nested native file drag state", async () => {
    vi.useFakeTimers();
    const wrapper = mount(FileWorkspaceView);
    const workspace = wrapper.get('[data-testid="file-workspace"]');
    const nonFile = { types: ["text/plain"], dropEffect: "none", files: [] };
    await workspace.trigger("dragenter", { dataTransfer: nonFile });
    await workspace.trigger("dragover", { dataTransfer: nonFile });
    await workspace.trigger("dragleave", { dataTransfer: nonFile });
    await workspace.trigger("drop", { dataTransfer: nonFile });
    expect(wrapper.find('[data-testid="external-drop-zone"]').exists()).toBe(false);

    const transfer = { types: ["Files"], dropEffect: "none", files: [] as File[] };
    await workspace.trigger("dragenter", { dataTransfer: transfer });
    await workspace.trigger("dragenter", { dataTransfer: transfer });
    await workspace.trigger("dragover", { dataTransfer: transfer });
    expect(transfer.dropEffect).toBe("copy");
    await workspace.trigger("dragleave", { dataTransfer: transfer });
    expect(wrapper.find('[data-testid="external-drop-zone"]').exists()).toBe(true);
    await workspace.trigger("dragleave", { dataTransfer: transfer });
    expect(wrapper.find('[data-testid="external-drop-zone"]').exists()).toBe(false);

    await workspace.trigger("drop", { dataTransfer: transfer });
    expect(wrapper.find('[data-testid="drop-feedback"]').exists()).toBe(true);
    vi.advanceTimersByTime(3_200);
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-testid="drop-feedback"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("routes context actions and revision operations through closed intents", async () => {
    const store = useDocumentWorkspaceStore();
    const protection = useWorkspaceProtectionStore();
    const selected = entry({
      documentId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      entryHandle: "history-1",
      displayName: "history.txt",
      effectiveRevisionId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      capabilities: ["open", "preview", "reveal", "relink", "history", "unlink", "dragOut", "diff"],
    });
    store.setEntries([selected]);
    store.selectAt(0);
    const revision: FileRevisionV2 = {
      contractVersion: "2.0",
      documentId: selected.documentId,
      revisionId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      parentRevisionId: null,
      revisionOrdinal: 1,
      objectId: `sha256:${"1".repeat(64)}`,
      contentHash: `sha256:${"2".repeat(64)}`,
      size: 12,
      mimeType: "text/plain",
      formalVersion: 1,
      kind: "formal",
      createdAt: "2026-08-12T00:00:00Z",
      createdBy: "device",
      deviceId: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
      comment: null,
      localSequence: null,
      restoredFromRevisionId: null,
    };
    protection.setFileTree({
      documentId: selected.documentId,
      effectiveRevisionId: selected.effectiveRevisionId!,
      revisions: [revision],
    });
    const wrapper = mount(FileWorkspaceView);
    store.selectAt(0);
    await wrapper.vm.$nextTick();

    for (const action of ["open", "preview", "reveal", "relink", "dragOut", "history"] as const) {
      await wrapper.get('[data-testid="document-row-history-1"]')
        .trigger("contextmenu", { clientX: 1, clientY: 2 });
      wrapper.getComponent(DocumentContextMenu).vm.$emit("action", action, selected);
      await wrapper.vm.$nextTick();
    }
    expect(wrapper.emitted("intent")?.map((events) => (events[0] as { type: string }).type))
      .toEqual(expect.arrayContaining([
        "document.openRequested", "document.previewRequested", "document.revealRequested",
        "document.relinkRequested", "document.dragOutRequested",
      ]));
    expect(wrapper.emitted("workspaceV2Action")?.at(-1)?.[0]).toMatchObject({
      method: "fileHistory.readTree",
    });

    const inspector = wrapper.getComponent(DocumentInspector);
    inspector.vm.$emit("restoreFileRevision", selected, revision);
    inspector.vm.$emit("upgradeFileRevision", selected, revision);
    inspector.vm.$emit("activateFileRevision", selected, revision);
    inspector.vm.$emit("compareFileRevision", selected, revision);
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("workspaceV2Action")?.slice(-3).map((events) => (events[0] as { method: string }).method))
      .toEqual(["fileHistory.restore", "fileHistory.upgrade", "fileHistory.activateLeaf"]);
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({
      type: "document.diffRequested",
      historicalRevisionId: revision.revisionId,
      expectedEffectiveRevisionId: selected.effectiveRevisionId,
    });

    protection.setFileTree({
      documentId: selected.documentId,
      effectiveRevisionId: revision.revisionId,
      revisions: [revision],
    });
    const beforeNoop = wrapper.emitted("intent")?.length;
    inspector.vm.$emit("compareFileRevision", selected, revision);
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("intent")).toHaveLength(beforeNoop!);
  });

  it("offers every fail-closed pending-file decision and closes when the queue drains", async () => {
    const store = useDocumentWorkspaceStore();
    const protection = useWorkspaceProtectionStore();
    const candidate = entry({
      documentId: "11111111-1111-4111-8111-111111111111",
      entryHandle: "candidate",
      displayName: "candidate.txt",
      effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
    });
    store.setEntries([candidate]);
    const baseChange = {
      observedHash: `sha256:${"ab".repeat(32)}`,
      reason: "ambiguous",
      createdAt: "2026-08-12T00:00:00Z",
      updatedAt: "2026-08-12T00:00:00Z",
    } as const;
    protection.setPendingFileChanges([
      { ...baseChange, changeId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", relativePath: "tiny.txt", missing: false, observedSize: 12, candidateDocumentIds: [] },
      { ...baseChange, changeId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", relativePath: "large.bin", missing: false, observedSize: 2 * 1024 * 1024, candidateDocumentIds: [candidate.documentId] },
      { ...baseChange, changeId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", relativePath: "gone.txt", missing: true, observedSize: 0, candidateDocumentIds: [candidate.documentId] },
    ]);
    const wrapper = mount(FileWorkspaceView, { global: { stubs: { teleport: true, transition: false } } });
    await wrapper.get('[data-testid="pending-file-change-alert"] button').trigger("click");
    expect(wrapper.text()).toContain("12 B");
    expect(wrapper.text()).toContain("2.0 MB");

    await wrapper.get('[data-testid="pending-new-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]').trigger("click");
    await wrapper.get('[data-testid="pending-dismiss-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]').trigger("click");
    const candidateSelect = wrapper.getComponent('[data-testid="pending-candidate-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"]') as VueWrapper;
    candidateSelect.vm.$emit("update:value", 7);
    candidateSelect.vm.$emit("update:value", candidate.documentId);
    await wrapper.get('[data-testid="pending-copy-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"]').trigger("click");
    await wrapper.get('[data-testid="pending-delete-cccccccc-cccc-4ccc-8ccc-cccccccccccc"]').trigger("click");

    const actions = wrapper.emitted("workspaceV2Action")!.map((events) => events[0] as { params: { action: string; documentId: string | null } });
    expect(actions.map((action) => action.params.action)).toEqual(["new", "dismiss", "copy", "delete"]);
    expect(actions[0]!.params.documentId).toBeNull();
    expect(actions.at(-1)!.params.documentId).toBe(candidate.documentId);

    protection.setPendingFileChanges([]);
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-testid="pending-change-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]').exists()).toBe(false);
  });

  it("renders loading and both failure recovery states", async () => {
    const store = useDocumentWorkspaceStore();
    store.beginLoad();
    const wrapper = mount(FileWorkspaceView);
    expect(wrapper.find(".file-skeleton").exists()).toBe(true);
    store.setFailed("offline");
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".file-empty").text()).toContain("offline");
    await wrapper.find(".file-empty button").trigger("click");

    store.setEntries([entry({ documentId: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", entryHandle: "one", displayName: "one.txt" })]);
    store.setFailed("stale");
    await wrapper.vm.$nextTick();
    expect(wrapper.get(".file-error").text()).toContain("stale");
    await wrapper.get(".file-error button").trigger("click");
    expect(wrapper.emitted("intent")?.at(-1)?.[0]).toMatchObject({ type: "document.listRequested" });
  });
});
