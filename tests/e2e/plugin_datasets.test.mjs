import assert from "node:assert/strict";
import test from "node:test";
import { exactSyntheticCents } from "./plugin_datasets.mjs";

test("independent synthetic dataset reconciler preserves cents beyond JS safe integers", () => {
  assert.equal(exactSyntheticCents([{ amount: "9007199254740993.01" }, { amount: "0.01" }, { amount: "-0.01" }], "amount"), "900719925474099301");
  for (const amount of [null, 0.1, "", "pending", "0.001"]) assert.throws(() => exactSyntheticCents([{ amount }], "amount"), /fixed-scale/);
});
