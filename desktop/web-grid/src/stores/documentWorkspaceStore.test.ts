import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useDocumentWorkspaceStore, type DocumentEntry } from "./documentWorkspaceStore";

const entries: readonly DocumentEntry[] = [
  { entryHandle: "a", documentId: "1", displayName: "A.docx", authority: "workspace", availability: "available", capabilities: ["open", "preview", "reveal", "history"] },
  { entryHandle: "b", documentId: "2", displayName: "B.pdf", authority: "workspace", availability: "available", capabilities: ["open", "preview"] },
  { entryHandle: "c", documentId: "3", displayName: "C.xlsx", authority: "workspace", availability: "missing", capabilities: ["relink", "history"] },
  { entryHandle: "cloud", documentId: "4", displayName: "photo.png", authority: "cloud", availability: "remote", capabilities: ["preview"] },
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

  it("separates workspace documents from cloud attachments", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    expect(store.visibleEntries.map((entry) => entry.entryHandle)).toEqual(["a", "b", "c"]);
    store.setAuthorityFilter("cloud");
    expect(store.visibleEntries.map((entry) => entry.entryHandle)).toEqual(["cloud"]);
  });

  it("keeps stale entries visible while a refresh is in progress", () => {
    const store = useDocumentWorkspaceStore();
    store.setEntries(entries);
    store.beginLoad();
    expect(store.phase).toBe("loading");
    expect(store.entries).toHaveLength(4);
  });
});
