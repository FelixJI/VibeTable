import { describe, expect, it } from "vitest";
import { lookupFormatter, normalizeTargets, relationFormatter } from "./relationLookupRenderer";
import type { LookupDefinition, NormalizedRelationDescriptor } from "@/contracts";

const relation: NormalizedRelationDescriptor = {
  relationId: "content.blocks", fieldRef: "blocks", sourceCollection: "content", kind: "m2a",
  relatedCollection: null, allowedCollections: ["images", "videos"], junction: null,
  unique: false, nullable: true, onDelete: "nullify", preset: "standard", selfRelation: false,
  managed: true, state: "valid", displayTemplate: "{{title}}", diagnostics: [],
};
const lookup: LookupDefinition = {
  lookupId: "orders.price", collection: "orders", fieldKey: "price", displayName: "Price",
  path: [{ relationId: "orders.contract" }], source: { kind: "target_field", fieldRef: "price" },
  m2aFieldMapping: [], aggregation: "single", outputType: "decimal", outputScale: 2,
  revision: 1, state: "valid", diagnostics: [], dependencies: [],
};

describe("relation / Lookup grid renderers", () => {
  it("labels M2A values with their target collection", () => {
    const node = relationFormatter(relation)({ getValue: () => ({
      collection: "videos", itemId: "v1", label: "Launch", junctionValues: {},
    }) });
    expect(node.textContent).toContain("videos");
    expect(node.textContent).toContain("Launch");
  });

  it.each([
    ["restricted", "受限"],
    ["invalid", "无效"],
    ["too_expensive", "超出预算"],
  ] as const)("renders %s explicitly", (state, label) => {
    const node = lookupFormatter(lookup, true)({
      getValue: () => ({ state, value: null, provenance: [], diagnostic: { code: state, message: "detail" } }),
    });
    expect(node.textContent).toContain(label);
    expect(node.querySelector(".vt-lookup-state")?.getAttribute("title")).toBe("detail");
  });

  it("renders a missing related source as a per-cell #REF diagnostic", () => {
    const node = lookupFormatter(lookup, true)({
      getValue: () => ({
        state: "invalid",
        value: null,
        provenance: [],
        diagnostic: {
          code: "lookup.value.source_missing",
          message: "关联的来源记录已不存在",
        },
      }),
    });

    expect(node.textContent).toContain("#REF!");
    expect(node.querySelector(".vt-lookup-state")?.getAttribute("title"))
      .toBe("关联的来源记录已不存在");
  });

  it("shows extension absence instead of computing a page-local fallback", () => {
    const node = lookupFormatter(lookup, false, "Lookup 扩展未安装")({ getValue: () => 99 });
    expect(node.textContent).toContain("不可用");
    expect(node.textContent).not.toContain("99");
  });

  it("offers a source-navigation action for authoritative provenance", () => {
    const navigated: Array<{ collection: string; itemId: string }> = [];
    const node = lookupFormatter(lookup, true, null, (source) => {
      navigated.push({ collection: source.collection, itemId: source.itemId });
    })({
      getValue: () => ({
        state: "ok",
        value: "99.00",
        provenance: [
          { collection: "contracts", collectionLabel: "合同", itemId: "c1", recordLabel: "CT-001", fieldId: "fld_amount", fieldLabel: "金额", value: "99.00" },
          { collection: "contracts", collectionLabel: "合同", itemId: "c2", recordLabel: "CT-002", fieldId: "fld_amount", fieldLabel: "金额", value: "101.00" },
        ],
      }),
    });
    const buttons = node.querySelectorAll<HTMLButtonElement>(".vt-lookup-source");
    expect(buttons).toHaveLength(2);
    expect(node.textContent).toContain("合同 · CT-001");
    expect(node.textContent).not.toContain("contracts");
    expect(node.textContent).not.toContain("c1");
    buttons[1]?.click();
    expect(navigated).toEqual([{ collection: "contracts", itemId: "c2" }]);
  });

  it("opens the paged source browser from the remainder action", () => {
    const requested: unknown[] = [];
    const provenance = [
      { collection: "contracts", collectionLabel: "合同", itemId: "c1", recordLabel: "CT-001", fieldId: "fld_amount", fieldLabel: "金额", value: 99 },
    ];
    const node = lookupFormatter(lookup, true, null, undefined, (intent) => {
      requested.push(intent);
    })({
      getValue: () => ({
        state: "ok", value: [99], provenance, provenanceTotal: 10_001,
        provenanceTotalKnown: true,
        provenanceOffset: 0, provenanceLimit: 100, provenanceHasMore: true,
      }),
      getRow: () => ({ getData: () => ({ rowKey: "order-1" }) }),
    });
    const more = node.querySelector<HTMLButtonElement>(".vt-lookup-source-more");
    expect(more?.textContent).toBe("+9998");
    more?.click();
    expect(requested).toEqual([expect.objectContaining({
      sourceRecordId: "order-1", fieldRef: "price",
    })]);
  });

  it("marks an early-stopped multi-hop source count as a lower bound", () => {
    const node = lookupFormatter(lookup, true)({
      getValue: () => ({
        state: "ok",
        value: [99, 101, 103],
        provenance: [
          { collection: "contracts", collectionLabel: "合同", itemId: "c1", recordLabel: "CT-001", fieldId: "fld_amount", fieldLabel: "金额", value: 99 },
          { collection: "contracts", collectionLabel: "合同", itemId: "c2", recordLabel: "CT-002", fieldId: "fld_amount", fieldLabel: "金额", value: 101 },
          { collection: "contracts", collectionLabel: "合同", itemId: "c3", recordLabel: "CT-003", fieldId: "fld_amount", fieldLabel: "金额", value: 103 },
        ],
        provenanceTotal: 4,
        provenanceTotalKnown: false,
        provenanceOffset: 0,
        provenanceLimit: 3,
        provenanceHasMore: true,
      }),
    });
    expect(node.title).toContain("4+");
    expect(node.querySelector(".vt-lookup-source-more")?.textContent).toBe("…");
  });

  it("preserves optimistic junction revisions while normalizing relation values", () => {
    const revision = "b".repeat(64);
    expect(normalizeTargets({
      collection: "tags", itemId: "t1", label: "Tag 1", junctionId: "j1",
      junctionRevision: revision, junctionValues: { weight: 2 },
    })[0]?.junctionRevision).toBe(revision);
  });
});
