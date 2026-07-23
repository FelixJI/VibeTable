import assert from "node:assert/strict";
import test from "node:test";

import {
  applyRelationDeltaInTransaction,
  computeRelationSchemaProof,
  computeJunctionRevision,
  mapRelationDeltaError,
  RELATION_DELTA_CONTRACT,
  RelationDeltaConflictError,
  type RelationDeltaRequest,
  validateRelationDelta,
} from "../relation-delta.ts";

function liveSchema(kind: "o2m" | "m2m" | "m2a") {
  return {
    collections: {
      orders: { primary: "id", fields: { id: {}, date_updated: {} } },
      tags: { primary: "id", fields: { id: {}, order: {} } },
      headings: { primary: "id", fields: { id: {} } },
      videos: { primary: "id", fields: { id: {} } },
      order_tags: {
        primary: "id",
        fields: { id: {}, order: {}, item: {}, collection: {}, quantity: {} },
      },
    },
    relations: kind === "o2m"
      ? [{ collection: "tags", field: "order", related_collection: "orders", meta: null }]
      : [
          { collection: "order_tags", field: "order", related_collection: "orders", meta: null },
          kind === "m2m"
            ? { collection: "order_tags", field: "item", related_collection: "tags", meta: null }
            : {
                collection: "order_tags",
                field: "item",
                related_collection: null,
                meta: {
                  one_collection_field: "collection",
                  one_allowed_collections: ["videos", "headings"],
                },
              },
        ],
  };
}

function request(kind: "o2m" | "m2m" | "m2a"): RelationDeltaRequest {
  return {
    contract: RELATION_DELTA_CONTRACT,
    idempotencyKey: "request-1",
    expectedSchemaRevision: "schema-1",
    schemaProof: "c".repeat(64),
    relation: {
      relationId: "orders.tags",
      kind,
      sourceCollection: "orders",
      sourcePrimaryKey: "id",
      sourceItemId: "order-1",
      relatedCollection: kind === "m2a" ? undefined : "tags",
      relatedPrimaryKey: kind === "m2a" ? undefined : "id",
      manyField: kind === "o2m" ? "order" : undefined,
      allowedCollections: kind === "m2a" ? ["headings", "videos"] : undefined,
      targetPrimaryKeys: kind === "m2a" ? { headings: "id", videos: "id" } : undefined,
      junction: kind === "o2m" ? undefined : {
        collection: "order_tags",
        sourceField: "order",
        targetField: "item",
        collectionField: kind === "m2a" ? "collection" : undefined,
        contextFields: ["quantity"],
      },
      adds: [{ collection: kind === "m2a" ? "headings" : "tags", itemId: "target-1", junctionValues: kind === "o2m" ? undefined : { quantity: 2 } }],
      updates: kind === "o2m" ? [] : [{ junctionId: "junction-1", values: { quantity: 3 }, expectedRevision: "a".repeat(64) }],
      removes: [{ collection: kind === "m2a" ? "videos" : "tags", itemId: "target-2", junctionId: kind === "o2m" ? undefined : "junction-2", expectedRevision: kind === "o2m" ? undefined : "b".repeat(64) }],
    },
  };
}

for (const kind of ["o2m", "m2m", "m2a"] as const) {
  test(`accepts a bounded ${kind} relation delta`, () => {
    assert.deepEqual(validateRelationDelta(request(kind), "request-1"), { ok: true });
  });
}

test("accepts Python JSON nulls for inapplicable optional physical fields", () => {
  const value = request("m2m");
  value.relation.sourceDateUpdatedField = null;
  value.relation.expectedDateUpdated = null;
  value.relation.manyField = null;
  value.relation.junction!.collectionField = null;
  assert.deepEqual(validateRelationDelta(value), { ok: true });

  const o2m = request("o2m");
  o2m.relation.junction = null;
  assert.deepEqual(validateRelationDelta(o2m), { ok: true });
});

test("rejects undeclared junction context fields", () => {
  const value = request("m2m");
  (value.relation.adds[0]! as { junctionValues: Record<string, unknown> }).junctionValues = {
    admin_only: true,
  };
  assert.match(validateRelationDelta(value).error ?? "", /undeclared junction field/);
});

test("rejects M2A targets outside the explicit allow-list", () => {
  const value = request("m2a");
  value.relation.adds[0]!.collection = "secrets";
  assert.match(validateRelationDelta(value).error ?? "", /outside the relation allow-list/);
});

test("rejects header/body idempotency mismatch", () => {
  assert.match(validateRelationDelta(request("m2m"), "other-key").error ?? "", /does not match/);
});

test("requires a canonical relation schema proof", () => {
  const value = request("m2m") as Partial<RelationDeltaRequest>;
  delete value.schemaProof;
  assert.match(validateRelationDelta(value).error ?? "", /schemaProof/);
});

test("requires optimistic revisions for junction updates and removals", () => {
  const value = request("m2m");
  delete (value.relation.updates[0] as { expectedRevision?: string }).expectedRevision;
  assert.match(validateRelationDelta(value).error ?? "", /update expectedRevision/);

  const removal = request("m2a");
  delete (removal.relation.removes[0] as { expectedRevision?: string }).expectedRevision;
  assert.match(validateRelationDelta(removal).error ?? "", /remove expectedRevision/);
});

for (const kind of ["o2m", "m2m", "m2a"] as const) {
  test(`recomputes and verifies a canonical live ${kind} schema proof`, () => {
    const value = request(kind);
    const digest = computeRelationSchemaProof(value.relation, liveSchema(kind));
    assert.match(digest, /^[a-f0-9]{64}$/);
    value.schemaProof = digest;
    assert.deepEqual(validateRelationDelta(value), { ok: true });

    const drifted = structuredClone(liveSchema(kind));
    delete (drifted.collections.orders.fields as Record<string, unknown>).id;
    assert.throws(
      () => computeRelationSchemaProof(value.relation, drifted),
      (error: unknown) => error instanceof RelationDeltaConflictError,
    );
  });
}

test("relation schema proof matches the cross-language canonical payload", () => {
  const relation = request("m2m").relation;
  relation.sourceDateUpdatedField = "date_updated";
  relation.junction = { ...relation.junction!, targetField: "tag" };
  const schema = liveSchema("m2m");
  (schema.collections.order_tags.fields as Record<string, unknown>).tag = {};
  schema.relations[1]!.field = "tag";
  assert.equal(
    computeRelationSchemaProof(relation, schema),
    "e03aa90bbebdaefe8fab31f72330da9158a02de7286d8194c63da48e3230ec0c",
  );
});

test("maps a source date_updated mismatch to the sanitized 409 conflict", async () => {
  const value = request("o2m");
  value.relation.adds = [];
  value.relation.removes = [];
  value.relation.sourceDateUpdatedField = "date_updated";
  value.relation.expectedDateUpdated = "previewed";
  value.schemaProof = computeRelationSchemaProof(value.relation, liveSchema("o2m"));

  class ItemsService {
    public constructor(_collection: string, _options: Record<string, unknown>) {}
    public async readOne(): Promise<Record<string, unknown>> {
      return { id: "order-1", date_updated: "changed" };
    }
  }
  const trx = Object.assign(
    () => ({
      where: () => ({ update: async () => 1 }),
    }),
    { client: { config: { client: "sqlite3" } } },
  );
  const database = {
    async transaction(callback: (transaction: typeof trx) => Promise<void>): Promise<void> {
      await callback(trx);
    },
  };
  await assert.rejects(
    applyRelationDeltaInTransaction(
      ItemsService,
      liveSchema("o2m"),
      database,
      { user: "user-1" } as never,
      value,
    ),
    (error: unknown) => {
      const outcome = mapRelationDeltaError(error);
      return error instanceof RelationDeltaConflictError
        && outcome?.status === 409
        && JSON.stringify(outcome.body).includes("EDIT_CONFLICT");
    },
  );
});

test("junction revision matches the cross-language canonical payload", () => {
  const relation = request("m2m").relation;
  assert.equal(
    computeJunctionRevision(
      { id: "j1", order: "o1", item: "t1", quantity: 2 },
      { ...relation, sourceItemId: "o1" },
    ),
    "0218b8f7bac85ccdbc62269def9a7a31f237467244e7233c3e5d2caf2dea0b83",
  );
});

test("maps relation conflicts without leaking internals", () => {
  const outcome = mapRelationDeltaError(new RelationDeltaConflictError("private"));
  assert.equal(outcome?.status, 409);
  assert.deepEqual(outcome?.body, {
    errors: [{
      message: "relation changed since preview",
      extensions: { code: "EDIT_CONFLICT" },
    }],
  });
});
