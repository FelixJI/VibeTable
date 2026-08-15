import { describe, expect, it, vi } from "vitest";
import { buildColumns, type RelationLookupGridContext } from "./createGrid";
import type { LookupDefinition, NormalizedRelationDescriptor, TablePage } from "@/contracts";

const relation: NormalizedRelationDescriptor = {
  relationId: "orders.contract", fieldRef: "contract", sourceCollection: "orders", kind: "m2o",
  relatedCollection: "contracts", unique: true, nullable: true,
  onDelete: "nullify", preset: "standard", selfRelation: false, managed: true, state: "valid",
  displayTemplate: "{{number}}", diagnostics: [],
};
const lookup: LookupDefinition = {
  lookupId: "orders.price", collection: "orders", fieldKey: "price", displayName: "Price",
  path: [{ relationId: "orders.contract" }], source: { kind: "target_field", fieldRef: "price" },
  outputType: "decimal", outputScale: 2,
  revision: 1, state: "valid", diagnostics: [], dependencies: [],
};
const page: TablePage = {
  table: "orders",
  columns: [
    { name: "contract", title: "Contract", kind: "relation", relationId: relation.relationId, dataType: "text", editable: true, nullable: true },
    { name: "price", title: "Price", kind: "lookup", lookupId: lookup.lookupId, dataType: "decimal", editable: false, nullable: true },
  ],
  rows: [], offset: 0, limit: 50, totalRows: 0, mode: "remote",
};

describe("relation + Lookup grid integration", () => {
  it("routes relation double-click while keeping Lookup strictly read-only", () => {
    const onEdit = vi.fn();
    const context: RelationLookupGridContext = {
      relations: new Map([[relation.relationId, relation]]),
      lookups: new Map([[lookup.lookupId, lookup]]),
      relationEditAvailable: true,
      lookupQueryAvailable: true,
      onRelationEditRequested: onEdit,
    };
    const columns = buildColumns(page, null, context);
    expect(columns[0]?.editable).toBe(false);
    expect(columns[0]?.cellDblClick).toEqual(expect.any(Function));
    expect(columns[1]?.editable).toBe(false);
    expect(columns[1]?.editor).toBeUndefined();

    columns[0]?.cellDblClick?.(new MouseEvent("dblclick"), {
      getField: () => "contract",
      getValue: () => ({ itemId: "c1" }),
      setValue: () => undefined,
      getRow: () => ({ getData: () => ({ rowKey: "o1" }) }),
    });
    expect(onEdit).toHaveBeenCalledWith("o1", "contract", relation, { itemId: "c1" });
  });

  it("does not expose an edit gesture without negotiated relationEditV1", () => {
    const columns = buildColumns(page, null, {
      relations: new Map([[relation.relationId, relation]]),
      lookups: new Map([[lookup.lookupId, lookup]]),
      relationEditAvailable: false,
      lookupQueryAvailable: false,
      onRelationEditRequested: vi.fn(),
    });
    expect(columns[0]?.cellDblClick).toBeUndefined();
  });
});
