import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";

function channel() {
  const calls = []; const listeners = new Set();
  return { calls, postMessage: value => calls.push(value), addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn),
    reply(result, error) { this.replyTo(calls.at(-1), result, error); },
    replyTo(request, result, error) { if (!request) return; for (const fn of listeners) fn({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: request.requestId, result, error } }); } };
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
  // Upsert without an approved key never starts a task; create_only keeps the null key.
  assert.throws(() => client.data.previewImport({ collection: "t", schemaRevision: "schema_1", mode: "upsert" }), error => error.code === "plugin_capability_invalid");
  assert.throws(() => client.data.previewImport({ collection: "t", schemaRevision: "schema_1", mode: "upsert", upsertKey: "" }), error => error.code === "plugin_capability_invalid");
  assert.equal(wire.calls.filter(call => call.method === "data.import.start").length, 0);
  const upserting = client.data.previewImport({ collection: "t", schemaRevision: "schema_1", mode: "upsert", upsertKey: "key" });
  assert.deepEqual(wire.calls.at(-1).params, { collection: "t", schemaRevision: "schema_1", mode: "upsert", upsertKey: "key", columnMapping: [] });
  wire.reply({ taskId: "upsert-owned" }); const upsertTask = await upserting;
  const upsertResult = upsertTask.result(); wire.reply({ taskId: "upsert-owned", state: "succeeded", progress: { done: 1, total: 1, message: "" }, result: { planId: "plan", affectedCount: 1, rows: [], summary: { totalRows: 1, validRows: 1, errorRows: 0 } } });
  assert.equal((await upsertResult).planId, "plan");
  const starting = client.data.previewImport({ collection: "t", schemaRevision: "schema_1" });
  assert.equal(wire.calls.at(-1).method, "data.import.start"); assert.equal("principal" in wire.calls.at(-1).params, false);
  assert.deepEqual(wire.calls.at(-1).params, { collection: "t", schemaRevision: "schema_1", mode: "create_only", upsertKey: null, columnMapping: [] });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const result = task.result(); wire.reply({ taskId: "owned", state: "succeeded", progress: { done: 1, total: 1, message: "" }, result: { planId: "plan", affectedCount: 1, rows: [], summary: { totalRows: 1, validRows: 1, errorRows: 0 } } });
  assert.equal((await result).planId, "plan");
  const startingExport = client.data.export({ collection: "t", fields: ["visible"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "export" }); const exporting = await startingExport;
  const cancelled = exporting.cancel(); assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  wire.reply({ cancelRequested: true }); await tick();
  // The acknowledgement is not the terminal; the tracked status settles the task and releases the handle.
  assert.equal(wire.calls.at(-1).method, "data.io.status");
  wire.reply({ taskId: "export", state: "failed", outcome: "unknown", progress: { done: 0, total: 0, message: "" } });
  await cancelled;
  await assert.rejects(exporting.result(), error => error.code === "plugin_commit_unknown");
  client.dispose();
});

test("terminal DataIO statuses keep the Host's stable error code with a safe fallback", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const limited = client.data.previewImport({ collection: "t", schemaRevision: "schema_1" });
  wire.reply({ taskId: "limited" }); const importing = await limited;
  const settling = importing.result(); wire.reply({ taskId: "limited", state: "failed", progress: { done: 1, total: 1, message: "" }, error: { code: "plugin_plan_limit", message: "插件数据任务未完成，请核对任务。" } });
  await assert.rejects(settling, error => error.code === "plugin_plan_limit");
  const bare = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "bare" }); const exporting = await bare;
  const withoutCode = exporting.result(); wire.reply({ taskId: "bare", state: "failed", progress: { done: 0, total: 0, message: "" } });
  await assert.rejects(withoutCode, error => error.code === "plugin_data_io_failed");
  const aborted = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out2.csv" });
  wire.reply({ taskId: "aborted" }); const aborting = await aborted;
  const unknown = aborting.result(); wire.reply({ taskId: "aborted", state: "aborted", progress: { done: 0, total: 0, message: "" }, error: { code: "plugin_plan_limit", message: "x" } });
  await assert.rejects(unknown, error => error.code === "plugin_commit_unknown");
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


test("owned DataIO cancellation stays bounded and recovers tracking after a full ordinary budget", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const busy = [];
  for (let i = 0; i < 8; i++) { const reading = client.storage.get(`key-${i}`); wire.reply({}); busy.push(reading); }
  await Promise.all(busy);
  const parked = [];
  for (let i = 0; i < 8; i++) { const reading = client.storage.get(`held-${i}`); parked.push({ reading, request: wire.calls.at(-1) }); }
  const cancelled = task.cancel(); assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  const count = wire.calls.length; const duplicate = task.cancel(); assert.equal(wire.calls.length, count);
  wire.reply({ cancelRequested: true });
  // The full ordinary budget blocks the terminal-tracking poll; the failure surfaces to
  // the caller, the handle stays owned, and nothing retries silently.
  await assert.rejects(cancelled, error => error.code === "plugin_concurrency_limit");
  await assert.rejects(duplicate, error => error.code === "plugin_concurrency_limit");
  assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 1);
  // Freeing the slots plus an explicit cancel() restarts the tracking and releases the handle.
  for (const entry of parked) wire.replyTo(entry.request, {});
  await Promise.all(parked.map(entry => entry.reading));
  const recovered = task.cancel();
  assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 2);
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.at(-1).method, "data.io.status");
  wire.reply({ taskId: "owned", state: "cancelled", progress: { done: 0, total: 0, message: "" } });
  await recovered;
  const burst = Array.from({ length: 8 }, (_, i) => {
    const next = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: `burst-${i}.csv` });
    wire.reply({ taskId: `burst-${i}` }); return next;
  });
  await Promise.all(burst);
  client.dispose();
});


for (const kind of ["file", "attachment"]) {
  test(`${kind} read consumes maximum binary blocks with plus-heavy and mixed base64`, async () => {
    const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
    const content = new Uint8Array(1024 * 1024);
    const plusBytes = [251, 239, 190];
    for (let index = 0; index < content.length; index++) content[index] = index < 3 * 262144 ? plusBytes[index % 3] : index % 256;
    let reading;
    if (kind === "file") {
      const picking = client.file.pickRead(); wire.reply({ grantId: "binary", displayName: "binary.bin", mediaType: "application/octet-stream" });
      reading = (await picking).read();
    } else reading = client.attachments.read("opaque");
    for (let offset = 0; offset < content.length; offset += 262144) {
      assert.equal(wire.calls.at(-1).method, kind === "file" ? "file.read" : "attachments.read");
      if (kind === "attachment") assert.equal(wire.calls.at(-1).params.offset, offset);
      const block = { base64: Buffer.from(content.subarray(offset, offset + 262144)).toString("base64"), eof: kind === "attachment" && offset + 262144 === content.length };
      const json = JSON.stringify(block); assert.ok(Buffer.byteLength(json) < 1048576);
      wire.reply(JSON.parse(json)); await tick();
    }
    if (kind === "file") wire.reply({ base64: "", eof: true });
    assert.deepEqual(await reading, content); client.dispose();
  });
}

for (const kind of ["import", "export"]) {
  test(`DataIO ${kind} receipt followed by dispose cancels before registering the task`, async () => {
    const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
    const starting = kind === "import" ? client.data.previewImport({ collection: "t", schemaRevision: "schema_1" })
      : client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
    // Delivery resolves call(), but the awaiting startIo continuation is still queued.
    wire.reply({ taskId: "late" }); client.dispose();
    await assert.rejects(starting, error => error.code === "plugin_surface_closed");
    assert.deepEqual(wire.calls.filter(call => call.method === "data.io.cancel").map(call => call.params), [{ taskId: "late" }]);
    assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 0);
    client.dispose();
    assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 1);
  });
}

for (const kind of ["import", "export"]) {
  test(`cancel-only DataIO ${kind} tasks track a stable terminal and release their slot`, async () => {
    const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
    for (let i = 0; i < 9; i++) {
      const starting = kind === "import" ? client.data.previewImport({ collection: "t", schemaRevision: "schema_1" })
        : client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: `out-${i}.csv` });
      wire.reply({ taskId: `owned-${i}` }); const task = await starting;
      const cancelled = task.cancel();
      assert.equal(wire.calls.at(-1).method, "data.io.cancel");
      wire.reply({ cancelRequested: true }); await tick();
      // The acknowledgement itself is not terminal; the released handle must come from a polled status.
      assert.equal(wire.calls.at(-1).method, "data.io.status");
      wire.reply({ taskId: `owned-${i}`, state: "cancelled", progress: { done: 0, total: 0, message: "" } });
      await cancelled;
    }
    client.dispose();
  });
}

test("a cancel acknowledgement is not terminal; tracking polls until a stable terminal", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const cancelled = task.cancel();
  assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 1);
  wire.reply({ taskId: "owned", state: "running", progress: { done: 1, total: 2, message: "" } });
  await new Promise(resolve => setTimeout(resolve, 130));
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 2, "polling continues while the worker is still running");
  wire.reply({ taskId: "owned", state: "cancelled", progress: { done: 1, total: 2, message: "" } });
  await new Promise(resolve => setTimeout(resolve, 30));
  const settled = wire.calls.filter(call => call.method === "data.io.status").length;
  await new Promise(resolve => setTimeout(resolve, 150));
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, settled, "tracking stops at the stable terminal");
  await cancelled;
  await task.cancel();
  // The released slot admits a full fresh burst; a leaked handle would reject the eighth start.
  const burst = Array.from({ length: 8 }, (_, i) => {
    const next = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: `burst-${i}.csv` });
    wire.reply({ taskId: `burst-${i}` }); return next;
  });
  await Promise.all(burst);
  client.dispose();
});

test("dispose stops terminal tracking without resending or assuming the acknowledgement was terminal", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const cancelled = task.cancel();
  assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 1);
  client.dispose();
  await assert.rejects(cancelled, error => error.code === "plugin_surface_closed");
  await assert.rejects(task.cancel(), error => error.code === "plugin_surface_closed");
  await new Promise(resolve => setTimeout(resolve, 150));
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 1, "no polls after dispose");
  assert.ok(wire.calls.some(call => call.method === "data.io.cancel" && call.params.taskId === "owned"));
});

test("a surface closed during the tracking sleep rejects instead of silently releasing", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const cancelled = task.cancel();
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.at(-1).method, "data.io.status");
  wire.reply({ taskId: "owned", state: "running", progress: { done: 1, total: 2, message: "" } });
  // Dispose inside the 100ms poll sleep: no terminal was ever received.
  client.dispose();
  await assert.rejects(cancelled, error => error.code === "plugin_surface_closed");
  await assert.rejects(task.cancel(), error => error.code === "plugin_surface_closed");
  await new Promise(resolve => setTimeout(resolve, 150));
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 1, "no polls after dispose");
});

test("result maps an export cancel wait interrupted by dispose to an unknown commit", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const failing = task.result(AbortSignal.abort());
  assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.at(-1).method, "data.io.status");   // terminal tracking in flight
  client.dispose();
  await assert.rejects(failing, error => error.code === "plugin_commit_unknown");
  client.dispose();
});

test("terminal tracking keeps the Host's unknown-commit semantics after a cancel acknowledgement", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const cancelled = task.cancel();
  assert.equal(wire.calls.at(-1).method, "data.io.cancel");
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.at(-1).method, "data.io.status");
  wire.reply({ taskId: "owned", state: "failed", outcome: "unknown", progress: { done: 0, total: 0, message: "" } });
  await cancelled;
  await assert.rejects(task.result(), error => error.code === "plugin_commit_unknown");
  client.dispose();
});

test("a failed terminal-tracking poll surfaces its error and an explicit cancel recovers the release", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const cancelled = task.cancel();
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.at(-1).method, "data.io.status");
  // A genuine remote failure is reported, never retried silently.
  wire.reply(undefined, { code: "plugin_transport_failed" });
  await assert.rejects(cancelled, error => error.code === "plugin_transport_failed");
  // An explicit caller retry re-acks (the Host coalesces) and restarts the tracking.
  const recovered = task.cancel();
  assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 2);
  wire.reply({ cancelRequested: true }); await tick();
  assert.equal(wire.calls.at(-1).method, "data.io.status");
  wire.reply({ taskId: "owned", state: "cancelled", progress: { done: 0, total: 0, message: "" } });
  await recovered;
  const burst = Array.from({ length: 8 }, (_, i) => {
    const next = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: `after-${i}.csv` });
    wire.reply({ taskId: `after-${i}` }); return next;
  });
  await Promise.all(burst);
  client.dispose();
});
for (const kind of ["import", "export"]) {
  test(`aborted DataIO ${kind} result keeps reporting progress after the cancel acknowledgement`, async () => {
    const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
    const starting = kind === "import" ? client.data.previewImport({ collection: "t", schemaRevision: "schema_1" })
      : client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
    wire.reply({ taskId: "owned" }); const task = await starting;
    const progress = [];
    const result = task.result(AbortSignal.abort(), value => progress.push(value.done));
    const rejected = assert.rejects(result, error => error.code === "plugin_cancel_requested");
    assert.equal(wire.calls.at(-1).method, "data.io.cancel");
    wire.reply({ cancelRequested: true }); await tick();
    assert.equal(wire.calls.at(-1).method, "data.io.status");
    wire.reply({ taskId: "owned", state: "running", progress: { done: 1, total: 2, message: "" } });
    await new Promise(resolve => setTimeout(resolve, 130));
    assert.equal(wire.calls.at(-1).method, "data.io.status");
    wire.reply({ taskId: "owned", state: "cancelled", progress: { done: 2, total: 2, message: "" } });
    await rejected;
    client.dispose();
    await task.cancel(); // Cached terminals remain settled after dispose.
    assert.deepEqual(progress, [1, 2]);
  });
}
for (const first of ["result", "cancel"]) {
  test(`concurrent DataIO result and cancel share the acknowledgement with ${first} first`, async () => {
    const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
    const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
    wire.reply({ taskId: "owned" }); const task = await starting;
    const progress = [];
    try {
      const result = first === "result" ? task.result(AbortSignal.abort(), value => progress.push(value.done)) : undefined;
      const cancelled = task.cancel();
      const resultOutcome = (result ?? task.result(AbortSignal.abort(), value => progress.push(value.done)))
        .then(value => ({ value }), error => ({ error }));
      const cancelOutcome = cancelled.then(() => "settled", error => error);
      assert.equal(task.cancel(), cancelled);
      assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 1);
      wire.reply({ cancelRequested: true }); await tick();
      const initial = wire.calls.filter(call => call.method === "data.io.status");
      assert.equal(initial.length, 2, "result keeps its own progress polling alongside cancel tracking");
      for (const request of initial) wire.replyTo(request, { taskId: "owned", state: "running", progress: { done: 1, total: 2, message: "" } });
      await new Promise(resolve => setTimeout(resolve, 130));
      const final = wire.calls.filter(call => call.method === "data.io.status").slice(initial.length);
      assert.equal(final.length, 2);
      for (const request of final) wire.replyTo(request, { taskId: "owned", state: "cancelled", progress: { done: 2, total: 2, message: "" } });
      assert.equal((await resultOutcome).error?.code, "plugin_cancel_requested");
      assert.equal(await cancelOutcome, "settled");
      assert.deepEqual(progress, [1, 2]);
      assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 1);
    } finally { client.dispose(); }
  });
}

test("a shared cancel acknowledgement failure is observable and an explicit cancel recovers", async () => {
  const wire = channel(); const client = sdk.createSurfaceClient("s", wire);
  const starting = client.data.export({ collection: "t", fields: ["f"], format: "csv", suggestedName: "out.csv" });
  wire.reply({ taskId: "owned" }); const task = await starting;
  const result = assert.rejects(task.result(AbortSignal.abort()), error => error.code === "plugin_transport_failed");
  const cancelled = assert.rejects(task.cancel(), error => error.code === "plugin_transport_failed");
  assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 1);
  wire.reply(undefined, { code: "plugin_transport_failed" });
  await Promise.all([result, cancelled]);
  assert.equal(wire.calls.filter(call => call.method === "data.io.status").length, 0);
  const recovered = task.cancel();
  assert.equal(wire.calls.filter(call => call.method === "data.io.cancel").length, 2);
  wire.reply({ cancelRequested: true }); await tick();
  wire.reply({ taskId: "owned", state: "cancelled", progress: { done: 1, total: 1, message: "" } });
  await recovered;
  client.dispose();
});
