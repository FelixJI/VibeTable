import assert from "node:assert/strict";
import test from "node:test";

import {
  applyRelationImportInTransaction,
  mapRelationImportError,
  RELATION_IMPORT_CONTRACT,
  RelationImportError,
  validateRelationImport,
  type RelationImportRequest,
} from "../relation-import.ts";

function request(overrides: Partial<RelationImportRequest> = {}): RelationImportRequest {
  return {
    contract: RELATION_IMPORT_CONTRACT,
    idempotencyKey: "import-1",
    sourceCollection: "orders",
    sourcePrimaryKey: "id",
    mode: "create",
    schemaProof: {
      collections: ["orders", "contracts"],
      fields: {
        orders: ["id", "number", "contract"],
        contracts: ["id", "contract_no", "price"],
      },
      uniqueFields: {
        orders: ["id", "number"],
        contracts: ["id", "contract_no"],
      },
      relationIds: ["orders.contract"],
    },
    rows: [{
      values: { number: "O-1" },
      relations: [{
        targetField: "contract",
        relationId: "orders.contract",
        targetCollection: "contracts",
        targetPrimaryKey: "id",
        matchField: "contract_no",
        sourceValue: "C-1",
        state: "matched",
        matchedPrimaryKey: "contract-1",
      }],
    }],
    ...overrides,
  };
}

const schema = {
  collections: {
    orders: { primary: "id", fields: { id: {}, number: { is_unique: true }, contract: {} } },
    contracts: { primary: "id", fields: { id: {}, contract_no: { is_unique: true }, price: {} } },
  },
  relations: [{
    collection: "orders",
    field: "contract",
    related_collection: "contracts",
  }],
};

type Row = Record<string, unknown>;
type State = Record<string, Row[]>;

function harness(
  initial: State,
  race?: { collection: string; field: string; value: unknown; winner: Row },
) {
  let committed: State = structuredClone(initial);
  let raceTriggered = false;
  const constructions: Array<{ collection: string; options: Record<string, unknown> }> = [];
  const primaryKeys: Record<string, string> = { orders: "id", contracts: "id" };

  class ItemsService {
    private readonly state: State;
    private readonly collection: string;
    constructor(collection: string, options: Record<string, unknown>) {
      constructions.push({ collection, options });
      this.collection = collection;
      this.state = (options.knex as { state: State }).state;
    }
    async readByQuery(query: {
      fields: string[];
      filter: Record<string, { _eq: unknown }>;
      limit: number;
    }): Promise<Row[]> {
      const [field, operator] = Object.entries(query.filter)[0]!;
      return (this.state[this.collection] ?? [])
        .filter((row) => row[field] === operator._eq)
        .slice(0, query.limit)
        .map((row) => Object.fromEntries(query.fields.map((key) => [key, row[key]])));
    }
    async createOne(values: Row): Promise<unknown> {
      if (values.fail === true) throw new Error("database password leaked in driver failure");
      const rows = this.state[this.collection] ?? (this.state[this.collection] = []);
      if (
        race
        && !raceTriggered
        && race.collection === this.collection
        && values[race.field] === race.value
      ) {
        raceTriggered = true;
        rows.push({ ...race.winner });
        throw Object.assign(new Error("duplicate value"), { code: "23505" });
      }
      const primaryKey = primaryKeys[this.collection]!;
      const key = values[primaryKey] ?? `${this.collection}-${rows.length + 1}`;
      rows.push({ ...values, [primaryKey]: key });
      return key;
    }
    async updateOne(key: unknown, values: Row): Promise<void> {
      const rows = this.state[this.collection] ?? [];
      const primaryKey = primaryKeys[this.collection]!;
      const row = rows.find((candidate) => candidate[primaryKey] === key);
      if (!row) throw new Error("missing row");
      Object.assign(row, values);
    }
  }

  const database = {
    async transaction(callback: (trx: { state: State }) => Promise<void>): Promise<void> {
      const candidate = structuredClone(committed);
      type Transaction = {
        state: State;
        transaction<T>(savepoint: (nested: Transaction) => Promise<T>): Promise<T>;
      };
      const trx: Transaction = {
        state: candidate,
        async transaction<T>(savepoint: (nested: typeof trx) => Promise<T>): Promise<T> {
          return await savepoint(trx);
        },
      };
      await callback(trx);
      committed = candidate;
    },
  };
  return {
    ItemsService,
    database,
    constructions,
    state: () => structuredClone(committed),
  };
}

test("accepts a narrow Python-compiled import plan", () => {
  assert.deepEqual(validateRelationImport(request(), "import-1"), { ok: true });
});

test("rejects undeclared fields, collections, relations and unknown properties", () => {
  const undeclaredField = request();
  (undeclaredField.rows[0]!.values as Row).admin_only = true;
  assert.match(validationError(undeclaredField), /outside schemaProof/);

  const undeclaredRelation = request();
  (undeclaredRelation.rows[0]!.relations[0] as { relationId: string }).relationId = "orders.secret";
  assert.equal(validateRelationImport(undeclaredRelation).ok, false);

  const unknown = { ...request(), rawFilter: { _or: [] } };
  assert.match(validationError(unknown), /unsupported properties/);
});

test("rejects header mismatch and invalid upsert plans", () => {
  assert.equal(validateRelationImport(request(), "different-key").ok, false);
  assert.equal(validateRelationImport(request({ mode: "upsert" })).ok, false);
  const upsert = request({ mode: "upsert", upsertKey: "number" });
  assert.equal(validateRelationImport(upsert).ok, true);
});

test("requires scalar exact values and explicit uniqueness proof", () => {
  const objectValue = request();
  (objectValue.rows[0]!.relations[0] as { sourceValue: unknown }).sourceValue = {
    _neq: "C-1",
  };
  assert.equal(validateRelationImport(objectValue).ok, false);

  const notUnique = request();
  (notUnique.schemaProof.uniqueFields as Record<string, readonly string[]>).contracts = ["id"];
  assert.match(validationError(notUnique), /not proven unique/);
});

test("matches a target exactly and creates a linked source row in one transaction", async () => {
  const fake = harness({
    contracts: [{ id: "contract-1", contract_no: "C-1", price: 100 }],
    orders: [],
  });
  const accountability = { user: "user-1" } as never;
  const result = await applyRelationImportInTransaction(
    fake.ItemsService,
    schema,
    fake.database,
    accountability,
    request(),
  );
  assert.deepEqual(result.createdSourceRowKeys, ["orders-1"]);
  assert.deepEqual(result.createdTargetRowKeys, []);
  assert.equal(fake.state().orders[0]!.contract, "contract-1");
  assert.ok(fake.constructions.every(({ options }) => options.accountability === accountability));
  assert.ok(fake.constructions.every(({ options }) => isTransactionHandle(options.knex)));
});

test("create-if-missing rechecks exact match and creates only when still absent", async () => {
  const fake = harness({ contracts: [], orders: [] });
  const create = request();
  (create.rows[0]!.relations as RelationImportRequest["rows"][number]["relations"][number][])[0] = {
    ...create.rows[0]!.relations[0]!,
    state: "create",
    matchedPrimaryKey: undefined,
    createValues: { price: 125 },
  };
  const result = await applyRelationImportInTransaction(
    fake.ItemsService,
    schema,
    fake.database,
    { user: "user-1" } as never,
    create,
  );
  assert.equal(fake.state().contracts[0]!.contract_no, "C-1");
  assert.equal(fake.state().contracts[0]!.price, 125);
  assert.equal(fake.state().orders[0]!.contract, "contracts-1");
  assert.equal(result.resolvedRelations[0]!.state, "created");

  const raced = harness({ contracts: [{ id: "raced-1", contract_no: "C-1" }], orders: [] });
  const racedResult = await applyRelationImportInTransaction(
    raced.ItemsService,
    schema,
    raced.database,
    { user: "user-1" } as never,
    create,
  );
  assert.equal(raced.state().contracts.length, 1);
  assert.equal(raced.state().orders[0]!.contract, "raced-1");
  assert.equal(racedResult.resolvedRelations[0]!.state, "matched");
});

test("create-if-missing resolves the winner after a concurrent unique conflict", async () => {
  const fake = harness(
    { contracts: [], orders: [] },
    {
      collection: "contracts",
      field: "contract_no",
      value: "C-1",
      winner: { id: "concurrent-1", contract_no: "C-1", price: 200 },
    },
  );
  const create = request();
  (create.rows[0]!.relations as RelationImportRequest["rows"][number]["relations"][number][])[0] = {
    ...create.rows[0]!.relations[0]!,
    state: "create",
    matchedPrimaryKey: undefined,
    createValues: { price: 125 },
  };
  const result = await applyRelationImportInTransaction(
    fake.ItemsService,
    schema,
    fake.database,
    { user: "user-1" } as never,
    create,
  );
  assert.equal(fake.state().contracts.length, 1);
  assert.equal(fake.state().orders[0]!.contract, "concurrent-1");
  assert.deepEqual(result.createdTargetRowKeys, []);
  assert.equal(result.resolvedRelations[0]!.state, "matched");
});

test("ambiguous exact matches fail the whole batch without partial writes", async () => {
  const fake = harness({
    contracts: [
      { id: "contract-1", contract_no: "C-1" },
      { id: "contract-2", contract_no: "C-1" },
    ],
    orders: [],
  });
  await assert.rejects(
    applyRelationImportInTransaction(
      fake.ItemsService,
      schema,
      fake.database,
      { user: "user-1" } as never,
      request(),
    ),
    (error: unknown) => error instanceof RelationImportError && error.code === "RELATION_MATCH_AMBIGUOUS",
  );
  assert.deepEqual(fake.state().orders, []);
});

test("a later failure rolls back earlier target and source creates", async () => {
  const fake = harness({ contracts: [], orders: [] });
  const body = request();
  (body.schemaProof.fields as Record<string, readonly string[]>).orders = [
    ...body.schemaProof.fields.orders,
    "fail",
  ];
  body.rows = [
    {
      values: { number: "O-1" },
      relations: [{
        ...body.rows[0]!.relations[0]!,
        state: "create",
        matchedPrimaryKey: undefined,
      }],
    },
    { values: { number: "O-2", fail: true }, relations: [] },
  ];
  const schemaWithFail = structuredClone(schema) as unknown as {
    collections: Record<string, { primary: string; fields: Record<string, unknown> }>;
  };
  schemaWithFail.collections.orders!.fields.fail = {};
  await assert.rejects(applyRelationImportInTransaction(
    fake.ItemsService,
    schemaWithFail,
    fake.database,
    { user: "user-1" } as never,
    body,
  ));
  assert.deepEqual(fake.state(), { contracts: [], orders: [] });
});

test("upsert updates an exact source match and rejects ambiguous source matches", async () => {
  const upsert = request({ mode: "upsert", upsertKey: "number" });
  const fake = harness({
    contracts: [{ id: "contract-1", contract_no: "C-1" }],
    orders: [{ id: "order-1", number: "O-1", contract: null }],
  });
  const result = await applyRelationImportInTransaction(
    fake.ItemsService,
    schema,
    fake.database,
    { user: "user-1" } as never,
    upsert,
  );
  assert.deepEqual(result.updatedSourceRowKeys, ["order-1"]);
  assert.equal(fake.state().orders[0]!.contract, "contract-1");

  const ambiguous = harness({
    contracts: [{ id: "contract-1", contract_no: "C-1" }],
    orders: [{ id: "order-1", number: "O-1" }, { id: "order-2", number: "O-1" }],
  });
  await assert.rejects(
    applyRelationImportInTransaction(
      ambiguous.ItemsService,
      schema,
      ambiguous.database,
      { user: "user-1" } as never,
      upsert,
    ),
    (error: unknown) => error instanceof RelationImportError && error.code === "SOURCE_MATCH_AMBIGUOUS",
  );
});

test("fails closed on stale schema proof and sanitizes unexpected errors", async () => {
  const fake = harness({ contracts: [], orders: [] });
  const stale = structuredClone(schema) as unknown as {
    collections: Record<string, { primary: string; fields: Record<string, unknown> }>;
  };
  delete stale.collections.contracts!.fields.contract_no;
  await assert.rejects(
    applyRelationImportInTransaction(
      fake.ItemsService,
      stale,
      fake.database,
      { user: "user-1" } as never,
      request(),
    ),
    (error: unknown) => error instanceof RelationImportError && error.code === "SCHEMA_PROOF_MISMATCH",
  );
  const mapped = mapRelationImportError(new Error("database password is secret"));
  assert.equal(mapped.status, 500);
  assert.doesNotMatch(JSON.stringify(mapped.body), /password|secret/);
});

test("fails closed when a claimed unique match field is not live-unique", async () => {
  const fake = harness({ contracts: [], orders: [] });
  const notUnique = structuredClone(schema);
  (notUnique.collections.contracts.fields as Record<string, unknown>).contract_no = {};
  await assert.rejects(
    applyRelationImportInTransaction(
      fake.ItemsService,
      notUnique,
      fake.database,
      { user: "user-1" } as never,
      request(),
    ),
    (error: unknown) => error instanceof RelationImportError
      && error.code === "SCHEMA_PROOF_MISMATCH",
  );
});

test("fails closed when the live physical relation no longer matches the proof", async () => {
  const fake = harness({ contracts: [], orders: [] });
  const drifted = structuredClone(schema);
  drifted.relations = [];
  await assert.rejects(
    applyRelationImportInTransaction(
      fake.ItemsService,
      drifted,
      fake.database,
      { user: "user-1" } as never,
      request(),
    ),
    (error: unknown) => error instanceof RelationImportError && error.code === "SCHEMA_PROOF_MISMATCH",
  );
});

function isTransactionHandle(value: unknown): boolean {
  return typeof value === "object" && value !== null && "state" in value;
}

function validationError(value: unknown): string {
  const result = validateRelationImport(value);
  return result.ok ? "" : result.error;
}
