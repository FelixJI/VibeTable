/**
 * Pure helpers for vibetable-bulk-mutation — validation, conflict mapping and the
 * bounded idempotency cache.
 *
 * These functions have NO Directus dependency so they can be unit-tested without
 * a running instance or the @directus/extensions-sdk installed. The endpoint
 * glue (index.ts) imports them; the transaction integration is exercised in the
 * deployment environment.
 */

import { createHmac, randomUUID, timingSafeEqual } from "node:crypto";

export const CONTRACT = "vibetable-bulk-mutation.v1";
export const RESTORE_CONTRACT = "vibetable-history-restore.v1";
export const RESTORE_PROOF_CONTRACT = "vibetable-history-preview-proof.v1";
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

export interface RestoreRequest {
  contract: string;
  collection: string;
  itemId: string;
  targetRevision: string;
  scope: "row" | "cell" | "archived";
  field?: string | null;
  schemaRevision: string;
  values: Record<string, unknown>;
  expectedValues: Record<string, unknown>;
}

export interface RestoreApplyRequest {
  contract: string;
  authorizationToken: string;
}

export interface RestoreProofRequest {
  contract: string;
  issuedAt: number;
  nonce: string;
  payload: string;
  subject: string;
  signature: string;
}

export interface HistoryMarkersRequest {
  activityIds: Array<string | number>;
}

export interface CachedResult {
  status: number;
  body: unknown;
}

interface RestoreAuthorization {
  request: RestoreRequest;
  user: string;
  expiresAt: number;
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

/** Validate the narrow, server-marked single-record restore contract. */
export function validateRestoreRequest(
  body: RestoreRequest | undefined,
): { ok: true } | { ok: false; error: string } {
  if (!body || typeof body !== "object") {
    return { ok: false, error: "request body is required" };
  }
  if (body.contract !== RESTORE_CONTRACT) {
    return { ok: false, error: `unsupported contract '${body.contract}'` };
  }
  if (!body.collection || !body.itemId || !body.targetRevision || !body.schemaRevision) {
    return {
      ok: false,
      error: "collection, itemId, targetRevision and schemaRevision are required",
    };
  }
  if (!["row", "cell", "archived"].includes(body.scope)) {
    return { ok: false, error: "scope is invalid" };
  }
  if (body.scope === "cell" && !body.field) {
    return { ok: false, error: "field is required for cell scope" };
  }
  if (!body.values || typeof body.values !== "object" || Array.isArray(body.values)) {
    return { ok: false, error: "values must be an object" };
  }
  if (Object.keys(body.values).length === 0 || Object.keys(body.values).length > 100) {
    return { ok: false, error: "values must contain between 1 and 100 fields" };
  }
  if (
    body.scope === "cell" &&
    (Object.keys(body.values).length !== 1 || !body.field || !(body.field in body.values))
  ) {
    return { ok: false, error: "cell restore values must contain only the selected field" };
  }
  if (
    !body.expectedValues ||
    typeof body.expectedValues !== "object" ||
    Array.isArray(body.expectedValues)
  ) {
    return { ok: false, error: "expectedValues must be an object" };
  }
  if (
    Object.keys(body.expectedValues).length === 0 ||
    Object.keys(body.values).some((field) => !(field in body.expectedValues))
  ) {
    return { ok: false, error: "expectedValues must cover every restored field" };
  }
  return { ok: true };
}

export function validateRestoreApplyRequest(
  body: RestoreApplyRequest | undefined,
  idempotencyKey: string | undefined,
): { ok: true } | { ok: false; error: string } {
  if (!body || body.contract !== RESTORE_CONTRACT || !body.authorizationToken) {
    return { ok: false, error: "a valid restore authorization token is required" };
  }
  if (!idempotencyKey) {
    return { ok: false, error: "Idempotency-Key header is required" };
  }
  return { ok: true };
}

export class RestoreAuthorizationCache {
  private readonly entries = new Map<string, RestoreAuthorization>();
  private readonly userTokens = new Map<string, Map<string, true>>();
  private readonly perUserBound: number;
  private readonly totalBound: number;
  private readonly now: () => number;

  constructor(
    perUserBound = 32,
    now: () => number = Date.now,
    totalBound = MAX_IDEMPOTENCY_ENTRIES * 32,
  ) {
    this.perUserBound = perUserBound;
    this.totalBound = totalBound;
    this.now = now;
  }

  authorize(request: RestoreRequest, user: string, ttlMs = 5 * 60_000): {
    token: string;
    expiresAt: string;
  } {
    this.pruneExpired();
    const tokens = this.userTokens.get(user) ?? new Map<string, true>();
    while (tokens.size >= this.perUserBound) {
      const oldest = tokens.keys().next();
      if (oldest.done) break;
      tokens.delete(oldest.value);
      this.entries.delete(oldest.value);
    }
    if (this.entries.size >= this.totalBound) {
      throw new Error("restore authorization capacity is exhausted");
    }
    const token = randomUUID();
    const expiresAt = this.now() + ttlMs;
    this.entries.set(token, { request, user, expiresAt });
    tokens.set(token, true);
    this.userTokens.set(user, tokens);
    return { token, expiresAt: new Date(expiresAt).toISOString() };
  }

  consume(token: string, user: string): RestoreRequest | undefined {
    const authorization = this.entries.get(token);
    if (!authorization || authorization.user !== user) return undefined;
    this.entries.delete(token);
    const tokens = this.userTokens.get(authorization.user);
    tokens?.delete(token);
    if (tokens?.size === 0) this.userTokens.delete(authorization.user);
    if (this.now() >= authorization.expiresAt) return undefined;
    return authorization.request;
  }

  private pruneExpired(): void {
    const now = this.now();
    for (const [token, authorization] of this.entries) {
      if (now < authorization.expiresAt) continue;
      this.entries.delete(token);
      const tokens = this.userTokens.get(authorization.user);
      tokens?.delete(token);
      if (tokens?.size === 0) this.userTokens.delete(authorization.user);
    }
  }
}

export class RestoreProofReplayCache {
  private readonly nonces = new Map<string, number>();
  private readonly bound: number;
  private readonly now: () => number;

  constructor(bound = MAX_IDEMPOTENCY_ENTRIES, now: () => number = Date.now) {
    this.bound = bound;
    this.now = now;
  }

  consume(nonce: string, expiresAt: number): boolean {
    const now = this.now();
    for (const [storedNonce, storedExpiry] of this.nonces) {
      if (now >= storedExpiry) this.nonces.delete(storedNonce);
    }
    if (this.nonces.has(nonce) || this.nonces.size >= this.bound) return false;
    this.nonces.set(nonce, expiresAt);
    return true;
  }
}

export function verifyRestoreProof(
  body: RestoreProofRequest | undefined,
  secret: string | undefined,
  replayCache: RestoreProofReplayCache,
  expectedSubject: string,
  now = Date.now(),
): { ok: true; request: RestoreRequest } | { ok: false; error: string } {
  if (!secret) return { ok: false, error: "restore proof secret is unavailable" };
  if (
    !body ||
    body.contract !== RESTORE_PROOF_CONTRACT ||
    !Number.isSafeInteger(body.issuedAt) ||
    typeof body.nonce !== "string" ||
    body.nonce.length < 16 ||
    body.nonce.length > 128 ||
    typeof body.payload !== "string" ||
    body.payload.length === 0 ||
    body.payload.length > 2_000_000 ||
    typeof body.subject !== "string" ||
    !/^[0-9a-f]{64}$/i.test(body.subject) ||
    typeof body.signature !== "string" ||
    !/^[0-9a-f]{64}$/i.test(body.signature)
  ) {
    return { ok: false, error: "restore preview proof is malformed" };
  }
  const expiresAt = body.issuedAt + 60_000;
  if (body.issuedAt > now + 10_000 || now >= expiresAt) {
    return { ok: false, error: "restore preview proof is expired" };
  }
  const subject = Buffer.from(body.subject, "hex");
  const expectedSubjectBytes = Buffer.from(expectedSubject, "hex");
  if (
    subject.length !== expectedSubjectBytes.length ||
    !timingSafeEqual(subject, expectedSubjectBytes)
  ) {
    return { ok: false, error: "restore preview proof belongs to another session" };
  }
  const material = `${body.issuedAt}\n${body.nonce}\n${body.subject}\n${body.payload}`;
  const expected = createHmac("sha256", secret).update(material, "utf8").digest();
  const supplied = Buffer.from(body.signature, "hex");
  if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) {
    return { ok: false, error: "restore preview proof is invalid" };
  }
  let decoded: unknown;
  try {
    decoded = JSON.parse(Buffer.from(body.payload, "base64url").toString("utf8"));
  } catch {
    return { ok: false, error: "restore preview proof payload is invalid" };
  }
  const validation = validateRestoreRequest(decoded as RestoreRequest | undefined);
  if (!validation.ok) return validation;
  if (!replayCache.consume(body.nonce, expiresAt)) {
    return { ok: false, error: "restore preview proof was already used" };
  }
  return { ok: true, request: decoded as RestoreRequest };
}

export function validateHistoryMarkersRequest(
  body: HistoryMarkersRequest | undefined,
): { ok: true } | { ok: false; error: string } {
  if (!body || !Array.isArray(body.activityIds)) {
    return { ok: false, error: "activityIds must be an array" };
  }
  if (body.activityIds.length > 500) {
    return { ok: false, error: "activityIds cannot exceed 500 entries" };
  }
  if (body.activityIds.some((value) => !String(value))) {
    return { ok: false, error: "activityIds cannot contain empty values" };
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
