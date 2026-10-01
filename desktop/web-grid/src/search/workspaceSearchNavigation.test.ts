import { defineComponent, h, nextTick, onMounted, ref } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import { defaultDocumentQuery, useDocumentWorkspaceService } from "@/services/documentWorkspaceService";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import { describe, expect, it, vi } from "vitest";
import type { SearchHit } from "@/contracts/generated/workbench";
import type {
  DocumentEntry,
  DocumentWorkspacePhase,
} from "@/stores/documentWorkspaceStore";
import { createWorkspaceSearchNavigation } from "./workspaceSearchNavigation";

function hit(kind: "record" | "attachment" | "file", overrides: Partial<SearchHit["openTarget"]> = {}): SearchHit {
  return {
    contractVersion: "1.0",
    hitId: `${kind}-hit`,
    kind,
    canonicalId: `${kind}-id`,
    title: kind,
    snippet: null,
    highlights: [],
    sourceRevision: "revision-old",
    revisionTime: "2026-08-12T00:00:00Z",
    score: 1,
    metadata: [],
    openTarget: {
      kind,
      tableId: kind === "file" ? null : "orders",
      recordId: kind === "file" ? null : "record-1",
      fieldId: kind === "attachment" ? "files" : null,
      documentId: kind === "file" ? "document-1" : null,
      ...overrides,
    },
  };
}

function document(effectiveRevisionId = "revision-current"): DocumentEntry {
  return {
    documentId: "document-1",
    entryHandle: "entry-1",
    displayName: "plan.md",
    relativePath: "docs/plan.md",
    extension: ".md",
    authority: "workspace",
    availability: "available",
    mimeType: "text/markdown",
    sizeBytes: 42,
    effectiveRevisionCreatedAt: "2026-08-12T00:00:00Z",
    formalVersion: 2,
    status: "active",
    effectiveRevisionId,
    capabilities: ["open", "history"],
  };
}

function harness(resolved: SearchHit | null) {
  const documents = ref<readonly DocumentEntry[]>([]);
  const phase = ref<DocumentWorkspacePhase>("loading");
  const ports = {
    resolveHit: vi.fn(async () => resolved),
    getDocuments: () => documents.value,
    getDocumentPhase: () => phase.value,
    dispatchDocument: vi.fn(),
    selectDocument: vi.fn(),
    showDocumentHistory: vi.fn(),
    readDocumentHistory: vi.fn(),
    setLookupNavigation: vi.fn(),
    selectTable: vi.fn(),
    navigate: vi.fn(),
    warnStale: vi.fn(),
    reportInvalid: vi.fn(),
  };
  return {
    documents,
    phase,
    ports,
    navigation: createWorkspaceSearchNavigation(ports),
  };
}

describe("workspace search navigation", () => {
  it("dispatches the target after the files mount query and selects only its ready handles", async () => {
    setActivePinia(createPinia());
    const store = useDocumentWorkspaceStore();
    const documentId = "11111111-1111-4111-8111-111111111111";
    const oldEntry = { ...document(), documentId, entryHandle: "old-handle" };
    store.setEntries([oldEntry]);
    const replies: Array<(payload: unknown) => void> = [];
    const request = vi.fn((_type: string, _payload: unknown) => new Promise<unknown>((resolve) => replies.push(resolve)));
    setHostBridgeForTesting({ request, on: vi.fn(() => () => undefined) } as unknown as HostBridge);
    const service = useDocumentWorkspaceService();
    const base = harness(null);
    const showFiles = ref(false);
    const selectDocument = vi.fn((index: number) => store.selectAt(index));
    const navigation = createWorkspaceSearchNavigation({
      ...base.ports,
      getDocuments: () => store.entries,
      getDocumentPhase: () => store.phase,
      dispatchDocument: service.dispatch,
      selectDocument,
      showDocumentHistory: () => store.showInspector("history"),
      navigate: () => { showFiles.value = true; },
    });
    const FilePane = defineComponent({
      setup() {
        onMounted(() => {
          store.setAuthorityFilter("workspace");
          service.dispatch({
            type: "document.listRequested", scope: { kind: "global" },
            authority: "workspace", query: defaultDocumentQuery(),
          });
        });
        return () => null;
      },
    });
    const wrapper = mount(defineComponent({
      setup: () => () => showFiles.value ? h(FilePane) : null,
    }));
    try {
      navigation.openDocument(documentId);
      expect(request).not.toHaveBeenCalled();
      await nextTick();
      expect(request).toHaveBeenCalledTimes(2);
      expect(request.mock.calls[0]).toEqual([
        "document.listRequested", expect.objectContaining({ query: defaultDocumentQuery() }),
      ]);
      expect(request.mock.calls[1]).toEqual([
        "document.listRequested", expect.objectContaining({
          query: expect.objectContaining({
            filters: [{ field: "documentId", operator: "eq", value: documentId }], limit: 1,
          }),
        }),
      ]);
      expect(store.phase).toBe("loading");
      expect(selectDocument).not.toHaveBeenCalled();
      const currentEntry = { ...oldEntry, entryHandle: "current-handle" };
      replies[1]!({ entries: [currentEntry], nextCursor: null, topologyRevision: 2 });
      await flushPromises();
      expect(store.primaryHandle).toBe("current-handle");
      expect(store.inspectorTab).toBe("history");
      expect(base.ports.readDocumentHistory).toHaveBeenCalledWith(documentId);
      replies[0]!({ entries: [oldEntry], nextCursor: null, topologyRevision: 1 });
      await flushPromises();
      expect(store.primaryHandle).toBe("current-handle");
      expect(store.entries).toHaveLength(1);
      expect(selectDocument).toHaveBeenCalledOnce();
    } finally {
      wrapper.unmount();
      setHostBridgeForTesting(null);
    }
  });

  it("ignores retained entries until the targeted list is ready", async () => {
    const h = harness(null);
    h.navigation.openDocument("document-1");
    await nextTick();
    h.documents.value = [{ ...document(), entryHandle: "retained-handle" }];
    await nextTick();
    expect(h.ports.selectDocument).not.toHaveBeenCalled();
    expect(h.ports.readDocumentHistory).not.toHaveBeenCalled();
    h.documents.value = [{ ...document(), entryHandle: "ready-handle" }];
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.selectDocument).toHaveBeenCalledOnce();
    expect(h.ports.readDocumentHistory).toHaveBeenCalledOnce();
  });

  it("retires a file selection while the next search authority read is still pending", async () => {
    let resolveSearch!: (value: SearchHit | null) => void;
    const h = harness(null);
    h.ports.resolveHit.mockImplementationOnce(() => new Promise((resolve) => {
      resolveSearch = resolve;
    }));
    h.navigation.openDocument("document-1");
    await nextTick();
    const nextSearch = h.navigation.open(hit("record"));
    h.documents.value = [document()];
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.selectDocument).not.toHaveBeenCalled();
    resolveSearch(hit("record"));
    await nextSearch;
    expect(h.ports.navigate).toHaveBeenLastCalledWith("tables");
  });
  it("retires an older scheduled document query after a newer linked click", async () => {
    const h = harness(null);
    h.navigation.openDocument("document-1");
    h.navigation.openDocument("document-2");
    await nextTick();
    expect(h.ports.dispatchDocument).toHaveBeenCalledOnce();
    expect(h.ports.dispatchDocument).toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({
        filters: [{ field: "documentId", operator: "eq", value: "document-2" }],
      }),
    }));
  });

  it("retires a pending file selection when a new search opens a table record", async () => {
    const h = harness(hit("record"));
    h.navigation.openDocument("document-1");
    await nextTick();
    await h.navigation.open(hit("record"));
    h.documents.value = [document()];
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.navigate).toHaveBeenLastCalledWith("tables");
    expect(h.ports.selectDocument).not.toHaveBeenCalled();
    expect(h.ports.readDocumentHistory).not.toHaveBeenCalled();
  });

  it("opens a linked document only after the current authority list resolves its identity", async () => {
    const h = harness(null);
    h.navigation.openDocument("document-1");
    await nextTick();
    expect(h.ports.resolveHit).not.toHaveBeenCalled();
    expect(h.ports.selectDocument).not.toHaveBeenCalled();
    expect(h.ports.dispatchDocument).toHaveBeenCalledWith(expect.objectContaining({
      type: "document.listRequested",
      query: expect.objectContaining({
        filters: [{ field: "documentId", operator: "eq", value: "document-1" }],
      }),
    }));
    h.documents.value = [document()];
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.selectDocument).toHaveBeenCalledWith(0);
    expect(h.ports.showDocumentHistory).toHaveBeenCalledOnce();
    expect(h.ports.readDocumentHistory).toHaveBeenCalledWith("document-1");
    expect(h.navigation.requestedRevisionId.value).toBeNull();
  });
  it("rereads authority before opening a record or attachment", async () => {
    const refreshed = hit("attachment", { recordId: "record-2" });
    const h = harness(refreshed);
    await h.navigation.open(hit("attachment"));
    expect(h.ports.resolveHit).toHaveBeenCalledWith(expect.objectContaining({ hitId: "attachment-hit" }));
    expect(h.ports.setLookupNavigation).toHaveBeenCalledWith(expect.objectContaining({
      open: "attachment",
      fieldId: "files",
      source: expect.objectContaining({ collection: "orders", itemId: "record-2" }),
    }));
    expect(h.ports.selectTable).toHaveBeenCalledWith("orders");
    expect(h.ports.navigate).toHaveBeenCalledWith("tables");
  });

  it("queries a file by stable document identity and opens an historical revision", async () => {
    const h = harness(hit("file"));
    await h.navigation.open(hit("file"));
    expect(h.ports.navigate).toHaveBeenCalledWith("files");
    expect(h.ports.dispatchDocument).toHaveBeenCalledWith(expect.objectContaining({
      type: "document.listRequested",
      query: expect.objectContaining({
        filters: [{ field: "documentId", operator: "eq", value: "document-1" }],
      }),
    }));
    h.documents.value = [document()];
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.selectDocument).toHaveBeenCalledWith(0);
    expect(h.navigation.requestedRevisionId.value).toBe("revision-old");
    expect(h.ports.showDocumentHistory).toHaveBeenCalledOnce();
    expect(h.ports.readDocumentHistory).toHaveBeenCalledWith("document-1");
  });

  it("keeps a current file on the effective revision", async () => {
    const current = { ...hit("file"), sourceRevision: "revision-current" };
    const h = harness(current);
    await h.navigation.open(current);
    h.documents.value = [document()];
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.selectDocument).toHaveBeenCalledWith(0);
    expect(h.navigation.requestedRevisionId.value).toBeNull();
    expect(h.ports.readDocumentHistory).not.toHaveBeenCalled();
  });

  it("removes pending file state after an authoritative miss", async () => {
    const h = harness(hit("file"));
    await h.navigation.open(hit("file"));
    h.phase.value = "ready";
    await nextTick();
    expect(h.ports.warnStale).toHaveBeenCalledOnce();
    h.documents.value = [document()];
    await nextTick();
    expect(h.ports.selectDocument).not.toHaveBeenCalled();
  });

  it.each([
    ["missing authority result", null],
    ["missing file document", hit("file", { documentId: null })],
    ["missing record identity", hit("record", { recordId: null })],
    ["missing attachment field", hit("attachment", { fieldId: null })],
  ] as const)("fails closed for %s", async (_label, resolved) => {
    const h = harness(resolved);
    await h.navigation.open(hit(resolved?.kind ?? "record"));
    if (resolved === null) {
      expect(h.ports.warnStale).toHaveBeenCalledOnce();
    } else {
      expect(h.ports.reportInvalid).toHaveBeenCalledOnce();
    }
    expect(h.ports.setLookupNavigation).not.toHaveBeenCalled();
    expect(h.ports.dispatchDocument).not.toHaveBeenCalled();
  });

  it("suppresses an older authority response after a newer click", async () => {
    let resolveFirst!: (value: SearchHit | null) => void;
    const first = new Promise<SearchHit | null>((resolve) => { resolveFirst = resolve; });
    const h = harness(hit("record", { recordId: "record-2" }));
    h.ports.resolveHit
      .mockImplementationOnce(() => first)
      .mockResolvedValueOnce(hit("record", { recordId: "record-2" }));

    const older = h.navigation.open(hit("record", { recordId: "record-1" }));
    await h.navigation.open(hit("record", { recordId: "record-2" }));
    resolveFirst(hit("record", { recordId: "record-1" }));
    await older;

    expect(h.ports.setLookupNavigation).toHaveBeenCalledTimes(1);
    expect(h.ports.setLookupNavigation).toHaveBeenCalledWith(expect.objectContaining({
      source: expect.objectContaining({ itemId: "record-2" }),
    }));
    expect(h.ports.warnStale).not.toHaveBeenCalled();
  });
});
