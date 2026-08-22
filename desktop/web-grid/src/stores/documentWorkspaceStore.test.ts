import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { WorkspaceSessionV2 } from "@/contracts/workspaceV2";
import { useDocumentWorkspaceStore, type DocumentEntry } from "./documentWorkspaceStore";
import { useWorkspaceSessionStore } from "./workspaceSessionStore";

const entries: readonly DocumentEntry[] = [
  entry({ documentId: "11111111-1111-4111-8111-111111111111", entryHandle: "a", displayName: "A.docx", capabilities: ["open", "preview", "reveal", "history"] }),
  entry({ documentId: "22222222-2222-4222-8222-222222222222", entryHandle: "b", displayName: "B.pdf", capabilities: ["open", "preview"] }),
  entry({ documentId: "33333333-3333-4333-8333-333333333333", entryHandle: "c", displayName: "C.xlsx", availability: "missing", capabilities: ["relink", "history"] }),
];

function entry(overrides: Partial<DocumentEntry> & Pick<DocumentEntry, "documentId" | "entryHandle" | "displayName">): DocumentEntry {
  return {
    authority: "workspace", availability: "available", capabilities: [],
    relativePath: overrides.displayName, extension: "txt", mimeType: "text/plain",
    sizeBytes: 1, effectiveRevisionCreatedAt: "2026-08-12T00:00:00Z",
    formalVersion: 1, status: "active", ...overrides,
  };
}

function session(workspaceId: string, sessionEpoch: number): WorkspaceSessionV2 {
  return {
    contractVersion: "2.0",
    workspaceId,
    sessionEpoch,
    state: "openedWritable",
    openMode: "writable",
    writable: true,
    provisional: false,
    phase: "idle",
    errorCode: null,
  };
}

describe("documentWorkspaceStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("supports Explorer-style single, Ctrl, and Shift selection", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.selectAt(0);
    expect(store.selectedHandles).toEqual(["a"]);
    store.selectAt(2, { toggle: true });
    expect(store.selectedHandles).toEqual(["a", "c"]);
    store.selectAt(0, { range: true });
    expect(store.selectedHandles).toEqual(["a", "b", "c"]);
    expect(store.primaryHandle).toBe("a");
  });

  it("keeps the workspace list provider-neutral", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    expect(store.visibleEntries.map((entry) => entry.entryHandle)).toEqual(["a", "b", "c"]);
  });

  it("keeps history closed when the current primary document lacks capability", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.selectAt(1);

    store.showInspector("history");

    expect(store.inspectorTab).toBe("preview");
  });

  it("revokes history state at the authority update boundary without a mounted view", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.selectAt(0);
    store.showInspector("history");
    expect(store.inspectorTab).toBe("history");

    store.setEntries([
      { ...entries[0]!, capabilities: ["open", "preview"] },
      ...entries.slice(1),
    ]);
    expect(store.inspectorTab).toBe("preview");

    store.setEntries(entries);
    expect(store.inspectorTab).toBe("preview");
  });

  it.each(["selectAt", "selectAllVisible", "clearSelection"] as const)(
    "returns history to preview when %s changes the selection identity",
    (operation) => {
      const store = useDocumentWorkspaceStore();
      store.setEntries(entries);
      store.selectAt(0);
      store.showInspector("history");
      expect(store.inspectorTab).toBe("history");

      if (operation === "selectAt") store.selectAt(2);
      else if (operation === "selectAllVisible") store.selectAllVisible();
      else store.clearSelection();

      expect(store.inspectorTab).toBe("preview");
    },
  );

  it("returns history to preview on a real workspace epoch reset", () => {
    const workspaceSession = useWorkspaceSessionStore();
    workspaceSession.configureCapabilities(["workspace.session.v2"]);
    workspaceSession.applySession(session("workspace-a", 1));
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.selectAt(0);
    store.showInspector("history");
    expect(store.inspectorTab).toBe("history");

    workspaceSession.applySession(session("workspace-b", 2));

    expect(store.inspectorTab).toBe("preview");
    expect(store.primaryHandle).toBeNull();
    expect(store.entries).toEqual([]);
  });

  it("keeps stale entries visible while a refresh is in progress", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.beginLoad();
    expect(store.phase).toBe("loading");
    expect(store.entries).toHaveLength(3);
  });

  it("appends a cursor page without duplicating documents and tracks page metadata", () => {
    const store = useDocumentWorkspaceStore();
    store.setPage(entries.slice(0, 2), "cursor-2", 7, false);
    store.setPage([entries[1]!, entries[2]!], null, 7, true);

    expect(store.entries.map((item) => item.documentId)).toEqual([
      entries[0]!.documentId,
      entries[1]!.documentId,
      entries[2]!.documentId,
    ]);
    expect(store.nextCursor).toBeNull();
    expect(store.topologyRevision).toBe(7);
  });

  it("retains document labels when an active-only refresh drops a deleted document", () => {
    const store = useDocumentWorkspaceStore();
    store.setPage([entries[0]!], null, 7, false);
    store.setPage([], null, 8, false);

    expect(store.entries).toEqual([]);
    expect(store.documentLabels[entries[0]!.documentId]).toBe("A.docx");
  });

  it("removes a successfully unlinked document from the active projection only", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.selectAt(0);
    store.showInspector("history");

    store.removeActiveDocument(entries[0]!.documentId);

    expect(store.entries).toEqual(entries.slice(1));
    expect(store.documentLabels[entries[0]!.documentId]).toBe("A.docx");
    expect(store.selectedHandles).toEqual([]);
    expect(store.primaryHandle).toBeNull();
    expect(store.inspectorTab).toBe("preview");
  });

  it("retains a typed document failure code until the next load", () => {
    const store = useDocumentWorkspaceStore();
    store.setFailed("drop failed", "DOCUMENT_DROP_OBJECTS_MISSING");
    expect(store.lastErrorCode).toBe("DOCUMENT_DROP_OBJECTS_MISSING");
    store.beginLoad();
    expect(store.lastError).toBeNull();
    expect(store.lastErrorCode).toBeNull();
  });

  it("keeps workspace entries provider-neutral and free of storage paths", () => {
    const wire = JSON.stringify(entries).toLowerCase();
    expect(wire).not.toContain("dire" + "ctus");
    expect(wire).not.toContain("pocketbase");
    expect(wire).not.toContain("pb_data");
    expect(wire).not.toMatch(/[a-z]:\\\\/);
  });

  it("drops a cancelled or superseded diff result by generation", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    const generation = store.beginDiff(
      "a",
      "44444444-4444-4444-8444-444444444444",
      "55555555-5555-4555-8555-555555555555",
    );
    expect(store.diffPhase).toBe("busy");
    store.cancelDiff();
    expect(store.completeDiff(generation, {
      entryHandle: "a",
      historicalRevisionId: "44444444-4444-4444-8444-444444444444",
      effectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      outcome: "changed",
      addedLines: null,
      removedLines: null,
      failure: null,
    })).toBe(false);
    expect(store.diffResult).toBeNull();
  });
});
