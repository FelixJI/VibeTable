/**
 * Pure helpers for vibetable-bulk-mutation — validation, conflict mapping and the
 * bounded idempotency cache.
 *
 * These functions have NO Directus dependency so they can be unit-tested without
 * a running instance or the @directus/extensions-sdk installed. The endpoint
 * glue (index.ts) imports them; the transaction integration is exercised in the
 * deployment environment.
 */

export const CONTRACT = "vibetable-bulk-mutation.v1";
export const MAX_OPERATIONS = 10_000;
const MAX_IDEMPOTENCY_ENTRIES = 1024;

export interface BulkOperation {
  kind: "create" | "update";
  primaryKey?: string;
  expectedDateUpdated?: string | null;
  values: Record<string, unknown>;
}

export interface BulkRequest {
  contract: string;
  collection: string;
  primaryKey: string;
  idempotencyKey: string;
  operations: BulkOperation[];
}

export interface ConflictRow {
  primaryKey: string;
  currentValue: Record<string, unknown>;
  expectedDateUpdated: string | null;
}

export interface BulkResult {
  createdRowKeys: string[];
  updatedRowKeys: string[];
  skippedRowKeys: string[];
  conflicts: ConflictRow[];
}

export interface CachedResult {
  status: number;
  body: unknown;
}

/**
 * Custom error carrying the conflict rows so the transaction wrapper can surface
 * them to the caller without losing the all-or-nothing rollback.
 */
export class BulkConflictError extends Error {
  readonly conflicts: ConflictRow[];
  constructor(conflicts: ConflictRow[]) {
    super("one or more rows changed since preview");
    this.name = "BulkConflictError";
    this.conflicts = conflicts;
  }
}

/** Pure request-envelope validation. */
export function validateRequest(
  body: BulkRequest | undefined,
  idempotencyKey: string | undefined,
): { ok: true } | { ok: false; error: string } {
  if (!body || typeof body !== "object") {
    return { ok: false, error: "request body is required" };
  }
  if (body.contract !== CONTRACT) {
    return { ok: false, error: `unsupported contract '${body.contract}'` };
  }
  if (!body.collection || !body.primaryKey) {
    return { ok: false, error: "collection and primaryKey are required" };
  }
  if (!idempotencyKey) {
    return { ok: false, error: "Idempotency-Key header is required" };
  }
  if (!Array.isArray(body.operations) || body.operations.length === 0) {
    return { ok: false, error: "operations must be a non-empty array" };
  }
  if (body.operations.length > MAX_OPERATIONS) {
    return {
      ok: false,
      error: `operations exceed the ${MAX_OPERATIONS} limit; use file import`,
    };
  }
  for (const op of body.operations) {
    if (op.kind !== "create" && op.kind !== "update") {
      return { ok: false, error: `unknown operation kind '${op.kind}'` };
    }
    if (op.kind === "update" && !op.primaryKey) {
      return { ok: false, error: "update operation requires a primaryKey" };
    }
    if (!op.values || typeof op.values !== "object") {
      return { ok: false, error: "operation values must be an object" };
    }
  }
  return { ok: true };
}

/** Pure error → HTTP outcome mapping. */
export function mapError(error: unknown): {
  status: number;
  body: unknown;
  cacheable: boolean;
} {
  if (error instanceof BulkConflictError) {
    return {
      status: 200,
      body: {
        data: {
          createdRowKeys: [],
          updatedRowKeys: [],
          skippedRowKeys: [],
          conflicts: error.conflicts,
        },
      },
      cacheable: true,
    };
  }
  const message = error instanceof Error ? error.message : "bulk mutation failed";
  return {
    status: 500,
    body: { errors: [{ message }] },
    cacheable: false,
  };
}

/** Bounded LRU idempotency cache (in-process; swap for a shared store in prod). */
export class IdempotencyCache {
  private readonly entries = new Map<string, CachedResult>();
  private readonly bound: number;

  constructor(bound: number = MAX_IDEMPOTENCY_ENTRIES) {
    this.bound = bound;
  }

  get(key: string): CachedResult | undefined {
    const entry = this.entries.get(key);
    if (entry) {
      this.entries.delete(key);
      this.entries.set(key, entry);
    }
    return entry;
  }

  set(key: string, value: CachedResult): void {
    if (this.entries.size >= this.bound) {
      const oldest = this.entries.keys().next();
      if (!oldest.done) {
        this.entries.delete(oldest.value as string);
      }
    }
    this.entries.set(key, value);
  }
}
