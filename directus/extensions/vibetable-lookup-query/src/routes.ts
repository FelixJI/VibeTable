import { createHash } from "node:crypto";

import type { LookupQueryPlan } from "./contracts.ts";
import { CONTRACT } from "./contracts.ts";
import { directusReadError, errorResponse, LookupQueryError } from "./errors.ts";
import { DEPLOYMENT_BUDGET, executeQuery, type ItemsServiceConstructor } from "./executor.ts";
import { dependencyOrder, validatePlan, validatePlanAgainstSchema } from "./validation.ts";

interface RequestLike {
  body?: unknown;
  accountability?: unknown;
}

interface ResponseLike {
  status(code: number): ResponseLike;
  json(body: unknown): unknown;
}

interface RouterLike {
  get(path: string, handler: (request: RequestLike, response: ResponseLike) => Promise<void>): unknown;
  post(path: string, handler: (request: RequestLike, response: ResponseLike) => Promise<void>): unknown;
}

interface EndpointContextLike {
  services: { ItemsService: ItemsServiceConstructor };
  database: unknown;
  getSchema(): Promise<unknown>;
}

function accountability(request: RequestLike): Record<string, unknown> {
  const value = request.accountability;
  if (!value || typeof value !== "object" || !(value as { user?: unknown }).user) {
    throw new LookupQueryError(
      "VIBETABLE_ACCOUNTABILITY_REQUIRED",
      "an authenticated Directus user is required",
    );
  }
  return value as Record<string, unknown>;
}

function respondError(response: ResponseLike, error: unknown): void {
  const outcome = errorResponse(error);
  response.status(outcome.status).json(outcome.body);
}

function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(
    Object.entries(value as Record<string, unknown>)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, item]) => [key, canonical(item)]),
  );
}

async function validateLookupRevision(
  plan: LookupQueryPlan,
  ItemsService: ItemsServiceConstructor,
  serviceOptions: Record<string, unknown>,
): Promise<void> {
  const definitions = new ItemsService("vibetable_lookup_definitions", serviceOptions);
  const proofEntries = Object.entries(plan.definitionRevisions);
  let rows: Array<Record<string, unknown>>;
  try {
    rows = await definitions.readByQuery({
      fields: ["collection", "definition", "lookup_id", "revision"],
      filter: proofEntries.length === 0
        ? {
            _and: [
              { collection: { _eq: plan.collection } },
              { status: { _eq: "active" } },
            ],
          }
        : {
            _or: [
              {
                _and: [
                  { collection: { _eq: plan.collection } },
                  { status: { _eq: "active" } },
                ],
              },
              {
                _and: [
                  { lookup_id: { _in: proofEntries.map(([lookupId]) => lookupId) } },
                  { status: { _eq: "active" } },
                ],
              },
            ],
          },
      limit: -1,
      sort: ["lookup_id"],
    });
  } catch (error) {
    throw directusReadError(error, { collection: "vibetable_lookup_definitions" });
  }
  const seen = new Set<string>();
  const metadata = rows.map((row) => {
    const collection = row.collection;
    const lookupId = row.lookup_id;
    const revision = row.revision;
    const definition = row.definition;
    if (
      typeof collection !== "string"
      || typeof lookupId !== "string"
      || seen.has(lookupId)
      || !Number.isSafeInteger(revision)
      || !definition
      || typeof definition !== "object"
      || (definition as { lookupId?: unknown }).lookupId !== lookupId
    ) {
      throw new LookupQueryError(
        "VIBETABLE_LOOKUP_SCHEMA_INVALID",
        "stored Lookup definition metadata is inconsistent",
      );
    }
    seen.add(lookupId);
    return { collection, lookupId, revision: Number(revision), definition };
  });
  const digest = createHash("sha256")
    .update(JSON.stringify(metadata
      .filter((item) => item.collection === plan.collection)
      .map((item) => canonical(item.definition))), "utf8")
    .digest("hex");
  if (digest !== plan.revisions.lookup) {
    throw new LookupQueryError(
      "VIBETABLE_LOOKUP_SCHEMA_INVALID",
      "Lookup definitions changed after the plan was compiled",
      { revision: "lookup" },
    );
  }
  const liveById = new Map(metadata.map((item) => [item.lookupId, item.revision]));
  for (const [lookupId, expectedRevision] of proofEntries) {
    if (liveById.get(lookupId) !== expectedRevision) {
      throw new LookupQueryError(
        "VIBETABLE_LOOKUP_SCHEMA_INVALID",
        "a Lookup dependency changed after the plan was compiled",
        { lookupId },
      );
    }
  }
}

export function registerLookupRoutes(router: RouterLike, context: EndpointContextLike): void {
  router.get("/capabilities", async (request, response) => {
    try {
      accountability(request);
      response.json({ data: { contract: CONTRACT } });
    } catch (error) {
      respondError(response, error);
    }
  });

  router.post("/validate", async (request, response) => {
    try {
      const caller = accountability(request);
      validatePlan(request.body);
      const plan = request.body as LookupQueryPlan;
      const schema = await context.getSchema();
      // Physical schema and current-accountability reads are checked live;
      // the opaque application revision strings are echoed only after those
      // stronger checks and the persisted Lookup revision proof succeed.
      validatePlanAgainstSchema(plan, schema);
      await validateLookupRevision(plan, context.services.ItemsService, {
        schema,
        knex: context.database,
        accountability: caller,
      });
      response.json({
        data: {
          contract: CONTRACT,
          valid: true,
          lookupOrder: dependencyOrder(plan.lookups).map((lookup) => lookup.lookupId),
          deploymentBudget: DEPLOYMENT_BUDGET,
          execution: {
            strategy: "permission-aware-frontier-batching",
            m2aBranchSelection: true,
            partialResults: false,
          },
        },
      });
    } catch (error) {
      respondError(response, error);
    }
  });

  router.post("/query", async (request, response) => {
    try {
      const caller = accountability(request);
      validatePlan(request.body);
      const plan = request.body as LookupQueryPlan;
      const schema = await context.getSchema();
      validatePlanAgainstSchema(plan, schema);
      await validateLookupRevision(plan, context.services.ItemsService, {
        schema,
        knex: context.database,
        accountability: caller,
      });
      const result = await executeQuery(
        plan,
        context.services.ItemsService,
        { schema, knex: context.database, accountability: caller },
      );
      response.json({ data: result });
    } catch (error) {
      respondError(response, error);
    }
  });
}
