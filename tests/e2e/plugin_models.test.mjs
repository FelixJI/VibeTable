import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { runPluginModels } from "./plugin_models.mjs";

// This is a scene-assertion harness, not a plugin test Host or GUI evidence.
// It verifies that the cold-reopen acceptance cannot pass on stale identity,
// changed canonical bindings, lost user data, or broadened grants.
async function coldScene(t, fault = "") {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "vibetable-model-scene-"));
  t.after(async () => {
    for (const name of ["seed.json", "54-models-reopened.json"]) await fs.rm(path.join(dir, name), { force: true });
    await fs.rmdir(dir);
  });
  const seed = {
    workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 2, projectKey: "local:synthetic",
    bindings: { assets: { tableId: "tbl_assets", fields: { name: "fld_name", checked: "fld_checked" } } },
    userData: { tableId: "tbl_assets", fieldId: "fld_private", physicalName: "private_value", value: "synthetic preserved value",
      recordId: "row_synthetic", renamedFieldId: "fld_name", renamedLabel: "User renamed",
      corePhysicalName: "core_name", coreValue: "synthetic asset" },
  };
  const statePath = path.join(dir, "seed.json");
  await fs.writeFile(statePath, JSON.stringify(seed));
  const reopened = { status: "completed", bindings: structuredClone(seed.bindings) };
  if (fault === "binding") reopened.bindings.assets.tableId = "tbl_replacement";
  const chain = { click: async () => {}, waitFor: async () => {}, filter: () => chain };
  const frame = {
    url: () => "https://example.plugins.vibetable.local/ui/index.html",
    locator: () => ({ innerText: async () => JSON.stringify(reopened) }),
    evaluate: async () => ({ result: { fields: [
      { fieldId: "id" }, { fieldId: "fld_name" }, { fieldId: "fld_checked" },
      ...(fault === "grant" ? [{ fieldId: "fld_private" }] : []),
    ] } }),
  };
  const page = { getByTestId: () => chain, frameLocator: () => ({ locator: () => chain }), frames: () => [frame] };
  const checks = [];
  const recorder = { check: (name, passed) => { checks.push(name); assert.ok(passed, name); } };
  const helpers = {
    activateRestartedWorkspace: async () => ({ switched: { result: {
      workspaceId: fault === "workspace" ? "22222222-2222-4222-8222-222222222222" : seed.workspaceId,
      sessionEpoch: fault === "epoch" ? seed.sessionEpoch : seed.sessionEpoch + 1,
    } } }),
    rawBridgeRequest: async (_page, method) => {
      if (method === "schema.getTable") return { payload: { fields: [
        { identity: { fieldId: "fld_name" }, displayName: fault === "rename" ? "lost rename" : seed.userData.renamedLabel },
        { identity: { fieldId: "fld_private" }, displayName: "User private" },
      ] } };
      assert.equal(method, "query.page");
      return { payload: { rows: [{ id: seed.userData.recordId, core_name: seed.userData.coreValue,
        private_value: fault === "value" ? "overwritten" : seed.userData.value }] } };
    },
  };
  const run = () => runPluginModels(page, recorder, { evidenceDir: dir }, helpers, statePath);
  return { dir, seed, checks, run };
}

test("model cold-reopen scene requires both persisted bindings and ungranted user data evidence", async t => {
  const fixture = await coldScene(t);
  const result = await fixture.run();
  assert.equal(result.workspaceId, fixture.seed.workspaceId);
  assert.equal(result.sessionEpoch, 3);
  assert.deepEqual(result.bindings, fixture.seed.bindings);
  assert.equal(fixture.checks.length, 4);
  const evidence = JSON.parse(await fs.readFile(path.join(fixture.dir, "54-models-reopened.json"), "utf8"));
  assert.equal(evidence.previousEpoch, 2);
  assert.equal(evidence.preserved.rows[0].private_value, fixture.seed.userData.value);
});

for (const fault of ["workspace", "epoch", "binding", "value", "rename", "grant"]) {
  test(`model cold-reopen scene rejects ${fault} regression`, async t => {
    const fixture = await coldScene(t, fault);
    await assert.rejects(fixture.run, assert.AssertionError);
    await assert.rejects(fs.access(path.join(fixture.dir, "54-models-reopened.json")), { code: "ENOENT" });
  });
}
