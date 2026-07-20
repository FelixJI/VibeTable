import assert from "node:assert/strict";
import test from "node:test";

import { normalizeText } from "../dist/workers/normalize-text.js";

test("normalization preview uses the same deterministic transform as execution", () => {
  assert.equal(normalizeText("  Vibe   Table  ", "collapse-whitespace"), "Vibe Table");
});
