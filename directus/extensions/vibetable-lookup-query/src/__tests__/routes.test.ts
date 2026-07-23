import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { CONTRACT, type LookupQueryPlan } from "../contracts.ts";
import { registerLookupRoutes } from "../routes.ts";

type Handler = (request: any, response: any) => Promise<void>;

function responseRecorder() {
  return {
    statusCode: 200,
    body: undefined as unknown,
    status(code: number) { this.statusCode = code; return this; },
    json(body: unknown) { this.body = body; return this; },
  };
}

function testRouter(routes: Map<string, Handler>) {
  return {
    get: (path: string, handler: Handler) => routes.set(`GET ${path}`, handler),
    post: (path: string, handler: Handler) => routes.set(`POST ${path}`, handler),
  };
}

function minimalPlan(): LookupQueryPlan {
  return {
    contract: CONTRACT,
    generation: 1,
    collection: "orders",
    primaryKey: "id",
    revisions: { schema: "s", permission: "p", lookup: "l" },
    baseFields: [],
    lookups: [],
    page: { offset: 0, limit: 10 },
  };
}

function liveSchema() {
  return {
    collections: { orders: { primary: "id", fields: { id: {} } } },
    relations: [],
  };
}

describe("lookup endpoint routes", () => {
  it("registers validate and query and rejects unauthenticated callers", async () => {
    const routes = new Map<string, Handler>();
    registerLookupRoutes(
      testRouter(routes),
      {
        services: { ItemsService: class { public async readByQuery() { return []; } } as never },
        database: {},
        getSchema: async () => liveSchema(),
      },
    );
    assert.deepEqual([...routes.keys()], ["GET /capabilities", "POST /validate", "POST /query"]);
    const response = responseRecorder();
    await routes.get("POST /validate")!({ body: minimalPlan() }, response);
    assert.equal(response.statusCode, 401);
    assert.equal((response.body as any).errors[0].extensions.code, "VIBETABLE_ACCOUNTABILITY_REQUIRED");
  });

  it("returns validation capability details and stable plan errors", async () => {
    const routes = new Map<string, Handler>();
    registerLookupRoutes(
      testRouter(routes),
      {
        services: { ItemsService: class { public async readByQuery() { return []; } } as never },
        database: {},
        getSchema: async () => liveSchema(),
      },
    );
    const valid = responseRecorder();
    await routes.get("POST /validate")!({ accountability: { user: "alice" }, body: minimalPlan() }, valid);
    assert.equal(valid.statusCode, 200);
    assert.equal((valid.body as any).data.execution.partialResults, false);

    const invalid = responseRecorder();
    await routes.get("POST /validate")!({ accountability: { user: "alice" }, body: { ...minimalPlan(), collection: "bad-name" } }, invalid);
    assert.equal(invalid.statusCode, 400);
    assert.equal((invalid.body as any).errors[0].extensions.code, "VIBETABLE_LOOKUP_PLAN_INVALID");
  });

  it("executes query with caller accountability and echoes generation/revisions", async () => {
    const routes = new Map<string, Handler>();
    let options: Record<string, unknown> | undefined;
    class EmptyItemsService {
      public constructor(_collection: string, incoming: Record<string, unknown>) { options = incoming; }
      public async readByQuery() { return []; }
    }
    let schemaReads = 0;
    registerLookupRoutes(
      testRouter(routes),
      {
        services: { ItemsService: EmptyItemsService },
        database: { marker: "database" },
        getSchema: async () => { schemaReads += 1; return liveSchema(); },
      },
    );
    const response = responseRecorder();
    const body = minimalPlan();
    await routes.get("POST /query")!({ accountability: { user: "alice", role: "reader" }, body }, response);
    assert.equal(response.statusCode, 200);
    assert.equal((response.body as any).data.generation, 1);
    assert.deepEqual((response.body as any).data.revisions, body.revisions);
    assert.deepEqual(options?.accountability, { user: "alice", role: "reader" });
    assert.equal(schemaReads, 1);
  });

  it("exposes only the authenticated capability contract", async () => {
    const routes = new Map<string, Handler>();
    registerLookupRoutes(
      testRouter(routes),
      {
        services: { ItemsService: class { public async readByQuery() { return []; } } as never },
        database: {},
        getSchema: async () => { throw new Error("capabilities must not read schema"); },
      },
    );
    const response = responseRecorder();
    await routes.get("GET /capabilities")!({ accountability: { user: "alice" } }, response);
    assert.deepEqual(response.body, { data: { contract: CONTRACT } });
    const anonymous = responseRecorder();
    await routes.get("GET /capabilities")!({}, anonymous);
    assert.equal(anonymous.statusCode, 401);
    assert.equal((anonymous.body as any).errors[0].extensions.code, "VIBETABLE_ACCOUNTABILITY_REQUIRED");
  });

  it("fails closed when the plan no longer matches the live schema", async () => {
    const routes = new Map<string, Handler>();
    registerLookupRoutes(
      testRouter(routes),
      {
        services: { ItemsService: class { public async readByQuery() { return []; } } as never },
        database: {},
        getSchema: async () => ({
          collections: { orders: { primary: "uuid", fields: { uuid: {} } } },
          relations: [],
        }),
      },
    );
    const response = responseRecorder();
    await routes.get("POST /query")!(
      { accountability: { user: "alice" }, body: minimalPlan() },
      response,
    );
    assert.equal(response.statusCode, 409);
    assert.equal(
      (response.body as any).errors[0].extensions.code,
      "VIBETABLE_LOOKUP_SCHEMA_INVALID",
    );
  });
});
