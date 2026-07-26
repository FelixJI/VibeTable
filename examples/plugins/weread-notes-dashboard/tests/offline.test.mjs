import assert from "node:assert/strict";
import test from "node:test";
import { existsSync } from "node:fs";
import { resolve } from "node:path";

test("plugin scaffold files exist", () => {
  const root = resolve("examples/plugins/weread-notes-dashboard");
  assert.ok(existsSync(resolve(root, "manifest.json")));
  assert.ok(existsSync(resolve(root, "schemas/action-input.v1.json")));
  assert.ok(existsSync(resolve(root, "schemas/action-output.v1.json")));
  assert.ok(existsSync(resolve(root, "src/weread-insights.ts")));
  assert.ok(existsSync(resolve(root, "dist/workers/weread-insights.js")));
  assert.ok(existsSync(resolve(root, "dist/views/weread-overview/index.html")));
  assert.ok(existsSync(resolve(root, "dist/views/weread-overview/index.js")));
  assert.ok(existsSync(resolve(root, "dist/views/weread-overview/index.css")));
});
