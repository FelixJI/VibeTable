import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useRelationLookupService } from "./relationLookupService";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import type { DataChangedEvent, LookupDefinition, SchemaSnapshot } from "@/contracts";

describe("relationLookupService", () => {
  const request = vi.fn();
  beforeEach(() => {
    setActivePinia(createPinia());
    request.mockReset();
    setHostBridgeForTesting({ request } as unknown as HostBridge);
  });

  it("does not issue lookup.query when authoritative capability is absent", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    store.acceptContext(generation, {
      collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: false, reason: "extension_missing",
    });
    const service = useRelationLookupService();
    await expect(service.queryLookups({
      collection: "orders", fieldRefs: ["orders.price"],
      query: { filters: [], sorts: [], groups: [], offset: 0, limit: 100 },
    })).rejects.toThrow("Lookup 扩展未安装");
    expect(request).not.toHaveBeenCalled();
  });

  it("omits a blank optional relation search query instead of sending invalid params", async () => {
    request.mockResolvedValue({ items: [], total: 0 });

    await useRelationLookupService().searchTargets({
      relationId: "orders.contract",
      query: "",
      offset: 0,
      limit: 50,
    });

    expect(request).toHaveBeenCalledWith("relation.searchTargets", {
      relationId: "orders.contract",
      offset: 0,
      limit: 50,
    });
  });

  it("sends a visual-label query so complete creation can find records beyond its initial page", async () => {
    request.mockResolvedValue({
      items: [{
        collection: "regions", itemId: "region-51", label: "华北仓",
      }],
      total: 1,
    });

    const result = await useRelationLookupService().searchTargets({
      relationId: "customers.region",
      query: "华北仓",
      offset: 0,
      limit: 50,
    });

    expect(request).toHaveBeenCalledWith("relation.searchTargets", {
      relationId: "customers.region",
      query: "华北仓",
      offset: 0,
      limit: 50,
    });
    expect(result.items[0]?.itemId).toBe("region-51");
  });

  it("creates a relation target from its visual label without exposing row or field ids", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    store.acceptContext(generation, {
      collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    request.mockResolvedValue({
      outcome: "committed",
      target: { collection: "customers", itemId: "c-2", label: "Grace" },
      requestId: "create-1",
    });

    const result = await useRelationLookupService().createTarget(
      "orders.customer",
      "  Grace  ",
    );

    expect(result.target.label).toBe("Grace");
    expect(request).toHaveBeenCalledWith("relation.createTarget", {
      relationId: "orders.customer",
      label: "Grace",
      idempotencyKey: expect.any(String),
    });
  });

	it("binds source-value pages to the active schema and lookup revisions", async () => {
		const store = useRelationLookupStore();
		const generation = store.beginContext("orders");
		store.acceptContext(generation, {
			collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
			schemaRevision: "schema_7", permissionRevision: "permission_7",
			capabilityHash: "c", lookupRevision: "lookup_7",
		}, [], {
			contract: "vibetable.relation-capabilities.v1",
			relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
		});
		request.mockResolvedValue({
			state: "ok", value: [2], provenance: [], provenanceTotal: 10_001,
			provenanceTotalKnown: true,
			provenanceOffset: 100, provenanceLimit: 100, provenanceHasMore: true,
		});

		await useRelationLookupService().readLookupValuePage({
			fieldRef: "line_skus", sourceRecordId: "order-1", offset: 100, limit: 100,
		});

		expect(request).toHaveBeenCalledWith("lookup.valuePage", {
			collection: "orders", fieldRef: "line_skus", sourceRecordId: "order-1",
			offset: 100, limit: 100, schemaRevision: "schema_7",
			permissionRevision: "permission_7", lookupRevision: "lookup_7",
		});
	});

  it("builds add/remove from a staged direct multi relation", () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("articles");
    store.acceptContext(generation, {
      collection: "articles", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    const removed = { collection: "tags", itemId: "1", label: "old" };
    const retained = { collection: "tags", itemId: "2", label: "same" };
    const added = { collection: "tags", itemId: "3", label: "new" };
    store.openDraft("articles.tags", "a1", [removed, retained]);
    store.toggleDraftTarget(removed);
    store.toggleDraftTarget(added);
    const delta = useRelationLookupService().buildDraftDelta();
    expect(delta.adds.map((item) => item.target.itemId)).toEqual(["3"]);
    expect(delta.removes.map((item) => item.target.itemId)).toEqual(["1"]);
  });

  it("hydrates a multi-relation draft from the authoritative backend preview", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("articles");
    store.acceptContext(generation, {
      collection: "articles", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    const current = [{
      collection: "tags", itemId: "t1", label: "Tag 1",
    }];
    request.mockResolvedValue({
      delta: {}, relationId: "articles.tags", sourceItemId: "a1",
      adds: 0, updates: 0, removes: 0, current, canApply: true,
      schemaRevision: "s", diagnostics: [],
    });

    await useRelationLookupService().loadDraft("articles.tags", "a1");

    expect(store.draft?.original).toEqual(current);
    expect(request).toHaveBeenCalledWith("relation.previewDelta", expect.objectContaining({
      relationId: "articles.tags", sourceItemId: "a1", adds: [], removes: [],
    }));
  });

  it("does not publish an authoritative draft after the editor epoch is invalidated", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("articles");
    store.acceptContext(generation, {
      collection: "articles", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    request.mockResolvedValue({
      delta: {}, relationId: "articles.tags", sourceItemId: "a1",
      adds: 0, updates: 0, removes: 0,
      current: [{ collection: "tags", itemId: "t1", label: "Tag 1" }],
      canApply: true, schemaRevision: "s", diagnostics: [],
    });

    await useRelationLookupService().loadDraft(
      "articles.tags",
      "a1",
      undefined,
      () => false,
    );

    expect(store.draft).toBeNull();
  });

  it("loads Lookup candidates from a path target collection", async () => {
    request.mockResolvedValue({ collection: "contracts", definitions: [], lookupRevision: "target-l" });
    const result = await useRelationLookupService().listCollectionLookups("contracts");
    expect(request).toHaveBeenCalledWith("lookup.list", { collection: "contracts" });
    expect(result.lookupRevision).toBe("target-l");
  });

  it("returns the scalar current target from relation.updateSingle", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    store.acceptContext(generation, {
      collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    const target = {
      collection: "contracts", itemId: "contract-7", label: "CT-0007",
    };
    request.mockResolvedValue({
      outcome: "committed", current: target, schemaRevision: "s", requestId: "update-1",
    });

    const result = await useRelationLookupService().updateSingle("orders.contract", "order-1", target);

    expect(result.current).toEqual(target);
    expect(Array.isArray(result.current)).toBe(false);
    expect(request).toHaveBeenCalledWith("relation.updateSingle", expect.objectContaining({
      relationId: "orders.contract", sourceItemId: "order-1", target,
    }));
  });

  it("attaches a record created in the target table using the captured source revision", async () => {
    const target = {
      collection: "customers", itemId: "customer-9", label: "Ada",
    };
    request.mockResolvedValueOnce({
      delta: {}, relationId: "orders.customers", sourceItemId: "order-1",
      adds: 1, updates: 0, removes: 0, current: [], canApply: true,
      schemaRevision: "schema-source", diagnostics: [],
    }).mockResolvedValueOnce({
      outcome: "committed", current: [target], schemaRevision: "schema-source",
      requestId: "attach-1",
    });

    const result = await useRelationLookupService().attachExistingTarget(
      "orders.customers", "order-1", target, "m2m", "schema-source",
    );

    expect(result.outcome).toBe("committed");
    expect(request).toHaveBeenNthCalledWith(1, "relation.previewDelta", expect.objectContaining({
      relationId: "orders.customers",
      sourceItemId: "order-1",
      expectedSchemaRevision: "schema-source",
      adds: [{ target }],
    }));
    expect(request).toHaveBeenNthCalledWith(2, "relation.applyDelta", expect.objectContaining({
      expectedSchemaRevision: "schema-source",
      adds: [{ target }],
    }));
  });

  it("invalidates an active deep Lookup for a change beyond the first-hop schema", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    const snapshot: SchemaSnapshot = {
      collection: "orders", primaryKey: "id", columns: [],
      normalizedRelations: [{
        relationId: "orders.contract", fieldRef: "contract", sourceCollection: "orders", kind: "m2o",
        relatedCollection: "contracts", unique: false, nullable: true,
        onDelete: "nullify", preset: "standard", selfRelation: false, managed: true, state: "valid",
        diagnostics: [],
      }],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    };
    const deepLookup: LookupDefinition = {
      lookupId: "orders.country_currency", collection: "orders", fieldKey: "country_currency",
      displayName: "Country currency",
      path: [
        { relationId: "orders.contract" },
        { relationId: "contracts.customer" },
        { relationId: "customers.country" },
      ],
      source: { kind: "target_field", fieldRef: "countries.currency" },
      outputType: "text", revision: 1,
      state: "valid", diagnostics: [], dependencies: [],
    };
    store.acceptContext(generation, snapshot, [deepLookup], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    let changed: ((change: DataChangedEvent) => void) | undefined;
    const invalidated = vi.fn();
    request.mockImplementation(async (method: string, payload: unknown) => {
      if (method === "schema.describe") {
        return {
          contract: "vibetable.schema-describe.v1",
          collection: "orders",
          requestGeneration: (payload as { requestGeneration: number }).requestGeneration,
          schema: snapshot,
          capabilities: {
            contract: "vibetable.relation-capabilities.v1",
            relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
          },
        };
      }
      if (method === "lookup.list") {
        return { collection: "orders", definitions: [deepLookup], lookupRevision: "l" };
      }
      throw new Error(`unexpected request: ${method}`);
    });
    setHostBridgeForTesting({
      request,
      on: vi.fn((_type, handler) => {
        expect(_type).toBe("data.changed");
        changed = handler as (change: DataChangedEvent) => void;
        return vi.fn();
      }),
    } as unknown as HostBridge);
    const service = useRelationLookupService();
    service.init(invalidated);

    changed?.({
      contractVersion: "2.0", topic: "data.changed", eventId: "country-change",
      sequence: 7, occurredAt: "2026-07-24T08:30:00Z",
      schemaRevision: "schema_0007", dataRevision: "data_0007",
      changeSetId: "chg-country", tableId: "countries",
      recordIds: ["country-1"], operation: "update",
    });
    // loadContext clears the visible definitions while it renegotiates. A
    // second deeper event in that window must still invalidate the table.
    changed?.({
      contractVersion: "2.0", topic: "data.changed", eventId: "currency-change",
      sequence: 8, occurredAt: "2026-07-24T08:31:00Z",
      schemaRevision: "schema_0007", dataRevision: "data_0008",
      changeSetId: "chg-currency", tableId: "currencies",
      recordIds: ["currency-1"], operation: "update",
    });

    expect(invalidated).toHaveBeenCalledTimes(2);
    await vi.waitFor(() => expect(request).toHaveBeenCalledWith(
      "schema.describe",
      expect.objectContaining({ collection: "orders" }),
    ));
    service.dispose();
  });
});
