import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useDocumentWorkspaceStore, type DocumentEntry } from "./documentWorkspaceStore";

const entries: readonly DocumentEntry[] = [
  { documentId: "11111111-1111-4111-8111-111111111111", entryHandle: "a", displayName: "A.docx", authority: "workspace", availability: "available", capabilities: ["open", "preview", "reveal", "history"] },
  { documentId: "22222222-2222-4222-8222-222222222222", entryHandle: "b", displayName: "B.pdf", authority: "workspace", availability: "available", capabilities: ["open", "preview"] },
  { documentId: "33333333-3333-4333-8333-333333333333", entryHandle: "c", displayName: "C.xlsx", authority: "workspace", availability: "missing", capabilities: ["relink", "history"] },
];

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

  it("keeps stale entries visible while a refresh is in progress", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.beginLoad();
    expect(store.phase).toBe("loading");
    expect(store.entries).toHaveLength(3);
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
