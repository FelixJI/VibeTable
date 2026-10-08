import path from "node:path";

/** Real WPF/WebView2 settings journey for the common field display families (scenario 45). */
export async function runCommonFieldDisplayJourney(page, recorder, runtime, deps) {
  const { createEmptyTable, createV2Field, closeFieldSettingsDrawer, selectTable,
    applyProductMutation, rawBridgeRequest, selectVisibleNOption, beginBridgeMessageCapture,
    waitForCapturedBridgeMessage, openWorkspaceCenterFromSwitcher, replicaUiMethod,
    beginWritableWorkspaceBootstrapCapture } = deps;
  const tableName = "E2E Common Field Display";
  const tableId = await createEmptyTable(page, tableName);
  const progress = await createV2Field(page, tableId, "进度", "number");
  await closeFieldSettingsDrawer(page);
  const phone = await createV2Field(page, tableId, "电话", "text");
  const rating = await createV2Field(page, tableId, "评分", "number");
  const calendarDate = await createV2Field(page, tableId, "日期", "date");
  const instant = await createV2Field(page, tableId, "瞬间", "dateTime");
  const clock = await createV2Field(page, tableId, "时刻", "time");
  const bool = await createV2Field(page, tableId, "完成", "bool");
  const rich = await createV2Field(page, tableId, "摘要", "editor");
  const optionDraft = draft => { draft.select = { options: [
    { optionId: "", label: "进行中", color: "#0ea5e9", order: 0, state: "active" },
    { optionId: "", label: "完成", color: "#22c55e", order: 1, state: "active" },
  ] }; return draft; };
  const choice = await createV2Field(page, tableId, "状态", "select", optionDraft);
  const choices = await createV2Field(page, tableId, "标签", "multiSelect", optionDraft);
  const ids = choice.definition.select.options.map(option => option.optionId);
  const manyIds = choices.definition.select.options.map(option => option.optionId);
  const values = [0, 0.125, 1, 1.2, -0.1, 1.5];
  const inserted = await applyProductMutation(page, tableId, values.map((value, index) => ({
    kind: "insert", recordId: null, values: {
      [progress.physicalName]: value, [phone.physicalName]: "+86 010-0012 ext.03",
      [rating.physicalName]: index === 0 ? 0 : index === 1 ? 5 : null,
      [calendarDate.physicalName]: "2026-03-08", [instant.physicalName]: "2026-03-08T07:00:00Z",
      [clock.physicalName]: "09:30:15.125", [bool.physicalName]: index === 0 ? null : index === 1,
      [rich.physicalName]: "<p>你好</p><script>window.__fieldDisplayExecuted=true</script><b>世界</b>",
      [choice.physicalName]: ids[0], [choices.physicalName]: [manyIds[1], manyIds[0]],
    },
  })), "e2e-common-display-seed");
  if (inserted.payload?.status !== "applied") throw new Error(`common field seed failed: ${JSON.stringify(inserted)}`);
  await selectTable(page, tableName);
  const query = () => rawBridgeRequest(page, "query.page", { tableId, query: { filters: [], sorts: [], offset: 0, limit: 100 } });
  const describe = field => rawBridgeRequest(page, "field.settings.describe", { tableId, fieldId: field.fieldId });
  const open = async field => {
    const header = page.locator(`.tabulator-col[tabulator-field="${field.physicalName}"]`);
    await header.locator(".tabulator-col-title").click({ button: "right" });
    await page.locator(".n-dropdown-option-body:visible").getByText("字段设置", { exact: true }).click();
    await page.getByTestId("field-display-name").waitFor();
  };
  const plan = async () => {
    await beginBridgeMessageCapture(page, ["field.change.plan", "operation.failed"]);
    await page.getByTestId("field-plan-button").click();
    return waitForCapturedBridgeMessage(page, 60_000);
  };
  const save = async () => {
    const planned = await plan();
    if (planned.type !== "field.change.plan") throw new Error(`common display plan failed: ${JSON.stringify(planned)}`);
    const card = page.getByTestId("field-change-plan"); await card.waitFor();
    for (const checkbox of await card.getByRole("checkbox").all()) if (!await checkbox.isChecked()) await checkbox.check();
    await beginBridgeMessageCapture(page, ["field.change.apply", "operation.failed"]);
    await page.getByTestId("field-apply-button").click();
    const applied = await waitForCapturedBridgeMessage(page, 60_000);
    if (applied.type !== "field.change.apply") throw new Error(`common display apply failed: ${JSON.stringify(applied)}`);
    await closeFieldSettingsDrawer(page);
  };
  const waitTexts = async (field, expected) => {
    await page.waitForFunction(({ name, wanted }) => {
      const texts = [...document.querySelectorAll(`.tabulator-cell[tabulator-field="${name}"]`)].map(cell => cell.textContent.trim());
      return wanted.every(text => texts.includes(text));
    }, { name: field.physicalName, wanted: expected }, { timeout: 30_000 });
  };
  const before = await query();
  await open(progress); await selectVisibleNOption(page, "field-logical-type", "进度"); await save();
  await waitTexts(progress, ["0%", "12.5%", "100%", "120%", "-10%", "150%"]);
  await open(progress);
  await page.getByTestId("progress-start").locator("input").fill("1");
  await page.getByTestId("progress-target").locator("input").fill("2");
  await page.getByTestId("progress-target").locator("input").press("Enter"); await save();
  await waitTexts(progress, ["150%"]);
  const afterProgress = await query();
  recorder.check("progress menu and changed display bounds keep every raw ratio including 1.5 and display 150%",
    afterProgress.payload.rows.every(row => before.payload.rows.some(original => original.id === row.id && original[progress.physicalName] === row[progress.physicalName]))
      && afterProgress.payload.snapshot.dataRevision === before.payload.snapshot.dataRevision,
    { before, afterProgress, display: (await describe(progress)).payload.definition.display });
  await open(progress); await selectVisibleNOption(page, "number-display-preset", "评分");
  const ratingRejected = await plan();
  await page.getByTestId("field-change-plan").waitFor();
  const rejectedPlan = ratingRejected.payload ?? {};
  const blockedCard = await page.getByTestId("field-change-plan").textContent();
  recorder.check("real rating preflight explains incompatible fractional/out-of-range samples and prevents apply",
    rejectedPlan.canApply === false
      && (rejectedPlan.errors ?? []).some(error => error.code === "field.constraint.existing_data_invalid")
      && (rejectedPlan.impact?.failures ?? []).length === 4
      && rejectedPlan.impact.failures.every(sample => /integer|range/i.test(sample.reason))
      && await page.getByTestId("field-apply-button").isDisabled()
      && blockedCard.includes("已阻止")
      && blockedCard.includes("value must be an integer"), { ratingRejected });
  await closeFieldSettingsDrawer(page);
  const afterReject = await query();
  recorder.check("rating preflight writes neither schema nor data",
    JSON.stringify(afterReject.payload.rows) === JSON.stringify(afterProgress.payload.rows)
      && afterReject.payload.snapshot.schemaRevision === afterProgress.payload.snapshot.schemaRevision
      && afterReject.payload.snapshot.dataRevision === afterProgress.payload.snapshot.dataRevision, { afterReject });
  await open(rating); await selectVisibleNOption(page, "field-logical-type", "评分"); await save();
  await waitTexts(rating, ["☆☆☆☆☆ 0/5", "★★★★★ 5/5", "—"]);
  await open(phone); await selectVisibleNOption(page, "field-logical-type", "电话号码"); await save();
  await waitTexts(phone, ["+86 010-0012 ext.03"]);
  await open(bool); await selectVisibleNOption(page, "bool-display-mode", "文字");
  await page.getByTestId("bool-true-label").locator("input").fill("已完成");
  await page.getByTestId("bool-false-label").locator("input").fill("未完成"); await save();
  await waitTexts(bool, ["—", "已完成", "未完成"]);
  // Calendar dates already use the recommended day precision.
  // Reselecting it must keep the unchanged draft ineligible for planning.
  await open(calendarDate);
  const datePrecisionLabel = (await page.getByTestId("field-display-precision").innerText()).trim();
  await selectVisibleNOption(page, "field-display-precision", "日期");
  recorder.check("date field keeps the recommended day precision and a same-value reselection cannot bypass the required plan",
    datePrecisionLabel.includes("日期") && await page.getByTestId("field-plan-button").isDisabled(),
    { datePrecisionLabel });
  await closeFieldSettingsDrawer(page);
  await open(clock); await selectVisibleNOption(page, "field-display-precision", "毫秒"); await save();
  // Canonical time storage normalizes to whole seconds; millisecond display pads zero.
  await waitTexts(clock, ["09:30:15.000"]);
  await open(choice);
  const optionRows = page.locator(".option-row");
  await optionRows.first().locator(".n-input input").fill("进行中（已改名）");
  await save(); await waitTexts(choice, ["进行中（已改名）"]);
  await open(choice); await page.getByRole("button", { name: "停用选项", exact: true }).first().click();
  await save(); await waitTexts(choice, ["进行中（已改名）（已停用）"]);
  const afterOptionRename = await query();
  recorder.check("real option label edit and retirement preserve stored stable IDs and multi-value order",
    afterOptionRename.payload.rows.every(row => row[choice.physicalName] === ids[0]
      && JSON.stringify(row[choices.physicalName]) === JSON.stringify([manyIds[1], manyIds[0]])), { afterOptionRename });
  await open(instant); await selectVisibleNOption(page, "field-display-precision", "秒");
  await selectVisibleNOption(page, "field-display-timezone", "UTC"); await save();
  await waitTexts(instant, ["2026-03-08 07:00:00"]);
  await waitTexts(calendarDate, ["2026-03-08"]);
  await waitTexts(rich, ["你好 世界"]); await waitTexts(choice, ["进行中（已改名）（已停用）"]); await waitTexts(choices, ["完成、进行中"]);
  recorder.check("rich text is a bounded inert summary and multi-select original order remains canonical",
    !await page.evaluate(() => window.__fieldDisplayExecuted === true)
      && (await query()).payload.rows.every(row => JSON.stringify(row[choices.physicalName]) === JSON.stringify([manyIds[1], manyIds[0]])), {});
  await page.screenshot({ path: path.join(runtime.evidenceDir, "45-common-field-display.png"), fullPage: true });
  const originalSession = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await openWorkspaceCenterFromSwitcher(page);
  const closed = await replicaUiMethod(page, recorder, "workspace.close", () => page.getByTestId("workspace-center").getByRole("button", { name: /关闭当前工作区|Close current workspace/ }).click());
  await beginWritableWorkspaceBootstrapCapture(page, originalSession.sessionEpoch, "workspace.open");
  await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
  const reopened = await waitForCapturedBridgeMessage(page, 60_000);
  await selectTable(page, tableName); await waitTexts(progress, ["150%"]);
  const restored = await describe(progress);
  recorder.check("actual offline workspace reopen preserves display parameters, workspace identity and raw values",
    closed.result?.state === "closed" && reopened.payload.session.workspaceId === originalSession.workspaceId
      && reopened.payload.session.sessionEpoch > originalSession.sessionEpoch
      && restored.payload.definition.display.progressStart === 1 && restored.payload.definition.display.progressTarget === 2
      && (await query()).payload.rows.some(row => row[progress.physicalName] === 1.5), { reopened, restored });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "45-common-field-display-reopened.png"), fullPage: true });
  // Current-format Snapshot captures the canonical display JSON, then real
  // restore must recover it after a visible settings change.
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-versions").click();
  const snapshotIds = await page.locator(".snapshot-row").evaluateAll(rows => rows.map(row => row.id));
  await page.getByTestId("snapshot-create").click();
  await page.waitForFunction(existing => [...document.querySelectorAll(".snapshot-row")].some(row => row.id && !existing.includes(row.id)), snapshotIds, { timeout: 60_000 });
  const snapshotId = await page.locator(".snapshot-row").evaluateAll((rows, existing) => rows.find(row => row.id && !existing.includes(row.id))?.id, snapshotIds);
  await page.getByTestId("nav-tables").click(); await selectTable(page, tableName);
  await open(progress); await page.getByTestId("progress-target").locator("input").fill("3");
  await page.getByTestId("progress-target").locator("input").press("Enter"); await save();
  recorder.check("changed progress target is persisted before snapshot restore", (await describe(progress)).payload.definition.display.progressTarget === 3, {});
  await page.getByTestId("nav-settings").click(); await page.getByTestId("settings-nav-versions").click();
  await page.locator(`[id="${snapshotId}"]`).click(); await page.getByTestId("snapshot-restore-open").click();
  const advance = page.getByTestId("snapshot-restore-preview"); await advance.click();
  await page.locator(".snapshot-restore-modal .plan-summary").waitFor({ timeout: 30_000 });
  const sourceEpoch = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession.sessionEpoch);
  await beginWritableWorkspaceBootstrapCapture(page, sourceEpoch); await advance.click();
  await page.locator(".snapshot-restore-modal").waitFor({ state: "hidden" });
  const restoredBootstrap = await waitForCapturedBridgeMessage(page, 60_000);
  await page.getByTestId("nav-tables").click(); await selectTable(page, tableName); await waitTexts(progress, ["150%"]);
  const snapshotDisplay = (await describe(progress)).payload.definition.display;
  recorder.check("real current-format snapshot restore recovers the original canonical display configuration and 1.5 raw value",
    restoredBootstrap.payload.session.sessionEpoch > sourceEpoch
      && snapshotDisplay.progressStart === 1 && snapshotDisplay.progressTarget === 2
      && (await query()).payload.rows.some(row => row[progress.physicalName] === 1.5), { snapshotId, restoredBootstrap, snapshotDisplay });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "45-common-field-display-restored.png"), fullPage: true });
  // New Gallery views inherit the current visible fields; select the tested summaries.
  await page.getByTestId("view-hidden-trigger").click();
  await page.getByTestId("view-hidden-hide-filtered").click();
  for (const summaryField of ["进度", "评分", "电话"]) {
    await page.getByTestId("view-hidden-search").locator("input").fill(summaryField);
    await page.getByTestId("view-hidden-show-filtered").click();
  }
  await page.getByTestId("view-hidden-search").locator("input").fill("");
  await page.getByTestId("view-hidden-apply").click();

  await page.getByTestId("view-create").click();
  const galleryDialog = page.locator(".view-dialog:visible"); await galleryDialog.waitFor();
  await galleryDialog.locator(".n-input input").fill("E2E Common Cards");
  await page.getByTestId("view-kind-gallery").click();
  await selectVisibleNOption(page, "view-gallery-title-field", "电话");
  await page.getByTestId("view-dialog-confirm").click();
  // Wait out the create-view dialog exit animation so the cards screenshot keeps the settled gallery.
  await galleryDialog.waitFor({ state: "hidden", timeout: 30_000 });
  await page.getByTestId("record-gallery-view").waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForFunction(() => document.querySelectorAll('[data-testid="gallery-card"]').length === 6);
  // Wait for the selected summaries to render before capturing the cards.
  await page.waitForFunction(wanted => {
    const texts = [...document.querySelectorAll('[data-testid="gallery-card"]')].map(card => card.textContent ?? "");
    return wanted.every(text => texts.some(card => card.includes(text)));
  }, ["150%", "☆☆☆☆☆ 0/5", "★★★★★ 5/5"], { timeout: 30_000 });
  const cards = await page.getByTestId("gallery-card").allInnerTexts();
  recorder.check("real record cards share progress and rating display with the grid after snapshot restore",
    cards.some(text => text.includes("150%")) && cards.some(text => text.includes("☆☆☆☆☆ 0/5"))
      && cards.some(text => text.includes("★★★★★ 5/5")) && cards.every(text => text.includes("+86 010-0012 ext.03")), { cards });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "45-common-field-display-cards.png"), fullPage: true });

}
