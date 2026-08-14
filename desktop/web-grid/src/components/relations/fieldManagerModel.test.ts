import { describe, expect, it } from "vitest";
import { lookupSourceOptions, resolveLookupPathCollection } from "./fieldManagerModel";
import type { LookupDefinition, NormalizedRelationDescriptor } from "@/contracts";

const relation = {
  relationId: "orders.contract", fieldRef: "orders.contract", sourceCollection: "orders",
  kind: "m2o", relatedCollection: "contracts", unique: false,
  nullable: true, onDelete: "nullify", preset: "standard", selfRelation: false,
  managed: true, state: "valid", diagnostics: [],
} satisfies NormalizedRelationDescriptor;
const targetLookup = {
  lookupId: "contracts.tax", collection: "contracts", fieldKey: "tax", displayName: "税额",
  path: [{ relationId: "contracts.tax_rate" }], source: { kind: "target_field", fieldRef: "rates.value" },
  outputType: "decimal", outputScale: 2,
  revision: 1, state: "valid", diagnostics: [], dependencies: [],
} satisfies LookupDefinition;

describe("fieldManagerModel", () => {
  it("resolves the final target collection and offers its Lookups, not root Lookups", () => {
    const target = resolveLookupPathCollection("orders", [{ relationId: "orders.contract" }], [relation]);
    expect(target).toBe("contracts");
    expect(lookupSourceOptions([targetLookup], target)).toEqual([
      { label: "税额 · tax", value: "contracts.tax" },
    ]);
    expect(lookupSourceOptions([targetLookup], "orders")).toEqual([]);
  });
});
