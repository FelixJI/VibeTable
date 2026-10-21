import assert from "node:assert/strict";
import test from "node:test";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// This directory is a frozen historical fixture (#486): a real legacy
// manifest v1 / pluginApi 1.x worker package kept for read-only inspection
// diagnostics and preservation tests. It must never be rebuilt or installed.
const root = fileURLToPath(new URL("../", import.meta.url));

test("frozen legacy fixture keeps its v1 worker manifest and real package", () => {
  const manifest = JSON.parse(readFileSync(`${root}manifest.json`, "utf8"));
  assert.equal(manifest["$schema"], "vibetable.plugin-manifest.v1");
  assert.equal(manifest.compatibility.pluginApi, "1.x");
  assert.equal(manifest.pluginId, "com.vibetable.examples.weread-notes-dashboard");
  assert.ok(manifest.actions.every(action => typeof action.workerEntry === "string"));
  assert.ok(existsSync(`${root}schemas/action-input.v1.json`));
  assert.ok(existsSync(`${root}schemas/action-output.v1.json`));
  assert.ok(existsSync(`${root}src/weread-insights.ts`));
  // The single retained real old-package ZIP for CLI legacy diagnostics.
  assert.ok(existsSync(`${root}weread-notes-dashboard.vtplugin`));
});

test("frozen fixture no longer carries duplicate release verification copies", () => {
  assert.equal(existsSync(`${root}release`), false);
  assert.equal(existsSync(`${root}tmp`), false);
  assert.equal(existsSync(`${root}../weread-notes-dashboard.vtplugin`), false);
});
