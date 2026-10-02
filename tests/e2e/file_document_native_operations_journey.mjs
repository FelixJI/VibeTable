import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

export function fileDocumentNativeFixture(id = randomUUID()) {
  assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  const name = `document-native-${id}.txt`;
  return { name, text: `VibeTable Task 415 native FileDocument\n${name}\n` };
}

export function requireNativeDragEvidence({ arm, completion, drop, source, copied, sourceBytes }) {
  assert.equal(completion.operationId, arm.operationId);
  assert.equal(completion.workspaceId, arm.workspaceId);
  assert.equal(completion.mode, arm.mode);
  assert.equal(completion.source, source);
  if (arm.mode === "cancel") {
    assert.equal(completion.effect, "None");
    assert.equal(drop, null, "cancel cannot count as a successful Copy");
  } else {
    assert.equal(arm.mode, "copy");
    assert.equal(completion.effect, "Copy");
    assert.equal(drop.operationId, arm.operationId);
    assert.equal(drop.workspaceId, arm.workspaceId);
    assert.equal(drop.source, source);
    assert.deepEqual(drop.received, [source]);
    assert.equal(drop.effect, "Copy");
    assert.ok(copied.equals(sourceBytes), "real Drop destination must contain the exact source bytes");
  }
}

export function requireNativeDocumentUnchanged(before, after, expectedBytes) {
  assert.deepEqual(after.document, before.document, "native action changed the authoritative document");
  assert.deepEqual(after.tree, before.tree, "native action changed immutable history or effective revision");
  assert.equal(after.workspaceId, before.workspaceId);
  assert.ok(after.importBytes.equals(expectedBytes), "native action changed the imported source");
  assert.ok(after.managedBytes.equals(expectedBytes), "native action changed managed bytes");
}

async function writeJson(file, value) {
  const temporary = `${file}.tmp`;
  await fs.writeFile(temporary, JSON.stringify(value), "utf8");
  await fs.rename(temporary, file);
}

async function readJson(file) {
  try { return JSON.parse(await fs.readFile(file, "utf8")); }
  catch (error) { if (error.code === "ENOENT") return null; throw error; }
}

async function waitJson(file, predicate, timeout = 5000) {
  const deadline = performance.now() + timeout;
  while (performance.now() < deadline) {
    const value = await readJson(file);
    if (value?.error) throw new Error(`${path.basename(file)}: ${value.error}`);
    if (value && predicate(value)) return value;
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  throw new Error(`no correlated native document evidence: ${path.basename(file)}`);
}

// Called only by the existing packaged-product scenario. This module neither
// launches a Host nor substitutes any Shell/COM/OLE production action.
export async function runFileDocumentNativeOperationsJourney(page, recorder, runtime, helpers) {
  const fixture = fileDocumentNativeFixture();
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  const workspaceId = session.workspaceId;
  const workspaceRoot = path.join(runtime.dataRoot, "workspaces", workspaceId);
  const manifest = await readJson(path.join(workspaceRoot, ".vibetable", "workspace.json"));
  recorder.check("native FileDocument fixture belongs to the current manifest UUID",
    manifest?.workspaceId === workspaceId && Number.isSafeInteger(session.sessionEpoch)
      && session.sessionEpoch > 0, { session, manifest });
  const original = path.join(runtime.controlsDir, fixture.name);
  await fs.writeFile(original, fixture.text, { encoding: "utf8", flag: "wx" });
  await page.getByTestId("nav-files").click();
  await page.locator(".file-search input").fill("");
  const filters = page.locator('[data-testid^="file-filter-chip-"]');
  for (let remaining = await filters.count(); remaining > 0; remaining--) await filters.first().click();
  const picker = path.join(runtime.controlsDir, "document-source.txt");
  const previousPicker = await fs.readFile(picker, "utf8");
  try {
    await fs.writeFile(picker, `${original}\n`, "utf8");
    await page.getByTestId("document-import").click();
    await page.locator('[data-testid^="document-row-"]').filter({ hasText: fixture.name })
      .waitFor({ state: "visible", timeout: 30000 });
  } finally {
    await fs.writeFile(picker, previousPicker, "utf8");
  }
  await helpers.beginBridgeMessageCapture(page, ["document.listLoaded", "operation.failed"]);
  await page.getByTestId("document-refresh").click();
  const listed = await helpers.waitForCapturedBridgeMessage(page);
  const entry = listed.payload?.entries?.find(item => item.relativePath === fixture.name);
  recorder.check("actual FileDocument list grants open/dragOut for the synthetic materialized file",
    listed.type === "document.listLoaded" && entry?.capabilities.includes("open")
      && entry.capabilities.includes("dragOut"), { entry });
  const source = path.join(workspaceRoot, "files", fixture.name);
  const sourceBytes = Buffer.from(fixture.text, "utf8");
  assert.ok((await fs.readFile(source)).equals(sourceBytes));
  async function nativeDocumentState(documentEntry = entry, importPath = original) {
    const query = await helpers.rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
      logic: "and", filters: [{ field: "relativePath", operator: "eq", value: documentEntry.relativePath }],
      sort: [{ field: "relativePath", direction: "asc" }], limit: 100, cursor: null,
    });
    assert.equal(query.result.documents.length, 1);
    const document = query.result.documents[0];
    assert.equal(document.documentId, documentEntry.documentId);
    assert.equal(document.relativePath, documentEntry.relativePath);
    const tree = (await helpers.rawWorkspaceV2Request(page, "fileHistory.readTree", {
      documentId: document.documentId,
    })).result;
    return { document, tree,
      workspaceId: (await readJson(path.join(workspaceRoot, ".vibetable", "workspace.json"))).workspaceId,
      importBytes: await fs.readFile(importPath),
      managedBytes: await fs.readFile(path.join(workspaceRoot, "files", documentEntry.relativePath)) };
  }
  const nativeBefore = await nativeDocumentState();
  assert.equal(nativeBefore.document.effectiveRevisionId, entry.effectiveRevisionId);
  requireNativeDocumentUnchanged(nativeBefore, nativeBefore, sourceBytes);
  async function verifyNativeDocument(action) {
    const after = await nativeDocumentState();
    requireNativeDocumentUnchanged(nativeBefore, after, sourceBytes);
    recorder.check(`native ${action} preserves document identity, immutable history and exact source bytes`,
      true, { document: after.document, tree: after.tree, workspaceId: after.workspaceId });
  }
  const row = page.getByTestId(`document-row-${entry.entryHandle}`);
  await row.waitFor({ state: "visible" });
  await row.scrollIntoViewIfNeeded();
  const destination = path.join(runtime.controlsDir, "document-drop", fixture.name);

  async function nativeRequest(action, operationId, extra = {}) {
    const requestId = randomUUID();
    await writeJson(path.join(runtime.evidenceDir, "file-document-native-request.json"), {
      requestId, operationId, action, workspaceId, relativePath: fixture.name, ...extra,
    });
    const result = await waitJson(path.join(runtime.evidenceDir, "file-document-native-result.json"),
      value => value.requestId === requestId, 10000);
    recorder.check(`native ${action} observation is verified`,
      result.status === (action === "copy" || action === "cancel" ? "injected" : "observed"), result);
    return result;
  }

  const dragResults = [];
  for (const mode of ["copy", "cancel"]) {
    const operationId = randomUUID();
    const arm = { operationId, workspaceId, relativePath: fixture.name, mode };
    for (const name of ["document-native-target.json", "document-native-drop-result.json",
      "document-native-drag-started.json", "document-native-drag-completed.json"]) {
      await fs.rm(path.join(runtime.controlsDir, name), { force: true });
    }
    await writeJson(path.join(runtime.controlsDir, "document-native-arm.json"), arm);
    await waitJson(path.join(runtime.controlsDir, "document-native-target.json"),
      value => value.operationId === operationId);
    const box = await row.boundingBox();
    assert.ok(box, "actual FileDocument row has no native input bounds");
    await nativeRequest(mode, operationId, {
      cssX: box.x + Math.min(50, box.width / 2), cssY: box.y + box.height / 2,
      devicePixelRatio: await page.evaluate(() => window.devicePixelRatio),
    });
    const completion = await waitJson(path.join(runtime.controlsDir, "document-native-drag-completed.json"),
      value => value.operationId === operationId);
    const drop = await readJson(path.join(runtime.controlsDir, "document-native-drop-result.json"));
    const copied = await fs.readFile(destination);
    requireNativeDragEvidence({ arm, completion, drop, source, copied, sourceBytes });
    assert.ok((await fs.readFile(source)).equals(sourceBytes), "native drag must retain the source");
    assert.ok(copied.equals(sourceBytes), "cancel must leave the prior Copy unchanged");
    recorder.check(`real FileDrop ${mode} has correlated effect and exact byte results`, true,
      { completion, drop, destination });
    dragResults.push({ completion, drop, destination });
  }

  const openOperationId = randomUUID();
  await nativeRequest("open-baseline", openOperationId);
  await helpers.beginBridgeMessageCapture(page, ["document.actionCompleted", "operation.failed"]);
  await row.dblclick();
  const openReply = await helpers.waitForCapturedBridgeMessage(page);
  recorder.check("production Shell Open receives the current synthetic FileDocument handle",
    openReply.type === "document.actionCompleted" && openReply.payload?.action === "open"
      && openReply.payload.entryHandle === entry.entryHandle, { openReply });
  const opened = await nativeRequest("open-observe", openOperationId);
  await verifyNativeDocument("open");

  const previewName = `document-native-preview-${randomUUID()}.html`;
  const previewText = "<!doctype html><html><body><pre>VibeTable Task 415 native preview\n"
    + `${previewName}\n</pre></body></html>\n`;
  const previewBytes = Buffer.from(previewText, "utf8");
  const previewOriginal = path.join(runtime.controlsDir, previewName);
  await fs.writeFile(previewOriginal, previewBytes, { flag: "wx" });
  try {
    await fs.writeFile(picker, `${previewOriginal}\n`, "utf8");
    await page.getByTestId("document-import").click();
    await page.locator('[data-testid^="document-row-"]').filter({ hasText: previewName })
      .waitFor({ state: "visible", timeout: 30000 });
  } finally {
    await fs.writeFile(picker, previousPicker, "utf8");
  }
  await helpers.beginBridgeMessageCapture(page, ["document.listLoaded", "operation.failed"]);
  await page.getByTestId("document-refresh").click();
  const previewListed = await helpers.waitForCapturedBridgeMessage(page);
  const previewEntry = previewListed.payload?.entries?.find(item => item.relativePath === previewName);
  recorder.check("actual synthetic HTML FileDocument requires a registered system preview handler",
    previewListed.type === "document.listLoaded" && previewEntry?.previewKind === "system"
      && previewEntry.capabilities.includes("preview"),
    { entry: previewEntry });
  const previewSource = path.join(workspaceRoot, "files", previewName);
  const previewBefore = await nativeDocumentState(previewEntry, previewOriginal);
  assert.equal(previewBefore.document.effectiveRevisionId, previewEntry.effectiveRevisionId);
  requireNativeDocumentUnchanged(previewBefore, previewBefore, previewBytes);
  await page.getByTestId(`document-row-${previewEntry.entryHandle}`).click();
  await fs.rm(path.join(runtime.controlsDir, "document-native-preview-result.json"), { force: true });
  await helpers.beginBridgeMessageCapture(page, ["document.actionCompleted", "operation.failed"]);
  await page.locator(".preview-stage").getByRole("button").click();
  const previewReply = await helpers.waitForCapturedBridgeMessage(page);
  recorder.check("production FileDocument preview reaches its actual handler",
    previewReply.type === "document.actionCompleted" && previewReply.payload?.action === "preview"
      && previewReply.payload.entryHandle === previewEntry.entryHandle, { previewReply });
  await waitJson(path.join(runtime.controlsDir, "document-native-preview-result.json"),
    value => value.source === previewSource);
  const preview = await nativeRequest("preview-observe", randomUUID(), { relativePath: previewName });
  const previewAfter = await nativeDocumentState(previewEntry, previewOriginal);
  requireNativeDocumentUnchanged(previewBefore, previewAfter, previewBytes);
  recorder.check("real HTML preview preserves document identity, immutable history and exact source bytes",
    true, { document: previewAfter.document, tree: previewAfter.tree, workspaceId: previewAfter.workspaceId });
  await verifyNativeDocument("preview of a separate HTML document");
  await page.screenshot({ path: path.join(runtime.evidenceDir, "415-native-file-document.png") });
  return { workspaceId, documentId: entry.documentId, source, dragResults, opened,
    previewDocumentId: previewEntry.documentId, previewSource, preview };
}
