import test from "node:test";
import assert from "node:assert/strict";
import { sdk } from "./load-sdk.mjs";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

test("public amount-only settlement type permits an omitted quantity", () => {
  const source = new URL(`../../../build/plugin-sdk/${process.pid}/amount-only.typecheck.ts`, import.meta.url);
  const entry = fileURLToPath(new URL("../src/index.ts", import.meta.url)).replaceAll("\\", "/");
  writeFileSync(source, `import type { SettlementSpec, ChangeEvidenceSpec, AllocationSpec } from ${JSON.stringify(entry)};
const actual: SettlementSpec["actual"] = {
  entity: "allocations", state: { path: [], field: "status" }, direction: { path: [], field: "direction" },
  directions: ["red"], settledStates: ["issued"], request: "application_source", capacity: "original",
  net: "net", tax: "tax", gross: "gross"
};
const capacity: SettlementSpec["capacity"] = {
  entity: "obligations", optional: true, quantity: "quantity"
};
const policy: Pick<ChangeEvidenceSpec, "allowInsert"> = { allowInsert: true };
void actual;
const complete: Pick<AllocationSpec, "requireCompleteWhenAllocated"> = { requireCompleteWhenAllocated: true };
const optional: import(${JSON.stringify(entry)}).TransactionRule = { id: "optional", kind: "sameTarget", entity: "rows", left: [{field:"x",target:"rows"}], right: [], skipEmptyRightEnd: true };
void policy;
void complete;
void optional;
void capacity;
`);
  execFileSync(process.execPath, [fileURLToPath(new URL("../node_modules/typescript/bin/tsc", import.meta.url)),
    "--ignoreConfig", "--noEmit", "--allowImportingTsExtensions", "--strict", "--target", "ES2022", "--module", "ESNext",
    "--moduleResolution", "Bundler", "--skipLibCheck", fileURLToPath(source)], { stdio: "inherit" });
});

function fixture() {
  const calls = [];
  let receive;
  const channel = {
    postMessage: message => calls.push(message),
    addEventListener: (_type, listener) => { receive = listener; },
    removeEventListener() {},
  };
  const client = sdk.createSurfaceClient("transaction-surface", channel);
  return { client, calls, reply(result, error) {
    receive({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: calls.at(-1).requestId, result, error } });
  } };
}

test("reversal declarations preserve owner snapshots and native rejection codes", async () => {
  const f = fixture();
  const rule = JSON.parse(readFileSync(new URL("../../../contracts/plugin-reversal-rule.json", import.meta.url), "utf8"));
  const declaration = { contract: "vibetable.plugin-transactions.v1", modelId: "synthetic", version: 1,
    decimals: [{ entity: "lines", field: "quantity", storage: "decimalText", scale: 0, digits: 38, sign: "positive" }], rules: [rule] };
  const configuring = f.client.transaction.configure({ declaration });
  assert.deepEqual(f.calls.at(-1).params, { declaration });
  f.reply(null);
  await configuring;
  for (const code of ["transaction.reversal_invalid", "transaction.reversal_mismatch", "transaction.reversal_exceeded"]) {
    const submit = f.client.transaction.submit({ requestId: "original-budget" });
    f.reply(undefined, { code });
    await assert.rejects(submit, error => error instanceof sdk.PluginCapabilityError && error.code === code);
  }
  f.client.dispose();
});

test("invoice guard declarations keep the natural closed shapes", async () => {
  const f = fixture();
  const allocation = JSON.parse(readFileSync(new URL("../../../contracts/plugin-allocation-rule.json", import.meta.url), "utf8"));
  const settlement = JSON.parse(readFileSync(new URL("../../../contracts/plugin-settlement-rule.json", import.meta.url), "utf8"));
  const evidence = JSON.parse(readFileSync(new URL("../../../contracts/plugin-evidence-rule.json", import.meta.url), "utf8"));
  const declaration = { contract: "vibetable.plugin-transactions.v1", modelId: "synthetic", version: 1,
    decimals: [], rules: [allocation, settlement, evidence] };
  const configuring = f.client.transaction.configure({ declaration });
  assert.deepEqual(f.calls.at(-1).params, { declaration });
  f.reply(null);
  await configuring;
  for (const code of ["transaction.settlement_exceeded", "transaction.allocation_exceeded", "transaction.evidence_required"]) {
    const submit = f.client.transaction.submit({ requestId: "invoice-guards" });
    f.reply(undefined, { code });
    await assert.rejects(submit, error => error instanceof sdk.PluginCapabilityError && error.code === code);
  }
  f.client.dispose();
});

test("plugin callers receive the native rejection code or uncertainty without resubmission", async () => {
  const f = fixture();
  const declaration = { contract: "vibetable.plugin-transactions.v1", modelId: "orders", version: 1,
    decimals: [], rules: [{ id: "code", kind: "unique", entity: "headers", fields: ["number"] }] };
  for (const [invoke, code] of [
    [() => f.client.mutation.submit("plan"), "transaction.same_target_violation"],
    [() => f.client.transaction.submit({ requestId: "business" }), "transaction.balance_violation"],
    [() => f.client.transaction.configure({ declaration }), "plugin_commit_unknown"],
  ]) {
    const request = invoke();
    f.reply(undefined, { code, message: "Plugin request failed." });
    await assert.rejects(request, error => error instanceof sdk.PluginCapabilityError && error.code === code);
  }
  assert.deepEqual(f.calls.map(call => call.method), ["mutation.submit", "transaction.submit", "transaction.configure"]);
  f.client.dispose();
});

test("transaction uses persistent business identity and single calls without retry or host commit channel", async () => {
  const f = fixture();
  const declaration = { contract: "vibetable.plugin-transactions.v1", modelId: "orders", version: 1,
    decimals: [], rules: [{ id: "unique", kind: "unique", entity: "headers", fields: ["number"] }] };
  const configured = f.client.transaction.configure({ declaration });
  assert.equal(f.calls.at(-1).method, "transaction.configure");
  assert.deepEqual(f.calls.at(-1).params, { declaration });
  f.reply(null);
  assert.equal(await configured, null);
  const plan = { contract: "vibetable.transaction-plan.v1", modelId: "orders", operations: [
    { entity: "headers", kind: "create", tempId: "head", values: { number: "SYNTHETIC" } },
    { entity: "lines", kind: "create", tempId: "line", values: { header: { entity: "headers", tempId: "head" }, amount: "0.30" } },
  ] };
  const preview = f.client.transaction.preview({ plan });
  assert.deepEqual(f.calls.at(-1).params, { plan });
  f.reply({ status: "prepared", requestId: "persistent-business-id", plan });
  assert.equal((await preview).requestId, "persistent-business-id");
  const submit = f.client.transaction.submit({ requestId: "persistent-business-id" });
  assert.equal(f.calls.at(-1).method, "transaction.submit");
  assert.deepEqual(f.calls.at(-1).params, { requestId: "persistent-business-id" });
  assert.notEqual(f.calls.at(-1).requestId, "persistent-business-id");
  f.reply(undefined, { code: "plugin_commit_unknown" });
  await assert.rejects(submit, error => error.code === "plugin_commit_unknown");
  assert.equal(f.calls.length, 3);
  const status = f.client.transaction.status({ requestId: "persistent-business-id" });
  f.reply({ status: "unknown", requestId: "persistent-business-id" });
  assert.equal((await status).status, "unknown");
  assert.equal(f.calls.length, 4);
  assert.equal("commit" in f.client.transaction, false);
  f.client.dispose();
});

test("closing after submit reports unknown and a new surface can query original request without submit", async () => {
  const f = fixture();
  const submit = f.client.transaction.submit({ requestId: "original" });
  f.client.dispose();
  await assert.rejects(submit, error => error.code === "plugin_commit_unknown");
  assert.equal(f.calls.length, 1);
  const reopened = fixture();
  const status = reopened.client.transaction.status({ requestId: "original" });
  const applied = { status: "applied", requestId: "original", newIds: { headers: { head: "actual-id" } } };
  reopened.reply(applied);
  assert.deepEqual(await status, applied);
  assert.deepEqual(reopened.calls.map(call => call.method), ["transaction.status"]);
  reopened.client.dispose();
});

test("abort requests cancellation but a received success remains success", async () => {
  const f = fixture();
  const controller = new AbortController();
  const submit = f.client.transaction.submit({ requestId: "original" }, controller.signal);
  const request = f.calls[0];
  controller.abort();
  assert.equal(f.calls[1].method, "request.cancel");
  assert.deepEqual(f.calls[1].params, { requestId: request.requestId });
  const applied = { status: "applied", requestId: "original" };
  // Cancel acknowledgement has independent transport correlation.
  f.calls.pop(); f.reply(applied);
  assert.deepEqual(await submit, applied);
  f.client.dispose();
});

test("only an explicit refresh requests a new preview revision and never submits it", async () => {
  const f = fixture();
  const plan = { contract: "vibetable.transaction-plan.v1", modelId: "orders", operations: [
    { entity: "headers", kind: "create", tempId: "head", values: { number: "SYNTHETIC" } },
  ] };
  const refreshing = f.client.transaction.preview({ requestId: "original", plan, refresh: true });
  assert.deepEqual(f.calls[0].params, { requestId: "original", plan, refresh: true });
  const prepared = { status: "prepared", requestId: "original", previewRevision: 2,
    newIds: { headers: { head: "same-id" } }, dataRevisions: { "table-head": "data_2" },
    effects: [{ tableId: "table-head", recordId: "same-id", operation: "insert", schemaRevision: "schema_1" },
      { tableId: "table-target", recordId: "existing", operation: "update", schemaRevision: "schema_1", expectedDigest: "guard" }] };
  f.reply(prepared);
  assert.deepEqual(await refreshing, prepared);
  assert.deepEqual(f.calls.map(call => call.method), ["transaction.preview"]);
  f.client.dispose();
});
