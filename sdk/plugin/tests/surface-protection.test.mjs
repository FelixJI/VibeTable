import assert from "node:assert/strict";
import test from "node:test";
import { sdk, testing } from "./load-sdk.mjs";

const model = {
  entities: [
    { key: "tasks", operations: ["read", "query", "create", "update", "delete"], fields: [{ key: "title" }, { key: "status" }] },
    { key: "notes", operations: ["read", "query"], fields: [{ key: "body" }] },
  ],
};
const declaration = (fields) => ({ contract: sdk.FIELD_PROTECTION_CONTRACT, fields });
const hostMethods = [
  "fieldProtection.describe", "fieldProtection.enable", "fieldProtection.release", "plugin.fieldProtection.enable",
];

test("manifest field protection declaration mirrors the Host limits and the Go/Python contract", () => {
  assert.equal(sdk.FIELD_PROTECTION_CONTRACT, "vibetable.plugin-field-protection.v1");
  assert.deepEqual(sdk.FIELD_PROTECTION_LIMITS, { maxProtectedFields: 45 * 64, maxEvents: 512 });
  assert.deepEqual([...sdk.FIELD_PROTECTION_HOST_METHODS], [
    "plugin.fieldProtection.describe", "plugin.fieldProtection.enable", "plugin.fieldProtection.list", "plugin.fieldProtection.release",
  ]);
  assert.doesNotThrow(() => sdk.validateFieldProtectionDeclaration(declaration([{ entity: "tasks", field: "status" }]), model));
  // Any write operation on the entity is enough; the field itself is what gets frozen.
  assert.doesNotThrow(() => sdk.validateFieldProtectionDeclaration(
    declaration([{ entity: "tasks", field: "title" }]), { entities: [{ key: "tasks", operations: ["read", "query", "delete"], fields: [{ key: "title" }] }] }));
  // Every valid model subset can be protected in full (45 entities x 64 fields).
  const full = declaration(Array.from({ length: sdk.FIELD_PROTECTION_LIMITS.maxProtectedFields }, (_, i) => ({ entity: "tasks", field: `f${i}` })));
  const fullModel = { entities: [{ key: "tasks", operations: ["update"], fields: full.fields.map(item => ({ key: item.field })) }] };
  assert.doesNotThrow(() => sdk.validateFieldProtectionDeclaration(full, fullModel));
});

test("declaration validation refuses exactly what Host admission refuses", () => {
  const refused = (value, entities = model, path = "fieldProtection") =>
    assert.throws(() => sdk.validateFieldProtectionDeclaration(value, entities), error =>
      error instanceof sdk.FieldProtectionDeclarationError && error.code === "plugin_field_protection.declaration_invalid" && error.path.startsWith(path));
  refused(undefined);
  refused({ ...declaration([{ entity: "tasks", field: "status" }]), on: true });
  refused({ contract: "vibetable.plugin-field-protection.v2", fields: [{ entity: "tasks", field: "status" }] }, model, "fieldProtection.contract");
  refused({ contract: sdk.FIELD_PROTECTION_CONTRACT });
  refused(declaration([]));
  refused(declaration([{ entity: "tasks", field: "status" }, { entity: "tasks", field: "status" }]), model, "fieldProtection.fields.1");
  refused(declaration([{ entity: "tasks", field: "status", label: "x" }]), model, "fieldProtection.fields.0");
  refused(declaration([{ entity: "tasks", field: "" }]), model, "fieldProtection.fields.0");
  refused(declaration([{ entity: "tasks", field: "missing" }]), model, "fieldProtection.fields.0");
  refused(declaration([{ entity: "missing", field: "status" }]), model, "fieldProtection.fields.0");
  // Protection means "only this plugin may change the field": a read-only entity would freeze it.
  refused(declaration([{ entity: "notes", field: "body" }]), model, "fieldProtection.fields.0");
  // Computed projections and user extension fields are never part of the model.
  refused(declaration([{ entity: "tasks", field: "computed_total" }]), model, "fieldProtection.fields.0");
  // No model, no protection (null cannot default to the fixture model).
  refused(declaration([{ entity: "tasks", field: "status" }]), null);
  const over = declaration(Array.from({ length: sdk.FIELD_PROTECTION_LIMITS.maxProtectedFields + 1 }, (_, i) => ({ entity: "tasks", field: `f${i}` })));
  refused(over, { entities: [{ key: "tasks", operations: ["update"], fields: over.fields.map(item => ({ key: item.field })) }] }, "fieldProtection.fields");
  assert.equal(sdk.readFieldProtectionDeclaration({ fieldProtection: declaration([{ entity: "tasks", field: "status" }]) }).fields.length, 1);
  assert.equal(sdk.readFieldProtectionDeclaration({ data: [] }), null);
  assert.equal(sdk.readFieldProtectionDeclaration(undefined), null);
});

test("the SDK reads current and retained protection without exposing administration", async () => {
  const status = { contract: sdk.FIELD_PROTECTION_CONTRACT, declared: true, activeProtectionCount: 1,
    currentProtectedCount: 0, fields: [{ entity: "tasks", field: "status", tableId: "tbl_new",
      fieldId: "fld_new", state: "unprotected", ownerStale: false, diagnostic: "" }] };
  const sent = [];
  let receive;
  const transport = {
    postMessage(request) {
      sent.push([request.method, request.params]);
      receive({ data: { contract: request.contract, surfaceToken: request.surfaceToken, requestId: request.requestId, result: status } });
    },
    addEventListener(_event, listener) { receive = listener; },
    removeEventListener() {},
  };
  const client = sdk.createSurfaceClient("surface-a", transport);
  assert.deepEqual(Object.keys(client.fieldProtection), ["status"]);
  assert.deepEqual(await client.fieldProtection.status(), status);
  assert.deepEqual(sent, [["fieldProtection.status", {}]]);
  client.dispose();
});

test("the synthetic host rejects protection administration like the real surface and never calls adapters", async () => {
  const calls = [];
  const host = testing.createOfflineSurfaceHost({
    pluginId: "synthetic", workspaceId: "w", surfaceToken: "surface", risk: "write",
    datasetAdapter: async (method) => { calls.push(method); return {}; },
    snapshotAdapter: async (method) => { calls.push(method); return {}; },
    versionsAdapter: async () => { calls.push("data.versions"); return {}; },
  });
  const replies = hostMethods.map(method => new Promise((resolve) => {
    const listener = (event) => {
      if (event.data.requestId !== method) return;
      host.transport.removeEventListener("message", listener);
      resolve(event.data);
    };
    host.transport.addEventListener("message", listener);
    host.transport.postMessage({
      contract: sdk.SURFACE_API_CONTRACT, surfaceToken: "surface", requestId: method, method,
      params: { installationId: "00000000-0000-4000-8000-000000000001", installationRevision: 1, fields: [{ entity: "tasks", field: "status" }] },
    });
  }));
  for (const message of await Promise.all(replies)) {
    assert.equal(message.error.code, "plugin_capability_invalid");
    assert.equal("result" in message, false);
  }
  assert.deepEqual(calls, []);
  host.retire();
});
