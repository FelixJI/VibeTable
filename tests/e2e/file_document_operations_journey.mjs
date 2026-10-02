import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";

// Keep nanoseconds: Date truncates distinct authority timestamps to milliseconds.
function timestamp(value) {
  const match = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z$/.exec(value);
  assert.ok(match, `unexpected authority timestamp: ${value}`);
  return `${match[1]}.${(match[2] ?? "").padEnd(9, "0")}`;
}

export function documentOrderOracle(documents) {
  return [...documents].sort((a, b) => {
    const left = timestamp(a.effectiveRevisionCreatedAt);
    const right = timestamp(b.effectiveRevisionCreatedAt);
    return (left < right ? 1 : left > right ? -1 : 0)
      || (a.documentId < b.documentId ? -1 : a.documentId > b.documentId ? 1 : 0);
  }).map(item => item.documentId);
}

export function requireDocumentPages(pages, documents, fixtures) {
  assert.equal(documents.length, fixtures.length);
  assert.equal(new Set(documents.map(item => item.documentId)).size, fixtures.length);
  const byPath = new Map(documents.map(item => [item.relativePath, item]));
  assert.equal(byPath.size, fixtures.length);
  for (const fixture of fixtures) {
    const document = byPath.get(fixture.relativePath);
    assert.ok(document, `missing fixture ${fixture.relativePath}`);
    assert.equal(document.sizeBytes, Buffer.byteLength(fixture.text));
    assert.equal(document.status, "active");
  }
  assert.deepEqual(pages.map(item => item.payload.entries.length), [100, 1]);
  assert.equal(pages[0].query.cursor, null);
  assert.ok(pages[0].payload.nextCursor);
  assert.equal(pages[1].query.cursor, pages[0].payload.nextCursor);
  assert.equal(pages[1].payload.nextCursor, null);
  assert.equal(pages[0].payload.topologyRevision, pages[1].payload.topologyRevision);
  for (const page of pages) {
    assert.equal(page.query.limit, 100);
    assert.deepEqual(page.query.filters, [{ field: "status", operator: "eq", value: "active" }]);
    assert.deepEqual(page.query.sort, [{ field: "effectiveRevisionCreatedAt", direction: "desc" }]);
  }
  const entries = pages.flatMap(item => item.payload.entries);
  // Assert the raw Host pages before the UI store can deduplicate them.
  assert.deepEqual(entries.map(item => item.documentId), documentOrderOracle(documents));
  for (const entry of entries) {
    const source = byPath.get(entry.relativePath);
    assert.equal(entry.documentId, source.documentId);
    assert.equal(entry.effectiveRevisionId, source.effectiveRevisionId);
  }
  return entries;
}

export function requireUnchangedRevisions(before, after) {
  for (const revision of before.revisions) {
    assert.deepEqual(after.revisions.find(item => item.revisionId === revision.revisionId), revision);
  }
}

export function requireAppendedRevision(before, after, { id, parent, kind, source = null }) {
  requireUnchangedRevisions(before, after);
  assert.equal(after.revisions.length, before.revisions.length + 1);
  assert.equal(after.documentId, before.documentId);
  assert.equal(after.effectiveRevisionId, id);
  const added = after.revisions.find(item => item.revisionId === id);
  assert.ok(added);
  assert.equal(added.parentRevisionId, parent);
  assert.equal(added.kind, kind);
  assert.equal(added.restoredFromRevisionId, source);
  assert.equal(added.revisionOrdinal, Math.max(...before.revisions.map(item => item.revisionOrdinal)) + 1);
  assert.equal(added.formalVersion, kind === "autosave" ? null
    : Math.max(0, ...before.revisions.map(item => item.formalVersion ?? 0)) + 1);
  if (source !== null) {
    const historical = before.revisions.find(item => item.revisionId === source);
    assert.equal(added.objectId, historical.objectId);
    assert.equal(added.contentHash, historical.contentHash);
    assert.equal(added.size, historical.size);
  }
  return added;
}

// Observe genuine UI list requests and their exact terminals; never populate a store.
export function installDocumentPageCaptureInPage() {
  const webview = window.chrome.webview;
  const original = webview.postMessage;
  const capture = { requests: {}, pages: [], original };
  const parse = value => typeof value === "string" ? JSON.parse(value) : value;
  capture.wrapper = function (raw) {
    const message = parse(raw);
    if (message?.type === "document.listRequested") {
      capture.requests[message.requestId] = structuredClone(message.payload.query);
    }
    return original.call(this, raw);
  };
  capture.listener = event => {
    let message;
    try { message = parse(event.data); } catch { return; }
    const query = capture.requests[message?.requestId];
    if (message?.type === "document.listLoaded" && query) {
      capture.pages.push({ requestId: message.requestId, query,
        payload: structuredClone(message.payload) });
      delete capture.requests[message.requestId];
    }
  };
  capture.release = () => {
    webview.removeEventListener("message", capture.listener);
    if (webview.postMessage === capture.wrapper) webview.postMessage = original;
  };
  webview.addEventListener("message", capture.listener);
  webview.postMessage = capture.wrapper;
  window.__s41DocumentPages = capture;
}

async function workspace(page, recorder, runtime, helpers) {
  await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  assert.match(session.workspaceId, /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  const root = path.resolve(runtime.dataRoot, "workspaces", session.workspaceId);
  const manifest = JSON.parse(await fs.readFile(path.join(root, ".vibetable", "workspace.json"), "utf8"));
  recorder.check("file operations bind the synthetic managed root to its manifest UUID",
    manifest.workspaceId === session.workspaceId
      && path.dirname(root) === path.resolve(runtime.dataRoot, "workspaces"), { session, root });
  return { workspaceId: session.workspaceId, root, files: path.join(root, "files") };
}

async function closeWorkspace(page, recorder, helpers) {
  await helpers.openWorkspaceCenterFromSwitcher(page);
  const reply = await helpers.replicaUiMethod(page, recorder, "workspace.close", () =>
    page.getByTestId("workspace-center").getByRole("button", {
      name: /关闭当前工作区|Close current workspace/,
    }).click());
  recorder.check("synthetic file operation closes the actual authority", reply.result.state === "closed");
}

async function reopen(page, recorder, helpers, fixture) {
  const opened = await helpers.activateWorkspaceThroughUi(page, {
    method: "workspace.open", waitForHydration: true,
    activate: () => page.getByTestId(`workspace-delete-${fixture.workspaceId}`).locator("..")
      .getByRole("button", { name: /打开|Open/, exact: true }).click(),
  });
  recorder.check("file rescan reopens the same writable workspace UUID",
    opened.session.workspaceId === fixture.workspaceId && opened.session.writable === true);
  await page.getByTestId("nav-files").click();
  await page.getByTestId("file-workspace").waitFor({ state: "visible", timeout: 30000 });
}

async function query(page, helpers, filters = []) {
  return (await helpers.rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "and", filters, sort: [{ field: "relativePath", direction: "asc" }],
    limit: 500, cursor: null,
  })).result;
}

async function tree(page, helpers, documentId) {
  return (await helpers.rawWorkspaceV2Request(page, "fileHistory.readTree", { documentId })).result;
}

export function documentRefreshReadyInPage({ priorRequestIds, relativePath, previousHandle, search }) {
  const diagnostics = window.__vibetableE2EBridgeDiagnostics;
  const loaded = (window.__s41DocumentPages?.pages ?? []).filter(page =>
    page.query.filters.some(filter => filter.field === "displayName"
      && filter.operator === "contains" && filter.value === search)
    && (diagnostics?.roundTrips ?? []).some(item =>
      item.requestId === page.requestId && !priorRequestIds.includes(item.requestId)
      && item.requestType === "document.listRequested"
      && item.responseType === "document.listLoaded" && item.code === null));
  const settled = Object.values(diagnostics?.pending ?? {}).every(item =>
    item.requestType !== "document.listRequested");
  return settled && loaded.some(page => [...document.querySelectorAll('[data-testid^="document-row-"]')]
    .some(row => row instanceof HTMLElement && row.getAttribute("data-testid") !== previousHandle
      && row.querySelector("small")?.textContent?.trim().startsWith(`${relativePath} · `)
      && page.payload.entries.some(entry => entry.relativePath === relativePath
        && row.getAttribute("data-testid") === `document-row-${entry.entryHandle}`)));
}

async function refreshRow(page, relativePath) {
  const name = path.posix.basename(relativePath);
  const previousHandle = await page.evaluate(relativePath => [...document.querySelectorAll('[data-testid^="document-row-"]')]
    .find(row => row.querySelector("small")?.textContent?.trim().startsWith(`${relativePath} · `))
    ?.getAttribute("data-testid") ?? null, relativePath);
  const priorRequestIds = await page.evaluate(() =>
    (window.__vibetableE2EBridgeDiagnostics?.roundTrips ?? []).map(item => item.requestId));
  const search = page.getByTestId("file-workspace").getByRole("textbox").first();
  await page.evaluate(installDocumentPageCaptureInPage);
  try {
    if (await search.inputValue() === name) await page.getByTestId("document-refresh").click();
    else await search.fill(name);
    await page.waitForFunction(documentRefreshReadyInPage,
      { priorRequestIds, relativePath, previousHandle, search: name }, { timeout: 30000 });
    const row = page.locator('[data-testid^="document-row-"]').filter({
      has: page.locator("small", { hasText: new RegExp(`^\\s*${relativePath.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} · `) }),
    });
    await row.waitFor({ state: "visible", timeout: 30000 });
    await row.click();
    await row.and(page.locator('button[aria-selected="true"]')).waitFor({ state: "visible", timeout: 30000 });
    return row;
  } finally {
    await page.evaluate(() => { window.__s41DocumentPages.release(); delete window.__s41DocumentPages; });
  }
}

async function importFile(page, recorder, runtime, helpers, name, text) {
  const source = path.join(runtime.controlsDir, name);
  await fs.writeFile(source, text, "utf8");
  await fs.writeFile(path.join(runtime.controlsDir, "document-source.txt"), `${source}\n`, "utf8");
  await page.getByTestId("nav-files").click();
  await helpers.beginBridgeMessageCapture(page, ["document.workspaceChanged", "document.operationFailed"]);
  await page.getByTestId("document-import").click();
  const imported = await helpers.waitForCapturedBridgeMessage(page, 30000);
  recorder.check("Host picker import reports its actual success", imported.payload?.reason === "import", { imported });
  const found = (await query(page, helpers, [{ field: "relativePath", operator: "eq", value: name }])).documents;
  recorder.check("real Host picker import creates exactly one active file identity", found.length === 1);
  return found[0];
}

async function bytes(recorder, fixture, relativePath, expected) {
  const actual = await fs.readFile(path.join(fixture.files, relativePath), "utf8");
  recorder.check(`${relativePath}: materialized literal bytes match the independent fixture`, actual === expected);
}

async function pending(page, helpers, relativePath, missing) {
  const changes = (await helpers.rawWorkspaceV2Request(page, "fileHistory.listPendingChanges", {})).result.changes;
  const found = changes.filter(item => item.relativePath === relativePath && item.missing === missing);
  assert.equal(found.length, 1, `expected one persisted pending for ${relativePath}`);
  return found[0];
}

async function confirm(page, recorder, helpers, change, action, sourcePath, documentId) {
  await refreshRow(page, sourcePath);
  await page.getByTestId("pending-file-change-alert").getByRole("button").click();
  const reply = await helpers.replicaUiMethod(page, recorder, "fileHistory.applyPendingChange", () =>
    page.getByTestId(`pending-${action}-${change.changeId}`).click());
  recorder.check(`${action} confirms the observed identity through the real pending UI`,
    reply.request.payload.params.changeId === change.changeId
      && reply.request.payload.params.documentId === documentId
      && reply.result.state === "applied", { request: reply.request, result: reply.result });
  await page.keyboard.press("Escape");
  return reply.result.document;
}

export async function runFileDocumentOperationsJourney(page, recorder, runtime, helpers) {
  const fixture = await workspace(page, recorder, runtime, helpers);
  const imported = await importFile(page, recorder, runtime, helpers, "identity-source.txt", "S41 original identity\n");
  const fixtures = [{ relativePath: imported.relativePath, text: "S41 original identity\n" },
    ...Array.from({ length: 98 }, (_, index) => ({
      relativePath: `page-${String(index).padStart(3, "0")}.txt`, text: `S41 distinct content ${index}\n`,
    })),
    { relativePath: "left/same-name.txt", text: "S41 left independently owned content\n" },
    { relativePath: "right/same-name.txt", text: "S41 right independently owned content\n" }];
  await closeWorkspace(page, recorder, helpers);
  // One batch while closed, then the normal startup watcher discovers all files.
  for (const item of fixtures.slice(1)) {
    await fs.mkdir(path.dirname(path.join(fixture.files, item.relativePath)), { recursive: true });
    await fs.writeFile(path.join(fixture.files, item.relativePath), item.text, "utf8");
  }
  await reopen(page, recorder, helpers, fixture);
  const all = await query(page, helpers);
  await page.locator('[data-testid^="document-row-"]').nth(99).waitFor({ state: "visible", timeout: 30000 });
  await page.waitForFunction(() => Object.values(window.__vibetableE2EBridgeDiagnostics?.pending ?? {})
    .every(item => item.requestType !== "document.listRequested"), null, { timeout: 30000 });
  await page.evaluate(installDocumentPageCaptureInPage);
  let entries;
  try {
    await page.getByTestId("document-refresh").click();
    await page.waitForFunction(() => window.__s41DocumentPages.pages.length === 1, null, { timeout: 30000 });
    await page.locator('[data-testid^="document-row-"]').nth(99).waitFor({ state: "visible", timeout: 30000 });
    await page.getByTestId("document-load-more").click();
    await page.waitForFunction(() => window.__s41DocumentPages.pages.length === 2, null, { timeout: 30000 });
    await page.locator('[data-testid^="document-row-"]').nth(100).waitFor({ state: "visible", timeout: 30000 });
    const pages = await page.evaluate(() => window.__s41DocumentPages.pages);
    entries = requireDocumentPages(pages, all.documents, fixtures);
    recorder.check("raw Host pages preserve the complete independent 101-item order with no loss or duplicates", true);
    const handles = await page.locator('[data-testid^="document-row-"]').evaluateAll(rows =>
      rows.map(row => row.getAttribute("data-testid")));
    recorder.check("real list displays every raw-page identity in authority order",
      isDeepStrictEqual(handles, entries.map(item => `document-row-${item.entryHandle}`))
        && await page.getByTestId("document-load-more").count() === 0, { handles });
    await fs.writeFile(path.join(runtime.evidenceDir, "41-original-document-pages.json"),
      JSON.stringify({ workspaceId: fixture.workspaceId, fixtures, documents: all.documents, pages }, null, 2));
  } finally {
    await page.evaluate(() => { window.__s41DocumentPages.release(); delete window.__s41DocumentPages; });
  }
  const left = all.documents.find(item => item.relativePath === "left/same-name.txt");
  const right = all.documents.find(item => item.relativePath === "right/same-name.txt");
  recorder.check("same basename with different contents creates separate identities",
    left.documentId !== right.documentId && left.effectiveRevisionId !== right.effectiveRevisionId);
  for (const item of fixtures) await bytes(recorder, fixture, item.relativePath, item.text);

  const at = relativePath => all.documents.find(item => item.relativePath === relativePath);
  const originalTree = await tree(page, helpers, imported.documentId);
  const movedTree = await tree(page, helpers, at("page-000.txt").documentId);
  const copyTree = await tree(page, helpers, at("page-001.txt").documentId);
  await closeWorkspace(page, recorder, helpers);
  await fs.mkdir(path.join(fixture.files, "archive"), { recursive: true });
  await fs.rename(path.join(fixture.files, imported.relativePath), path.join(fixture.files, "identity-renamed.txt"));
  await fs.rename(path.join(fixture.files, "page-000.txt"), path.join(fixture.files, "archive/page-000.txt"));
  await fs.copyFile(path.join(fixture.files, "page-001.txt"), path.join(fixture.files, "confirmed-copy.txt"));
  await fs.rename(path.join(fixture.files, "page-002.txt"), path.join(runtime.controlsDir, "41-missing-source.txt"));
  await reopen(page, recorder, helpers, fixture);
  const beforeConfirmation = await query(page, helpers);
  recorder.check("watcher cannot auto-bind unconfirmed rename, move or copy",
    beforeConfirmation.documents.length === 101
      && !beforeConfirmation.documents.some(item => ["identity-renamed.txt", "archive/page-000.txt", "confirmed-copy.txt"]
        .includes(item.relativePath)));
  const stale = await pending(page, helpers, imported.relativePath, true);
  const renamed = await confirm(page, recorder, helpers, await pending(page, helpers, "identity-renamed.txt", false),
    "move", imported.relativePath, imported.documentId);
  recorder.check("confirmed rename preserves the original document and effective revision",
    renamed.documentId === imported.documentId && renamed.relativePath === "identity-renamed.txt"
      && renamed.effectiveRevisionId === imported.effectiveRevisionId);
  assert.deepEqual(await tree(page, helpers, imported.documentId), originalTree);
  await bytes(recorder, fixture, "identity-renamed.txt", fixtures[0].text);
  await refreshRow(page, "identity-renamed.txt");
  await page.getByTestId("pending-file-change-alert").getByRole("button").click();
  await helpers.beginWorkspaceV2MethodCapture(page, "fileHistory.applyPendingChange");
  await page.getByTestId(`pending-delete-${stale.changeId}`).click();
  const rejected = await helpers.waitForCapturedBridgeMessage(page, 30000);
  recorder.check("old missing confirmation is rejected after a same-effective rename",
    rejected.payload?.ok === false && rejected.payload?.error?.code === "file_history.pending_change_stale", { rejected });
  await helpers.acknowledgeExpectedBridgeFailure(page, rejected);
  await page.keyboard.press("Escape");
  assert.deepEqual(await tree(page, helpers, imported.documentId), originalTree);
  const preserved = (await query(page, helpers)).documents.find(item => item.documentId === imported.documentId);
  recorder.check("rejected stale delete preserves current path, status and effective revision",
    preserved.relativePath === "identity-renamed.txt" && preserved.status === "active"
      && preserved.effectiveRevisionId === imported.effectiveRevisionId);
  assert.equal((await pending(page, helpers, imported.relativePath, true)).changeId, stale.changeId);
  await bytes(recorder, fixture, "identity-renamed.txt", fixtures[0].text);

  const moved = await confirm(page, recorder, helpers, await pending(page, helpers, "archive/page-000.txt", false),
    "move", "page-000.txt", at("page-000.txt").documentId);
  recorder.check("confirmed directory move preserves identity and effective revision",
    moved.documentId === at("page-000.txt").documentId && moved.relativePath === "archive/page-000.txt"
      && moved.effectiveRevisionId === at("page-000.txt").effectiveRevisionId);
  assert.deepEqual(await tree(page, helpers, moved.documentId), movedTree);
  const copied = await confirm(page, recorder, helpers, await pending(page, helpers, "confirmed-copy.txt", false),
    "copy", "page-001.txt", at("page-001.txt").documentId);
  recorder.check("explicit copy confirmation creates a new identity without changing its source",
    copied.documentId !== at("page-001.txt").documentId && copied.relativePath === "confirmed-copy.txt");
  assert.deepEqual(await tree(page, helpers, at("page-001.txt").documentId), copyTree);
  await bytes(recorder, fixture, "archive/page-000.txt", fixtures[1].text);
  await bytes(recorder, fixture, "confirmed-copy.txt", fixtures[2].text);
  await bytes(recorder, fixture, "page-001.txt", fixtures[2].text);

  const missing = at("page-002.txt");
  const missingTree = await tree(page, helpers, missing.documentId);
  // Confirmed transactions rematerialize active leaves, so create fresh missing state afterwards.
  await bytes(recorder, fixture, missing.relativePath, fixtures[3].text);
  await closeWorkspace(page, recorder, helpers);
  const missingMaterializedSource = path.join(runtime.controlsDir, "41-missing-materialized-source.txt");
  await assert.rejects(fs.stat(missingMaterializedSource), { code: "ENOENT" });
  await fs.rename(path.join(fixture.files, missing.relativePath), missingMaterializedSource);
  await assert.rejects(fs.stat(path.join(fixture.files, missing.relativePath)), { code: "ENOENT" });
  recorder.check("fresh missing fixture preserves both independently moved sources",
    await fs.readFile(missingMaterializedSource, "utf8") === fixtures[3].text
      && await fs.readFile(path.join(runtime.controlsDir, "41-missing-source.txt"), "utf8") === fixtures[3].text);
  await reopen(page, recorder, helpers, fixture);
  await refreshRow(page, missing.relativePath);
  await helpers.beginBridgeMessageCapture(page, ["document.listLoaded"]);
  await page.getByTestId("document-refresh").click();
  const missingList = await helpers.waitForCapturedBridgeMessage(page, 30000);
  const missingEntry = missingList.payload.entries.find(item => item.documentId === missing.documentId);
  recorder.check("missing state exposes only the supported picker reconnection capability",
    missingEntry?.availability === "missing" && missingEntry.capabilities.includes("relink")
      && !missingEntry.capabilities.includes("open"), { missingEntry, missingList });
  await refreshRow(page, missing.relativePath);
  const replacement = "S41 replacement chosen through the Host picker\n";
  const replacementSource = path.join(runtime.controlsDir, "41-relink-source.txt");
  await fs.writeFile(replacementSource, replacement, "utf8");
  await fs.writeFile(path.join(runtime.controlsDir, "document-source.txt"), `${replacementSource}\n`, "utf8");
  await helpers.beginBridgeMessageCapture(page, ["document.workspaceChanged", "document.operationFailed"]);
  await page.getByTestId("file-workspace").getByRole("button", { name: /^(重新定位|Locate again)$/ }).click();
  const relinkNotice = await helpers.waitForCapturedBridgeMessage(page, 30000);
  recorder.check("picker reconnect reports its actual success", relinkNotice.payload?.reason === "relink", { relinkNotice });
  const relinked = (await query(page, helpers)).documents.find(item => item.documentId === missing.documentId);
  recorder.check("picker reconnection preserves document/path identity and advances content",
    relinked.relativePath === missing.relativePath && relinked.status === "active"
      && relinked.effectiveRevisionId !== missing.effectiveRevisionId);
  requireAppendedRevision(missingTree, await tree(page, helpers, missing.documentId), {
    id: relinked.effectiveRevisionId, parent: missing.effectiveRevisionId, kind: "formal",
  });
  await bytes(recorder, fixture, missing.relativePath, replacement);
  recorder.check("relink reads the selected synthetic source without rewriting it",
    await fs.readFile(replacementSource, "utf8") === replacement
      && await fs.readFile(path.join(runtime.controlsDir, "41-missing-source.txt"), "utf8") === fixtures[3].text
      && await fs.readFile(missingMaterializedSource, "utf8") === fixtures[3].text);
  const final = await query(page, helpers);
  recorder.check("all confirmed operations preserve the expected document set", final.documents.length === 102);
  await page.screenshot({ path: path.join(runtime.evidenceDir, "41-file-identity-confirmations.png") });
  return { workspaceId: fixture.workspaceId, documents: final.documents.length };
}

export async function historyUi(page, relativePath, effectiveRevisionId) {
  await refreshRow(page, relativePath);
  await page.getByTestId("file-workspace").locator(".inspector-tabs button").nth(1).click();
  const treeUi = page.getByTestId("file-revision-tree");
  await treeUi.locator(`[data-revision-id="${effectiveRevisionId}"][aria-current="true"]`)
    .waitFor({ state: "visible", timeout: 30000 });
  const show = treeUi.locator('header button[aria-pressed="false"]');
  if (await show.count()) await show.click();
  return treeUi;
}

export function requireActivatedLeaf(before, after, leafId) {
  assert.equal(after.documentId, before.documentId);
  assert.ok(before.revisions.some(item => item.revisionId === leafId));
  assert.ok(!before.revisions.some(item => item.parentRevisionId === leafId));
  assert.equal(after.effectiveRevisionId, leafId);
  assert.deepEqual(after.revisions, before.revisions);
}

export async function runFileRevisionLeavesJourney(page, recorder, runtime, helpers) {
  const fixture = await workspace(page, recorder, runtime, helpers);
  const document = await importFile(page, recorder, runtime, helpers, "revision-leaves.txt", "S43 formal seed\n");
  let previous = await tree(page, helpers, document.documentId);
  const seed = previous.effectiveRevisionId;
  recorder.check("picker seed is a canonical formal revision", previous.revisions.length === 1
    && previous.revisions[0].formalVersion === 1 && previous.revisions[0].revisionOrdinal === 1);
  const autosaves = [];
  for (const text of ["S43 first external autosave\n", "S43 second external autosave\n"]) {
    await closeWorkspace(page, recorder, helpers);
    await fs.writeFile(path.join(fixture.files, document.relativePath), text, "utf8");
    await reopen(page, recorder, helpers, fixture);
    const next = await tree(page, helpers, document.documentId);
    const added = requireAppendedRevision(previous, next, {
      id: next.effectiveRevisionId, parent: previous.effectiveRevisionId, kind: "autosave",
    });
    autosaves.push(added.revisionId);
    recorder.check("real startup watcher appends an immutable autosave under the previous effective revision", true,
      { revision: added });
    await bytes(recorder, fixture, document.relativePath, text);
    previous = next;
  }
  const treeUi = await historyUi(page, document.relativePath, previous.effectiveRevisionId);
  await treeUi.locator(`[data-revision-id="${autosaves[0]}"]`).waitFor({ state: "visible", timeout: 30000 });
  const upload = path.join(runtime.controlsDir, "43-formal-branch.txt");
  const branchText = "S43 formal branch from first autosave\n";
  await fs.writeFile(upload, branchText, "utf8");
  await fs.writeFile(path.join(runtime.controlsDir, "file-upgrade-source.txt"), `${upload}\n`, "utf8");
  const upgraded = await helpers.replicaUiMethod(page, recorder, "fileHistory.upgrade", () =>
    treeUi.locator(`[data-revision-id="${autosaves[0]}"]`)
      .getByRole("button", { name: /^(从此升级|Upgrade from here)$/ }).click());
  const branch = await tree(page, helpers, document.documentId);
  requireAppendedRevision(previous, branch, {
    id: upgraded.result.revisionId, parent: autosaves[0], kind: "formal",
  });
  recorder.check("formal upgrade creates a sibling branch without renumbering autosaves",
    branch.revisions.filter(item => item.parentRevisionId === autosaves[0]).length === 2
      && upgraded.result.formalVersion === 2 && seed === branch.revisions[0].revisionId);
  await bytes(recorder, fixture, document.relativePath, branchText);
  await historyUi(page, document.relativePath, branch.effectiveRevisionId);
  const leaf = treeUi.locator(`[data-revision-id="${autosaves[1]}"]`);
  await treeUi.locator(`[data-revision-id="${branch.effectiveRevisionId}"][aria-current="true"]`)
    .waitFor({ state: "visible", timeout: 30000 });
  await leaf.waitFor({ state: "visible", timeout: 30000 });
  recorder.check("the restored source is a visible noncurrent leaf",
    await leaf.getAttribute("aria-current") !== "true"
      && !branch.revisions.some(item => item.parentRevisionId === autosaves[1]));
  const restored = await helpers.replicaUiMethod(page, recorder, "fileHistory.restore", () =>
    leaf.getByRole("button", { name: /^(恢复为新版本|Restore as new version)$/ }).click());
  const afterRestore = await tree(page, helpers, document.documentId);
  requireAppendedRevision(branch, afterRestore, { id: restored.result.revisionId,
    parent: branch.effectiveRevisionId, kind: "restore", source: autosaves[1] });
  recorder.check("noncurrent leaf restore appends a new formal revision from the selected source",
    restored.result.formalVersion === 3 && restored.result.revisionId !== autosaves[1]);
  await bytes(recorder, fixture, document.relativePath, "S43 second external autosave\n");
  await historyUi(page, document.relativePath, afterRestore.effectiveRevisionId);
  await treeUi.locator(`[data-revision-id="${afterRestore.effectiveRevisionId}"][aria-current="true"]`)
    .waitFor({ state: "visible", timeout: 30000 });
  await helpers.replicaUiMethod(page, recorder, "fileHistory.activateLeaf", () =>
    treeUi.locator(`[data-revision-id="${autosaves[1]}"]`)
      .getByRole("button", { name: /^(设为当前分支|Make current branch)$/ }).click());
  const activated = await tree(page, helpers, document.documentId);
  requireActivatedLeaf(afterRestore, activated, autosaves[1]);
  recorder.check("leaf activation changes only the effective pointer and keeps all ordinals/history",
    activated.effectiveRevisionId === autosaves[1]
      && isDeepStrictEqual(activated.revisions, afterRestore.revisions));
  await bytes(recorder, fixture, document.relativePath, "S43 second external autosave\n");
  await historyUi(page, document.relativePath, activated.effectiveRevisionId);
  await treeUi.locator(`[data-revision-id="${autosaves[1]}"][aria-current="true"]`)
    .waitFor({ state: "visible", timeout: 30000 });
  await closeWorkspace(page, recorder, helpers);
  await reopen(page, recorder, helpers, fixture);
  assert.deepEqual(await tree(page, helpers, document.documentId), activated);
  await bytes(recorder, fixture, document.relativePath, "S43 second external autosave\n");
  const summary = (await query(page, helpers)).documents.find(item => item.documentId === document.documentId);
  recorder.check("same-UUID reopen and list retain the activated leaf",
    summary.effectiveRevisionId === autosaves[1] && summary.relativePath === document.relativePath);
  await historyUi(page, document.relativePath, activated.effectiveRevisionId);
  await page.screenshot({ path: path.join(runtime.evidenceDir, "43-file-revision-leaves.png") });
  await fs.writeFile(path.join(runtime.evidenceDir, "43-file-revision-authority.json"),
    JSON.stringify({ workspaceId: fixture.workspaceId, seed, autosaves, branch, afterRestore, activated }, null, 2));
  return { workspaceId: fixture.workspaceId, documentId: document.documentId };
}
