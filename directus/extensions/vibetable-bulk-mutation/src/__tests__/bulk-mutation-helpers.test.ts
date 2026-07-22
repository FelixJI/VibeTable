/**
 * Unit tests for the pure helpers of vibetable-bulk-mutation.
 *
 * These tests exercise request validation, error → outcome mapping and the
 * idempotency cache WITHOUT a running Directus instance or the
 * @directus/extensions-sdk installed. The transaction/integration behaviour is
 * covered by the deployment environment (see README.md).
 */

import assert from "node:assert";
import { createHmac } from "node:crypto";
import { test } from "node:test";

import {
  BulkConflictError,
  CONTRACT,
  IdempotencyCache,
  MAX_OPERATIONS,
  RESTORE_CONTRACT,
  RESTORE_PROOF_CONTRACT,
  RestoreAuthorizationCache,
  RestoreProofReplayCache,
  mapError,
  validateHistoryMarkersRequest,
  validateRestoreApplyRequest,
  validateRestoreRequest,
  validateRequest,
  verifyRestoreProof,
  type BulkRequest,
  type RestoreRequest,
} from "../bulk-mutation-helpers.ts";

function validBody(overrides: Partial<BulkRequest> = {}): BulkRequest {
  return {
    contract: CONTRACT,
    collection: "vibetable_contracts",
    primaryKey: "id",
    idempotencyKey: "idem-1",
    operations: [{ kind: "create", values: { number: "A-1" } }],
    ...overrides,
  };
}

test("validateRequest accepts a well-formed request", () => {
  const result = validateRequest(validBody(), "idem-1");
  assert.equal(result.ok, true);
});

test("validateRestoreRequest accepts the narrow single-record CAS contract", () => {
  const body: RestoreRequest = {
    contract: RESTORE_CONTRACT,
    collection: "orders",
    itemId: "1",
    targetRevision: "42",
    scope: "cell",
    field: "title",
    schemaRevision: "schema-1",
    values: { title: "Earlier" },
    expectedValues: { title: "Current" },
  };
  assert.equal(validateRestoreRequest(body).ok, true);
  assert.equal(validateRestoreRequest({ ...body, values: {} }).ok, false);
  assert.equal(
    validateRestoreRequest({ ...body, values: { title: "Earlier", amount: 1 } }).ok,
    false,
  );
  assert.equal(
    validateRestoreApplyRequest(
      { contract: RESTORE_CONTRACT, authorizationToken: "token" },
      "restore-1",
    ).ok,
    true,
  );
});

test("RestoreAuthorizationCache binds user and consumes a token once", () => {
  let now = 1000;
  const cache = new RestoreAuthorizationCache(2, () => now);
  const request: RestoreRequest = {
    contract: RESTORE_CONTRACT,
    collection: "orders",
    itemId: "1",
    targetRevision: "42",
    scope: "row",
    schemaRevision: "schema-1",
    values: { title: "Earlier" },
    expectedValues: { title: "Current" },
  };
  const authorization = cache.authorize(request, "user-1", 100);
  assert.equal(cache.consume(authorization.token, "other-user"), undefined);
  assert.equal(cache.consume(authorization.token, "user-1")?.targetRevision, "42");
  const second = cache.authorize(request, "user-1", 100);
  now = 1001;
  assert.equal(cache.consume(second.token, "user-1")?.targetRevision, "42");
  assert.equal(cache.consume(second.token, "user-1"), undefined);
});

test("RestoreAuthorizationCache applies its quota per user", () => {
  const cache = new RestoreAuthorizationCache(1, () => 1000);
  const request: RestoreRequest = {
    contract: RESTORE_CONTRACT,
    collection: "orders",
    itemId: "1",
    targetRevision: "42",
    scope: "row",
    schemaRevision: "schema-1",
    values: { title: "Earlier" },
    expectedValues: { title: "Current" },
  };
  const firstUser = cache.authorize(request, "user-1");
  cache.authorize(request, "user-2");
  cache.authorize(request, "user-2");
  assert.equal(cache.consume(firstUser.token, "user-1")?.itemId, "1");
});

test("verifyRestoreProof authenticates and consumes a signed preview once", () => {
  const secret = "test-history-proof-secret";
  const request: RestoreRequest = {
    contract: RESTORE_CONTRACT,
    collection: "orders",
    itemId: "1",
    targetRevision: "42",
    scope: "cell",
    field: "title",
    schemaRevision: "schema-1",
    values: { title: "Earlier" },
    expectedValues: { title: "Current" },
  };
  const issuedAt = 1000;
  const nonce = "unique-preview-nonce-1";
  const subject = "a".repeat(64);
  const payload = Buffer.from(JSON.stringify(request), "utf8").toString("base64url");
  const signature = createHmac("sha256", secret)
    .update(`${issuedAt}\n${nonce}\n${subject}\n${payload}`, "utf8")
    .digest("hex");
  const proof = {
    contract: RESTORE_PROOF_CONTRACT,
    issuedAt,
    nonce,
    payload,
    subject,
    signature,
  };
  const replayCache = new RestoreProofReplayCache(2, () => issuedAt);
  const verified = verifyRestoreProof(proof, secret, replayCache, subject, issuedAt);
  assert.equal(verified.ok, true);
  assert.equal(verified.ok && verified.request.field, "title");
  assert.equal(verifyRestoreProof(proof, secret, replayCache, subject, issuedAt).ok, false);
  assert.equal(
    verifyRestoreProof(
      { ...proof, nonce: "another-preview-nonce" },
      secret,
      replayCache,
      subject,
      issuedAt,
    ).ok,
    false,
  );
  assert.equal(
    verifyRestoreProof(proof, secret, new RestoreProofReplayCache(), "b".repeat(64), issuedAt)
      .ok,
    false,
  );
});

test("validateHistoryMarkersRequest bounds private marker lookups", () => {
  assert.equal(validateHistoryMarkersRequest({ activityIds: [1, "2"] }).ok, true);
  assert.equal(validateHistoryMarkersRequest({ activityIds: Array(501).fill(1) }).ok, false);
});

test("validateRequest rejects a missing body", () => {
  const result = validateRequest(undefined, "idem-1");
  assert.equal(result.ok, false);
});

test("validateRequest rejects an unsupported contract", () => {
  const result = validateRequest(validBody({ contract: "other" }), "idem-1");
  assert.equal(result.ok, false);
  assert.match((result as { error: string }).error, /unsupported contract/);
});

test("validateRequest rejects a missing collection", () => {
  const result = validateRequest(validBody({ collection: "" }), "idem-1");
  assert.equal(result.ok, false);
});

test("validateRequest rejects a missing idempotency key", () => {
  const result = validateRequest(validBody(), undefined);
  assert.equal(result.ok, false);
  assert.match((result as { error: string }).error, /Idempotency-Key/);
});

test("validateRequest rejects an empty operations array", () => {
  const result = validateRequest(validBody({ operations: [] }), "idem-1");
  assert.equal(result.ok, false);
});

test("validateRequest rejects an oversize batch", () => {
  const ops = Array.from({ length: MAX_OPERATIONS + 1 }, () => ({
    kind: "create" as const,
    values: { x: 1 },
  }));
  const result = validateRequest(validBody({ operations: ops }), "idem-1");
  assert.equal(result.ok, false);
  assert.match((result as { error: string }).error, /exceed the/);
});

test("validateRequest rejects an unknown operation kind", () => {
  const result = validateRequest(
    validBody({ operations: [{ kind: "delete" as never, values: {} }] }),
    "idem-1",
  );
  assert.equal(result.ok, false);
  assert.match((result as { error: string }).error, /unknown operation kind/);
});

test("validateRequest rejects an update without a primaryKey", () => {
  const result = validateRequest(
    validBody({ operations: [{ kind: "update", values: {} }] }),
    "idem-1",
  );
  assert.equal(result.ok, false);
  assert.match((result as { error: string }).error, /primaryKey/);
});

test("mapError returns the conflict body and is cacheable", () => {
  const conflicts = [
    { primaryKey: "1", currentValue: { number: "x" }, expectedDateUpdated: "rev-1" },
  ];
  const outcome = mapError(new BulkConflictError(conflicts));
  assert.equal(outcome.status, 200);
  assert.equal(outcome.cacheable, true);
  assert.equal((outcome.body as { data: { conflicts: unknown[] } }).data.conflicts.length, 1);
});

test("mapError maps a generic error to a 500 and is not cacheable", () => {
  const outcome = mapError(new Error("boom"));
  assert.equal(outcome.status, 500);
  assert.equal(outcome.cacheable, false);
});

test("IdempotencyCache returns the stored result and evicts the oldest entry", () => {
  const cache = new IdempotencyCache(2);
  cache.set("a", { status: 200, body: { a: 1 } });
  cache.set("b", { status: 200, body: { b: 2 } });
  assert.deepEqual(cache.get("a")?.body, { a: 1 });
  cache.set("c", { status: 200, body: { c: 3 } }); // evicts "b"
  assert.equal(cache.get("b"), undefined);
  assert.deepEqual(cache.get("c")?.body, { c: 3 });
});
