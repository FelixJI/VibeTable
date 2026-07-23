import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { directusReadError, errorResponse, LookupQueryError } from "../errors.ts";

describe("stable lookup errors", () => {
  it("maps every declared domain error without changing its code", () => {
    const cases = [
      ["VIBETABLE_ACCOUNTABILITY_REQUIRED", 401],
      ["VIBETABLE_LOOKUP_PLAN_INVALID", 400],
      ["VIBETABLE_LOOKUP_UNSUPPORTED", 422],
      ["VIBETABLE_LOOKUP_TOO_EXPENSIVE", 422],
      ["VIBETABLE_LOOKUP_RESTRICTED", 403],
      ["VIBETABLE_LOOKUP_SCHEMA_INVALID", 409],
      ["VIBETABLE_LOOKUP_INTERNAL", 500],
    ] as const;
    for (const [code, status] of cases) {
      const outcome = errorResponse(new LookupQueryError(code, "safe", { path: "lookup.x" }));
      assert.equal(outcome.status, status);
      assert.equal(outcome.body.errors[0]!.extensions.code, code);
      assert.deepEqual(outcome.body.errors[0]!.extensions.details, { path: "lookup.x" });
    }
  });

  it("maps Directus denial separately from schema drift", () => {
    assert.equal(
      directusReadError({ status: 403 }, { collection: "contracts" }).code,
      "VIBETABLE_LOOKUP_RESTRICTED",
    );
    assert.equal(
      directusReadError(new Error("missing field"), { collection: "contracts" }).code,
      "VIBETABLE_LOOKUP_SCHEMA_INVALID",
    );
  });

  it("does not expose unexpected internal exception text", () => {
    const outcome = errorResponse(new Error("database secret"));
    assert.equal(outcome.status, 500);
    assert.equal(outcome.body.errors[0]!.message, "lookup query failed");
    assert.equal(outcome.body.errors[0]!.extensions.code, "VIBETABLE_LOOKUP_INTERNAL");
  });
});
