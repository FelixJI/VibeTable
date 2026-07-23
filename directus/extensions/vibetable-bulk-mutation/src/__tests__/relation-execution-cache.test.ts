import assert from "node:assert/strict";
import test from "node:test";

import { RelationExecutionCache } from "../relation-execution-cache.ts";

test("relation execution cache evicts the least-recently-used completed entry", () => {
  let now = 0;
  const cache = new RelationExecutionCache<string>(2, 1_000, () => now);
  assert.equal(cache.set("a", { fingerprint: "a", result: "A" }), true);
  now += 1;
  assert.equal(cache.set("b", { fingerprint: "b", result: "B" }), true);
  assert.equal(cache.get("a")?.result, "A");
  now += 1;
  assert.equal(cache.set("c", { fingerprint: "c", result: "C" }), true);
  assert.equal(cache.get("b"), undefined);
  assert.equal(cache.get("a")?.result, "A");
  assert.equal(cache.get("c")?.result, "C");
});

test("relation execution cache expires completed entries but preserves in-flight work", async () => {
  let now = 0;
  const cache = new RelationExecutionCache<string>(1, 10, () => now);
  let resolve!: (value: string) => void;
  const inFlight = new Promise<string>((done) => { resolve = done; });
  assert.equal(cache.set("active", { fingerprint: "a", inFlight }), true);
  now = 11;
  assert.equal(cache.set("other", { fingerprint: "b", result: "B" }), false);
  resolve("A");
  await inFlight;
  const active = cache.get("active")!;
  active.inFlight = undefined;
  now = 22;
  assert.equal(cache.set("other", { fingerprint: "b", result: "B" }), true);
  assert.equal(cache.get("active"), undefined);
});
