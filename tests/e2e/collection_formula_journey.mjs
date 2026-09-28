import fs from "node:fs/promises";
import path from "node:path";

// Scenario 37 (#395): the cel-v2 collection formula journey through the real
// product UI. TABLE({表}) 与 CurrentValue.{字段} 通过公式工作台的来源表/来源
// 字段选择器插入并绑定稳定 ID，四条正式表达式（同合同且已发货 SUMIF、COUNTIF、
// 去重编码 ARRAYJOIN、日期区间 SUMIF）经真实 UI 输入、预览、保存并显示在网格。
// 每个期望值都由本文件内的独立 JavaScript oracle 从合成 fixture 直接计算，
// 不调用任何被测产品 API 推导期望。
export async function runCollectionFormulaJourney(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, closeFieldSettingsDrawer,
    applyV2FieldChange, applyProductMutation, rawBridgeRequest, waitForQueryPage,
    selectTable, selectVisibleNOption, fillNInput, waitForVisibleRowCount,
    chooseToolbarMore, parseCsv, openFieldSettingsFromHeader,
    beginBridgeMessageCapture, waitForCapturedBridgeMessage,
    beginWritableWorkspaceBootstrapCapture, openWorkspaceCenterFromSwitcher,
    rawLifecycleWorkspaceV2Request, waitForFieldMigration,
  } = helpers;

  const SHIPPED = "\u5df2\u53d1\u8d27"; // 已发货
  const PENDING = "\u5f85\u53d1\u8d27"; // 待发货
  const SHIP_TABLE = "\u51fa\u8d27\u6d41\u6c34"; // 出货流水
  const LEDGER_TABLE = "\u5408\u540c\u53f0\u8d26"; // 合同台账

  // ---- 独立 oracle fixture：160+1+4 合成来源行，3 个台账行 ----
  const ledgerRows = [
    { id: "ledgerrow000100", marker: "\u53f0\u8d26\u7532", contract: "\u5408\u540c\u7532", start: "2026-03-05", end: "2026-03-19" },
    { id: "ledgerrow000200", marker: "\u53f0\u8d26\u4e59", contract: "\u5408\u540c\u4e59", start: "2026-03-01", end: "2026-03-31" },
    { id: "ledgerrow000300", marker: "\u53f0\u8d26\u4e19", contract: "\u5408\u540c\u4e19", start: "2026-03-01", end: "2026-03-31" },
  ];
  const shipments = [];
  for (let index = 0; index < 160; index += 1) {
    shipments.push({
      id: `ship${String(index).padStart(11, "0")}`,
      contract: "\u5408\u540c\u7532",
      status: index === 1 || index >= 153 ? PENDING : SHIPPED,
      amount: 1 + (index % 9),
      code: `\u7532-${String(index % 40).padStart(3, "0")}`,
      date: `2026-03-${String(1 + (index % 30)).padStart(2, "0")}`,
    });
  }
  shipments.push({
    id: "ship00000000160", contract: "\u5408\u540c\u4e59", status: SHIPPED, amount: 7,
    code: "\u4e59-001", date: "2026-03-10",
  });
  for (let index = 0; index < 4; index += 1) {
    shipments.push({
      id: `noise${String(index).padStart(10, "0")}`, contract: "\u5176\u5b83\u5408\u540c", status: SHIPPED,
      amount: 5, code: `\u566a-${index}`, date: "2026-03-15",
    });
  }
  // 与产品一致的读取顺序：稳定记录 ID 升序（fixture 生成顺序即 ID 顺序）。
  const orderedShipments = () => [...shipments].sort((left, right) => left.id.localeCompare(right.id));
  // 固定 JS oracle：只用 shipments/ledgerRows 计算，绝不读取被测 API 的输出。
  const oracle = () => ledgerRows.map((row) => {
    const rows = orderedShipments().filter((item) => item.contract === row.contract);
    const shipped = rows.filter((item) => item.status === SHIPPED);
    const inWindow = rows.filter((item) => item.date >= row.start && item.date <= row.end);
    return {
      ...row,
      sumifShipped: shipped.reduce((total, item) => total + item.amount, 0),
      countifShipped: shipped.length,
      uniqueCodes: [...new Set(rows.map((item) => item.code))].join(", "),
      windowSum: inWindow.reduce((total, item) => total + item.amount, 0),
    };
  });

  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();

  // ---- 建表与字段（全部经真实 createV2Field/Schema V2 计划器） ----
  const ship = await createSimpleTable(page, SHIP_TABLE, "\u5408\u540c");
  const shipStatus = await createV2Field(page, ship.tableId, "\u72b6\u6001", "text");
  const shipAmount = await createV2Field(page, ship.tableId, "\u91d1\u989d", "number");
  const shipCode = await createV2Field(page, ship.tableId, "\u7f16\u7801", "text");
  const shipDate = await createV2Field(page, ship.tableId, "\u65e5\u671f", "date");
  const ledger = await createSimpleTable(page, LEDGER_TABLE, "\u6807\u8bb0");
  const ledgerContract = await createV2Field(page, ledger.tableId, "\u5408\u540c", "text");
  const ledgerStart = await createV2Field(page, ledger.tableId, "\u5f00\u59cb\u65e5\u671f", "date");
  const ledgerEnd = await createV2Field(page, ledger.tableId, "\u7ed3\u675f\u65e5\u671f", "date");
  const shipSeeded = await applyProductMutation(page, ship.tableId, shipments.map((row) => ({
    kind: "insert", recordId: row.id,
    values: {
      [ship.field.physicalName]: row.contract,
      [shipStatus.physicalName]: row.status,
      [shipAmount.physicalName]: row.amount,
      [shipCode.physicalName]: row.code,
      [shipDate.physicalName]: row.date,
    },
  })), "37-collection-ship-seed");
  const ledgerSeeded = await applyProductMutation(page, ledger.tableId, ledgerRows.map((row) => ({
    kind: "insert", recordId: row.id,
    values: {
      [ledger.field.physicalName]: row.marker,
      [ledgerContract.physicalName]: row.contract,
      [ledgerStart.physicalName]: row.start,
      [ledgerEnd.physicalName]: row.end,
    },
  })), "37-collection-ledger-seed");
  if (shipSeeded.payload?.status !== "applied" || ledgerSeeded.payload?.status !== "applied") {
    throw new Error(`collection fixture did not commit: ${JSON.stringify({ shipSeeded, ledgerSeeded })}`);
  }
  recorder.check("165 synthetic source rows cover the 160/1/0 match ledgers",
    shipments.length === 165 && ledgerRows.length === 3, {
      shipments: shipments.length,
      ledgerRows: ledgerRows.length,
    });
  await selectTable(page, LEDGER_TABLE);
  await chooseToolbarMore(page, "refresh");
  await waitForVisibleRowCount(page, 3);

  // ---- 通过真实公式工作台创建四个集合公式字段 ----
  const waitSourceEndsWith = async (suffix) => {
    await page.waitForFunction((expected) => {
      const node = document.querySelector('[data-testid="formula-source"] textarea');
      return node?.value?.endsWith(expected) === true;
    }, suffix, { timeout: 10_000 });
  };
  const insertSourceFieldThroughPicker = async (label) => {
    await selectVisibleNOption(page, "formula-source-field", label);
    await page.getByTestId("formula-insert-source-field").click();
    await waitSourceEndsWith(`CurrentValue.{${label}}`);
  };
  const describeField = async (tableId, fieldId) => {
    const described = await rawBridgeRequest(page, "field.settings.describe", { tableId, fieldId });
    const definition = described.payload?.definition;
    if (!definition?.identity?.physicalName) {
      throw new Error(`field definition unavailable: ${JSON.stringify(described)}`);
    }
    return definition;
  };
  const planAndApplyThroughUi = async () => {
    await page.getByTestId("field-plan-button").click();
    const planCard = page.getByTestId("field-change-plan");
    await planCard.waitFor({ state: "visible", timeout: 30_000 });
    for (const checkbox of await planCard.getByRole("checkbox").all()) {
      if (!await checkbox.isChecked()) await checkbox.check();
    }
    await page.waitForFunction(() => {
      const button = document.querySelector('[data-testid="field-apply-button"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    }, undefined, { timeout: 30_000 });
    await beginBridgeMessageCapture(page, ["field.change.apply", "operation.failed"]);
    await page.getByTestId("field-apply-button").click();
    const applied = await waitForCapturedBridgeMessage(page, 60_000);
    if (applied.type !== "field.change.apply" || applied.payload?.error) {
      throw new Error(`collection field apply failed: ${JSON.stringify(applied)}`);
    }
    if (applied.payload?.migrationJobId) {
      const migration = await waitForFieldMigration(page, applied.payload.migrationJobId);
      if (migration.payload?.phase !== "completed") throw new Error(JSON.stringify(migration));
    }
    await closeFieldSettingsDrawer(page);
    return applied;
  };
  const openNewComputedFieldDrawer = async (displayName, typeLabel) => {
    await page.getByTestId("toolbar-field-manager").click();
    await page.getByTestId("field-display-name").waitFor({ timeout: 30_000 });
    await page.getByTestId("field-display-name").locator("input").fill(displayName);
    const typeSelect = page.getByTestId("field-logical-type");
    await typeSelect.locator(".n-base-selection").click();
    await typeSelect.locator("input").fill(typeLabel);
    await page.locator(".n-base-select-option:visible").getByText(typeLabel, { exact: true }).first().click();
  };
  const waitForFormulaValid = async () => {
    await page.getByTestId("formula-field-editor").getByRole("alert")
      .filter({ hasText: "\u516c\u5f0f\u6709\u6548" }).waitFor({ timeout: 30_000 });
  };
  const firstRow = oracle()[0];
  const createFormulaField = async ({ displayName, configureDraft, previewText, screenshot }) => {
    await openNewComputedFieldDrawer(displayName, "\u516c\u5f0f");
    await page.getByTestId("formula-editor-entry").click();
    await configureDraft();
    await waitForFormulaValid();
    await page.waitForFunction(() => document.querySelector('[data-testid="formula-preview-value"]')
      || document.querySelector('[data-testid="formula-preview-error"]'), undefined, { timeout: 30_000 });
    const previewError = page.getByTestId("formula-preview-error");
    if (await previewError.isVisible()) {
      const rows = await rawBridgeRequest(page, "query.page", {
        tableId: ledger.tableId, query: { filters: [], sorts: [], offset: 0, limit: 1 },
      });
      throw new Error(`${await previewError.innerText()}; sample=${JSON.stringify(rows.payload)}`);
    }
    await page.getByTestId("formula-preview-value").filter({ hasText: previewText })
      .waitFor({ timeout: 30_000 });
    if (screenshot) {
      const editorPart = screenshot.includes("pickers") ? "formula-source-table" : "formula-source";
      await page.getByTestId(editorPart).scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(runtime.evidenceDir, screenshot), fullPage: true });
      await page.getByTestId("formula-preview-value").scrollIntoViewIfNeeded();
      await page.screenshot({
        path: path.join(runtime.evidenceDir, screenshot.replace(".png", "-preview.png")), fullPage: true,
      });
    }
    await page.getByTestId("formula-editor-commit").click();
    const applied = await planAndApplyThroughUi();
    return describeField(ledger.tableId, applied.payload.fieldId);
  };

  // 公式 1：同合同且已发货 SUMIF —— TABLE/CurrentValue 全部经来源表/来源字段
  // 选择器插入（stable token 由编辑器绑定），其余片段以正式输入补全。
  const sumifDef = await createFormulaField({
    displayName: "\u5df2\u53d1\u8d27\u91d1\u989d",
    previewText: String(firstRow.sumifShipped),
    screenshot: "37-collection-editor-pickers.png",
    configureDraft: async () => {
      await page.getByTestId("formula-source-table").waitFor({ state: "visible", timeout: 30_000 });
      const textarea = page.getByTestId("formula-source").locator("textarea");
      await textarea.fill("");
      await page.keyboard.type("SUMIF(");
      await selectVisibleNOption(page, "formula-source-table", SHIP_TABLE);
      await page.getByTestId("formula-source-field").waitFor({ state: "visible", timeout: 30_000 });
      await page.getByTestId("formula-insert-table").click();
      await waitSourceEndsWith(`TABLE({${SHIP_TABLE}})`);
      await page.keyboard.type(", ");
      await insertSourceFieldThroughPicker("\u5408\u540c");
      await page.keyboard.type(" == {\u5408\u540c} && ");
      await insertSourceFieldThroughPicker("\u72b6\u6001");
      await page.keyboard.type(` == "${SHIPPED}", `);
      await insertSourceFieldThroughPicker("\u91d1\u989d");
      await page.keyboard.type(")");
    },
  });
  // 公式 2：COUNTIF；公式 3：ARRAYJOIN(UNIQUE(PROJECT(FILTER(...))))；公式 4：日期区间 SUMIF。
  // 这三条走直接输入路径（等价表达），仍由 Go 权威按显示名解析并绑定稳定 ID。
  const typedCases = [
    {
      displayName: "\u5df2\u53d1\u8d27\u5355\u6570",
      previewText: String(firstRow.countifShipped),
      source: `COUNTIF(TABLE({${SHIP_TABLE}}), CurrentValue.{\u5408\u540c} == {\u5408\u540c} && CurrentValue.{\u72b6\u6001} == "${SHIPPED}")`,
    },
    {
      displayName: "\u53bb\u91cd\u7f16\u7801",
      previewText: firstRow.uniqueCodes,
      screenshot: "37-collection-editor-arrayjoin.png",
      source: `ARRAYJOIN(UNIQUE(PROJECT(FILTER(TABLE({${SHIP_TABLE}}), CurrentValue.{\u5408\u540c} == {\u5408\u540c}), CurrentValue.{\u7f16\u7801})), ", ")`,
    },
    {
      displayName: "\u533a\u95f4\u91d1\u989d",
      previewText: String(firstRow.windowSum),
      source: `SUMIF(TABLE({${SHIP_TABLE}}), CurrentValue.{\u5408\u540c} == {\u5408\u540c} && CurrentValue.{\u65e5\u671f} >= {\u5f00\u59cb\u65e5\u671f} && CurrentValue.{\u65e5\u671f} <= {\u7ed3\u675f\u65e5\u671f}, CurrentValue.{\u91d1\u989d})`,
    },
  ];
  const typedDefs = [];
  for (const item of typedCases) {
    typedDefs.push(await createFormulaField({
      displayName: item.displayName,
      previewText: item.previewText,
      screenshot: item.screenshot,
      configureDraft: async () => {
        await fillNInput(page, "formula-source", item.source);
      },
    }));
  }
  const countifDef = typedDefs[0];
  const codesDef = typedDefs[1];
  const windowDef = typedDefs[2];

  recorder.check(
    "picker-built SUMIF persists cel-v2 with stable table/field identities instead of display names",
    sumifDef.formula?.language === "cel-v2"
      && sumifDef.formula?.source?.includes(`TABLE("${ship.tableId}")`)
      && [ship.field, shipStatus, shipAmount, ledgerContract].every(
        (field) => sumifDef.formula.source.includes(field.physicalName))
      && !sumifDef.formula.source.includes("{\u5408\u540c}")
      && !sumifDef.formula.source.includes(`TABLE({${SHIP_TABLE}})`),
    { source: sumifDef.formula?.source },
  );
  recorder.check(
    "typed cel-v2 collection formulas save with inferred scalar result types",
    [countifDef, codesDef, windowDef].every((definition) => definition.formula?.language === "cel-v2")
      && countifDef.formula?.resultType === "number"
      && codesDef.formula?.resultType === "text"
      && windowDef.formula?.resultType === "number"
      && windowDef.formula.source.includes(shipDate.physicalName)
      && windowDef.formula.source.includes(ledgerStart.physicalName)
      && windowDef.formula.source.includes(ledgerEnd.physicalName),
    {
      countif: countifDef.formula, codes: codesDef.formula, window: windowDef.formula,
    },
  );

  // ---- 同条件 Lookup 完整集合对照（条件筛选 + SUM，真实编辑器） ----
  await openNewComputedFieldDrawer("\u5bf9\u7167\u53d1\u8d27\u603b\u989d", "\u67e5\u627e\u5f15\u7528");
  await page.getByTestId("lookup-editor-entry").click();
  await selectVisibleNOption(page, "lookup-mode", "\u6761\u4ef6\u7b5b\u9009\uff08\u6309\u6761\u4ef6\u67e5\u8be2\u6765\u6e90\u8868\uff09");
  await selectVisibleNOption(page, "lookup-condition-source-table", SHIP_TABLE);
  await selectVisibleNOption(page, "lookup-target-field", "\u91d1\u989d");
  await selectVisibleNOption(page, "lookup-rule-source-field-0", "\u5408\u540c");
  await selectVisibleNOption(page, "lookup-rule-operand-field-0", "\u5408\u540c");
  await page.getByTestId("lookup-condition-add-rule").click();
  await selectVisibleNOption(page, "lookup-rule-source-field-1", "\u72b6\u6001");
  await selectVisibleNOption(page, "lookup-rule-operand-kind-1", "\u7c7b\u578b\u5316\u5e38\u91cf");
  await fillNInput(page, "lookup-rule-constant-text-1", SHIPPED);
  await selectVisibleNOption(page, "lookup-condition-match", "\u6ee1\u8db3\u5168\u90e8\uff08ALL\uff09");
  await selectVisibleNOption(page, "lookup-aggregation", "SUM \u6c42\u548c");
  await page.getByTestId("lookup-preview-value").filter({ hasText: String(firstRow.sumifShipped) })
    .waitFor({ timeout: 30_000 });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "37-lookup-condition-editor.png"), fullPage: true,
  });
  await page.getByTestId("lookup-editor-commit").click();
  const lookupApplied = await planAndApplyThroughUi();
  const lookupDef = await describeField(ledger.tableId, lookupApplied.payload.fieldId);
  recorder.check("same-condition Lookup persists the closed SUM condition contract",
    lookupDef.logicalType === "lookup"
      && lookupDef.lookup?.condition?.sourceTableId === ship.tableId
      && lookupDef.lookup?.condition?.match === "all"
      && lookupDef.lookup?.condition?.rules?.length === 2
      && lookupDef.lookup?.targetFieldId === shipAmount.fieldId,
    { lookup: lookupDef.lookup },
  );

  // ---- 权威值 + 网格可见性对齐独立 oracle ----
  const computedPhysical = {
    sumif: sumifDef.identity.physicalName,
    countif: countifDef.identity.physicalName,
    codes: codesDef.identity.physicalName,
    window: windowDef.identity.physicalName,
    lookup: lookupDef.identity.physicalName,
  };
  const ledgerQuery = { filters: [], sorts: [], offset: 0, limit: 100 };
  const computedExpectations = () => oracle().flatMap((row) => [
    { id: row.id, field: computedPhysical.sumif, value: row.sumifShipped },
    { id: row.id, field: computedPhysical.countif, value: row.countifShipped },
    { id: row.id, field: computedPhysical.codes, value: row.uniqueCodes },
    { id: row.id, field: computedPhysical.window, value: row.windowSum },
    { id: row.id, field: computedPhysical.lookup, value: row.sumifShipped },
  ]);
  const waitForGridCellText = async (marker, field, expected) => {
    await page.waitForFunction((probe) => {
      const row = [...document.querySelectorAll(".tabulator-row")].find((candidate) =>
        candidate.querySelector(`.tabulator-cell[tabulator-field="${probe.markerField}"]`)
          ?.textContent === probe.marker);
      const cell = row?.querySelector(`.tabulator-cell[tabulator-field="${probe.field}"]`);
      const value = cell?.querySelector(".vt-lookup-text") ?? cell;
      return (value?.textContent ?? "").trim() === probe.expected;
    }, {
      markerField: ledger.field.physicalName, marker, field, expected,
    }, { timeout: 30_000 });
  };
  const verifyComputedSurface = async (stage) => {
    const expected = computedExpectations();
    await waitForQueryPage(page, { tableId: ledger.tableId, query: ledgerQuery }, (payload) =>
      expected.every((item) => payload?.rows?.find((row) => row.id === item.id)?.[item.field] === item.value));
    await chooseToolbarMore(page, "refresh");
    for (const row of oracle()) {
      await waitForGridCellText(row.marker, computedPhysical.sumif, String(row.sumifShipped));
      await waitForGridCellText(row.marker, computedPhysical.countif, String(row.countifShipped));
      await waitForGridCellText(row.marker, computedPhysical.codes, row.uniqueCodes);
      await waitForGridCellText(row.marker, computedPhysical.window, String(row.windowSum));
      await waitForGridCellText(row.marker, computedPhysical.lookup, String(row.sumifShipped));
    }
    recorder.check(`authoritative values and grid cells match the independent oracle after ${stage}`,
      true, { stage });
  };
  await verifyComputedSurface("initial save");

  // ---- 导出结果与 oracle 一致 ----
  await chooseToolbarMore(page, "export-csv");
  await page.getByTestId("export-lookup-panel").waitFor({ state: "visible", timeout: 60_000 });
  await page.getByTestId("export-lookup-confirm").click();
  const exportTarget = path.join(runtime.controlsDir, "export-result.csv");
  let exported = "";
  const exportDeadline = Date.now() + 60_000;
  while (Date.now() < exportDeadline) {
    try {
      exported = await fs.readFile(exportTarget, "utf8");
      if (exported.includes(computedPhysical.lookup)) break;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  const exportedRows = parseCsv(exported);
  const header = exportedRows[0] ?? [];
  const exportMatches = oracle().every((row) => {
    const csvRow = exportedRows.find((candidate, index) =>
      index > 0 && candidate[header.indexOf(ledger.field.physicalName)] === row.marker);
    return csvRow
      && csvRow[header.indexOf(computedPhysical.sumif)] === String(row.sumifShipped)
      && csvRow[header.indexOf(computedPhysical.countif)] === String(row.countifShipped)
      && csvRow[header.indexOf(computedPhysical.codes)] === row.uniqueCodes
      && csvRow[header.indexOf(computedPhysical.window)] === String(row.windowSum)
      && csvRow[header.indexOf(computedPhysical.lookup)] === String(row.sumifShipped);
  });
  recorder.check("CSV export matches the independent oracle for every computed column",
    exportMatches, { exported });

  // ---- 来源编辑进入/退出/删除与窗口变更自动更新 ----
  const applySourceChange = async (stage, mutateFixture, operations, tableId = ship.tableId) => {
    const response = await applyProductMutation(page, tableId, operations, `37-collection-${stage}`);
    if (response.payload?.status !== "applied") {
      throw new Error(`source change ${stage} did not commit: ${JSON.stringify(response)}`);
    }
    mutateFixture();
    await verifyComputedSurface(stage);
  };
  await applySourceChange("match-enter", () => {
    shipments.find((row) => row.id === "ship00000000001").status = SHIPPED;
  }, [{
    kind: "update", recordId: "ship00000000001", values: { [shipStatus.physicalName]: SHIPPED },
  }]);
  await applySourceChange("match-exit", () => {
    shipments.find((row) => row.id === "ship00000000000").status = PENDING;
  }, [{
    kind: "update", recordId: "ship00000000000", values: { [shipStatus.physicalName]: PENDING },
  }]);
  await applySourceChange("amount-edit", () => {
    shipments.find((row) => row.id === "ship00000000002").amount = 8;
  }, [{
    kind: "update", recordId: "ship00000000002", values: { [shipAmount.physicalName]: 8 },
  }]);
  await applySourceChange("row-delete", () => {
    const index = shipments.findIndex((row) => row.id === "ship00000000004");
    shipments.splice(index, 1);
  }, [{
    kind: "delete", recordId: "ship00000000004",
  }]);
  await applySourceChange("window-narrow", () => {
    ledgerRows[0].start = "2026-03-10";
  }, [{
    kind: "update", recordId: ledgerRows[0].id,
    values: { [ledgerStart.physicalName]: "2026-03-10" },
  }], ledger.tableId);

  // ---- source 字段 rename：稳定 ID + 源码/显示名往返 ----
  await selectTable(page, SHIP_TABLE);
  const renamed = await applyV2FieldChange(page, ship.tableId, ship.field.fieldId, "update", {
    mutateDraft: (draft) => {
      draft.displayName = "\u5408\u540c\u7f16\u53f7";
      return draft;
    },
  });
  if (renamed.applied?.type !== "field.change.apply") {
    throw new Error(`source rename did not apply: ${JSON.stringify(renamed)}`);
  }
  const afterRename = await describeField(ledger.tableId, sumifDef.identity.fieldId);
  recorder.check(
    "renaming the source field keeps the persisted stable source and identical results",
    afterRename.formula?.source === sumifDef.formula.source
      && afterRename.formula?.language === "cel-v2"
      && afterRename.identity.fieldId === sumifDef.identity.fieldId,
    { persisted: afterRename.formula?.source, original: sumifDef.formula.source },
  );
  await selectTable(page, LEDGER_TABLE);
  await verifyComputedSurface("source rename");
  await openFieldSettingsFromHeader(page, computedPhysical.sumif);
  await page.waitForFunction((expectedTable) => {
    const text = document.querySelector('[data-testid="formula-summary-source"]')?.textContent ?? "";
    return text.includes(`TABLE({${expectedTable}})`)
      && text.includes("CurrentValue.{\u5408\u540c\u7f16\u53f7} == {\u5408\u540c}");
  }, SHIP_TABLE, { timeout: 30_000 });
  const restoredSummary = await page.getByTestId("formula-summary-source").innerText();
  recorder.check("reopened editor restores display names while storage keeps stable identities",
    restoredSummary.includes("CurrentValue.{\u5408\u540c\u7f16\u53f7}")
      && restoredSummary.includes(`TABLE({${SHIP_TABLE}})`)
      && !restoredSummary.includes(sumifDef.formula.source),
    { restoredSummary },
  );
  await closeFieldSettingsDrawer(page);
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "37-collection-grid.png"), fullPage: true,
  });

  // ---- workspace 关闭重开：源码与计算值持久（复用 scenario05 的重开路径） ----
  const session = await page.evaluate(() => window.__vibetableE2EBridgeDiagnostics.workspaceSession);
  await beginWritableWorkspaceBootstrapCapture(page, session.sessionEpoch, "workspace.open");
  const closed = await rawLifecycleWorkspaceV2Request(page, "workspace.close", { reason: "user" }, 60_000);
  if (closed.result?.state !== "closed") {
    throw new Error(`collection workspace close failed: ${JSON.stringify(closed)}`);
  }
  await openWorkspaceCenterFromSwitcher(page);
  await page.getByTestId("workspace-center").getByRole("button", { name: /E2E Product Workspace/ }).click();
  const reopened = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check("same workspace UUID reopens in a fresh writable epoch",
    reopened.payload.session.workspaceId === session.workspaceId
      && reopened.payload.session.sessionEpoch > session.sessionEpoch,
    { reopened });
  await page.getByTestId("nav-tables").click();
  await selectTable(page, LEDGER_TABLE);
  await verifyComputedSurface("workspace reopen");
  await openFieldSettingsFromHeader(page, computedPhysical.sumif);
  await page.waitForFunction((expectedTable) => {
    const text = document.querySelector('[data-testid="formula-summary-source"]')?.textContent ?? "";
    return text.includes(`TABLE({${expectedTable}})`);
  }, SHIP_TABLE, { timeout: 30_000 });
  await closeFieldSettingsDrawer(page);
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "37-collection-reopened.png"), fullPage: true,
  });
}
