import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, selectPlugin, commitInspectedApplicationUpgrade } from "./plugin_applications.mjs";
import { isAppliedMutationResponse } from "./bridge_raw_request.mjs";

const PLUGIN = "com.vibetable.examples.model-conformance";
const MAPPED = "com.vibetable.e2e.model-mapped";

export function modelMappingSelections(tables, schemas, bindings) {
  const selections = [];
  for (const [entity, fields] of [["people", ["name", "tickets"]], ["tickets", ["title", "person"]], ["assets", ["name", "quantity"]]]) {
    const binding = bindings[entity];
    const table = tables.filter(item => item.tableId === binding.tableId);
    if (table.length !== 1) throw new Error("real canonical mapping table unavailable");
    selections.push({ controlId: `PluginModelTable_${entity}`, label: table[0].displayName });
    for (const field of fields) {
      const choice = schemas[entity].fields.filter(item => item.identity.fieldId === binding.fields[field]);
      if (choice.length !== 1) throw new Error("real canonical mapping field unavailable");
      selections.push({ controlId: `PluginModelField_${entity}_${field}`, label: `${choice[0].displayName} · ${choice[0].logicalType}` });
    }
  }
  return selections;
}

// Read-only observation of the same Host catalog projection used by the sidebar.
// Ordinary notifications have no wire scope: stale delivery is prevented by the
// Host's generation/lease gate, and this observer retires on any local epoch change.
export function installModelCatalogObserverInPage(initial) {
  const webview = window.chrome.webview;
  if (window.__e2eModelCatalog) throw new Error("model catalog observer is already installed");
  const session = { ...window.__vibetableE2EBridgeDiagnostics?.workspaceSession };
  if (!session?.workspaceId || !Number.isSafeInteger(session.sessionEpoch) || session.sessionEpoch < 1)
    throw new Error("model catalog requires the active workspace session");
  if (initial.projectKey !== `local:${session.workspaceId.replaceAll("-", "").toLowerCase()}`)
    throw new Error("model catalog belongs to a different workspace");
  const sameSession = value => value?.workspaceId === session.workspaceId && value?.sessionEpoch === session.sessionEpoch;
  const validate = payload => {
    if (!payload || !Array.isArray(payload.tables) || !Array.isArray(payload.views)
      || !payload.displayNames || typeof payload.displayNames !== "object"
      || [...payload.tables, ...payload.views].some(id => typeof id !== "string" || !id || typeof payload.displayNames[id] !== "string")
      || new Set([...payload.tables, ...payload.views]).size !== payload.tables.length + payload.views.length)
      throw new Error("invalid Host table catalog");
    return structuredClone(payload);
  };
  const log = { session: { ...session }, revision: 0, payload: validate(initial), error: null };
  log.checkCurrent = () => {
    if (!sameSession(window.__vibetableE2EBridgeDiagnostics?.workspaceSession)) log.error = "model catalog session retired";
  };
  log.handler = event => {
    let message = event.data;
    if (typeof message === "string") { try { message = JSON.parse(message); } catch { return; } }
    if (message?.type === "workspace.v2.bootstrap" && !sameSession(message.payload?.session)) log.error = "model catalog session retired";
    log.checkCurrent();
    if (log.error || !message) return;
    const scope = message.wire ?? message.scope;
    if (scope && !sameSession(scope)) return;
    if (message.type === "operation.failed" && message.payload?.operation === "plugin_model.catalog") {
      log.error = `${message.payload.code}: ${message.payload.message}`;
      return;
    }
    if (message.type !== "database.collectionsChanged" || message.requestId != null) return;
    try { log.payload = validate(message.payload); log.revision++; }
    catch (error) { log.error = error.message; }
  };
  window.__e2eModelCatalog = log;
  webview.addEventListener("message", log.handler);
}

export function readModelCatalogInPage() {
  const log = window.__e2eModelCatalog;
  if (!log) throw new Error("model catalog observer is missing");
  log.checkCurrent();
  if (log.error) throw new Error(log.error);
  return { revision: log.revision, tables: log.payload.tables.map(tableId => ({ tableId, displayName: log.payload.displayNames[tableId], kind: "base" })), views: log.payload.views };
}

export function releaseModelCatalogObserverInPage() {
  const log = window.__e2eModelCatalog;
  if (!log) return;
  window.chrome.webview.removeEventListener("message", log.handler);
  delete window.__e2eModelCatalog;
}

export async function waitForModelCatalog(page, bindings, afterRevision) {
  const ids = Object.values(bindings).map(binding => binding.tableId);
  await page.waitForFunction(({ ids, afterRevision }) => {
    const log = window.__e2eModelCatalog;
    if (!log) return true;
    log.checkCurrent();
    return Boolean(log.error) || log.revision > afterRevision && log.payload.views.length === 0
      && log.payload.tables.length === ids.length && ids.every(id => log.payload.tables.includes(id));
  }, { ids, afterRevision }, { timeout: 30000 });
  return page.evaluate(readModelCatalogInPage);
}

export async function nativeModel(runtime, action, buttonId, stage, selections) {
  const requestId = `model-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const target = path.join(runtime.evidenceDir, "plugin-native-request.json");
  await fs.writeFile(`${target}.tmp`, JSON.stringify({ requestId, action, buttonId, stage, ...(selections ? { selections } : {}) }));
  await fs.rename(`${target}.tmp`, target);
  const until = Date.now() + (action === "map" ? 75000 : 20000);
  while (Date.now() < until) {
    const reply = await fs.readFile(path.join(runtime.evidenceDir, "plugin-native-result.json"), "utf8").then(JSON.parse).catch(error => { if (error.code === "ENOENT") return null; throw error; });
    if (reply?.requestId === requestId) {
      if (reply.status !== "completed") throw new Error(`native model input failed: ${JSON.stringify(reply)}`);
      return reply;
    }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error("native model input deadline");
}
async function state(frame) { return JSON.parse(await frame.locator("#result").innerText()); }
async function completed(frame) {
  await frame.waitForFunction(() => { try { return JSON.parse(document.querySelector("#result").textContent).status === "completed"; } catch { return false; } }, null, { timeout: 120000 });
  return state(frame);
}
async function frameCall(frame, method, params = {}) {
  return frame.evaluate(({ method, params }) => new Promise((resolve, reject) => {
    const requestId = crypto.randomUUID(); const channel = chrome.webview;
    const listener = event => { if (event.data?.requestId !== requestId) return; channel.removeEventListener("message", listener); clearTimeout(timer); resolve(event.data); };
    const timer = setTimeout(() => { channel.removeEventListener("message", listener); reject(new Error("surface reply deadline")); }, 20000);
    channel.addEventListener("message", listener);
    channel.postMessage({ contract: "vibetable.plugin-api.v1", surfaceToken: window.name, requestId, method, params });
  }), { method, params });
}

async function renameNativeModelField(page, helpers, tableId, fieldId, displayName) {
  const described = await helpers.rawBridgeRequest(page, "field.settings.describe", { tableId, fieldId });
  const { contract: _contract, identity: _identity, lifecycle: _lifecycle, ...draft } = described.payload.definition;
  const planned = await helpers.rawBridgeRequest(page, "field.change.plan", {
    action: "update", tableId, fieldId, expectedSchemaRevision: described.payload.schemaRevision,
    expectedDataRevision: described.payload.dataRevision, draft: { ...draft, displayName },
    actor: { id: "product-e2e", kind: "user" }, conversionRule: "", confirmation: "", backupReceipt: "",
  });
  if (!planned.payload?.canApply || planned.payload.createsMigration) throw new Error(`native rename rejected: ${JSON.stringify(planned)}`);
  const applied = await helpers.rawBridgeRequest(page, "field.change.apply", {
    planId: planned.payload.planId, planHash: planned.payload.planHash, operationId: `model-native-rename-${Date.now()}`,
    actor: { id: "product-e2e", kind: "user" }, confirmations: planned.payload.confirmations ?? [],
  });
  if (applied.type !== "field.change.apply") throw new Error(`native rename failed: ${JSON.stringify(applied)}`);
}

async function verifyUserData(page, frame, recorder, helpers, userData, stage) {
  const schema = (await helpers.rawBridgeRequest(page, "schema.getTable", { tableId: userData.tableId })).payload;
  const rows = (await helpers.rawBridgeRequest(page, "query.page", {
    tableId: userData.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 },
  })).payload.rows;
  const row = rows.find(item => item.id === userData.recordId);
  const core = schema.fields.find(field => field.identity.fieldId === userData.renamedFieldId);
  const extra = schema.fields.find(field => field.identity.fieldId === userData.fieldId);
  recorder.check(`${stage} preserves native rename and user extension values`, Boolean(core && extra && row)
    && core.displayName === userData.renamedLabel && row[userData.physicalName] === userData.value
    && row[userData.corePhysicalName] === userData.coreValue, { schema, rows, userData });
  const description = await frameCall(frame, "data.describe", { accepts: ["vibetable.plugin-data.v2"], collection: userData.tableId });
  recorder.check(`${stage} excludes the user extension from resolved model grants`, Array.isArray(description.result?.fields)
    && description.result.fields.some(field => field.fieldId === userData.renamedFieldId)
    && !description.result.fields.some(field => field.fieldId === userData.fieldId), { description });
  return { schema, rows, description };
}

export async function runPluginModels(page, recorder, runtime, helpers, statePath = null) {
  if (statePath) {
    const seed = JSON.parse(await fs.readFile(statePath, "utf8"));
    const { switched } = await helpers.activateRestartedWorkspace(page, seed.workspaceId);
    recorder.check("cold Host reopens the exact model workspace at a fresh epoch", switched.result?.workspaceId === seed.workspaceId
      && switched.result.sessionEpoch > seed.sessionEpoch, { seedEpoch: seed.sessionEpoch, switched: switched.result });
    const frame = await openApplication(page, PLUGIN, "open-model");
    const reopened = await state(frame);
    recorder.check("cold Host reads completed canonical bindings without reinitialization", reopened.status === "completed"
      && JSON.stringify(reopened.bindings) === JSON.stringify(seed.bindings), { reopened, bindings: seed.bindings });
    const preserved = await verifyUserData(page, frame, recorder, helpers, seed.userData, "cold reopen");
    await fs.writeFile(path.join(runtime.evidenceDir, "54-models-reopened.json"), JSON.stringify({ workspaceId: seed.workspaceId,
      previousEpoch: seed.sessionEpoch, sessionEpoch: switched.result.sessionEpoch, reopened, preserved }, null, 2));
    return { workspaceId: seed.workspaceId, projectKey: seed.projectKey, sessionEpoch: switched.result.sessionEpoch,
      bindings: reopened.bindings, userData: seed.userData };
  }
  const opened = await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.evaluate(installModelCatalogObserverInPage, opened.payload);
  try {
    const catalog = async () => (await page.evaluate(readModelCatalogInPage)).tables;
    const catalogRevision = async () => (await page.evaluate(readModelCatalogInPage)).revision;
    const initialCatalog = await page.evaluate(readModelCatalogInPage);
    const original = initialCatalog.tables;
    recorder.check("model qualification begins with an empty synthetic workspace", original.length === 0 && initialCatalog.views.length === 0, { original });
    await installApplication(page, runtime, "model-conformance", PLUGIN, "open-model");
    let frame = await openApplication(page, PLUGIN, "open-model");
    await frame.locator("#create").click();
    const observed = await nativeModel(runtime, "observe", "plugin-model-confirm", "create");
    recorder.check("native exact model preview has not created business tables", (await catalog()).length === 0 && observed.observation.buttons.some(button => button.id === "plugin-model-confirm"), { observed });
    await nativeModel(runtime, "click", "plugin-model-cancel", "create");
    await frame.locator("#result").filter({ hasText: /^null$/ }).waitFor({ timeout: 30000 });
    const cancelled = await frameCall(frame, "model.read");
    recorder.check("cancelled native initialization leaves no tables or completed model", cancelled.result?.status === "unconfigured"
      && (await catalog()).length === 0, { cancelled });
    let beforeCatalog = await catalogRevision();
    await frame.locator("#create").click();
    await nativeModel(runtime, "click", "plugin-model-confirm", "create");
    const first = await completed(frame);
    await waitForModelCatalog(page, first.bindings, beforeCatalog);
    recorder.check("three-table native model initialization completes all committed steps", first.status === "completed" && first.completed === first.total && (await catalog()).length === 3, { first });
    beforeCatalog = await catalogRevision();
    await frame.locator("#create").click();
    const repeated = await completed(frame);
    await waitForModelCatalog(page, repeated.bindings, beforeCatalog);
    recorder.check("repeated initialization reuses canonical table and field identities", JSON.stringify(first.bindings) === JSON.stringify(repeated.bindings), { repeated });
    const hidden = await frameCall(frame, "model.catalog");
    recorder.check("plugin frame cannot request the ungranted workspace catalog", Boolean(hidden.error), { hidden });
    const assets = first.bindings.assets;
    const extra = await helpers.createV2Field(page, assets.tableId, "合成用户私有备注", "text");
    const assetSchema = (await helpers.rawBridgeRequest(page, "schema.getTable", { tableId: assets.tableId })).payload;
    const coreName = assetSchema.fields.find(field => field.identity.fieldId === assets.fields.name);
    const userData = { tableId: assets.tableId, fieldId: extra.fieldId, physicalName: extra.physicalName, value: "合成备注必须原样保留",
      renamedFieldId: assets.fields.name, renamedLabel: "用户改过的合成资产名称", corePhysicalName: coreName.identity.physicalName, coreValue: "合成资产原值" };
    await renameNativeModelField(page, helpers, assets.tableId, assets.fields.name, userData.renamedLabel);
    const inserted = await helpers.applyProductMutation(page, assets.tableId, [{ kind: "insert", recordId: null,
      values: { [userData.physicalName]: userData.value, [userData.corePhysicalName]: userData.coreValue } }], "model-user-data");
    if (!isAppliedMutationResponse(inserted)) throw new Error(`model user data insert failed: ${JSON.stringify(inserted)}`);
    const originalRows = (await helpers.rawBridgeRequest(page, "query.page", { tableId: assets.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } })).payload.rows;
    userData.recordId = originalRows.find(row => row[userData.physicalName] === userData.value)?.id;
    if (!userData.recordId) throw new Error("synthetic user record identity missing");
    await verifyUserData(page, frame, recorder, helpers, userData, "before upgrade");
    const tables = await catalog(); const schemas = {};
    for (const [key, binding] of Object.entries(first.bindings)) schemas[key] = (await helpers.rawBridgeRequest(page, "schema.getTable", { tableId: binding.tableId })).payload;
    await page.getByTestId("plugin-app-return").click();
    await installApplication(page, runtime, "model-mapped", MAPPED, "open-model");
    frame = await openApplication(page, MAPPED, "open-model");
    const unconfigured = await frameCall(frame, "data.describe", { accepts: ["vibetable.plugin-data.v2"] });
    recorder.check("second plugin has no model grant before native mapping", unconfigured.result?.tables?.length === 0, { unconfigured });
    beforeCatalog = await catalogRevision();
    await frame.locator("#map").click();
    const selections = modelMappingSelections(tables, schemas, first.bindings);
    await nativeModel(runtime, "map", "plugin-model-preview", "map", selections);
    await nativeModel(runtime, "click", "plugin-model-confirm", "map");
    const mapped = await completed(frame);
    await waitForModelCatalog(page, mapped.bindings, beforeCatalog);
    recorder.check("native manual mapping binds differently named logical objects without creating tables", JSON.stringify(mapped.bindings) === JSON.stringify(first.bindings) && (await catalog()).length === 3, { mapped });
    await page.getByTestId("plugin-app-return").click();
    await selectPlugin(page, PLUGIN);
    await fs.writeFile(path.join(runtime.controlsDir, "plugin-source.txt"), `${path.join(runtime.controlsDir, "model-upgrade.vtplugin")}\n`);
    await helpers.beginBridgeMessageCapture(page, ["plugin.install.inspect", "operation.failed"]);
    await page.getByTestId("plugin-upgrade").click();
    const inspection = await helpers.waitForCapturedBridgeMessage(page, 30000);
    if (inspection.type !== "plugin.install.inspect") throw new Error("model upgrade inspection failed");
    await commitInspectedApplicationUpgrade(page, inspection.payload, helpers);
    frame = await openApplication(page, PLUGIN, "open-model");
    const beforeUpgrade = await frameCall(frame, "data.describe", { accepts: ["vibetable.plugin-data.v2"] });
    recorder.check("upgraded declaration receives no implicit model grant", beforeUpgrade.result?.tables?.length === 0, { beforeUpgrade });
    beforeCatalog = await catalogRevision();
    await frame.locator("#create").click();
    await nativeModel(runtime, "click", "plugin-model-confirm", "upgrade");
    const upgraded = await completed(frame);
    await waitForModelCatalog(page, upgraded.bindings, beforeCatalog);
    recorder.check("confirmed upgrade adds exactly one field and preserves previous canonical bindings", upgraded.bindings.assets.fields.checked && upgraded.bindings.assets.fields.name === first.bindings.assets.fields.name && (await catalog()).length === 3, { upgraded });
    const preserved = await verifyUserData(page, frame, recorder, helpers, userData, "confirmed upgrade");
    await page.getByTestId("plugin-app-return").click();
    frame = await openApplication(page, PLUGIN, "open-model");
    const reopened = await state(frame);
    recorder.check("reopened real application reads the durable completed model", reopened.status === "completed" && JSON.stringify(reopened.bindings) === JSON.stringify(upgraded.bindings), { reopened });
    const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
    recorder.check("model seed records the authoritative workspace session", typeof session?.workspaceId === "string"
      && Number.isSafeInteger(session.sessionEpoch) && session.sessionEpoch > 0
      && opened.payload.projectKey === `local:${session.workspaceId.replaceAll("-", "").toLowerCase()}`, { session });
    await fs.writeFile(path.join(runtime.evidenceDir, "54-models.json"), JSON.stringify({ first, repeated, mapped, beforeUpgrade, upgraded,
      reopened, cancelled, userData, preserved, nativePreview: observed, session }, null, 2));
    return { workspaceId: session.workspaceId, sessionEpoch: session.sessionEpoch, projectKey: opened.payload.projectKey,
      bindings: reopened.bindings, userData };
  } finally { await page.evaluate(releaseModelCatalogObserverInPage); }
}
