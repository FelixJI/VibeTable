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
      collection: null,
      offset: 0,
      limit: 50,
    });

    expect(request).toHaveBeenCalledWith("relation.searchTargets", {
      relationId: "orders.contract",
      collection: null,
      offset: 0,
      limit: 50,
    });
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
      target: { collection: "customers", itemId: "c-2", label: "Grace", junctionValues: {} },
      requestId: "create-1",
    });

    const result = await useRelationLookupService().createTarget(
      "orders.customer",
      "  Grace  ",
      "customers",
    );

    expect(result.target.label).toBe("Grace");
    expect(request).toHaveBeenCalledWith("relation.createTarget", {
      relationId: "orders.customer",
      label: "Grace",
      collection: "customers",
      idempotencyKey: expect.any(String),
    });
  });

  it("loads an authoritative Lookup dataset through backend-bounded pages", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    store.acceptContext(generation, {
      collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    request.mockImplementation(async (_method: string, payload: {
      requestGeneration: number;
      query: { offset: number; limit: number };
    }) => {
      const count = payload.query.offset === 0 ? 500 : 100;
      return {
        contract: "vibetable.lookup-query.v1",
        collection: "orders",
        requestGeneration: payload.requestGeneration,
        schemaRevision: "s",
        permissionRevision: "p",
        lookupRevision: "l",
        columns: [],
        rows: Array.from({ length: count }, (_, index) => ({
          rowKey: payload.query.offset + index,
        })),
        groups: [],
        offset: payload.query.offset,
        limit: payload.query.limit,
        filteredRows: 600,
        totalRows: 600,
        snapshot: {},
      };
    });

    const result = await useRelationLookupService().queryDataset({
      collection: "orders",
      fieldRefs: ["customer_name"],
      query: { filters: [], sorts: [], groups: [] },
    });

    expect(result.rows).toHaveLength(600);
    expect(request).toHaveBeenNthCalledWith(1, "lookup.query", expect.objectContaining({
      query: expect.objectContaining({ offset: 0, limit: 500 }),
    }));
    expect(request).toHaveBeenNthCalledWith(2, "lookup.query", expect.objectContaining({
      query: expect.objectContaining({ offset: 500, limit: 500 }),
    }));
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

  it("builds add/update/remove from a staged multi relation", () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("articles");
    store.acceptContext(generation, {
      collection: "articles", primaryKey: "id", columns: [], normalizedRelations: [],
      schemaRevision: "s", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
    });
    const removed = { collection: "tags", itemId: "1", label: "old", junctionId: "j1", junctionRevision: "a".repeat(64), junctionValues: {} };
    const updated = { collection: "tags", itemId: "2", label: "same", junctionId: "j2", junctionRevision: "b".repeat(64), junctionValues: { note: "a" } };
    const added = { collection: "tags", itemId: "3", label: "new", junctionValues: {} };
    store.openDraft("articles.tags", "a1", [removed, updated]);
    store.toggleDraftTarget(removed);
    store.toggleDraftTarget(added);
    store.patchDraftJunction(updated, { note: "b" });
    const delta = useRelationLookupService().buildDraftDelta();
    expect(delta.adds.map((item) => item.target.itemId)).toEqual(["3"]);
    expect(delta.removes.map((item) => item.target.itemId)).toEqual(["1"]);
    expect(delta.removes[0]?.expectedRevision).toBe("a".repeat(64));
    expect(delta.updates).toEqual([{
      junctionId: "j2",
      values: { note: "b" },
      expectedRevision: "b".repeat(64),
    }]);
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
      collection: "tags", itemId: "t1", label: "Tag 1", junctionId: "j1",
      junctionRevision: "a".repeat(64), junctionValues: { weight: 2 },
    }];
    request.mockResolvedValue({
      delta: {}, relationId: "articles.tags", sourceItemId: "a1",
      adds: 0, updates: 0, removes: 0, current, canApply: true,
      schemaRevision: "s", diagnostics: [],
    });

    await useRelationLookupService().loadDraft("articles.tags", "a1");

    expect(store.draft?.original).toEqual(current);
    expect(request).toHaveBeenCalledWith("relation.previewDelta", expect.objectContaining({
      relationId: "articles.tags", sourceItemId: "a1", adds: [], updates: [], removes: [],
    }));
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
      collection: "contracts", itemId: "contract-7", label: "CT-0007", junctionValues: {},
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

  it("invalidates an active deep Lookup for a change beyond the first-hop schema", async () => {
    const store = useRelationLookupStore();
    const generation = store.beginContext("orders");
    const snapshot: SchemaSnapshot = {
      collection: "orders", primaryKey: "id", columns: [],
      normalizedRelations: [{
        relationId: "orders.contract", fieldRef: "contract", sourceCollection: "orders", kind: "m2o",
        relatedCollection: "contracts", allowedCollections: [], unique: false, nullable: true,
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
      m2aFieldMapping: [], aggregation: "single", outputType: "text", revision: 1,
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
      contractVersion: "1.0", topic: "data.changed", eventId: "country-change",
      sequence: 7, occurredAt: "2026-07-24T08:30:00Z",
      schemaRevision: "schema_0007", dataRevision: "data_0007",
      changeSetId: "chg-country", tableId: "countries",
      recordIds: ["country-1"], operation: "update",
    });
    // loadContext clears the visible definitions while it renegotiates. A
    // second deeper event in that window must still invalidate the table.
    changed?.({
      contractVersion: "1.0", topic: "data.changed", eventId: "currency-change",
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
