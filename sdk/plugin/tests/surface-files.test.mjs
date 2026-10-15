import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";

function channel() {
  const calls = []; const listeners = new Set();
  return { calls, postMessage: value => calls.push(value), addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn),
    reply(result, error) { const wire = calls.at(-1); for (const fn of listeners) fn({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: wire.requestId, result, error } }); } };
}
const tick = () => new Promise(resolve => setImmediate(resolve));

test("native file grants read/write bounded blocks once, cancel normally, and release on dispose", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const cancelled = client.file.pickRead(); wire.reply(null); assert.equal(await cancelled, null);
  const picked = client.file.pickRead({ mediaTypes: ["text/plain"] });
  assert.deepEqual(wire.calls.at(-1).params, { mediaTypes: ["text/plain"] });
  wire.reply({ grantId: "opaque", displayName: "input.txt", mediaType: "text/plain" }); const grant = await picked;
  const reading = grant.read(); wire.reply({ base64: btoa("hello"), eof: false }); await tick(); wire.reply({ base64: "", eof: true });
  assert.equal(new TextDecoder().decode(await reading), "hello");
  assert.equal(wire.calls.at(-1).method, "file.release");
  await assert.rejects(grant.read(), error => error.code === "plugin_file_denied");
  const target = client.file.pickWrite({ suggestedName: "out.bin", mediaType: "application/octet-stream" });
  wire.reply({ grantId: "write", displayName: "out.bin" }); const writer = await target;
  const content = new Uint8Array(300_000); const saving = writer.write(content);
  assert.equal(atob(wire.calls.at(-1).params.base64).length, 262144); assert.equal(wire.calls.at(-1).params.finish, false);
  wire.reply({ offset: 262144 }); await tick(); assert.equal(wire.calls.at(-1).params.offset, 262144);
  wire.reply({ committed: true, bytes: 300_000 }); await saving;
  const unused = client.file.pickRead(); wire.reply({ grantId: "unused", displayName: "x", mediaType: "x" }); await unused;
  client.dispose(); assert.ok(wire.calls.some(call => call.method === "file.release" && call.params.grantId === "unused"));
});

test("file limits, lost receipt and pending picker disposal never replay a write", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const pick = client.file.pickWrite({ suggestedName: "out", mediaType: "text/plain" }); wire.reply({ grantId: "w", displayName: "out" }); const grant = await pick;
  await assert.rejects(grant.write(new Uint8Array(1048577)), error => error.code === "plugin_file_limit");
  const again = client.file.pickWrite({ suggestedName: "out", mediaType: "text/plain" }); wire.reply({ grantId: "w2", displayName: "out" }); const writer = await again;
  const saving = writer.write(new Uint8Array([1])); wire.reply(undefined, { code: "plugin_commit_unknown" });
  await assert.rejects(saving, error => error.code === "plugin_commit_unknown");
  assert.equal(wire.calls.filter(call => call.method === "file.write").length, 1);
  const pending = client.file.pickRead(); const request = wire.calls.at(-1); client.dispose();
  await assert.rejects(pending, error => error.code === "plugin_surface_closed");
  assert.ok(wire.calls.some(call => call.method === "request.cancel" && call.params.requestId === request.requestId));
});

test("DataIO uses owned task status and cancellation; preview yields only a mutation handle", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.previewImport({ collection: "t", schemaRevision: "schema_1" });
  assert.equal(wire.calls.at(-1).method, "data.import.start"); assert.equal("principal" in wire.calls.at(-1).params, false);
  wire.reply({ taskId: "owned" }); const task = await starting;
  const result = task.result(); wire.reply({ taskId: "owned", state: "succeeded", progress: { done: 1, total: 1, message: "" }, result: { planId: "plan", affectedCount: 1, rows: [], summary: { totalRows: 1, validRows: 1, errorRows: 0 } } });
  assert.equal((await result).planId, "plan");
  const startingExport = client.data.export({ collection: "t", fields: ["visible"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "export" }); const exporting = await startingExport;
  const cancelled = exporting.cancel(); assert.equal(wire.calls.at(-1).method, "data.io.cancel"); wire.reply({ cancelRequested: true }); await cancelled;
  const failed = exporting.result(); wire.reply({ taskId: "export", state: "failed", outcome: "unknown", progress: { done: 0, total: 0, message: "" } });
  await assert.rejects(failed, error => error.code === "plugin_commit_unknown");
  client.dispose();
});

test("attachment reading is bounded and preview never sends native paths or stage handles", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const listing = client.attachments.list({ collection: "t", recordId: "r", fieldId: "f" });
  wire.reply({ items: [{ attachmentId: "opaque", displayName: "x.txt", mediaType: "text/plain", sizeBytes: 3 }] }); const refs = await listing;
  const reading = client.attachments.read(refs[0].attachmentId); assert.deepEqual(wire.calls.at(-1).params, { attachmentId: "opaque", offset: 0 });
  wire.reply({ base64: btoa("abc"), eof: true }); assert.deepEqual([...await reading], [97,98,99]);
  const preview = client.attachments.previewChange({ collection: "t", recordId: "r", fieldId: "f", expectedDigest: "existing-row-guard", addFile: true });
  assert.deepEqual(wire.calls.at(-1).params.removeAttachmentIds, []); assert.equal("path" in wire.calls.at(-1).params, false);
  wire.reply({ planId: "native-plan", affectedCount: 1 }); assert.equal((await preview).planId, "native-plan"); client.dispose();
});


test("owned DataIO cancellation stays bounded and reachable with full normal request slots", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const busy = Array.from({ length: 8 }, (_, i) => client.storage.get(`key-${i}`));
  const cancelled = task.cancel(); assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  const count = wire.calls.length; const duplicate = task.cancel(); assert.equal(wire.calls.length, count);
  wire.reply({ cancelRequested: true }); await Promise.all([cancelled, duplicate]);
  assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 1);
  client.dispose(); await Promise.allSettled(busy);
});
