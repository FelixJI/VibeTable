import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { aggregateValues, normalizeScalar } from "../values.ts";

describe("strongly typed lookup values", () => {
  it("sums and averages decimal strings without binary floating point", () => {
    const values = [{ value: "0.10" }, { value: "0.20" }, { value: null }];
    assert.equal(aggregateValues(values, "sum", { kind: "decimal", scale: 2 }), "0.30");
    assert.equal(aggregateValues(values, "avg", { kind: "decimal", scale: 2 }), "0.15");
    assert.equal(
      aggregateValues([{ value: "0.0049" }, { value: "0.0049" }], "sum", { kind: "decimal", scale: 2 }),
      "0.01",
    );
    assert.equal(
      aggregateValues([{ value: "1" }, { value: "0" }, { value: "0" }], "avg", { kind: "decimal", scale: 2 }),
      "0.33",
    );
  });

  it("applies the frozen null rules", () => {
    const values = [{ value: null }, { value: "2.00" }, { value: null }];
    assert.deepEqual(aggregateValues(values, "list", { kind: "decimal", scale: 2 }), [null, "2.00", null]);
    assert.deepEqual(aggregateValues(values, "distinct", { kind: "decimal", scale: 2 }), [null, "2.00"]);
    assert.equal(aggregateValues(values, "count", { kind: "integer" }), 3);
    assert.equal(aggregateValues(values, "count_non_null", { kind: "integer" }), 1);
    assert.equal(aggregateValues([{ value: null }], "sum", { kind: "decimal", scale: 2 }), null);
  });

  it("keeps M2A list provenance including null values", () => {
    const value = aggregateValues([
      { collection: "notes", itemId: "n1", value: null },
      { collection: "assets", itemId: "a1", value: "file.pdf" },
    ], "list", { kind: "string" }, true);
    assert.deepEqual(value, [
      { collection: "notes", itemId: "n1", value: null },
      { collection: "assets", itemId: "a1", value: "file.pdf" },
    ]);
  });

  it("normalizes offset date-times to UTC and rejects implicit offsets", () => {
    assert.equal(normalizeScalar("2026-07-22T12:00:00+08:00", { kind: "datetime" }), "2026-07-22T04:00:00.000Z");
    assert.throws(() => normalizeScalar("2026-07-22T12:00:00", { kind: "datetime" }), /offset/);
  });
});
