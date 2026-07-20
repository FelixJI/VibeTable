import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

test("custom page stays offline and uses only the host message channel", async () => {
  const source = await readFile(new URL("../dist/views/overview/index.js", import.meta.url), "utf8");
  assert.equal(/\b(fetch|XMLHttpRequest|WebSocket|EventSource)\b/u.test(source), false);
  assert.match(source, /vibetable\.plugin-surface\.v1/u);
  assert.match(source, /https:\/\/app\.vibetable\.local/u);
  assert.doesNotMatch(source, /postMessage\([^)]*,\s*["']\*["']/u);
});
