import { nextTick, ref } from "vue";
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
