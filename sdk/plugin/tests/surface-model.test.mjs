import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { sdk, testing } from "./load-sdk.mjs";

test("model calls have no catalog, mappings, approval or implicit replay channel", async () => {
  const calls = [];
  let receive;
  const channel = {
    postMessage: message => calls.push(message),
    addEventListener: (_type, listener) => { receive = listener; },
    removeEventListener() {},
  };
  const client = sdk.createSurfaceClient("owned-model", channel);
  const pending = client.model.configure({ mode: "create" });
  const request = calls.at(-1);
  assert.equal(request.method, "model.configure");
  assert.deepEqual(request.params, { mode: "create" });
  receive({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: request.requestId,
    error: { code: "plugin_commit_unknown", message: "Inspect model.read before retry." } } });
  await assert.rejects(pending, error => error.code === "plugin_commit_unknown");
  assert.equal(calls.length, 1);
  const reading = client.model.read();
  const read = calls.at(-1);
  receive({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: read.requestId,
    result: { contract: "vibetable.plugin-model.v1", status: "partial", completed: 2, total: 8, bindings: {} } } });
  assert.equal((await reading).status, "partial");
  assert.equal("catalog" in client.model, false);
  client.dispose();
});

test("offline host does not invent native model support", async () => {
  const host = testing.createOfflineSurfaceHost({ pluginId: "synthetic", workspaceId: "workspace-a", surfaceToken: "surface-a" });
  const client = sdk.createSurfaceClient("surface-a", host.transport);
  await assert.rejects(client.model.configure({ mode: "create" }), error => error.code === "plugin_api_unsupported");
  client.dispose();
});

test("native model catalog stays closed and bounded in the shared contract", () => {
  const contract = JSON.parse(readFileSync(new URL("../../../contracts/plugin-surface-api.v1.json", import.meta.url), "utf8"));
  assert.deepEqual(contract.model.hostOnly, ["model.catalog", "model.preview", "model.commit"]);
  for (const method of contract.model.hostOnly) assert.equal(contract.methods.includes(method), false);
  assert.equal(contract.model.catalog.pageItems, 32);
  assert.equal(contract.model.catalog.messageBytes, contract.limits.messageBytes);
  assert.equal(contract.limits.messageBytes, 1048576);
});
