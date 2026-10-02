import fs from "node:fs/promises";
import path from "node:path";

// The fixture producer and this consumer both use the existing file-history
// authority. This journey starts in a real packaged Host with cold data.
export async function runFileHistoryCapacityJourney(page, recorder, runtime, helpers) {
  if (!helpers.metadataPath) {
    throw new Error("the capacity journey requires its fixture metadata path (--state)");
  }
  const metadata = JSON.parse(await fs.readFile(helpers.metadataPath, "utf8"));
  const fixture = metadata.fixtures[0];
  // The producer may serialize the lease identity nested or flattened; both
  // shapes carry the same fields, and nothing here synthesizes either one.
  const identity = fixture.identity ?? fixture;
  recorder.check("capacity fixture stays within the shared root quotas",
    fixture.documents <= 10000 && fixture.revisions <= 10000
      && fixture.selectedMultiRevisionCount <= 4096,
    { documents: fixture.documents, revisions: fixture.revisions });
  const manifest = JSON.parse(await fs.readFile(fixture.manifestPath, "utf8"));
  recorder.check("cold Host connects the declared workspace UUID",
    manifest.workspaceId === identity.workspaceId, { workspaceId: manifest.workspaceId });
  await fs.writeFile(path.join(runtime.controlsDir, "workspace-root.txt"),
    `${fixture.workspaceRoot}\n`, "utf8");
  await page.getByTestId("workspace-center").waitFor({ state: "visible" });
  await page.getByTestId("workspace-connect").click();
  const registered = await helpers.replicaUiMethod(page, recorder, "workspace.register",
    () => page.getByTestId("workspace-flow-confirm").click());
  recorder.check("normal picker registration preserves the fixture UUID",
    registered.result.workspaceId === identity.workspaceId
      && registered.result.status === "registered");
  const started = performance.now();
  const opened = await helpers.activateWorkspaceThroughUi(page, {
    waitForHydration: true, method: "workspace.open",
    activate: () => page.getByTestId(`workspace-delete-${identity.workspaceId}`)
      .locator("..").getByRole("button", { name: /打开|Open/, exact: true }).click(),
  });
  recorder.check("normal cold open inherits persisted authority and advances the session",
    opened.session.workspaceId === identity.workspaceId
      && opened.session.sessionEpoch > identity.sessionEpoch
      && opened.session.writable === true);
  recorder.check("cold Host open stays within the frozen 30s budget",
    performance.now() - started <= 30000, { elapsedMs: performance.now() - started });
  await page.getByTestId("nav-files").click();
  await page.getByTestId("file-workspace").waitFor({ state: "visible" });
  await page.locator('[data-testid^="document-row-"]').first()
    .waitFor({ state: "visible", timeout: 30000 });
  recorder.check("cold-open file first screen stays within the existing 30s open budget",
    performance.now() - started <= 30000, { elapsedMs: performance.now() - started });
  runtime.recordUiTiming("file-history-capacity-first-screen", performance.now() - started,
    { scale: fixture.name, documents: fixture.documents, revisions: fixture.revisions });

  const pageStarted = performance.now();
  const first = await helpers.rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "and", filters: [], sort: [{ field: "relativePath", direction: "asc" }],
    limit: 50, cursor: null,
  });
  const pageHarnessMs = performance.now() - pageStarted;
  recorder.check("real RPC first page obeys the frozen 2s budget and bounded count",
    first.elapsedMs <= 2000 && first.result.documents.length === Math.min(50, fixture.documents),
    { elapsedMs: first.elapsedMs, harnessElapsedMs: pageHarnessMs,
      count: first.result.documents.length });
  const treeStarted = performance.now();
  const tree = await helpers.rawWorkspaceV2Request(page, "fileHistory.readTree", {
    documentId: fixture.selectedMultiDocumentId,
  });
  const treeHarnessMs = performance.now() - treeStarted;
  recorder.check("real RPC serializes the complete legal revision chain within 2s",
    tree.elapsedMs <= 2000 && tree.result.revisions.length === fixture.selectedMultiRevisionCount
      && tree.result.effectiveRevisionId === fixture.selectedMultiEffectiveRevisionId,
    { elapsedMs: tree.elapsedMs, harnessElapsedMs: treeHarnessMs,
      count: tree.result.revisions.length });
  const selected = await helpers.rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "and", filters: [{ field: "relativePath", operator: "eq",
      value: fixture.selectedMultiRelativePath }],
    sort: [{ field: "relativePath", direction: "asc" }], limit: 50, cursor: null,
  });
  recorder.check("the selected multi-version document belongs to this workspace",
    selected.result.documents.some(document => document.documentId === fixture.selectedMultiDocumentId));
  const displayedPath = selected.result.documents
    .find(document => document.documentId === fixture.selectedMultiDocumentId).relativePath;
  const search = page.getByTestId("file-workspace").getByRole("textbox").first();
  const row = page.locator('[data-testid^="document-row-"]')
    .filter({ hasText: path.basename(displayedPath) });
  const previousHandle = await row.count() ? await row.getAttribute("data-testid") : null;
  const filterMarker = new Date().toISOString();
  await search.fill(path.basename(displayedPath));
  // The debounced list reload replaces entry handles and clears stale selection.
  await page.waitForFunction(({ marker, handle, name }) => {
    const refreshed = (window.__vibetableE2EBridgeDiagnostics?.roundTrips ?? []).some(item =>
      item.requestType === "document.listRequested" && item.startedAt > marker
      && item.responseType === "document.listLoaded" && item.code === null);
    const current = [...document.querySelectorAll('[data-testid^="document-row-"]')]
      .find(candidate => candidate.textContent?.includes(name));
    return refreshed && current instanceof HTMLElement
      && current.getAttribute("data-testid") !== handle;
  }, { marker: filterMarker, handle: previousHandle, name: path.basename(displayedPath) },
  { timeout: 30000 });
  await row.waitFor({ state: "visible", timeout: 30000 });
  const treeUiStarted = performance.now();
  await row.click();
  // The revision tree lives on the inspector's history tab; opening the row
  // alone keeps the summary tab, exactly like the other file scenarios.
  await page.getByTestId("file-workspace").locator(".inspector-tabs button").nth(1).click();
  const revisionTree = page.getByTestId("file-revision-tree");
  await revisionTree.waitFor({ state: "visible", timeout: 30000 });
  const firstRevision = revisionTree.locator(
    `.tree-row[data-revision-id="${fixture.selectedMultiFirstRevisionId}"]`);
  const effective = revisionTree.locator(
    `.tree-row[data-revision-id="${fixture.selectedMultiEffectiveRevisionId}"][aria-current="true"]`);
  await firstRevision.waitFor({ state: "visible", timeout: 30000 });
  await effective.waitFor({ state: "visible", timeout: 30000 });
  // Seal readiness before attribute evidence and its CDP trace snapshots.
  const treeUiMs = performance.now() - treeUiStarted;
  recorder.check("the legal all-formal chain renders its first and effective revisions",
    await firstRevision.getAttribute("data-revision-id") === fixture.selectedMultiFirstRevisionId
      && await effective.getAttribute("aria-current") === "true");
  const treeUiHarnessMs = performance.now() - treeUiStarted;
  recorder.check("full legal tree first screen stays within the existing 30s UI wait",
    treeUiMs <= 30000, { elapsedMs: treeUiMs, harnessElapsedMs: treeUiHarnessMs });
  runtime.recordUiTiming("file-history-capacity-tree-first-screen", treeUiMs,
    { scale: fixture.name, revisions: fixture.selectedMultiRevisionCount,
      harnessElapsedMs: treeUiHarnessMs });
  await page.screenshot({ path: path.join(runtime.evidenceDir, `${fixture.name}-file-history.png`) });
  return { workspaceId: identity.workspaceId, capacityScale: fixture.name,
    documents: fixture.documents, revisions: fixture.revisions };
}
