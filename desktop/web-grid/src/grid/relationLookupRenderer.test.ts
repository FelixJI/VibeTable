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

  it("shows extension absence instead of computing a page-local fallback", () => {
    const node = lookupFormatter(lookup, false, "Lookup 扩展未安装")({ getValue: () => 99 });
    expect(node.textContent).toContain("不可用");
    expect(node.textContent).not.toContain("99");
  });

  it("preserves optimistic junction revisions while normalizing relation values", () => {
    const revision = "b".repeat(64);
    expect(normalizeTargets({
      collection: "tags", itemId: "t1", label: "Tag 1", junctionId: "j1",
      junctionRevision: revision, junctionValues: { weight: 2 },
    })[0]?.junctionRevision).toBe(revision);
  });
});
