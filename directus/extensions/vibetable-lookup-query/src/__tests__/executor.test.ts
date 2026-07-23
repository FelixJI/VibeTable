import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { CONTRACT, type LookupQueryPlan } from "../contracts.ts";
import { LookupQueryError } from "../errors.ts";
import { DEPLOYMENT_BUDGET, executeQuery, type ItemRow } from "../executor.ts";
import { validatePlan } from "../validation.ts";

const database: Record<string, ItemRow[]> = {
  orders: [
    { id: 1, number: "B", contract_id: 10 },
    { id: 2, number: "A", contract_id: 20 },
    { id: 3, number: "C", contract_id: null },
  ],
  contracts: [
    { id: 10, price: "10.25" },
    { id: 20, price: "2.00" },
  ],
  lines: [
    { id: 101, order_id: 1, amount: "2.25" },
    { id: 102, order_id: 1, amount: "3.00" },
    { id: 103, order_id: 2, amount: null },
  ],
  orders_tags: [
    { order_id: 1, tag_id: 1001, quantity: "1.50" },
    { order_id: 1, tag_id: 1002, quantity: "2.00" },
    { order_id: 2, tag_id: 1002, quantity: "4.00" },
  ],
  tags: [
    { id: 1001, label: "red" },
    { id: 1002, label: "blue" },
  ],
  order_links: [
    { order_id: 1, item_id: "n1", item_collection: "notes" },
    { order_id: 1, item_id: "a1", item_collection: "assets" },
    { order_id: 2, item_id: "n2", item_collection: "notes" },
  ],
  notes: [
    { id: "n1", title: "first", author_id: "u1" },
    { id: "n2", title: null, author_id: "u2" },
  ],
  assets: [{ asset_id: "a1", filename: "contract.pdf" }],
  authors: [
    { id: "u1", name: "Alice" },
    { id: "u2", name: "Bob" },
  ],
};

function compare(left: unknown, right: unknown): number {
  return String(left).localeCompare(String(right), undefined, { numeric: true });
}

class MemoryItemsService {
  public static accountability: unknown[] = [];
  private readonly collection: string;

  public constructor(collection: string, options: Record<string, unknown>) {
    this.collection = collection;
    MemoryItemsService.accountability.push(options.accountability);
  }

  public async readByQuery(query: Record<string, unknown>): Promise<ItemRow[]> {
    let rows = [...(database[this.collection] ?? [])];
    const filter = query.filter as Record<string, { _in?: unknown[] }> | undefined;
    if (filter) {
      const [field, condition] = Object.entries(filter)[0]!;
      if (condition._in) {
        const allowed = new Set(condition._in.map((value) => JSON.stringify(value)));
        rows = rows.filter((row) => allowed.has(JSON.stringify(row[field])));
      }
    }
    const sorts = (query.sort as string[] | undefined) ?? [];
    rows.sort((left, right) => {
      for (const raw of sorts) {
        const descending = raw.startsWith("-");
        const field = descending ? raw.slice(1) : raw;
        const result = compare(left[field], right[field]);
        if (result) return descending ? -result : result;
      }
      return 0;
    });
    const offset = Number(query.offset ?? 0);
    const limit = Number(query.limit ?? rows.length);
    const fields = query.fields as string[] | undefined;
    return rows.slice(offset, offset + limit).map((row) => fields
      ? Object.fromEntries(fields.map((field) => [field, row[field]]))
      : { ...row });
  }
}

function plan(): LookupQueryPlan {
  return {
    contract: CONTRACT,
    generation: "generation-9",
    collection: "orders",
    primaryKey: "id",
    revisions: { schema: "s1", permission: "p1", lookup: "l1" },
    baseFields: [{ ref: "order.number", field: "number", outputType: { kind: "string" } }],
    lookups: [
      {
        lookupId: "contract-price",
        ref: "lookup.contract-price",
        path: [{ relationId: "order-contract", kind: "m2o", fromCollection: "orders", toCollection: "contracts", sourceField: "contract_id", targetField: "id" }],
        source: { kind: "field", field: "price" },
        aggregate: "scalar",
        outputType: { kind: "decimal", scale: 2 },
      },
      {
        lookupId: "line-total",
        ref: "lookup.line-total",
        path: [{ relationId: "order-lines", kind: "o2m", fromCollection: "orders", toCollection: "lines", sourceField: "id", targetField: "order_id", destinationPrimaryKey: "id" }],
        source: { kind: "field", field: "amount" },
        aggregate: "sum",
        outputType: { kind: "decimal", scale: 2 },
      },
      {
        lookupId: "tag-quantities",
        ref: "lookup.tag-quantities",
        path: [{
          relationId: "order-tags",
          kind: "m2m",
          fromCollection: "orders",
          toCollection: "tags",
          sourceField: "id",
          targetField: "id",
          junction: { collection: "orders_tags", sourceField: "order_id", targetField: "tag_id" },
        }],
        source: { kind: "junction", step: 0, field: "quantity" },
        aggregate: "sum",
        outputType: { kind: "decimal", scale: 2 },
      },
      {
        lookupId: "linked-labels",
        ref: "lookup.linked-labels",
        path: [{
          relationId: "order-links",
          kind: "m2a",
          fromCollection: "orders",
          sourceField: "id",
          targetCollections: ["notes", "assets"],
          targetPrimaryKeys: { notes: "id", assets: "asset_id" },
          junction: { collection: "order_links", sourceField: "order_id", targetField: "item_id", collectionField: "item_collection" },
        }],
        source: { kind: "m2a", fields: { notes: "title", assets: "filename" } },
        aggregate: "list",
        outputType: { kind: "string" },
      },
    ],
    filter: { fieldRef: "lookup.contract-price", operator: "gte", value: "2.0" },
    sort: [{ fieldRef: "lookup.contract-price", direction: "desc" }],
    groupBy: ["order.number"],
    groupAggregates: [{ ref: "group.total", fieldRef: "lookup.line-total", aggregate: "sum", outputType: { kind: "decimal", scale: 2 } }],
    page: { offset: 0, limit: 2 },
  };
}

describe("permission-aware frontier executor", () => {
  it("executes four relation kinds, junction values and full-dataset query operations", async () => {
    MemoryItemsService.accountability = [];
    const input = plan();
    validatePlan(input);
    const result = await executeQuery(input, MemoryItemsService, { accountability: { user: "alice" } });

    assert.equal(result.rootTotal, 3);
    assert.equal(result.total, 2);
    assert.deepEqual(result.rows.map((row) => row.primaryKey), [1, 2]);
    assert.equal(result.rows[0]!.cells["lookup.contract-price"], "10.25");
    assert.equal(result.rows[0]!.cells["lookup.line-total"], "5.25");
    assert.equal(result.rows[0]!.cells["lookup.tag-quantities"], "3.50");
    assert.deepEqual(result.rows[0]!.cells["lookup.linked-labels"], [
      { collection: "notes", itemId: "n1", value: "first" },
      { collection: "assets", itemId: "a1", value: "contract.pdf" },
    ]);
    assert.equal(result.groups.length, 2);
    assert.equal(result.groups.find((group) => group.key === "B")!.aggregateCells["group.total"], "5.25");
    assert.ok(result.groups[0]!.childPageCursor.length > 0);
    assert.ok(MemoryItemsService.accountability.every((value) => (value as { user: string }).user === "alice"));
  });

  it("reads the real target PK when constructing O2M provenance", async () => {
    const lineQueries: Record<string, unknown>[] = [];
    class QueryRecordingItemsService extends MemoryItemsService {
      private readonly targetCollection: string;
      public constructor(collection: string, options: Record<string, unknown>) {
        super(collection, options);
        this.targetCollection = collection;
      }
      public override async readByQuery(query: Record<string, unknown>): Promise<ItemRow[]> {
        if (this.targetCollection === "lines") lineQueries.push(query);
        return super.readByQuery(query);
      }
    }
    const input = plan();
    input.filter = undefined;
    input.sort = [];
    input.groupBy = [];
    input.groupAggregates = [];
    input.lookups = [input.lookups.find((lookup) => lookup.lookupId === "line-total")!];
    validatePlan(input);
    await executeQuery(input, QueryRecordingItemsService, { accountability: { user: "alice" } });
    assert.ok(lineQueries.length > 0);
    assert.ok(lineQueries.every((query) => (query.fields as string[]).includes("id")));
  });

  it("uses stable primary-key tie breaking after requested sorts", async () => {
    const input = plan();
    input.filter = undefined;
    input.groupBy = [];
    input.groupAggregates = [];
    input.sort = [{ fieldRef: "order.number", direction: "asc" }];
    input.page = { offset: 0, limit: 3 };
    validatePlan(input);
    const result = await executeQuery(input, MemoryItemsService, { accountability: { user: "alice" } });
    assert.deepEqual(result.rows.map((row) => row.primaryKey), [2, 1, 3]);
    assert.equal(result.stableTieBreaker, "id");
  });

  it("returns no partial result when a budget is exceeded", async () => {
    const input = plan();
    input.budgetHint = { maxRootItems: 2 };
    validatePlan(input);
    await assert.rejects(
      executeQuery(input, MemoryItemsService, { accountability: { user: "alice" } }, DEPLOYMENT_BUDGET),
      (error: unknown) => error instanceof LookupQueryError
        && error.code === "VIBETABLE_LOOKUP_TOO_EXPENSIVE"
        && error.details?.metric === "root_items",
    );
  });

  it("maps field permission failures to an explicit restricted error", async () => {
    class RestrictedItemsService extends MemoryItemsService {
      private readonly restrictedCollection: string;
      public constructor(collection: string, options: Record<string, unknown>) {
        super(collection, options);
        this.restrictedCollection = collection;
      }
      public override async readByQuery(query: Record<string, unknown>): Promise<ItemRow[]> {
        if (this.restrictedCollection === "contracts") {
          throw { status: 403, extensions: { code: "FORBIDDEN" } };
        }
        return super.readByQuery(query);
      }
    }
    await assert.rejects(
      executeQuery(plan(), RestrictedItemsService, { accountability: { user: "alice" } }),
      (error: unknown) => error instanceof LookupQueryError && error.code === "VIBETABLE_LOOKUP_RESTRICTED",
    );
  });

  it("does not count a junction whose target row is hidden by row permissions", async () => {
    class RowFilteredItemsService extends MemoryItemsService {
      private readonly targetCollection: string;
      public constructor(collection: string, options: Record<string, unknown>) {
        super(collection, options);
        this.targetCollection = collection;
      }
      public override async readByQuery(query: Record<string, unknown>): Promise<ItemRow[]> {
        const rows = await super.readByQuery(query);
        return this.targetCollection === "tags" ? rows.filter((row) => row.id !== 1002) : rows;
      }
    }
    const input = plan();
    input.filter = undefined;
    input.sort = [];
    input.groupBy = [];
    input.groupAggregates = [];
    input.page = { offset: 0, limit: 3 };
    input.lookups = [{
      ...input.lookups[2]!,
      lookupId: "visible-tag-count",
      ref: "lookup.visible-tag-count",
      aggregate: "count",
      outputType: { kind: "integer" },
    }];
    validatePlan(input);
    const result = await executeQuery(input, RowFilteredItemsService, { accountability: { user: "alice" } });
    const orderOne = result.rows.find((row) => row.primaryKey === 1)!;
    assert.equal(orderOne.cells["lookup.visible-tag-count"], 1);
  });

  it("executes a target Lookup after a relation path", async () => {
    const input = plan();
    input.filter = undefined;
    input.sort = [];
    input.groupBy = [];
    input.groupAggregates = [];
    input.page = { offset: 0, limit: 3 };
    input.lookups = [
      {
        lookupId: "order-contract-price-copy",
        ref: "lookup.contract-price-copy",
        path: [{ relationId: "order-contract", kind: "m2o", fromCollection: "orders", toCollection: "contracts", sourceField: "contract_id", targetField: "id" }],
        source: { kind: "lookup", lookupId: "contract-price-internal" },
        aggregate: "scalar",
        outputType: { kind: "decimal", scale: 2 },
      },
      {
        lookupId: "contract-price-internal",
        ref: "internal.contract-price",
        collection: "contracts",
        primaryKey: "id",
        expose: false,
        path: [],
        source: { kind: "field", field: "price" },
        aggregate: "scalar",
        outputType: { kind: "decimal", scale: 2 },
      },
    ];
    validatePlan(input);
    const result = await executeQuery(input, MemoryItemsService, { accountability: { user: "alice" } });
    assert.equal(result.rows.find((row) => row.primaryKey === 1)!.cells["lookup.contract-price-copy"], "10.25");
    assert.equal(result.rows.find((row) => row.primaryKey === 3)!.cells["lookup.contract-price-copy"], null);
  });

  it("continues through an explicitly selected M2A collection", async () => {
    const input = plan();
    input.filter = undefined;
    input.sort = [];
    input.groupBy = [];
    input.groupAggregates = [];
    input.page = { offset: 0, limit: 3 };
    input.lookups = [{
      lookupId: "linked-authors",
      ref: "lookup.linked-authors",
      path: [
        {
          relationId: "order-links",
          kind: "m2a",
          fromCollection: "orders",
          toCollection: "notes",
          sourceField: "id",
          targetCollections: ["notes", "assets"],
          targetPrimaryKeys: { notes: "id", assets: "asset_id" },
          junction: { collection: "order_links", sourceField: "order_id", targetField: "item_id", collectionField: "item_collection" },
        },
        { relationId: "note-author", kind: "m2o", fromCollection: "notes", toCollection: "authors", sourceField: "author_id", targetField: "id" },
      ],
      source: { kind: "field", field: "name" },
      aggregate: "list",
      outputType: { kind: "string" },
    }];
    validatePlan(input);
    const result = await executeQuery(input, MemoryItemsService, { accountability: { user: "alice" } });
    assert.deepEqual(result.rows.find((row) => row.primaryKey === 1)!.cells["lookup.linked-authors"], ["Alice"]);
    assert.deepEqual(result.rows.find((row) => row.primaryKey === 2)!.cells["lookup.linked-authors"], ["Bob"]);
  });
});
