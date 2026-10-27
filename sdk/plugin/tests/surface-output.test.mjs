import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";
const { createSurfaceOutputClient } = await import(new URL(`../../../build/plugin-sdk/${process.pid}/surface-output.js`, import.meta.url));

const source = { templateVersion: "template-1", dataVersion: "business-7", purpose: "issued" };
function harness() {
  const calls = [], releases = [], saved = new Map(), sizes = new Map();
  let id = 0, failSave = false;
  const call = async (method, params) => {
    calls.push({ method, params });
    if (method === "output.create") {
      const outputId = `out-${++id}`; sizes.set(outputId, params.sizeBytes);
      return { outputId, contract: sdk.SURFACE_OUTPUT_CONTRACT, maxBytes: 33554432, blockBytes: 262144 };
    }
    if (method === "output.write") return { offset: params.offset + atob(params.base64).length };
    if (method === "output.save") {
      const result = { outputId: params.outputId, saveId: params.saveId, state: "succeeded", committed: true, bytes: sizes.get(params.outputId), displayName: "issued.xlsx", source };
      saved.set(params.saveId, result);
      if (failSave) throw new sdk.PluginCapabilityError("plugin_commit_unknown", "ack lost");
      return result;
    }
    if (method === "output.status") return saved.get(params.saveId);
    if (method === "output.reopen") return { outputId: `out-${++id}`, contract: sdk.SURFACE_OUTPUT_CONTRACT, source, sizeBytes: 1 };
    if (method === "output.previewArchive") return { planId: "host-plan", affectedCount: 1, artifactId: params.outputId };
    return {};
  };
  return { calls, releases, set failSave(value) { failSave = value; },
    client: createSurfaceOutputClient(call, (method, params) => releases.push({ method, params })) };
}
const options = { suggestedName: "issued.xlsx", mediaType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", source };

test("output owns an immutable >1 MiB snapshot and never submits business while saving or reprinting", async () => {
  const h = harness();
  const bytes = new Uint8Array(1048593).fill(7);
  const creating = h.client.create(bytes, options);
  bytes.fill(99);
  const output = await creating;
  const blocks = h.calls.filter(call => call.method === "output.write");
  assert.equal(blocks.length, 5);
  assert.ok(blocks.every(call => atob(call.params.base64).length <= 262144));
  assert.equal(atob(blocks[0].params.base64).charCodeAt(0), 7);
  assert.equal(h.calls.at(-1).method, "output.seal");
  assert.deepEqual(output.source, source);
  const first = output.save("save-once");
  assert.equal(first, output.save("save-once"));
  assert.equal((await first).committed, true);
  assert.equal((await output.status("save-once")).state, "succeeded");
  await output.save("explicit-reprint");
  assert.equal(h.calls.filter(call => call.method === "output.save").length, 2);
  const plan = await output.previewArchive({ collection: "t", recordId: "r", fieldId: "f", expectedDigest: "guard" });
  assert.equal(plan.planId, "host-plan");
  assert.equal(h.calls.filter(call => call.method === "mutation.submit").length, 0);
  output.release();
  assert.equal(h.releases.at(-1).method, "output.release");
  h.client.dispose();
});

test("unknown save acknowledgements never reissue the native save, and status can recover success", async () => {
  const h = harness(); h.failSave = true;
  const output = await h.client.create(new Uint8Array([1]), options);
  const saving = output.save("stable-attempt");
  await assert.rejects(saving, error => error.code === "plugin_commit_unknown");
  assert.equal(output.save("stable-attempt"), saving);
  await assert.rejects(output.save("stable-attempt"), error => error.code === "plugin_commit_unknown");
  assert.equal(h.calls.filter(call => call.method === "output.save").length, 1);
  assert.equal((await output.status("stable-attempt")).state, "succeeded");
  assert.equal((await output.save("stable-attempt")).state, "succeeded", "explicit status recovery reuses the positive receipt without another write");
  h.client.dispose();
});

test("artifact bounds, current ownership, and explicit compatibility fail closed", async () => {
  const h = harness();
  await assert.rejects(h.client.create(new Uint8Array(33554433), options), error => error.code === "plugin_file_limit");
  assert.equal(h.calls.length, 0);
  const one = await h.client.create(new Uint8Array([1]), options);
  const two = await h.client.reopen("durable-artifact");
  assert.deepEqual(h.calls.at(-1).params, { artifactId: "durable-artifact", contract: sdk.SURFACE_OUTPUT_CONTRACT });
  await assert.rejects(h.client.create(new Uint8Array([1]), options), error => error.code === "plugin_file_limit");
  assert.equal(two.source.dataVersion, "business-7");
  one.release(); two.release(); h.client.dispose();
  await assert.rejects(h.client.reopen("durable-artifact"), error => error.code === "plugin_surface_closed");
  const releases = [];
  const oldHost = createSurfaceOutputClient(async () => ({ outputId: "unknown", contract: "old", maxBytes: 1048576, blockBytes: 262144 }),
    (method, params) => releases.push({ method, params }));
  await assert.rejects(oldHost.create(new Uint8Array([1]), options), error => error.code === "plugin_api_unsupported");
  assert.equal(releases.length, 1);
});
