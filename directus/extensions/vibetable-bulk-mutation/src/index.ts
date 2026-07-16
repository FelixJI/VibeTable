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
import {
  BulkConflictError,
  CONTRACT,
  IdempotencyCache,
  mapError,
  validateRequest,
  type BulkRequest,
  type ConflictRow,
  type BulkResult,
} from "./bulk-mutation-helpers.js";

export { BulkConflictError, CONTRACT } from "./bulk-mutation-helpers.js";

export default defineEndpoint((router, context) => {
  const { services, getSchema, database } = context;
  const { ItemsService } = services;
  const cache = new IdempotencyCache();

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
});

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
