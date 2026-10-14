import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

test("built isolated surface bundles public SDK without Worker or runtime imports", async () => {
  const manifest = JSON.parse(await readFile(new URL("../manifest.json", import.meta.url), "utf8"));
  assert.equal(manifest.$schema, "vibetable.plugin-manifest.v2");
  assert.equal(manifest.compatibility.pluginApi, "3.x");
  assert.ok(manifest.actions.every(action => !("workerEntry" in action)));
  const source = await readFile(new URL("../dist/surface.js", import.meta.url), "utf8");
  assert.match(source, /vibetable\.plugin-api\.v1/);
  assert.match(source, /chrome\?\.webview/);
  assert.doesNotMatch(source, /\b(?:fetch|XMLHttpRequest|WebSocket|importScripts)\s*\(/);
  assert.doesNotMatch(source, /\bimport\s*(?:\(|["'])/);
});
