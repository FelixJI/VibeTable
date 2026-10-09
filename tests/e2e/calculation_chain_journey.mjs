import fs from "node:fs/promises";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify, isDeepStrictEqual } from "node:util";
import { fileURLToPath } from "node:url";

const executeFile = promisify(execFile);
const workbookHelper = fileURLToPath(new URL("./data_io_workbook.py", import.meta.url));

// Fixed, synthetic input. Expected values never come from a product response.
export function calculationChainUiFixture() {
  return Array.from({ length: 201 }, (_, index) => ({
    marker: `来源-${String(index).padStart(3, "0")}`,
    contract: index < 199 ? "合同甲" : index === 199 ? "合同乙" : "其它合同",
    amount: index < 199 ? 1 + index % 9 : index === 199 ? 7 : 900,
  }));
}

export function calculationChainUiOracle(sources, multiplier) {
  return ["合同甲", "合同乙", "合同丙"].map((contract) => {
    const matched = sources.filter(row => row.contract === contract);
    const sum = matched.reduce((total, row) => total + row.amount, 0);
    return { contract, matches: matched.length, sum, doubled: sum * multiplier,
      total: sum * multiplier + 1 };
  });
}

// #445: number cells now render through the PR display spec defaults
// (useGrouping true, scaleMode max, displayScale 2), so the DOM text for this
// journey's integer results is 1982 -> "1,982", 7 -> "7", 0 -> "0". This is
// an independent, journey-local DOM oracle over that default contract: it
// must not import the product formatter, and raw query/export authority
// keeps comparing unformatted numbers. Non-numeric cells (the marker text)
// pass through unchanged.
export function calculationChainDisplayText(value) {
  if (typeof value !== "number") return value;
  return new Intl.NumberFormat("zh-CN", {
    useGrouping: true, minimumFractionDigits: 0, maximumFractionDigits: 2,
  }).format(value);
}

// Computed apply receipts precede their asynchronous backfill. A subsequent
// drawer must describe after all fixture rows expose current materialized values.
export function waitForApplyBackfillFreshness(page, waitForQueryPage, tableId, query, receipt) {
  const field = receipt.definition.identity.physicalName;
  return waitForQueryPage(page, { tableId, query }, payload =>
    payload?.snapshot?.schemaRevision === receipt.schemaRevision
      && payload.rows?.length === 3
      && new Set(payload.rows.map(row => row.id)).size === 3
      && payload.rows.every(row => typeof row[field] === "number" && Number.isFinite(row[field])));
}

// S38 / #396: UI qualification is deliberately separate from 10k/50k timing.
// Fixture imports use the visible toolbar and native picker. Computed field
// creation/editing, source edits, queries, provenance and exports are UI actions;
// additional bridge calls only observe authority/metadata. Primitive table/field
// setup reuses the existing helpers; computed fields never use their write shortcut.
export async function runCalculationChainJourney(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, closeFieldSettingsDrawer,
    rawBridgeRequest, waitForQueryPage, selectTable, selectVisibleNOption, fillNInput,
    waitForVisibleRowCount, chooseToolbarMore, openFieldSettingsFromHeader,
    beginBridgeMessageCapture, waitForCapturedBridgeMessage, waitForFieldMigration,
    beginCellEdit, waitForStableGridState, waitForImportSuccess,
    openWorkspaceCenterFromSwitcher, replicaUiMethod, activateWorkspaceThroughUi,
    canonicalJsonText,
  } = helpers;
  const sourceName = "链路来源", summaryName = "链路汇总", mainName = "链路主表";
  const sources = calculationChainUiFixture();
  let multiplier = 2;
  const oracle = () => calculationChainUiOracle(sources, multiplier);
  const query = { filters: [], sorts: [], offset: 0, limit: 500 };
  const request = async (type, payload) => {
    const response = await rawBridgeRequest(page, type, payload);
    if (response.type === "operation.failed" || response.payload?.error) {
      throw new Error(`${type}: ${JSON.stringify(response)}`);
    }
    return response.payload;
  };
  const authority = async tableId => {
    const result = await request("query.page", { tableId, query });
    return { rows: result.rows, table: result.snapshot.table,
      schemaRevision: result.snapshot.schemaRevision, dataRevision: result.snapshot.dataRevision };
  };
  const screenshot = name => page.screenshot({ path: path.join(runtime.evidenceDir, `38-${name}.png`), fullPage: true });
  const importRows = async (name, columns, rows) => {
    const target = path.join(runtime.controlsDir, `38-${name}.csv`);
    const cell = value => `"${String(value).replaceAll('"', '""')}"`;
    await fs.writeFile(target, `\uFEFF${[columns, ...rows].map(row => row.map(cell).join(",")).join("\r\n")}\r\n`, "utf8");
    await fs.writeFile(path.join(runtime.controlsDir, "import-source.txt"), `${target}\r\n`, "utf8");
    await chooseToolbarMore(page, "refresh");
    await chooseToolbarMore(page, "import");
    await page.getByTestId("import-preview-panel").waitFor({ timeout: 60_000 });
    // Follow the existing preview acknowledgement flow. Full-value oracle
    // checks below still reject dropped, mis-mapped or partially imported data.
    const acknowledgement = page.getByTestId("import-ack");
    if (await acknowledgement.count()) await acknowledgement.click();
    await page.getByTestId("import-confirm").click();
    await waitForImportSuccess(page, rows.length);
  };
  const applyDraft = async tableId => {
    await page.getByTestId("field-plan-button").click();
    const plan = page.getByTestId("field-change-plan");
    await plan.waitFor({ timeout: 30_000 });
    for (const checkbox of await plan.getByRole("checkbox").all()) {
      if (!await checkbox.isChecked()) await checkbox.check();
    }
    await page.waitForFunction(() => {
      const button = document.querySelector('[data-testid="field-apply-button"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    });
    await beginBridgeMessageCapture(page, ["field.change.apply", "operation.failed"]);
    await page.getByTestId("field-apply-button").click();
    const applied = await waitForCapturedBridgeMessage(page, 60_000);
    if (applied.type !== "field.change.apply" || applied.payload?.error) throw new Error(JSON.stringify(applied));
    if (applied.payload?.migrationJobId) {
      const migration = await waitForFieldMigration(page, applied.payload.migrationJobId);
      if (migration.payload?.phase !== "completed") throw new Error(JSON.stringify(migration));
    }
    if (["formula", "lookup"].includes(applied.payload?.definition?.logicalType)) {
      await waitForApplyBackfillFreshness(page, waitForQueryPage, tableId, query, applied.payload);
    }
    await closeFieldSettingsDrawer(page);
    const described = await request("field.settings.describe", { tableId, fieldId: applied.payload.fieldId });
    if (!described.definition?.identity?.physicalName) throw new Error(JSON.stringify(described));
    return described.definition;
  };
  const openNewField = async (name, type) => {
    await page.getByTestId("toolbar-field-manager").click();
    await fillNInput(page, "field-display-name", name);
    const select = page.getByTestId("field-logical-type");
    await select.locator(".n-base-selection").click();
    await select.locator("input").fill(type);
    await page.locator(".n-base-select-option:visible").getByText(type, { exact: true }).first().click();
  };
  const commitFormula = async (tableId, expression) => {
    await page.getByTestId("formula-editor-entry").click();
    await beginBridgeMessageCapture(page, ["formula.preview", "operation.failed"]);
    await fillNInput(page, "formula-source", expression);
    await page.getByTestId("formula-field-editor").getByRole("alert")
      .filter({ hasText: "公式有效" }).waitFor({ timeout: 30_000 });
    await page.waitForFunction(() => document.querySelector('[data-testid="formula-preview-value"]')
      || document.querySelector('[data-testid="formula-preview-error"]'), undefined, { timeout: 30_000 });
    if (await page.getByTestId("formula-preview-error").isVisible()) {
      const preview = await waitForCapturedBridgeMessage(page, 30_000);
      throw new Error(`${await page.getByTestId("formula-preview-error").innerText()}; `
        + `formula.preview=${JSON.stringify(preview)}`);
    }
    await page.getByTestId("formula-editor-commit").click();
    return applyDraft(tableId);
  };

  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const source = await createSimpleTable(page, sourceName, "条目");
  const sourceContract = await createV2Field(page, source.tableId, "合同", "text");
  const amount = await createV2Field(page, source.tableId, "金额", "number");
  await importRows("source", [source.field.physicalName, sourceContract.physicalName, amount.physicalName],
    sources.map(row => [row.marker, row.contract, row.amount]));
  const summary = await createSimpleTable(page, summaryName, "合同");
  await importRows("summary", [summary.field.physicalName], oracle().map(row => [row.contract]));
  const main = await createSimpleTable(page, mainName, "合同");
  const note = await createV2Field(page, main.tableId, "文本", "text");
  await importRows("main", [main.field.physicalName, note.physicalName], oracle().map(row => [row.contract, "=1+1"]));
  const seeded = await authority(source.tableId);
  recorder.check("201 imported sources retain the 199/1/0 matching fixture and every raw value",
    seeded.rows.length === 201 && sources.every(expected => seeded.rows.some(row =>
      row[source.field.physicalName] === expected.marker && row[sourceContract.physicalName] === expected.contract
      && row[amount.physicalName] === expected.amount)), { counts: oracle().map(row => row.matches) });

  await selectTable(page, summaryName);
  await waitForVisibleRowCount(page, 3);
  await openNewField("匹配金额", "查找引用");
  await page.getByTestId("lookup-editor-entry").click();
  await selectVisibleNOption(page, "lookup-mode", "条件筛选（按条件查询来源表）");
  await selectVisibleNOption(page, "lookup-condition-source-table", sourceName);
  await selectVisibleNOption(page, "lookup-target-field", "金额");
  await selectVisibleNOption(page, "lookup-rule-source-field-0", "合同");
  await selectVisibleNOption(page, "lookup-rule-operand-field-0", "合同");
  await selectVisibleNOption(page, "lookup-aggregation", "SUM 求和");
  await page.getByTestId("lookup-preview-value").waitFor({ timeout: 30_000 });
  await screenshot("lookup-editor");
  await page.getByTestId("lookup-editor-commit").click();
  const lookup = await applyDraft(summary.tableId);
  await openNewField("二级金额", "公式");
  const doubled = await commitFormula(summary.tableId, "{匹配金额} * 2.0");
  await selectTable(page, mainName);
  await waitForVisibleRowCount(page, 3);
  await openNewField("三级金额", "公式");
  const total = await commitFormula(main.tableId,
    `SUMIF(TABLE({${summaryName}}), CurrentValue.{合同} == {合同}, CurrentValue.{二级金额}) + 1.0`);
  recorder.check("three-table dependency is authored through the real Lookup and formula editors",
    lookup.lookup?.condition?.sourceTableId === source.tableId && lookup.lookup?.targetFieldId === amount.fieldId
      && doubled.formula?.language === "cel-v1"
      && doubled.formula?.source?.includes(lookup.identity.physicalName)
      && total.formula?.language === "cel-v2" && total.formula?.source?.includes(`TABLE("${summary.tableId}")`));

  // #414 adds a real relation -> path Lookup -> scalar formula to the same
  // independent chain. Only primitive relation setup uses the existing helper.
  const relation = await createV2Field(page, main.tableId, "关联合同", "relation", draft => {
    draft.relation.targetTableId = summary.tableId;
    draft.relation.displayFieldId = summary.field.fieldId;
    return draft;
  });
  await selectTable(page, mainName);
  await openNewField("关联金额", "查找引用");
  await page.getByTestId("lookup-editor-entry").click();
  await selectVisibleNOption(page, "lookup-mode", "关系路径（沿引用逐跳取值）");
  await selectVisibleNOption(page, "lookup-relation-step-0", "关联合同");
  await selectVisibleNOption(page, "lookup-target-field", "二级金额");
  await selectVisibleNOption(page, "lookup-aggregation", "SUM 求和");
  await page.getByTestId("lookup-preview-value").waitFor({ timeout: 30_000 });
  await page.getByTestId("lookup-editor-commit").click();
  const linkedLookup = await applyDraft(main.tableId);
  await openNewField("关系总额", "公式");
  const linkedTotal = await commitFormula(main.tableId, "{关联金额} + 1.0");
  const targets = await authority(summary.tableId);
  for (const wanted of oracle()) {
    const row = page.locator(".grid-wrapper .tabulator-row:visible").filter({
      has: page.locator(`.tabulator-cell[tabulator-field="${main.field.physicalName}"]`, { hasText: wanted.contract }),
    });
    await row.locator(`.tabulator-cell.vt-relation-cell--editable[tabulator-field="${relation.physicalName}"]`).dblclick();
    const picker = page.locator(".relation-editor:visible");
    await picker.waitFor();
    await picker.locator(".relation-editor__candidate").filter({ hasText: wanted.contract }).click();
    await picker.waitFor({ state: "hidden" });
    const target = targets.rows.find(item => item[summary.field.physicalName] === wanted.contract);
    const updated = await authority(main.tableId);
    recorder.check(`${wanted.contract}: real relation picker binds the stable target identity`,
      updated.rows.find(item => item[main.field.physicalName] === wanted.contract)?.[relation.physicalName] === target?.id);
  }
  recorder.check("path Lookup retains relation/target field identities and typed aggregation",
    linkedLookup.lookup?.path?.[0]?.relationFieldId === relation.fieldId
      && linkedLookup.lookup?.targetFieldId === doubled.identity.fieldId
      && linkedLookup.lookup?.aggregation === "sum");
  const gridRows = async (marker, fields) => page.locator(".grid-wrapper .tabulator-row:visible").evaluateAll(
    (nodes, probe) => nodes.map(row => [probe.marker, ...probe.fields].map(field => {
      const cell = row.querySelector(`.tabulator-cell[tabulator-field="${field}"]`);
      return (cell?.querySelector(".vt-lookup-text") ?? cell)?.textContent?.trim() ?? "";
    })), { marker, fields });
  const verifyTable = async (table, name, fields, values, stage) => {
    await waitForQueryPage(page, { tableId: table.tableId, query }, payload =>
      payload?.rows?.length === 3 && values.every(expected => payload.rows.some(row =>
        row[table.field.physicalName] === expected[0] && fields.every((field, index) => row[field] === expected[index + 1]))));
    // #445: DOM cells follow the default display spec while the raw query above
    // keeps numeric authority. waitForFunction runs in the browser and cannot
    // call Node helpers, so the expected display text is prepared here.
    const displayValues = values.map(row => row.map(calculationChainDisplayText));
    await selectTable(page, name);
    await chooseToolbarMore(page, "refresh");
    await waitForVisibleRowCount(page, 3);
    await page.waitForFunction(({ marker, fields, expected }) => {
      const rows = [...document.querySelectorAll(".grid-wrapper .tabulator-row")];
      return expected.every(values => rows.some(row => [marker, ...fields].every((field, index) => {
        const cell = row.querySelector(`.tabulator-cell[tabulator-field="${field}"]`);
        return (cell?.querySelector(".vt-lookup-text") ?? cell)?.textContent?.trim() === values[index];
      })));
    }, { marker: table.field.physicalName, fields, expected: displayValues }, { timeout: 30_000 });
    recorder.check(`${stage}: ${name} grid and fresh query match every independent oracle cell`, true);
  };
  const verifyChain = async stage => {
    await verifyTable(summary, summaryName, [lookup.identity.physicalName, doubled.identity.physicalName],
      oracle().map(row => [row.contract, row.sum, row.doubled]), stage);
    await verifyTable(main, mainName, [total.identity.physicalName, linkedLookup.identity.physicalName, linkedTotal.identity.physicalName],
      oracle().map(row => [row.contract, row.total, row.doubled, row.total]), stage);
  };
  await verifyChain("initial creation");

  // Change the source through its visible cell, then edit an already saved formula.
  await selectTable(page, sourceName);
  await fillNInput(page, "view-keyword", sources[0].marker);
  await waitForVisibleRowCount(page, 1);
  const sourceCell = page.locator(`.grid-wrapper[aria-busy="false"] .tabulator-cell[tabulator-field="${amount.physicalName}"]`).first();
  await waitForStableGridState(page, { expectedRows: 1, matchingCell: sourceCell, expectedMatchingCells: 1 });
  const editor = await beginCellEdit(sourceCell);
  sources[0].amount += 3;
  await editor.fill(String(sources[0].amount));
  await editor.press("Enter");
  await verifyChain("source value UI edit");
  await selectTable(page, summaryName);
  await openFieldSettingsFromHeader(page, doubled.identity.physicalName);
  await commitFormula(summary.tableId, "{匹配金额} * 3.0");
  multiplier = 3;
  await verifyChain("saved formula UI edit");

  // Both computed kinds remain non-editable; an explicit F2 attempt opens no editor.
  await selectTable(page, summaryName);
  const beforeReadonly = await authority(summary.tableId);
  for (const field of [lookup.identity.physicalName, doubled.identity.physicalName]) {
    const cell = page.locator(`.grid-wrapper[aria-busy="false"] .tabulator-cell[tabulator-field="${field}"]`).first();
    // Lookup cells also contain source-navigation buttons; select their value.
    await (field === lookup.identity.physicalName ? cell.locator(".vt-lookup-text") : cell).click();
    await page.keyboard.press("F2");
    recorder.check(`${field} remains read-only in the real grid`,
      !(await cell.getAttribute("class"))?.includes("tabulator-editable") && await cell.locator("input, textarea").count() === 0);
    await page.keyboard.press("Escape");
  }
  recorder.check("read-only interaction leaves authority and revision unchanged",
    canonicalJsonText(beforeReadonly) === canonicalJsonText(await authority(summary.tableId)));

  // Source details must enumerate all 199 distinct records with their current values.
  const matched = sources.filter(row => row.contract === "合同甲");
  const matchingRow = page.locator(".tabulator-row").filter({ has: page.locator(
    `.tabulator-cell[tabulator-field="${summary.field.physicalName}"]`, { hasText: "合同甲" }) });
  await matchingRow.locator(`.tabulator-cell[tabulator-field="${lookup.identity.physicalName}"] .vt-lookup-source-more`).click();
  const panel = page.getByTestId("lookup-sources-panel");
  await panel.waitFor();
  await page.waitForFunction(() => document.querySelectorAll('[data-testid="lookup-sources-panel"] ol > li').length === 100);
  recorder.check("condition Lookup first source page is 100 / 199", (await panel.locator("header small").innerText()).trim() === "100 / 199");
  await panel.locator("footer button").click();
  await page.waitForFunction(() => document.querySelectorAll('[data-testid="lookup-sources-panel"] ol > li').length === 199);
  const provenance = await panel.locator("ol > li").evaluateAll(nodes => nodes.map(node => ({
    label: node.querySelector("span")?.textContent?.trim(), value: node.querySelector("small")?.textContent?.trim(),
  })));
  recorder.check("condition Lookup source paging exhausts all 199 unique oracle records and values",
    provenance.length === 199 && new Set(provenance.map(item => item.label)).size === 199
      && matched.every(row => provenance.some(item => item.label === `${sourceName} · ${row.marker}` && item.value === `金额 · ${row.amount}`))
      && await panel.locator("footer").count() === 0 && await panel.locator('[role="alert"]').count() === 0, { provenance });
  await screenshot("sources-complete");
  await panel.getByRole("button", { name: /^(关闭|Close)$/u }).click();
  await panel.waitFor({ state: "hidden" });

  await selectTable(page, summaryName);
  await openFieldSettingsFromHeader(page, doubled.identity.physicalName);
  await fillNInput(page, "field-display-name", "二级金额（改名）");
  const renamed = await applyDraft(summary.tableId);
  recorder.check("renaming a referenced formula through field settings keeps stable identity",
    renamed.identity.fieldId === doubled.identity.fieldId
      && renamed.identity.physicalName === doubled.identity.physicalName);
  await verifyChain("referenced schema UI rename");
  await screenshot("relation-schema-lifecycle");
  // Filter and order the third table by its downstream computed value through UI.
  await selectTable(page, mainName);
  await page.getByTestId("view-filter-trigger").click();
  const filter = page.locator(".control-card--wide:visible");
  await filter.getByRole("button", { name: "＋ 条件", exact: true }).click();
  const node = filter.locator(".filter-node").first();
  await node.locator(".field-select .n-base-selection").click();
  await page.locator(".n-base-select-option:visible").getByText("三级金额", { exact: true }).click();
  await node.locator(".operator-select .n-base-selection").click();
  await page.locator(".n-base-select-option:visible").getByText("大于", { exact: true }).click();
  await node.locator(".value-input input").fill("1");
  await beginBridgeMessageCapture(page, ["gridState.save", "operation.failed"]);
  await page.getByTestId("view-filter-apply").click();
  const filtered = await waitForCapturedBridgeMessage(page, 30_000);
  if (filtered.type !== "gridState.save" || filtered.payload?.conflict) throw new Error(JSON.stringify(filtered));
  await page.getByTestId("view-filter-trigger").click();
  await filter.waitFor({ state: "hidden" });
  const header = page.locator(`.tabulator-col[tabulator-field="${total.identity.physicalName}"]`).first();
  for (let clicks = 0; clicks < 2 && await header.getAttribute("aria-sort") !== "descending"; clicks++) {
    await beginBridgeMessageCapture(page, ["gridState.save", "operation.failed"]);
    await header.locator(".tabulator-col-title").click();
    const saved = await waitForCapturedBridgeMessage(page, 30_000);
    if (saved.type !== "gridState.save" || saved.payload?.conflict) throw new Error(JSON.stringify(saved));
  }
  await page.waitForFunction(field => document.querySelector(`.tabulator-col[tabulator-field="${field}"]`)?.getAttribute("aria-sort") === "descending", total.identity.physicalName);
  const ordered = oracle().filter(row => row.total > 1).sort((left, right) => right.total - left.total);
  const verifyOrderedView = async stage => {
    await waitForStableGridState(page, { expectedRows: 2 });
    const expected = ordered.map(row => [row.contract, String(row.total), String(row.doubled), String(row.total)]);
    // #445: same raw/DOM split — the grid shows grouped display text.
    const displayExpected = ordered.map(row => [row.contract, calculationChainDisplayText(row.total),
      calculationChainDisplayText(row.doubled), calculationChainDisplayText(row.total)]);
    const complete = await authority(main.tableId);
    recorder.check(`${stage}: unfiltered authority retains all three oracle results, including zero-match`,
      complete.rows.length === 3 && oracle().every(expectedRow => complete.rows.some(row =>
        row[main.field.physicalName] === expectedRow.contract && row[total.identity.physicalName] === expectedRow.total)));
    const view = await request("gridState.get", { table: main.tableId });
    recorder.check(`${stage}: UI stores the computed filter and descending sort`,
      view.state.filters.some(item => item.field === total.identity.physicalName && item.operator === "gt" && Number(item.value) === 1)
      && view.state.sorts[0]?.field === total.identity.physicalName && view.state.sorts[0]?.direction === "desc");
    const result = await request("query.page", { tableId: main.tableId,
      query: { filters: view.state.filters, sorts: view.state.sorts, offset: 0, limit: 100 } });
    recorder.check(`${stage}: full-result query and visible row order/count match the oracle`,
      result.totalRows === 3 && result.filteredRows === 2
      && isDeepStrictEqual(result.rows.map(row => [row[main.field.physicalName], String(row[total.identity.physicalName]),
        String(row[linkedLookup.identity.physicalName]), String(row[linkedTotal.identity.physicalName])]), expected)
      && isDeepStrictEqual(await gridRows(main.field.physicalName, [total.identity.physicalName,
        linkedLookup.identity.physicalName, linkedTotal.identity.physicalName]), displayExpected), { expected, displayExpected });
  };
  await verifyOrderedView("filtered view");
  await screenshot("filtered-sorted-grid");

  // Export the same filtered view in both formats. The existing independent
  // Python reader rejects executable formulas and compares every selected cell.
  const beforeExport = await Promise.all([source, summary, main].map(table => authority(table.tableId)));
  for (const format of ["csv", "xlsx"]) {
    const target = path.join(runtime.controlsDir, `38-chain.${format}`);
    await fs.writeFile(path.join(runtime.controlsDir, "export-target.txt"), `${target}\r\n`, "utf8");
    await chooseToolbarMore(page, `export-${format}`);
    await page.getByTestId("export-lookup-panel").waitFor({ timeout: 60_000 });
    await page.getByTestId("export-lookup-confirm").click();
    const deadline = Date.now() + 60_000;
    let ready = false;
    while (Date.now() < deadline && !ready) {
      try { await fs.access(target); ready = true; }
      catch (error) { if (error.code !== "ENOENT") throw error; await page.waitForTimeout(100); }
    }
    if (!ready) throw new Error(`${format} export did not create its granted output`);
    if (!runtime.pythonExecutable) throw new Error("Runner locked Python is required for independent export verification");
    const { stdout } = await executeFile(runtime.pythonExecutable, [workbookHelper, "verify-values", target, JSON.stringify({
      columns: [main.field.physicalName, total.identity.physicalName, note.physicalName,
        linkedLookup.identity.physicalName, linkedTotal.identity.physicalName],
      rows: ordered.map(row => [row.contract, row.total, "=1+1", row.doubled, row.total]),
    })], { encoding: "utf8", timeout: 30_000, maxBuffer: 1024 * 1024, env: { ...process.env, PYTHONUTF8: "1" } });
    recorder.check(`${format} export matches every same-snapshot computed cell and keeps formula-like text inert`,
      JSON.parse(stdout).rows === 2, { verifier: JSON.parse(stdout) });
  }
  recorder.check("both exports preserve all three authorities and revisions",
    canonicalJsonText(beforeExport) === canonicalJsonText(await Promise.all([source, summary, main].map(table => authority(table.tableId)))));
  await verifyOrderedView("after exports");

  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await openWorkspaceCenterFromSwitcher(page);
  const center = page.getByTestId("workspace-center");
  const closed = await replicaUiMethod(page, recorder, "workspace.close", () =>
    center.getByRole("button", { name: /关闭当前工作区|Close current workspace/ }).click());
  if (closed.result.state !== "closed") throw new Error(JSON.stringify(closed));
  const reopened = await activateWorkspaceThroughUi(page, { method: "workspace.open",
    activate: () => center.getByRole("button", { name: /E2E Product Workspace/ }).click() });
  recorder.check("UI close/reopen keeps the workspace identity with a fresh epoch",
    reopened.session.workspaceId === session.workspaceId && reopened.session.sessionEpoch > session.sessionEpoch);
  await page.getByTestId("nav-tables").click();
  await selectTable(page, summaryName);
  await verifyTable(summary, summaryName, [lookup.identity.physicalName, doubled.identity.physicalName],
    oracle().map(row => [row.contract, row.sum, row.doubled]), "reopen");
  await selectTable(page, mainName);
  const reopenedRelations = await authority(main.tableId);
  recorder.check("relation, path Lookup and scalar reference survive current-version reopen",
    oracle().every(expected => reopenedRelations.rows.some(row =>
      row[main.field.physicalName] === expected.contract
      && row[linkedLookup.identity.physicalName] === expected.doubled
      && row[linkedTotal.identity.physicalName] === expected.total)));

  await verifyOrderedView("reopen");
  await screenshot("reopened");
  const identity = ({ fieldId, physicalName }) => ({ fieldId, physicalName });
  return {
    workspaceId: session.workspaceId,
    chain: {
      source: { tableId: source.tableId, marker: identity(source.field),
        contract: identity(sourceContract), amount: identity(amount) },
      summary: { tableId: summary.tableId, marker: identity(summary.field),
        lookup: identity(lookup.identity), doubled: identity(doubled.identity) },
      main: { tableId: main.tableId, marker: identity(main.field), note: identity(note),
        relation: identity(relation), total: identity(total.identity),
        linkedLookup: identity(linkedLookup.identity), linkedTotal: identity(linkedTotal.identity) },
    },
  };
  // Shared runner checks no external renderer traffic and releases the Host
  // process scope, ports and workspace handles on both success and failure.
}
