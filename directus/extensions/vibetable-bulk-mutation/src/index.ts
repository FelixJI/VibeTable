/**
 * VibeTable bulk-mutation endpoint — `vibetable-bulk-mutation.v1`.
 *
 * This is the FIRST approved Directus custom extension for VibeTable. It applies a
 * batch of create/update operations against a single collection in ONE database
 * transaction (all-or-nothing), under the requesting user's own permissions, with
 * idempotency-key deduplication for safe retries.
 *
 * Why a custom endpoint (not client-side loops)?
 *   The Python data plane (`DirectusClient`) only has single-item create/update.
 *   A client-side loop cannot guarantee atomicity (a crash mid-batch leaves a
 *   partial write) and amplifies permission/validation races. This endpoint
 *   wraps the batch in a single transaction so any failure rolls everything back.
 *
 * Contract (request body):
 *   {
 *     "contract": "vibetable-bulk-mutation.v1",
 *     "collection": "vibetable_contracts",
 *     "primaryKey": "id",
 *     "idempotencyKey": "uuid",
 *     "operations": [
 *       {"kind":"update","primaryKey":"1","expectedDateUpdated":"...","values":{...}},
 *       {"kind":"create","values":{...}}
 *     ]
 *   }
 *
 * Contract (200 response, committed):
 *   { "data": { "createdRowKeys":[...], "updatedRowKeys":[...],
 *               "skippedRowKeys":[...], "conflicts":[] } }
 *
 * Conflict (200 with conflicts): when one or more `update` operations observe a
 * `date_updated` that no longer matches `expectedDateUpdated`, the whole batch is
 * rolled back and the conflicting rows + their current values are returned so the
 * client can re-preview.
 *
 * Safety:
 *   - NEVER bypasses permissions: runs under `accountability` for the requesting
 *     user; Directus enforces field/row policy per item.
 *   - Rejects unknown `kind`, missing `primaryKey` on updates, oversize batches,
 *     raw SQL, arbitrary filter JSON and `*.*` field wildcards.
 *   - Idempotency: a repeated `Idempotency-Key` returns the ORIGINAL result.
 *
 * Build/deploy + verification status: see README.md.
 */

import { defineEndpoint } from "@directus/extensions-sdk";
import type { Accountability } from "@directus/types";
import { createHash } from "node:crypto";
import { isDeepStrictEqual } from "node:util";
import {
  BulkConflictError,
  CONTRACT,
  IdempotencyCache,
  RestoreAuthorizationCache,
  RestoreProofReplayCache,
  mapError,
  validateHistoryMarkersRequest,
  validateRestoreApplyRequest,
  validateRequest,
  verifyRestoreProof,
  type BulkRequest,
  type ConflictRow,
  type BulkResult,
  type HistoryMarkersRequest,
  type RestoreApplyRequest,
  type RestoreProofRequest,
  type RestoreRequest,
} from "./bulk-mutation-helpers.js";

export { BulkConflictError, CONTRACT, RESTORE_CONTRACT } from "./bulk-mutation-helpers.js";

const HISTORY_MARKER_TABLE = "vibetable_history_markers";
const RESTORE_EXECUTION_LIMIT = 1024;
let historyMarkerInitialization: Promise<void> | null = null;

type RestoreOutcome = { status: number; body: unknown; cacheable: boolean };
type RestoreExecutionEntry = {
  fingerprint: string;
  cached?: { status: number; body: unknown };
  inFlight?: Promise<RestoreOutcome>;
};

export default defineEndpoint((router, context) => {
  const { services, getSchema, database } = context;
  const { ItemsService } = services;
  const cache = new IdempotencyCache();
  const restoreAuthorizations = new RestoreAuthorizationCache();
  const restoreProofs = new RestoreProofReplayCache();
  const restoreExecutions = new Map<string, RestoreExecutionEntry>();
  const restoreProofSecret =
    typeof context.env.VIBETABLE_HISTORY_PROOF_SECRET === "string"
      ? context.env.VIBETABLE_HISTORY_PROOF_SECRET
      : undefined;

  router.post("/apply", async (req, res) => {
    const body = req.body as BulkRequest | undefined;
    const idempotencyKey =
      (req.get("Idempotency-Key") as string | undefined) ?? body?.idempotencyKey;

    // --- Validate the request envelope ---------------------------------
    const validation = validateRequest(body, idempotencyKey);
    if (!validation.ok) {
      res.status(400).json({ errors: [{ message: validation.error }] });
      return;
    }
    const request = body as BulkRequest;

    // --- Idempotency: return the cached result for a repeat key -------
    const cached = idempotencyKey ? cache.get(idempotencyKey) : undefined;
    if (cached) {
      res.status(cached.status).json(cached.body);
      return;
    }

    const accountability = (
      req as typeof req & { accountability?: Accountability }
    ).accountability ?? null;
    const schema = await getSchema();

    // The collection must exist; we do not auto-create it.
    if (!(request.collection in schema.collections)) {
      res.status(404).json({
        errors: [{ message: `collection '${request.collection}' does not exist` }],
      });
      return;
    }

    try {
      const result = await applyInTransaction(
        ItemsService,
        schema,
        database,
        accountability,
        request,
      );
      const response = { data: result };
      if (idempotencyKey) {
        cache.set(idempotencyKey, { status: 200, body: response });
      }
      res.status(200).json(response);
    } catch (error) {
      const outcome = mapError(error);
      if (idempotencyKey && outcome.cacheable) {
        cache.set(idempotencyKey, { status: outcome.status, body: outcome.body });
      }
      res.status(outcome.status).json(outcome.body);
    }
  });

  router.post("/restore/authorize", async (req, res) => {
    const body = req.body as RestoreProofRequest | undefined;
    const accountability = (
      req as typeof req & { accountability?: Accountability }
    ).accountability;
    const user = String(accountability?.user ?? "");
    if (!accountability || !user) {
      res.status(401).json({ errors: [{ message: "authentication is required" }] });
      return;
    }
    const authorizationHeader = req.get("Authorization") ?? "";
    const bearerMatch = /^Bearer\s+(.+)$/i.exec(authorizationHeader);
    const expectedSubject = bearerMatch
      ? createHash("sha256").update(bearerMatch[1] as string, "utf8").digest("hex")
      : "";
    const proof = verifyRestoreProof(
      body,
      restoreProofSecret,
      restoreProofs,
      expectedSubject,
    );
    if (!proof.ok) {
      res.status(403).json({
        errors: [
          {
            message: proof.error,
            extensions: { code: "RESTORE_PROOF_INVALID" },
          },
        ],
      });
      return;
    }
    const request = proof.request;
    const schema = await getSchema();
    if (!(request.collection in schema.collections)) {
      res.status(404).json({ errors: [{ message: "collection does not exist" }] });
      return;
    }
    try {
      await validateRestoreAuthorization(
        ItemsService,
        schema,
        database,
        accountability,
        request,
      );
      const authorization = restoreAuthorizations.authorize(request, user);
      res.status(200).json({
        data: {
          authorizationToken: authorization.token,
          expiresAt: authorization.expiresAt,
        },
      });
    } catch (error) {
      if (error instanceof RestoreConflictError) {
        res.status(409).json({
          errors: [
            {
              message: "item changed during restore preview",
              extensions: { code: "EDIT_CONFLICT" },
            },
          ],
        });
        return;
      }
      const outcome = mapError(error);
      res.status(outcome.status).json(outcome.body);
    }
  });

  router.post("/restore", async (req, res) => {
    const body = req.body as RestoreApplyRequest | undefined;
    const idempotencyKey = req.get("Idempotency-Key") as string | undefined;
    const validation = validateRestoreApplyRequest(body, idempotencyKey);
    if (!validation.ok) {
      res.status(400).json({ errors: [{ message: validation.error }] });
      return;
    }
    const accountability = (
      req as typeof req & { accountability?: Accountability }
    ).accountability;
    const user = String(accountability?.user ?? "");
    if (!accountability || !user) {
      res.status(401).json({ errors: [{ message: "authentication is required" }] });
      return;
    }
    const cacheKey = `${user}:${idempotencyKey}`;
    const fingerprint = createHash("sha256")
      .update(body!.authorizationToken, "utf8")
      .digest("hex");
    let entry = restoreExecutions.get(cacheKey);
    if (entry && entry.fingerprint !== fingerprint) {
      res.status(409).json({
        errors: [
          {
            message: "Idempotency-Key was already used for another restore request",
            extensions: { code: "IDEMPOTENCY_KEY_MISMATCH" },
          },
        ],
      });
      return;
    }
    if (!entry) {
      if (restoreExecutions.size >= RESTORE_EXECUTION_LIMIT) {
        const evictable = Array.from(restoreExecutions).find(
          ([, candidate]) => candidate.inFlight === undefined,
        );
        if (evictable) restoreExecutions.delete(evictable[0]);
      }
      if (restoreExecutions.size >= RESTORE_EXECUTION_LIMIT) {
        res.status(503).json({
          errors: [{ message: "restore execution capacity is temporarily exhausted" }],
        });
        return;
      }
      entry = { fingerprint };
      restoreExecutions.set(cacheKey, entry);
    }
    if (entry.cached) {
      res.status(entry.cached.status).json(entry.cached.body);
      return;
    }
    let execution = entry.inFlight;
    if (!execution) {
      execution = (async () => {
        const request = restoreAuthorizations.consume(body!.authorizationToken, user);
        if (!request) {
          return {
            status: 409,
            body: {
              errors: [
                {
                  message: "restore authorization is unknown, expired, or already used",
                  extensions: { code: "RESTORE_TOKEN_INVALID" },
                },
              ],
            },
            cacheable: false,
          };
        }
        const schema = await getSchema();
        if (!(request.collection in schema.collections)) {
          return {
            status: 404,
            body: { errors: [{ message: "collection does not exist" }] },
            cacheable: false,
          };
        }
        try {
          await ensureHistoryMarkerTable(database);
          const data = await applyRestoreInTransaction(
            ItemsService,
            schema,
            database,
            accountability,
            request,
          );
          return { status: 200, body: { data }, cacheable: true };
        } catch (error) {
          if (error instanceof RestoreConflictError) {
            return {
              status: 409,
              body: {
                errors: [
                  {
                    message: "item changed since restore preview",
                    extensions: { code: "EDIT_CONFLICT" },
                  },
                ],
              },
              cacheable: true,
            };
          }
          return mapError(error);
        }
      })();
      entry.inFlight = execution;
    }
    try {
      const outcome = await execution;
      if (outcome.cacheable) {
        entry.cached = { status: outcome.status, body: outcome.body };
      } else if (restoreExecutions.get(cacheKey) === entry) {
        restoreExecutions.delete(cacheKey);
      }
      res.status(outcome.status).json(outcome.body);
    } finally {
      if (entry.inFlight === execution) entry.inFlight = undefined;
    }
  });

  router.post("/history-markers", async (req, res) => {
    const accountability = (
      req as typeof req & { accountability?: Accountability }
    ).accountability;
    if (!accountability) {
      res.status(401).json({ errors: [{ message: "authentication is required" }] });
      return;
    }
    const body = req.body as HistoryMarkersRequest | undefined;
    const validation = validateHistoryMarkersRequest(body);
    if (!validation.ok) {
      res.status(400).json({ errors: [{ message: validation.error }] });
      return;
    }
    const schema = await getSchema();
    const data = await readAuthorizedHistoryMarkers(
      ItemsService,
      schema,
      database,
      accountability,
      body!.activityIds.map(String),
    );
    res.status(200).json({ data });
  });
});

class RestoreConflictError extends Error {}

export async function applyRestoreInTransaction(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ItemsService: any,
  schema: unknown,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  knex: any,
  accountability: Accountability,
  request: RestoreRequest,
): Promise<Record<string, unknown>> {
  let result: Record<string, unknown> = {};
  const primaryKey = (
    schema as { collections: Record<string, { primary?: string }> }
  ).collections[request.collection]?.primary;
  if (!primaryKey) throw new Error("collection primary key is unavailable");
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  await knex.transaction(async (trx: any) => {
    // Lock the business row before the permission-aware read/compare/update.
    // This closes the compare-then-write race on PostgreSQL/MySQL; SQLite's
    // transaction writer serialization fails competing writes rather than
    // silently overwriting them.
    const clientName = String(trx.client?.config?.client ?? "").toLowerCase();
    if (clientName.includes("sqlite")) {
      // SQLite has no SELECT FOR UPDATE. A no-op primary-key update acquires
      // the transaction's write lock before the comparison without changing
      // business data or invoking Directus hooks.
      const lockedRows = await trx(request.collection)
        .where(primaryKey, request.itemId)
        .update({ [primaryKey]: request.itemId });
      if (lockedRows !== 1) throw new RestoreConflictError();
    } else {
      const locked = await trx(request.collection)
        .where(primaryKey, request.itemId)
        .forUpdate()
        .first(primaryKey);
      if (!locked) throw new RestoreConflictError();
    }

    const targetRevision = await trx("directus_revisions")
      .where({
        id: request.targetRevision,
        collection: request.collection,
        item: String(request.itemId),
      })
      .first("data");
    const rawTarget = targetRevision?.data;
    const targetData =
      typeof rawTarget === "string" ? (JSON.parse(rawTarget) as unknown) : rawTarget;
    if (!targetData || typeof targetData !== "object" || Array.isArray(targetData)) {
      throw new Error("target revision is unavailable");
    }
    for (const [field, value] of Object.entries(request.values)) {
      if (!isDeepStrictEqual((targetData as Record<string, unknown>)[field], value)) {
        throw new Error("restore values do not match the target revision");
      }
    }

    const items = new ItemsService(request.collection, {
      schema,
      knex: trx,
      accountability,
    });
    const fields = Array.from(
      new Set([...Object.keys(request.expectedValues), ...Object.keys(request.values)]),
    );
    const current = (await items.readOne(request.itemId, { fields })) as Record<
      string,
      unknown
    >;
    for (const [field, expected] of Object.entries(request.expectedValues)) {
      if (!isDeepStrictEqual(current[field], expected)) {
        throw new RestoreConflictError();
      }
    }
    const previousRevision = await trx("directus_revisions")
      .where({ collection: request.collection, item: String(request.itemId) })
      .orderBy("id", "desc")
      .first("id");
    const previousRevisionId = Number(previousRevision?.id ?? 0);
    if (!Number.isSafeInteger(previousRevisionId) || previousRevisionId < 0) {
      throw new Error("latest revision id is invalid");
    }
    await items.updateOne(request.itemId, request.values);
    const createdRevision = await trx("directus_revisions")
      .where({ collection: request.collection, item: String(request.itemId) })
      .orderBy("id", "desc")
      .first("id", "activity");
    const createdRevisionId = Number(createdRevision?.id);
    if (
      !Number.isSafeInteger(createdRevisionId) ||
      createdRevisionId <= previousRevisionId ||
      createdRevision?.activity === undefined ||
      createdRevision?.activity === null
    ) {
      throw new Error("restore did not create an Activity revision");
    }
    await trx(HISTORY_MARKER_TABLE)
      .insert({ activity_id: String(createdRevision.activity), action: "restore" })
      .onConflict("activity_id")
      .merge({ action: "restore" });
    result = { ...current, ...request.values };
  });
  return result;
}

async function validateRestoreAuthorization(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ItemsService: any,
  schema: unknown,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  database: any,
  accountability: Accountability,
  request: RestoreRequest,
): Promise<void> {
  const targetRevision = await database("directus_revisions")
    .where({
      id: request.targetRevision,
      collection: request.collection,
      item: String(request.itemId),
    })
    .first("data");
  const rawTarget = targetRevision?.data;
  const targetData =
    typeof rawTarget === "string" ? (JSON.parse(rawTarget) as unknown) : rawTarget;
  if (!targetData || typeof targetData !== "object" || Array.isArray(targetData)) {
    throw new Error("target revision is unavailable");
  }
  for (const [field, value] of Object.entries(request.values)) {
    if (!isDeepStrictEqual((targetData as Record<string, unknown>)[field], value)) {
      throw new Error("restore values do not match the target revision");
    }
  }

  const items = new ItemsService(request.collection, { schema, accountability });
  const fields = Array.from(
    new Set([...Object.keys(request.expectedValues), ...Object.keys(request.values)]),
  );
  const current = (await items.readOne(request.itemId, { fields })) as Record<
    string,
    unknown
  >;
  for (const [field, expected] of Object.entries(request.expectedValues)) {
    if (!isDeepStrictEqual(current[field], expected)) {
      throw new RestoreConflictError();
    }
  }
}

async function readAuthorizedHistoryMarkers(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ItemsService: any,
  schema: unknown,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  database: any,
  accountability: Accountability,
  activityIds: string[],
): Promise<Record<string, string>> {
  await ensureHistoryMarkerTable(database);
  if (activityIds.length === 0) return {};

  const markerRows = (await database(HISTORY_MARKER_TABLE)
    .select("activity_id", "action")
    .whereIn("activity_id", activityIds)) as Array<{
    activity_id: string;
    action: string;
  }>;
  if (markerRows.length === 0) return {};
  const markerActivityIds = markerRows.map((row) => String(row.activity_id));

  let revisions: Array<{
    activity: string | number;
    collection: string;
    item: string | number;
  }>;
  try {
    const revisionItems = new ItemsService("directus_revisions", {
      schema,
      accountability,
    });
    revisions = (await revisionItems.readByQuery({
      fields: ["activity", "collection", "item"],
      filter: { activity: { _in: markerActivityIds } },
      limit: markerActivityIds.length,
    })) as typeof revisions;
  } catch {
    // Revision history permission is a separate capability from item read.
    return {};
  }
  const byCollection = new Map<string, typeof revisions>();
  for (const revision of revisions) {
    const group = byCollection.get(String(revision.collection)) ?? [];
    group.push(revision);
    byCollection.set(String(revision.collection), group);
  }

  const allowedActivities = new Set<string>();
  const collections = (schema as {
    collections: Record<string, { primary?: string }>;
  }).collections;
  for (const [collection, collectionRevisions] of byCollection) {
    const primaryKey = collections[collection]?.primary;
    if (!primaryKey) continue;
    const itemIds = Array.from(
      new Set(collectionRevisions.map((revision) => String(revision.item))),
    );
    try {
      const items = new ItemsService(collection, { schema, accountability });
      const readable = (await items.readByQuery({
        fields: [primaryKey],
        filter: { [primaryKey]: { _in: itemIds } },
        limit: itemIds.length,
      })) as Array<Record<string, unknown>>;
      const readableIds = new Set(readable.map((item) => String(item[primaryKey])));
      for (const revision of collectionRevisions) {
        if (readableIds.has(String(revision.item))) {
          allowedActivities.add(String(revision.activity));
        }
      }
    } catch {
      // Fail closed when collection, row, or field permissions reject the lookup.
    }
  }

  if (allowedActivities.size === 0) return {};
  return Object.fromEntries(
    markerRows
      .filter((row) => allowedActivities.has(String(row.activity_id)))
      .map((row) => [String(row.activity_id), String(row.action)]),
  );
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
async function ensureHistoryMarkerTable(database: any): Promise<void> {
  if (historyMarkerInitialization === null) {
    historyMarkerInitialization = (async () => {
      if (await database.schema.hasTable(HISTORY_MARKER_TABLE)) return;
      try {
        await database.schema.createTable(HISTORY_MARKER_TABLE, (table: any) => {
          table.string("activity_id", 128).primary();
          table.string("action", 32).notNullable();
        });
      } catch (error) {
        if (!(await database.schema.hasTable(HISTORY_MARKER_TABLE))) throw error;
      }
    })().catch((error) => {
      historyMarkerInitialization = null;
      throw error;
    });
  }
  await historyMarkerInitialization;
}

/**
 * Apply the whole batch inside a single transaction. Any failure rolls back
 * every change (all-or-nothing). Conflict detection re-reads each update
 * target's `date_updated` inside the transaction so a concurrent change is
 * observed before the write.
 *
 * Transaction threading: every {@link ItemsService} is constructed *inside*
 * the knex transaction callback and bound to the transaction handle (`trx`),
 * NOT to the shared main DB handle. Binding to the main handle deadlocks on
 * single-connection databases (SQLite): the transaction occupies the only
 * connection, and the service's own query then waits for a second connection
 * that never frees — producing an indefinite hang with no error. Binding to
 * `trx` makes every read/write participate in the same transaction, which is
 * also what makes the all-or-nothing rollback real.
 */
async function applyInTransaction(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ItemsService: any,
  schema: unknown,
  knex: { transaction: (cb: (trx: unknown) => Promise<unknown>) => Promise<unknown> },
  accountability: Accountability | null,
  request: BulkRequest,
): Promise<BulkResult> {
  const created: string[] = [];
  const updated: string[] = [];
  const skipped: string[] = [];
  const conflicts: ConflictRow[] = [];

  await knex.transaction(async (trx) => {
    const items = new ItemsService(request.collection, {
      schema,
      knex: trx,
      accountability,
    });
    for (const op of request.operations) {
      if (op.kind === "create") {
        const id = await items.createOne(op.values);
        created.push(String(id));
      } else {
        const key = op.primaryKey as string;
        const current = await items.readOne(key);
        const currentDate = current["date_updated"];
        if (
          op.expectedDateUpdated &&
          String(currentDate) !== String(op.expectedDateUpdated)
        ) {
          conflicts.push({
            primaryKey: key,
            currentValue: current,
            expectedDateUpdated: op.expectedDateUpdated ?? null,
          });
          continue;
        }
        await items.updateOne(key, op.values);
        updated.push(key);
      }
    }
    if (conflicts.length > 0) {
      throw new BulkConflictError(conflicts);
    }
  });

  return {
    createdRowKeys: created,
    updatedRowKeys: updated,
    skippedRowKeys: skipped,
    conflicts,
  };
}
