import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { CONTRACT, type LookupQueryPlan, type RelationStep } from "../contracts.ts";
import { LookupQueryError } from "../errors.ts";
import { dependencyOrder, validatePlan } from "../validation.ts";

function relation(kind: RelationStep["kind"], overrides: Partial<RelationStep> = {}): RelationStep {
  return {
    relationId: `relation-${kind}`,
    kind,
    fromCollection: "orders",
    toCollection: kind === "m2a" ? undefined : "targets",
    sourceField: "id",
    ...(kind === "m2a" ? {} : { targetField: "id" }),
    ...(kind === "o2m" ? { destinationPrimaryKey: "id" } : {}),
    ...(kind === "m2m" || kind === "m2a"
      ? {
          junction: {
            collection: "orders_targets",
            sourceField: "order_id",
            targetField: "target_id",
            ...(kind === "m2a" ? { collectionField: "target_collection" } : {}),
          },
        }
      : {}),
    ...(kind === "m2a"
      ? { targetCollections: ["notes", "assets"], targetPrimaryKeys: { notes: "id", assets: "asset_id" } }
      : {}),
    ...overrides,
  };
}

function plan(overrides: Partial<LookupQueryPlan> = {}): LookupQueryPlan {
  return {
    contract: CONTRACT,
    generation: 7,
    collection: "orders",
    primaryKey: "id",
    revisions: { schema: "schema-1", permission: "permission-1", lookup: "lookup-1" },
    baseFields: [{ ref: "order.number", field: "number", outputType: { kind: "string" } }],
    lookups: [{
      lookupId: "lookup-total",
      ref: "lookup.total",
      path: [relation("o2m", { toCollection: "lines", targetField: "order_id", destinationPrimaryKey: "id" })],
      source: { kind: "field", field: "amount" },
      aggregate: "sum",
      outputType: { kind: "decimal", scale: 2 },
    }],
    page: { offset: 0, limit: 100 },
    ...overrides,
  };
}

describe("strict lookup plan validation", () => {
  it("requires the real destination primary key for O2M provenance", () => {
    const step = relation("o2m");
    delete step.destinationPrimaryKey;
    assert.throws(
      () => validatePlan(plan({ lookups: [{ ...plan().lookups[0]!, path: [step] }] })),
      /destinationPrimaryKey/,
    );
  });

  it("accepts explicit M2O, O2M, M2M, M2A and junction sources", () => {
    for (const kind of ["m2o", "o2m", "m2m", "m2a"] as const) {
      const step = relation(kind);
      const source = kind === "m2a"
        ? { kind: "m2a" as const, fields: { notes: "title", assets: "filename" } }
        : kind === "m2m"
          ? { kind: "junction" as const, step: 0, field: "quantity" }
          : { kind: "field" as const, field: "value" };
      const aggregate = kind === "m2o" ? "scalar" as const : "list" as const;
      assert.doesNotThrow(() => validatePlan(plan({
        lookups: [{ lookupId: `lookup-${kind}`, ref: `lookup.${kind}`, path: [step], source, aggregate, outputType: { kind: "string" } }],
      })));
    }
  });

  it("continues through an explicitly selected M2A branch and rejects an ambiguous branch", () => {
    const continued = {
        lookupId: "lookup-m2a",
        ref: "lookup.m2a",
        path: [
          relation("m2a", { toCollection: "notes" }),
          relation("m2o", { fromCollection: "notes", toCollection: "authors", sourceField: "author_id" }),
        ],
        source: { kind: "field", field: "value" },
        aggregate: "list",
        outputType: { kind: "string" },
      } as const;
    assert.doesNotThrow(() => validatePlan(plan({ lookups: [continued] })));
    assert.throws(
      () => validatePlan(plan({ lookups: [{ ...continued, path: [relation("m2a"), continued.path[1]] }] })),
      /toCollection is required/,
    );
  });

  it("rejects direct and indirect lookup dependency cycles", () => {
    const dependency = (lookupId: string, sourceId: string) => ({
      lookupId,
      ref: `ref.${lookupId}`,
      path: [],
      source: { kind: "lookup" as const, lookupId: sourceId },
      aggregate: "scalar" as const,
      outputType: { kind: "string" as const },
    });
    assert.throws(
      () => validatePlan(plan({ lookups: [dependency("a", "b"), dependency("b", "a")] })),
      /cycle/i,
    );
  });

  it("orders acyclic lookup dependencies before consumers", () => {
    const base = plan().lookups[0]!;
    const derived = {
      lookupId: "lookup-copy",
      ref: "lookup.copy",
      path: [],
      source: { kind: "lookup" as const, lookupId: base.lookupId },
      aggregate: "scalar" as const,
      outputType: { kind: "decimal" as const, scale: 2 },
    };
    const input = plan({ lookups: [derived, base] });
    validatePlan(input);
    assert.deepEqual(dependencyOrder(input.lookups).map((lookup) => lookup.lookupId), [base.lookupId, derived.lookupId]);
  });

  it("allows a relation path to terminate in a Lookup on the selected target collection", () => {
    const dependency = {
      lookupId: "contract-net",
      ref: "internal.contract-net",
      collection: "contracts",
      primaryKey: "id",
      expose: false,
      path: [],
      source: { kind: "field" as const, field: "net" },
      aggregate: "scalar" as const,
      outputType: { kind: "decimal" as const, scale: 2 },
    };
    const consumer = {
      lookupId: "order-contract-net",
      ref: "lookup.contract-net",
      path: [relation("m2o", { sourceField: "contract_id", targetField: "id", toCollection: "contracts" })],
      source: { kind: "lookup" as const, lookupId: dependency.lookupId },
      aggregate: "scalar" as const,
      outputType: { kind: "decimal" as const, scale: 2 },
    };
    assert.doesNotThrow(() => validatePlan(plan({ lookups: [consumer, dependency] })));
  });

  it("does not impose a logical hop limit and permits finite self-relation paths", () => {
    const steps = Array.from({ length: 129 }, (_, index) => relation("m2o", {
      relationId: `self-${index}`,
      fromCollection: "orders",
      toCollection: "orders",
      sourceField: "parent_id",
      targetField: "id",
    }));
    assert.doesNotThrow(() => validatePlan(plan({ lookups: [{
      lookupId: "ancestor-value",
      ref: "lookup.ancestor",
      path: steps,
      source: { kind: "field", field: "value" },
      aggregate: "scalar",
      outputType: { kind: "string" },
    }] })));
  });

  it("rejects unsafe identifiers, protected system collections and raising-shaped hints", () => {
    assert.throws(() => validatePlan(plan({ collection: "orders;drop" })), /identifier/);
    assert.throws(
      () => validatePlan(plan({ collection: "directus_permissions" })),
      (error: unknown) => error instanceof LookupQueryError && error.code === "VIBETABLE_LOOKUP_UNSUPPORTED",
    );
    assert.throws(() => validatePlan(plan({ budgetHint: { maxRootItems: 0 } })), /positive integer/);
  });

  it("requires canonical decimal scale and stable query refs", () => {
    assert.throws(() => validatePlan(plan({ lookups: [{ ...plan().lookups[0]!, outputType: { kind: "decimal" } }] })), /scale/);
    assert.throws(() => validatePlan(plan({ sort: [{ fieldRef: "missing", direction: "asc" }] })), /unknown/);
  });

  it("accepts a 10,000-row page while response budgets remain authoritative", () => {
    assert.doesNotThrow(() => validatePlan(plan({ page: { offset: 0, limit: 10_000 } })));
    assert.throws(() => validatePlan(plan({ page: { offset: 0, limit: 10_001 } })), /10000/);
  });
});
