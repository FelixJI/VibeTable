import test from "node:test";
import assert from "node:assert/strict";
import {
  calculationChainDisplayText, calculationChainUiFixture, calculationChainUiOracle,
} from "./calculation_chain_journey.mjs";

test("S38 independent display oracle pins the default grouped DOM number text", () => {
  assert.equal(calculationChainDisplayText(1982), "1,982");
  assert.equal(calculationChainDisplayText(1983), "1,983");
  assert.equal(calculationChainDisplayText(2982), "2,982");
  assert.equal(calculationChainDisplayText(2983), "2,983");
  assert.equal(calculationChainDisplayText(991), "991");
  assert.equal(calculationChainDisplayText(7), "7");
  assert.equal(calculationChainDisplayText(0), "0");
  const raw = [["合同甲", 991, 1982], ["合同丙", 0, 0]];
  const displayed = raw.map(row => row.map(calculationChainDisplayText));
  assert.deepEqual(displayed, [["合同甲", "991", "1,982"], ["合同丙", "0", "0"]]);
  assert.deepEqual(raw, [["合同甲", 991, 1982], ["合同丙", 0, 0]]);
});

test("S38 independent oracle pins 199/1/0 sources and both UI edits", () => {
  const sources = calculationChainUiFixture();
  assert.equal(sources.length, 201);
  assert.equal(new Set(sources.map(row => row.marker)).size, 201);
  assert.deepEqual(calculationChainUiOracle(sources, 2), [
    { contract: "合同甲", matches: 199, sum: 991, doubled: 1982, total: 1983 },
    { contract: "合同乙", matches: 1, sum: 7, doubled: 14, total: 15 },
    { contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 },
  ]);
  sources[0].amount += 3;
  assert.deepEqual(calculationChainUiOracle(sources, 3), [
    { contract: "合同甲", matches: 199, sum: 994, doubled: 2982, total: 2983 },
    { contract: "合同乙", matches: 1, sum: 7, doubled: 21, total: 22 },
    { contract: "合同丙", matches: 0, sum: 0, doubled: 0, total: 1 },
  ]);
});
