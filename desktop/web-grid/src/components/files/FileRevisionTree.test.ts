import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import FileRevisionTree from "./FileRevisionTree.vue";
import { parseDocumentDiffSessionResult, type DocumentDiffChange } from "@/contracts/documentDiffV2";
import type { FileRevisionV2 } from "@/contracts/workspaceV2";

const documentId = "22222222-2222-4222-8222-222222222222";
const base = {
  contractVersion: "2.0",
  documentId,
  objectId: `sha256:${"1".repeat(64)}`,
  contentHash: `sha256:${"2".repeat(64)}`,
  size: 100,
  mimeType: "text/plain",
  createdBy: "device A",
  deviceId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  comment: null,
  localSequence: null,
  restoredFromRevisionId: null,
} as const;
const revisions: readonly FileRevisionV2[] = [
  {
    ...base,
    revisionId: "33333333-3333-4333-8333-333333333333",
    parentRevisionId: null,
    revisionOrdinal: 1,
    formalVersion: 1,
    kind: "formal",
    createdAt: "2026-07-28T08:00:00Z",
  },
  {
    ...base,
    revisionId: "44444444-4444-4444-8444-444444444444",
    parentRevisionId: "33333333-3333-4333-8333-333333333333",
    revisionOrdinal: 2,
    formalVersion: null,
    kind: "autosave",
    createdAt: "2026-07-28T09:00:00Z",
  },
  {
    ...base,
    revisionId: "55555555-5555-4555-8555-555555555555",
    parentRevisionId: "44444444-4444-4444-8444-444444444444",
    revisionOrdinal: 3,
    formalVersion: 2,
    kind: "formal",
    createdAt: "2026-07-28T10:00:00Z",
  },
];

describe("FileRevisionTree", () => {
  it("renders escaped before-after context and partial coverage with page and close actions", async () => {
    const result = parseDocumentDiffSessionResult({ outcome: "ready", failure: null, session: {
      contractVersion: "2.0", sessionId: "33333333-3333-4333-8333-333333333333",
      entryHandle: "entry", historicalRevisionId: "11111111-1111-4111-8111-111111111111",
      effectiveRevisionId: "22222222-2222-4222-8222-222222222222",
      format: "text", provider: "builtIn", fidelity: "structural",
      summary: { totalChangeGroups: 1, rawRevisionCount: 1, insertions: 0, deletions: 0, replacements: 1,
        moves: 0, formattingChanges: 0, tableChanges: 0, commentChanges: 0, otherChanges: 0 },
      coverage: { truncated: false, areas: [{ area: "visibleText", status: "partial" }] },
      warnings: ["partialCoverage"], canOpenComparisonArtifact: false, canExportComparisonArtifact: false,
    } });
    const run = { text: "<img src=x onerror=alert(1)>", role: "deleted" as const, bold: null,
      italic: null, underline: null, strike: null, fontSizePt: null, fontFamily: null,
      foreground: null, background: null, styleName: null };
    const change: DocumentDiffChange = { changeId: "44444444-4444-4444-8444-444444444444", kind: "replace",
      location: { part: "body", paragraphIndex: 0, sectionIndex: null, nearestHeading: null,
        tableIndex: null, rowIndex: null, columnIndex: null, sheetName: null, cellAddress: null },
      before: { runs: [run] }, after: { runs: [{ ...run, text: "after", role: "inserted" }] }, confidence: "exact" };
    const wrapper = mount(FileRevisionTree, { props: { tree: null, busy: false,
      diffPhase: "ready", diffResult: result, diffChanges: [change], diffNextCursor: "opaque" } });
    expect(wrapper.text()).toContain(run.text);
    expect(wrapper.find("img").exists()).toBe(false);
    expect(wrapper.text()).toContain("仅部分内容被覆盖");
    await wrapper.get('[data-testid="diff-next-page"]').trigger("click");
    await wrapper.get('[data-testid="diff-close"]').trigger("click");
    expect(wrapper.emitted("nextPage")).toHaveLength(1);
    expect(wrapper.emitted("closeCompare")).toHaveLength(1);
  });

  beforeEach(() => setActivePinia(createPinia()));

  it("renders an ARIA tree, collapses autosaves, and marks the effective path", () => {
    const wrapper = mount(FileRevisionTree, {
      props: {
        tree: {
          documentId: "opaque-document",
          effectiveRevisionId: revisions[2]!.revisionId,
          revisions,
        },
        busy: false,
      },
    });
    const rows = wrapper.findAll('[role="treeitem"]');
    expect(wrapper.find('[role="tree"]').exists()).toBe(true);
    expect(rows).toHaveLength(2);
    expect(rows[1]?.attributes("aria-current")).toBe("true");
    expect(wrapper.text()).not.toContain("r2");
  });

  it("supports roving focus with arrow keys", async () => {
    const wrapper = mount(FileRevisionTree, {
      attachTo: document.body,
      props: {
        tree: {
          documentId: "opaque-document",
          effectiveRevisionId: revisions[2]!.revisionId,
          revisions,
        },
        busy: false,
      },
    });
    const first = wrapper.findAll<HTMLElement>('[role="treeitem"]')[0]!;
    await first.trigger("focus");
    await first.trigger("keydown", { key: "ArrowRight" });
    expect(document.activeElement?.getAttribute("data-revision-id")).toBe(revisions[2]!.revisionId);
    await wrapper.findAll<HTMLElement>('[role="treeitem"]')[1]!.trigger("keydown", { key: "ArrowLeft" });
    await wrapper.vm.$nextTick();
    expect(document.activeElement?.getAttribute("data-revision-id")).toBe(revisions[0]!.revisionId);
    await first.trigger("keydown", { key: "ArrowLeft" });
    expect(wrapper.findAll('[role="treeitem"]')).toHaveLength(1);
    await first.trigger("keydown", { key: "ArrowRight" });
    expect(wrapper.findAll('[role="treeitem"]')).toHaveLength(2);
    wrapper.unmount();
  });

  it("reveals and focuses the exact historical revision requested by search", async () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    const wrapper = mount(FileRevisionTree, {
      attachTo: document.body,
      props: {
        tree: {
          documentId: "opaque-document",
          effectiveRevisionId: revisions[2]!.revisionId,
          revisions,
        },
        busy: false,
        requestedRevisionId: revisions[1]!.revisionId,
      },
    });
    await wrapper.vm.$nextTick();

    const requested = wrapper.get(`[data-revision-id="${revisions[1]!.revisionId}"]`);
    expect(requested.classes()).toContain("requested");
    expect(document.activeElement).toBe(requested.element);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
    wrapper.unmount();
  });

  it("labels provisional revisions without claiming a canonical ordinal or Vn", () => {
    const provisional: FileRevisionV2 = {
      ...base,
      revisionId: "66666666-6666-4666-8666-666666666666",
      parentRevisionId: revisions[2]!.revisionId,
      revisionOrdinal: 0,
      localSequence: 8,
      formalVersion: null,
      kind: "autosave",
      createdAt: "2026-07-28T11:00:00Z",
    };
    const wrapper = mount(FileRevisionTree, {
      props: {
        tree: {
          documentId: "opaque-document",
          effectiveRevisionId: provisional.revisionId,
          revisions: [...revisions, provisional],
        },
        busy: false,
      },
    });

    expect(wrapper.text()).toContain("p8");
    expect(wrapper.text()).toContain("待接纳");
    expect(wrapper.text()).not.toContain("r0");
    const row = wrapper.get(
      `[data-revision-id="${provisional.revisionId}"]`,
    );
    expect(row.findAll(".tree-actions button")).toHaveLength(0);
  });

  it("keeps a non-effective provisional conflict leaf visible by default", () => {
    const provisional: FileRevisionV2 = {
      ...base,
      revisionId: "77777777-7777-4777-8777-777777777777",
      parentRevisionId: revisions[1]!.revisionId,
      revisionOrdinal: 0,
      localSequence: 9,
      formalVersion: null,
      kind: "autosave",
      createdAt: "2026-07-28T11:00:00Z",
    };
    const wrapper = mount(FileRevisionTree, {
      props: {
        tree: {
          documentId: "opaque-document",
          effectiveRevisionId: revisions[2]!.revisionId,
          revisions: [...revisions, provisional],
        },
        busy: false,
      },
    });

    expect(wrapper.text()).toContain("p9");
    expect(wrapper.text()).toContain("待接纳");
    expect(wrapper.get(
      `[data-revision-id="${provisional.revisionId}"]`,
    ).findAll(".tree-actions button")).toHaveLength(0);
  });

  it("offers a closed compare action only for non-effective revisions", async () => {
    const wrapper = mount(FileRevisionTree, {
      props: {
        tree: {
          documentId: "opaque-document",
          effectiveRevisionId: revisions[2]!.revisionId,
          revisions,
        },
        busy: false,
        canCompare: true,
      },
    });
    const compare = wrapper.get('[data-testid="compare-revision"]');
    expect(compare.text()).toBe("查看变化摘要");
    await compare.trigger("click");
    expect(wrapper.emitted("compare")?.[0]?.[0]).toMatchObject({
      revisionId: revisions[0]!.revisionId,
    });
    expect(wrapper.findAll('[data-testid="compare-revision"]')).toHaveLength(1);
  });
});
