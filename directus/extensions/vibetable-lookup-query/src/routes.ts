import type { LookupQueryPlan } from "./contracts.ts";
import { CONTRACT } from "./contracts.ts";
import { errorResponse, LookupQueryError } from "./errors.ts";
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
      accountability(request);
      validatePlan(request.body);
      const plan = request.body as LookupQueryPlan;
      const schema = await context.getSchema();
      validatePlanAgainstSchema(plan, schema);
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
