/**
 * Unit tests for the pure helpers of vibetable-bulk-mutation.
 *
 * These tests exercise request validation, error → outcome mapping and the
 * idempotency cache WITHOUT a running Directus instance or the
 * @directus/extensions-sdk installed. The transaction/integration behaviour is
 * covered by the deployment environment (see README.md).
 */

import assert from "node:assert";
import { test } from "node:test";

import {
  BulkConflictError,
  CONTRACT,
  IdempotencyCache,
  MAX_OPERATIONS,
  mapError,
  validateRequest,
  type BulkRequest,
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
