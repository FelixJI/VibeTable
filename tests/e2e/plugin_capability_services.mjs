import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, nativePluginInput } from "./plugin_applications.mjs";
const ID = "com.vibetable.e2e.capability-services";
const OTHER = "com.vibetable.e2e.capability-other";
export function requireReceipt(receipt, label) {
  if (!receipt || receipt.ok !== true) throw new Error(`${label} failed: ${JSON.stringify(receipt)}`);
  return receipt.value;
}
export function requireScopedInvalidations(events, collection) {
  if (!events.length || events.some(event => event.type !== "invalidated" || event.collection !== collection
    || !Number.isSafeInteger(event.sequence) || Object.keys(event).sort().join() !== "collection,sequence,type"))
    throw new Error("subscription must only disclose scoped invalidation metadata");
  if (events.some((event, index) => index && event.sequence <= events[index - 1].sequence)) throw new Error("non-monotone invalidation");
}
export function requireSingleColumnExport(csv, expectedHeader, expectedValues) {
  const rows = csv.replace(/^\uFEFF/, "").trimEnd().split(/\r?\n/).map(line => {
    if (/^"(?:[^"\r\n]|"")*"$/.test(line)) return line.slice(1, -1).replaceAll('""', '"');
    if (/[",\r\n]/.test(line)) throw new Error("unexpected CSV column or quoting");
    return line;
  });
  // The SDK request and the Host-authorized projection use plugin field IDs, so the CSV
  // header carries that stable identifier; mutable display names never become column identities.
  if (rows.shift() !== expectedHeader || JSON.stringify(rows.sort()) !== JSON.stringify([...expectedValues].sort())) throw new Error("export bytes do not equal the explicit authorized projection");
}
async function picker(runtime, kind, action = "select") {
  const requestId = crypto.randomUUID();
  const target = path.join(runtime.evidenceDir, "plugin-file-dialog-request.json");
  await fs.writeFile(`${target}.tmp`, JSON.stringify({ requestId, kind, action }));
  await fs.rename(`${target}.tmp`, target);
  const until = Date.now() + 25000;
  while (Date.now() < until) {
    const result = await fs.readFile(path.join(runtime.evidenceDir, "plugin-file-dialog-result.json"), "utf8").then(JSON.parse).catch(error => { if (error.code === "ENOENT") return null; throw error; });
    if (result?.requestId === requestId) { if (result.status !== "completed") throw new Error(`real native picker failed: ${JSON.stringify(result)}`); return result; }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error("real native picker deadline");
}
export async function runCapabilityServices(page, recorder, runtime, helpers) {
  const evidence = { nativePickers: [], receipts: {}, authority: [], scope: {} };
  const save = () => fs.writeFile(path.join(runtime.evidenceDir, "52-capability-services.json"), JSON.stringify(evidence, null, 2));
  try {
    await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
    await page.getByTestId("nav-tables").click();
    const forbidden = await helpers.createSimpleTable(page, "Forbidden Capability Table", "Text");
    await helpers.applyProductMutation(page, forbidden.tableId, [{ kind: "insert", recordId: null, values: { [forbidden.field.physicalName]: "VIBETABLE_483_FORBIDDEN_TABLE" } }], "capability-forbidden-seed");
    const table = await helpers.createSimpleTable(page, "Capability Services", "Text");
    await helpers.createV2Field(page, table.tableId, "Attachments", "file");
    const relation = await helpers.createV2Field(page, table.tableId, "Self relation", "relation", draft => ({ ...draft, relation: { ...draft.relation, targetTableId: table.tableId, displayFieldId: table.field.fieldId } }));
    await helpers.applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null, values: { [table.field.physicalName]: "synthetic original" } }, { kind: "insert", recordId: null, values: { [table.field.physicalName]: "synthetic relation target" } }], "capability-seed");
    for (const name of ["attachment-replacement-source.txt", "attachment-target.txt", "import-source.txt", "export-target.txt"]) await fs.writeFile(path.join(runtime.controlsDir, name), "native-dialog\n");
    await fs.writeFile(path.join(runtime.controlsDir, "52-input.csv"), "VIBETABLE_INPUT_483\n");
    await fs.writeFile(path.join(runtime.controlsDir, "52-import.csv"), "Text\nimport-one\nimport-two\n");
    await installApplication(page, runtime, "capability-services", ID);
    await installApplication(page, runtime, "capability-other", OTHER);
    let frame = await openApplication(page, ID);
    let serial = 0;
    const begin = async (action, value = "") => { const key = `${action}-${++serial}`; await frame.evaluate(({ key, action, value }) => { void window.qa.run(key, action, value); }, { key, action, value }); return key; };
    const finish = async (key, allowError = false) => { await frame.waitForFunction(key => Object.hasOwn(window.qa.results, key), key, { timeout: 30000 }); const receipt = await frame.evaluate(key => window.qa.results[key], key); evidence.receipts[key] = receipt; await save(); return allowError ? receipt : requireReceipt(receipt, key); };
    const run = async (action, value) => finish(await begin(action, value));
    const authority = async () => { const result = await helpers.rawBridgeRequest(page, "query.page", { tableId: table.tableId, query: { filters: [], sorts: [], offset: 0, limit: 100 } }); evidence.authority.push(result.payload); await save(); return result.payload.rows; };
    const confirm = async (decision, label) => {
      const key = await begin("submit");
      await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
      const result = await nativePluginInput(runtime, "click", `plugin-confirm-${decision === "approved" ? "approve" : "reject"}`, decision);
      recorder.check(`${label} has real owned native confirmation input`, result.status === "completed", { result });
      await fs.copyFile(path.join(runtime.evidenceDir, `48-native-${decision}.png`), path.join(runtime.evidenceDir, `52-confirm-${label}.png`));
      const receipt = await finish(key);
      recorder.check(`${label} returns truthful mutation status`, receipt.status === (decision === "approved" ? "succeeded" : "rejected"), { receipt });
      return receipt;
    };
    const nativeCall = async (action, kind, decision = "select") => { const key = await begin(action); evidence.nativePickers.push(await picker(runtime, kind, decision)); await save(); return finish(key); };
    recorder.check("context and notification are closed presentation services", (await run("ui")).notification.shown === true);
    await page.screenshot({ path: path.join(runtime.evidenceDir, "52-ui-context.png"), fullPage: true });
    const listenerLifecycle = await run("listeners");
    recorder.check("twelve real SDK clients dispose to exact listener baseline", listenerLifecycle.baseline === 1 && listenerLifecycle.counts.length === 12 && listenerLifecycle.counts.every(count => count === 1), { listenerLifecycle });
    const deniedCollection = await run("denyCollection", forbidden.tableId);
    const deniedField = await run("denyField");
    recorder.check("actual Go reads reject ungranted table and unknown field", deniedCollection === "plugin_read_denied" && deniedField === "plugin_read_denied", { deniedCollection, deniedField });
    const initial = await authority();
    const cancelled = await nativeCall("pickRead", "read", "cancel");
    recorder.check("actual native picker cancellation returns null", cancelled === null);
    const readGrant = await nativeCall("pickRead", "read");
    const read = await run("read");
    recorder.check("real selected file bytes and one-shot grant agree", read.content === "VIBETABLE_INPUT_483\n" && read.reused === "plugin_file_denied" && !Object.hasOwn(readGrant, "path"), { read });
    const written = await nativeCall("write", "write");
    evidence.files = { read, written, actualWrittenText: await fs.readFile(path.join(runtime.controlsDir, "52-output.bin"), "utf8") };
    recorder.check("native output writes exact bytes to the selected synthetic file", evidence.files.actualWrittenText === "VIBETABLE_483_LITERAL_FILE\n");
    const invalid = await run("invalidBatch");
    recorder.check("mixed invalid batch has zero authoritative effect", invalid !== "accepted" && JSON.stringify(initial) === JSON.stringify(await authority()), { invalid });
    await run("prepare", "update"); await confirm("rejected", "update-rejected");
    recorder.check("native rejection commits zero records", JSON.stringify(initial) === JSON.stringify(await authority()));
    await run("prepare", "update"); await confirm("approved", "update-approved");
    recorder.check("SDK update reaches Go authority", (await authority()).some(row => row[table.field.physicalName] === "plugin-updated"));
    await run("prepare", "relation"); await confirm("approved", "relation-approved");
    const related = await run("snapshot");
    recorder.check("legal relation writes reach canonical plugin query", related.rows.items[0][relation.fieldId] === String(related.rows.items[1].id), { related });
    const attached = await nativeCall("attach", "read");
    recorder.check("attachment stages through the actual native selected file", attached.affectedCount === 1);
    await confirm("approved", "attachment-approved");
    const attachments = await run("attachments");
    recorder.check("authorized attachment returns actual selected contents", attachments.list.length === 1 && attachments.contents[0] === "VIBETABLE_INPUT_483\n", { attachments });
    const imported = await nativeCall("previewImport", "import");
    recorder.check("import task reports a real bounded successful preview", imported.status.state === "succeeded" && imported.result.summary.validRows === 2 && imported.result.summary.errorRows === 0 && imported.result.planId, { imported });
    await confirm("approved", "import-approved");
    const importedRows = await authority();
    recorder.check("approved import writes both actual source rows", ["import-one", "import-two"].every(value => importedRows.some(row => row[table.field.physicalName] === value)));
    const exported = await nativeCall("export", "export");
    const csv = await fs.readFile(path.join(runtime.controlsDir, "52-export.csv"), "utf8");
    evidence.export = { csv, receipt: exported };
    // The exported header is the authorized plugin field ID the fixture requested via data.export.
    requireSingleColumnExport(csv, table.field.fieldId, importedRows.map(row => String(row[table.field.physicalName] ?? "")));
    recorder.check("export task success matches selected output contents", exported.status.state === "succeeded" && csv.includes(table.field.fieldId) && csv.includes("import-one") && csv.includes("import-two") && !csv.includes("VIBETABLE_483_FORBIDDEN_TABLE") && !csv.includes("Self relation") && !csv.includes("Attachments"), { exported, csv });
    await run("prepare", "create"); await confirm("approved", "create-approved");
    recorder.check("SDK create has an authoritative row", (await authority()).some(row => row[table.field.physicalName] === "plugin-created"));
    const beforeDelete = await authority();
    await run("prepare", "delete"); await confirm("rejected", "delete-rejected");
    recorder.check("native destructive rejection preserves exact authority rows", JSON.stringify(await authority()) === JSON.stringify(beforeDelete));
    await run("prepare", "delete"); await confirm("approved", "delete-approved");
    recorder.check("native destructive confirmation deletes exactly one real row", (await authority()).length === beforeDelete.length - 1);
    const snapshot = await run("snapshot");
    requireScopedInvalidations(snapshot.events, table.tableId);
    recorder.check("subscription exposes no row or field contents and tasks reach success", snapshot.tasks.some(task => task.state === "succeeded"));
    await run("prepare", "create"); const pending = await begin("submit");
    await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
    const beforeCancel = await authority();
    const cancel = await run("cancelTask");
    recorder.check("projected task cancellation is a request receipt", cancel.receipt.cancelRequested === true, { cancel });
    const cancelledTask = await finish(pending, true);
    recorder.check("cancelled task never claims successful commit", cancelledTask.ok === false && cancelledTask.code === "plugin_cancel_requested", { cancelledTask });
    recorder.check("pre-approval task cancellation has zero Go mutation", JSON.stringify(await authority()) === JSON.stringify(beforeCancel));
    const held = await nativeCall("pickRead", "read");
    const token = await frame.evaluate(() => window.name);
    const unsubscribed = await run("unsubscribe");
    const countBefore = unsubscribed.events.length;
    await run("afterUnsubscribe");
    await helpers.applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null, values: { [table.field.physicalName]: "after-unsubscribe" } }], "after-unsubscribe");
    await frame.waitForFunction(() => window.qa.markerEvents.length > 0, undefined, { timeout: 10000 });
    const unsubscribedSnapshot = await run("snapshot");
    requireScopedInvalidations(unsubscribedSnapshot.markerEvents, table.tableId);
    recorder.check("unsubscribe stops its listener through a real replacement subscription delivery barrier", unsubscribedSnapshot.events.length === countBefore, { unsubscribedSnapshot });
    await begin("back");
    await page.getByTestId("plugin-app-viewport").waitFor({ state: "hidden", timeout: 30000 });
    frame = await openApplication(page, OTHER);
    const foreign = await run("foreignRead", held.grantId);
    const stale = await run("staleContext", token);
    const foreignAttachment = await run("foreignAttachment", attachments.list[0].attachmentId);
    recorder.check("retired cross-surface file grants and source tokens are rejected", foreign !== "accepted" && foreignAttachment !== "accepted" && stale === "plugin_source_denied", { foreign, foreignAttachment, stale });
    evidence.scope = { foreign, foreignAttachment, stale, firstToken: token, newToken: await frame.evaluate(() => window.name) };
    await helpers.applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null, values: { [table.field.physicalName]: "after-new-surface" } }], "after-new-surface");
    await frame.waitForFunction(() => window.qa.events.length > 0, undefined, { timeout: 10000 });
    const fresh = await run("snapshot");
    requireScopedInvalidations(fresh.events, table.tableId);
    recorder.check("a fresh surface receives scoped monotone notices after retirement", fresh.events.length > 0, { fresh });
    const freshListeners = await run("listeners");
    recorder.check("fresh surface repeat-dispose preserves exact listener baseline", freshListeners.counts.length === 12 && freshListeners.counts.every(count => count === 1), { freshListeners });
    await page.screenshot({ path: path.join(runtime.evidenceDir, "52-capability-services.png"), fullPage: true });
    await page.getByTestId("plugin-app-return").click();
    await save();
    return { tableId: table.tableId, nodeWorkerDisabled: true };
  } finally { await save(); }
}
