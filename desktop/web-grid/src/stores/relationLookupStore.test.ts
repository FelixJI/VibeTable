import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useRelationLookupStore } from "./relationLookupStore";
import type { RelationLookupCapabilities, SchemaSnapshot } from "@/contracts";

const capabilities: RelationLookupCapabilities = {
  contract: "vibetable.relation-capabilities.v1",
  relationReadV1: true,
  relationEditV1: true,
  lookupQueryV1: true,
};
const schema = (collection: string, revision = "s1"): SchemaSnapshot => ({
  collection,
  primaryKey: "id",
  columns: [],
  normalizedRelations: [],
  schemaRevision: revision,
  permissionRevision: "p1",
  capabilityHash: "c1",
  lookupRevision: "l1",
});

describe("relationLookupStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("rejects an old generation after a table switch", () => {
    const store = useRelationLookupStore();
    const old = store.beginContext("orders");
    const current = store.beginContext("contracts");
    expect(store.acceptContext(old, schema("orders"), [], capabilities)).toBe(false);
    expect(store.acceptContext(current, schema("contracts"), [], capabilities)).toBe(true);
    expect(store.schema?.collection).toBe("contracts");
  });

  it("rejects Lookup rows when any authority revision changed", () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    store.acceptContext(generation, schema("orders"), [], capabilities);
    expect(store.acceptLookup({
      contract: "vibetable.lookup-query.v1",
      collection: "orders",
      requestGeneration: generation,
      schemaRevision: "stale",
      permissionRevision: "p1",
      lookupRevision: "l1",
      columns: [], rows: [], groups: [], offset: 0, limit: 100, filteredRows: 0, totalRows: 0,
    })).toBe(false);
  });

  it("stages a multi-value selection without mutating the original", () => {
    const store = useRelationLookupStore();
    const first = { collection: "tags", itemId: "1", label: "A", junctionValues: {} };
    const second = { collection: "tags", itemId: "2", label: "B", junctionValues: {} };
    store.openDraft("articles.tags", "article-1", [first]);
    store.toggleDraftTarget(second);
    expect(store.draft?.original).toEqual([first]);
    expect(store.draft?.selected).toEqual([first, second]);
  });

  it("matches a searched target to an existing junction row by logical target", () => {
    const store = useRelationLookupStore();
    store.openDraft("articles.tags", "article-1", [{
      collection: "tags", itemId: "1", label: "A", junctionId: "junction-9", junctionValues: {},
    }]);
    store.toggleDraftTarget({ collection: "tags", itemId: "1", label: "A", junctionValues: {} });
    expect(store.draft?.selected).toEqual([]);
  });
});
