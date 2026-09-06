import assert from "node:assert/strict";
import test from "node:test";
import { measureRpcLatency } from "./packaged_rpc_probe.mjs";

function fakePage({ reply, timeout = false, elapsed = 2 } = {}) {
  const requests = [];
  let released = 0;
  let disposed = 0;
  const page = {
    async evaluate(fn, args) {
      if (fn.name === "beginRawBridgeRequestInPage") {
        assert.equal(requests.length, released, "requests must remain sequential");
        requests.push(args);
        return `request-${requests.length}`;
      }
      if (fn.name === "readRawBridgeRequestElapsedInPage") return elapsed;
      if (fn.name === "releaseRawBridgeRequestInPage") { released += 1; return true; }
      throw new Error(`unexpected evaluation: ${fn.name}`);
    },
    async waitForFunction(_fn, args, options) {
      assert.equal(args.requestId, `request-${requests.length}`);
      assert.equal(options.timeout, 20_000);
      if (timeout) throw new Error("observation timed out");
      const request = requests.at(-1);
      return {
        async jsonValue() {
          if (typeof reply === "function") return reply(request, requests.length);
          return reply ?? { type: request.requestType, payload: request.requestType === "schema.getTable"
            ? { tableId: "tbl_first", schemaRevision: "1", fields: [] }
            : { rows: [], snapshot: { table: "tbl_first", schemaRevision: "1" } } };
        },
        async dispose() { disposed += 1; },
      };
    },
  };
  return { page, requests, get released() { return released; }, get disposed() { return disposed; } };
}

test("RPC baseline collects both read contracts sequentially without discarding first sample", async () => {
  const fake = fakePage();
  const evidence = await measureRpcLatency(fake.page, "tbl_first");
  assert.deepEqual(evidence, { status: "passed", tableId: "tbl_first", samples: {
    "schema.getTable": Array(30).fill(2), "query.page": Array(30).fill(2),
  } });
  assert.equal(fake.released, 60);
  assert.equal(fake.disposed, 60);
  assert.deepEqual(fake.requests[30].requestPayload, {
    tableId: "tbl_first", query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
});

for (const [name, options] of [
  ["failure reply", { reply: { type: "operation.failed", payload: { message: "private" } } }],
  ["different table", { reply: { type: "schema.getTable", payload: {
    tableId: "tbl_other", schemaRevision: "1", fields: [],
  } } }],
  ["missing timing", { elapsed: null }],
  ["timeout", { timeout: true }],
]) {
  test(`RPC baseline rejects ${name} and releases its outstanding listener`, async () => {
    const fake = fakePage(options);
    await assert.rejects(measureRpcLatency(fake.page, "tbl_first"), (error) => {
      assert.equal(error.message.includes("private"), false);
      return true;
    });
    assert.equal(fake.requests.length, 1);
    assert.equal(fake.released, 1);
  });
}


for (const [name, snapshot] of [
  ["different query table", { table: "tbl_other", schemaRevision: "1" }],
  ["changed query schema", { table: "tbl_first", schemaRevision: "2" }],
]) {
  test(`RPC baseline rejects ${name} after valid schema samples`, async () => {
    const fake = fakePage({ reply: (request) => ({ type: request.requestType,
      payload: request.requestType === "schema.getTable"
        ? { tableId: "tbl_first", schemaRevision: "1", fields: [] }
        : { rows: [], snapshot },
    }) });
    await assert.rejects(measureRpcLatency(fake.page, "tbl_first"), /same empty table snapshot/);
    assert.equal(fake.requests.length, 31);
    assert.equal(fake.released, 31);
    assert.equal(fake.disposed, 31);
  });
}
