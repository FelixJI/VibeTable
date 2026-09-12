import { expect, it } from "vitest";
import { gridStateNumberText, parseGridStateJson } from "./gridStateJson";

it.each([
  ["0.10", 0.1], ["1.2300e+2", 123], ["-7E-2", -0.07],
  ["1000000000000000000000", 1e21], ["9007199254740992", 9007199254740992],
  ["0e9007199254740993", 0],
])("keeps a decimal-equivalent token %s as an ordinary number", (token, value) => {
  const parsed = parseGridStateJson(String(token));
  expect(typeof parsed).toBe("number");
  expect(parsed).toBe(value);
  expect(gridStateNumberText(parsed)).toBeNull();
});

it("preserves signed zero and nested numeric operands through JSON serialization", () => {
  const source = '{"value":[-0,{"decimal":0.1234567890123456789,"overflow":1e400,"underflow":1e-400}]}';
  expect(JSON.stringify(parseGridStateJson(source))).toBe(source);
});
