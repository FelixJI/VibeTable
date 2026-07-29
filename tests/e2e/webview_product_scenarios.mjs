import fs from "node:fs/promises";
import fsSync from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";
import process from "node:process";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";

function parseArgs(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key?.startsWith("--") || value === undefined) {
      throw new Error(`invalid argument list near ${key ?? "<end>"}`);
    }
    values[key.slice(2)] = value;
  }
  for (const required of ["cdp-url", "scenario", "evidence-dir", "controls-dir"]) {
    if (!values[required]) throw new Error(`missing --${required}`);
  }
  return values;
}

async function locateProductPage(browser, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    for (const context of browser.contexts()) {
      for (const page of context.pages()) {
        if (page.url().startsWith("https://app.vibetable.local/")) return page;
      }
    }
    const context = browser.contexts()[0];
    if (context) {
      const page = context.pages()[0];
      if (page) {
        try {
          await page.waitForURL("https://app.vibetable.local/**", { timeout: 500 });
          return page;
        } catch {
          // WebView2 can expose the target while it is still about:blank.
        }
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("WebView2 CDP target never navigated to app.vibetable.local");
}

function makeRecorder() {
  const assertions = [];
  return {
    assertions,
    check(name, passed, details = {}) {
      assertions.push({ name, passed: Boolean(passed), details });
      if (!passed) throw new Error(`assertion failed: ${name}`);
    },
  };
}

/**
 * Collect browser-computed surface evidence with transparency resolved through
 * the ancestor stack. Kept self-contained because Playwright serializes this
 * function into the WebView2 renderer.
 */
function collectBrowserSurfaceEvidence(element) {
  const parseColor = (value) => {
    const match = value.match(
      /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+))?\s*\)$/i,
    );
    if (!match) return null;
    return [
      Number(match[1]),
      Number(match[2]),
      Number(match[3]),
      match[4] === undefined ? 1 : Number(match[4]),
    ];
  };
  const compositeBehind = (front, back) => {
    const alpha = front[3] + back[3] * (1 - front[3]);
    if (alpha <= 0) return [0, 0, 0, 0];
    return [
      (front[0] * front[3] + back[0] * back[3] * (1 - front[3])) / alpha,
      (front[1] * front[3] + back[1] * back[3] * (1 - front[3])) / alpha,
      (front[2] * front[3] + back[2] * back[3] * (1 - front[3])) / alpha,
      alpha,
    ];
  };
  const luminance = (color) => {
    const channels = color.slice(0, 3).map((value) => {
      const normalized = value / 255;
      return normalized <= 0.04045
        ? normalized / 12.92
        : ((normalized + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
  };

  const style = getComputedStyle(element);
  let effective = [0, 0, 0, 0];
  let current = element;
  while (current) {
    const background = parseColor(getComputedStyle(current).backgroundColor);
    if (background) effective = compositeBehind(effective, background);
    if (effective[3] >= 0.999) break;
    current = current.parentElement;
  }
  if (effective[3] < 0.999) {
    effective = compositeBehind(effective, [255, 255, 255, 1]);
  }
  const foreground = parseColor(style.color);
  const backgroundLuminance = luminance(effective);
  const parsedOpacity = Number.parseFloat(style.opacity);
  const opacity = Number.isFinite(parsedOpacity)
    ? Math.max(0, Math.min(1, parsedOpacity))
    : 1;
  const effectiveForeground = foreground
    ? compositeBehind(
      [foreground[0], foreground[1], foreground[2], foreground[3] * opacity],
      effective,
    )
    : null;
  const foregroundLuminance = effectiveForeground
    ? luminance(effectiveForeground)
    : null;
  const contrast = foregroundLuminance === null
    ? null
    : (Math.max(backgroundLuminance, foregroundLuminance) + 0.05)
      / (Math.min(backgroundLuminance, foregroundLuminance) + 0.05);
  const rect = element.getBoundingClientRect();
  return {
    rawBackground: style.backgroundColor,
    effectiveBackground: effective,
    backgroundLuminance,
    foreground: style.color,
    effectiveForeground,
    opacity,
    contrast,
    visible: rect.width > 0 && rect.height > 0,
  };
}

const ALWAYS_FATAL_CONSOLE_PATTERNS = Object.freeze([
  /unknown (?:column definition )?option/i,
  /invalid column definition option/i,
  /tabulator.*unknown/i,
]);

function assertCleanRendererDiagnostics(recorder, consoleEntries, pageErrors, networkEntries) {
  const unexpectedConsole = consoleEntries.filter((entry) => {
    if (!["warning", "error"].includes(entry.type)) return false;
    if (ALWAYS_FATAL_CONSOLE_PATTERNS.some((pattern) => pattern.test(entry.text))) return true;
    return true;
  });
  const externalRequests = networkEntries.filter((entry) => {
    let url;
    try {
      url = new URL(entry.url);
    } catch {
      return false;
    }
    if (!["http:", "https:"].includes(url.protocol)) return false;
    if (url.protocol === "https:" && url.hostname === "app.vibetable.local") return false;
    return !["127.0.0.1", "::1", "localhost"].includes(url.hostname);
  });
  recorder.check(
    "renderer completed without errors, unexpected diagnostics, or external HTTP traffic",
    pageErrors.length === 0
      && unexpectedConsole.length === 0
      && externalRequests.length === 0,
    { pageErrors, unexpectedConsole, externalRequests },
  );
}

function assertCleanBridgeDiagnostics(recorder, diagnostics) {
  const failures = diagnostics?.failures ?? [];
  const pending = diagnostics?.pending ?? [];
  recorder.check(
    "bridge completed without unexpected operation.failed or pending requests",
    failures.length === 0 && pending.length === 0,
    { failures, pending },
  );
}

async function waitForShell(page, recorder) {
  await page.getByTestId("nav-home").waitFor({ state: "visible", timeout: 60_000 });
  await page.getByTestId("home-view").waitFor({ state: "visible" });
  await page.getByTestId("connection-pill").waitFor({ state: "visible" });
  recorder.check("real WebView2 renderer reached the home workspace",
    page.url().startsWith("https://app.vibetable.local/"), {
    url: page.url(),
  });
}

async function selectNValue(page, testId, value) {
  const exactLabels = {
    shortText: /^(短文本|Short text)$/u,
    integer: /^(整数（Integer）|Integer)$/u,
    select: /^(单选|Single select)$/u,
    multiSelect: /^(多选|Multi-select)$/u,
    json: /^(结构化数据（JSON）|Structured data \(JSON\))$/u,
    formula: /^(公式|Formula)$/u,
    file: /^(托管附件|Managed attachment)$/u,
    relation: /^(关联|Relation)$/u,
    lookup: /^(查找引用|Lookup)$/u,
    autoDate: /^(系统时间|System time)$/u,
  };
  const localizedSearch = {
    shortText: { zh: "短文本", en: "Short text" },
    integer: { zh: "整数", en: "Integer" },
    select: { zh: "单选", en: "Single select" },
    multiSelect: { zh: "多选", en: "Multi-select" },
    json: { zh: "JSON", en: "Structured data" },
    formula: { zh: "公式", en: "Formula" },
    file: { zh: "托管附件", en: "Managed attachment" },
    relation: { zh: "关联", en: "Relation" },
    lookup: { zh: "查找引用", en: "Lookup" },
    autoDate: { zh: "系统时间", en: "System time" },
  };
  // Keep the selectors ASCII-stable even if a Windows editor rewrites the
  // surrounding legacy fixture text with the wrong code page.
  Object.assign(exactLabels, {
    shortText: /^(?:\u77ed\u6587\u672c|Short text)$/u,
    integer: /^(?:\u6574\u6570\uff08Integer\uff09|Integer)$/u,
    select: /^(?:\u5355\u9009|Single select)$/u,
    multiSelect: /^(?:\u591a\u9009|Multi-select)$/u,
    json: /^(?:\u7ed3\u6784\u5316\u6570\u636e\uff08JSON\uff09|Structured data \(JSON\))$/u,
    formula: /^(?:\u516c\u5f0f|Formula)$/u,
    file: /^(?:\u6258\u7ba1\u9644\u4ef6|Managed attachment)$/u,
    relation: /^(?:\u5173\u8054|Relation)$/u,
    lookup: /^(?:\u67e5\u627e\u5f15\u7528|Lookup)$/u,
    autoDate: /^(?:\u7cfb\u7edf\u65f6\u95f4|System time)$/u,
  });
  Object.assign(localizedSearch, {
    shortText: { zh: "\u77ed\u6587\u672c", en: "Short text" },
    integer: { zh: "\u6574\u6570", en: "Integer" },
    select: { zh: "\u5355\u9009", en: "Single select" },
    multiSelect: { zh: "\u591a\u9009", en: "Multi-select" },
    json: { zh: "JSON", en: "Structured data" },
    formula: { zh: "\u516c\u5f0f", en: "Formula" },
    file: { zh: "\u6258\u7ba1\u9644\u4ef6", en: "Managed attachment" },
    relation: { zh: "\u5173\u8054", en: "Relation" },
    lookup: { zh: "\u67e5\u627e\u5f15\u7528", en: "Lookup" },
    autoDate: { zh: "\u7cfb\u7edf\u65f6\u95f4", en: "System time" },
  });
  const label = exactLabels[value];
  const search = localizedSearch[value];
  if (!label || !search) throw new Error(`missing E2E field-type label mapping: ${value}`);
  const select = page.getByTestId(testId);
  await select.click();
  const language = await page.locator("html").getAttribute("lang");
  await select.locator("input").fill(language?.startsWith("zh") ? search.zh : search.en);
  const option = page.locator(".n-base-select-option:visible")
    .filter({ hasText: label })
    .first();
  await option.waitFor({ state: "visible" });
  await option.click();
  // Filterable NSelect renders the chosen label as an input value rather than
  // a text node, so getByText is not a valid post-selection assertion.
  // The popup closing proves the option click committed; scenario-specific
  // field-editor locators then verify the selected type.
  await option.waitFor({ state: "hidden" });
}

async function fillNInput(page, testId, value) {
  await page.getByTestId(testId).locator("input, textarea").first().fill(value);
}

async function closeVisibleNSelectMenu(page) {
  const visibleMenu = page.locator(".n-base-select-menu:visible").first();
  if (!await visibleMenu.count()) return;
  // A real pointer gesture on the fixed modal header triggers Naive UI's
  // click-outside handler without risking NModal's Escape-to-close behavior.
  const headerBox = await page.locator(".create-table-modal .n-card-header")
    .boundingBox();
  if (!headerBox) throw new Error("create-table modal header is not visible");
  await page.mouse.click(headerBox.x + headerBox.width / 2, headerBox.y + 8);
  await visibleMenu.waitFor({ state: "hidden" });
}

async function selectNOptionsByText(page, testId, labels) {
  const select = page.getByTestId(testId);
  for (const label of labels) {
    // Naive UI teleports every select menu to the document body. Close any
    // previously-open menu before opening this exact control; otherwise an
    // option from the preceding index editor can satisfy a global locator.
    await closeVisibleNSelectMenu(page);
    // The long schema form recalculates its index option list when fields
    // change, briefly replacing the NSelect root. Its selection surface is
    // still the intended target, so bypass animation stability checks while
    // retaining a fresh locator resolution.
    await select.locator(".n-base-selection")
      .evaluate((node) => node.click());
    const option = page.locator(".n-base-select-option:visible")
      .filter({ hasText: label })
      .first();
    await option.waitFor();
    await option.evaluate((node) => node.click());
  }
  await closeVisibleNSelectMenu(page);
}

async function beginCellEdit(cell) {
  // Range selection owns the pointer gesture in the production grid. F2 is
  // the explicit, accessible edit command and deterministically asks
  // Tabulator to open the active cell editor.
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if ((await cell.getAttribute("class"))?.includes("tabulator-editable")) break;
    await cell.waitFor({ state: "visible" });
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (!(await cell.getAttribute("class"))?.includes("tabulator-editable")) {
    throw new Error("cell did not become editable after the edit schema loaded");
  }
  // The production grid intentionally uses a double-click edit trigger so a
  // single click remains dedicated to spreadsheet-style range selection.
  await cell.dblclick();
  const editor = cell.locator("input, textarea").first();
  await editor.waitFor({ state: "visible", timeout: 10_000 });
  return editor;
}

async function insertRowFromToolbar(page) {
  const button = page.getByTestId("toolbar-insert-row");
  await button.waitFor({ state: "visible", timeout: 30_000 });
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (!await button.isDisabled()) {
      await button.click();
      return;
    }
    await page.waitForTimeout(50);
  }
  throw new Error("insert row stayed disabled because the table mutation revision never became ready");
}

async function waitForVisibleRowCount(page, expectedRows, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let observed = -1;
  while (Date.now() < deadline) {
    observed = await page.locator(".tabulator-row:visible").count();
    if (observed === expectedRows) return observed;
    await page.waitForTimeout(50);
  }
  throw new Error(`observed ${observed} visible rows, expected ${expectedRows}`);
}

async function waitForImportSuccess(page, expectedRows, timeoutMs = 60_000) {
  const expectedMessages = [
    `Imported ${expectedRows} row(s).`,
    `已导入 ${expectedRows} 行。`,
  ];
  const deadline = Date.now() + timeoutMs;
  let messages = [];
  const observedMessages = new Set();
  while (Date.now() < deadline) {
    messages = await page.locator(".n-message").allInnerTexts();
    for (const message of messages) observedMessages.add(message);
    const success = messages.find((message) =>
      expectedMessages.some((expected) => message.includes(expected)));
    if (success) return success;
    const failure = messages.find((message) =>
      /failed|error|invalid|cancelled|失败|错误|无效|取消|Imported 0 row|已导入 0 行/iu.test(message));
    if (failure) throw new Error(`import failed in the product UI: ${failure}`);
    await page.waitForTimeout(50);
  }
  throw new Error(
    `import outcome toast did not appear; observed messages: ${JSON.stringify([...observedMessages])}`,
  );
}

function cloneJson(value) {
  return JSON.parse(JSON.stringify(value));
}

async function createEmptyTable(page, displayName) {
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", displayName);
  const submit = page.getByTestId("create-table-submit");
  await submit.click();
  await waitForCreateTableSubmission(page, submit);
  const row = page.locator(".table-row").filter({
    has: page.getByTestId("sidebar-table-name").filter({ hasText: displayName }),
  }).last();
  await row.waitFor();
  const tableId = (await row.locator("small").innerText()).trim();
  if (!tableId.startsWith("tbl_")) {
    throw new Error(`host did not expose an opaque table identity for ${displayName}: ${tableId}`);
  }
  await page.getByTestId("field-display-name").waitFor({ timeout: 30_000 });
  return tableId;
}

async function createV2Field(
  page,
  tableId,
  displayName,
  logicalType = "text",
  customize = (draft) => draft,
) {
  const described = await rawBridgeRequest(page, "field.settings.describe", { tableId });
  const capability = described.payload?.capabilities?.find(
    (item) => item.logicalType === logicalType && item.userCreatable,
  );
  if (!capability) {
    throw new Error(`no user-creatable ${logicalType} capability for ${tableId}`);
  }
  const recommended = cloneJson(capability.recommended);
  let draft = {
    displayName,
    help: "",
    logicalType,
    value: recommended.value,
    constraints: recommended.constraints,
    storage: recommended.storage,
    display: recommended.display,
    ...(recommended.file ? { file: recommended.file } : {}),
    ...(recommended.json ? { json: recommended.json } : {}),
    ...(recommended.formula ? { formula: recommended.formula } : {}),
    ...(recommended.lookup ? { lookup: recommended.lookup } : {}),
  };
  if (logicalType === "select" || logicalType === "multiSelect") {
    draft.select = {
      options: [{ optionId: "", label: "Option 1", color: "#64748b", order: 10, state: "active" }],
    };
  }
  if (logicalType === "relation") {
    draft.relation = {
      targetTableId: "",
      cardinality: "one",
      deletePolicy: "setNull",
      displayFieldId: "",
    };
  }
  draft = customize(cloneJson(draft));
  const intent = {
    action: "create",
    tableId,
    fieldId: described.payload?.fieldId ?? "",
    expectedSchemaRevision: described.payload?.schemaRevision,
    expectedDataRevision: described.payload?.dataRevision ?? null,
    draft,
    actor: { id: "product-e2e", kind: "user" },
    conversionRule: "",
    confirmation: "",
    backupReceipt: "",
  };
  const planned = await rawBridgeRequest(page, "field.change.plan", intent);
  if (planned.type === "operation.failed" || !planned.payload?.canApply) {
    throw new Error(`field plan failed: ${JSON.stringify(planned)}`);
  }
  const operationId = `op_e2e_${Date.now()}_${Math.random().toString(16).slice(2)}`;
  const applied = await rawBridgeRequest(page, "field.change.apply", {
    planId: planned.payload.planId,
    planHash: planned.payload.planHash,
    operationId,
    actor: { id: "product-e2e", kind: "user" },
    confirmations: [...(planned.payload.confirmations ?? [])],
  });
  if (applied.type === "operation.failed") {
    throw new Error(`field apply failed: ${JSON.stringify(applied)}`);
  }
  const definition = planned.payload.after;
  return {
    tableId,
    fieldId: applied.payload?.fieldId ?? definition?.identity?.fieldId,
    physicalName: definition?.identity?.physicalName,
    definition,
    receipt: applied.payload,
  };
}

async function closeFieldSettingsDrawer(page) {
  const confirmation = page.waitForEvent("dialog", { timeout: 2_000 })
    .then(async (dialog) => {
      await dialog.accept();
      return dialog.message();
    })
    .catch(() => null);
  await page.getByTestId("field-close-button").click();
  await confirmation;
  await page.getByTestId("field-display-name").waitFor({ state: "hidden" });
}

async function applyV2FieldChange(
  page,
  tableId,
  fieldId,
  action,
  {
    mutateDraft = (draft) => draft,
    conversionRule = "",
    confirmation = "",
    backupReceipt = "",
  } = {},
) {
  const described = await rawBridgeRequest(page, "field.settings.describe", { tableId, fieldId });
  const definition = described.payload?.definition;
  let draft = null;
  if (!["retire", "restore", "purge"].includes(action)) {
    if (!definition) throw new Error(`field ${fieldId} is not active`);
    const {
      contract: _contract,
      identity: _identity,
      lifecycle: _lifecycle,
      ...editable
    } = cloneJson(definition);
    draft = mutateDraft(editable);
  }
  const intent = {
    action,
    tableId,
    fieldId,
    expectedSchemaRevision: described.payload?.schemaRevision,
    expectedDataRevision: described.payload?.dataRevision ?? null,
    draft,
    actor: { id: "product-e2e", kind: "user" },
    conversionRule,
    confirmation,
    backupReceipt,
  };
  const planned = await rawBridgeRequest(page, "field.change.plan", intent);
  if (planned.type === "operation.failed" || !planned.payload?.canApply) return { planned, applied: null };
  const applied = await rawBridgeRequest(page, "field.change.apply", {
    planId: planned.payload.planId,
    planHash: planned.payload.planHash,
    operationId: `op_e2e_${Date.now()}_${Math.random().toString(16).slice(2)}`,
    actor: { id: "product-e2e", kind: "user" },
    confirmations: [...(planned.payload.confirmations ?? [])],
  });
  return { planned, applied };
}

async function waitForFieldMigration(page, jobId, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  let status = null;
  while (Date.now() < deadline) {
    status = await rawBridgeRequest(page, "field.change.status", { jobId });
    if (["completed", "cancelled", "failed", "rolled_back"].includes(status.payload?.phase)) {
      return status;
    }
    await page.waitForTimeout(100);
  }
  throw new Error(`field migration did not finish: ${jobId}: ${JSON.stringify(status)}`);
}

async function applyProductMutation(page, tableId, operations, label, expectFailure = false) {
  const snapshot = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const token = `${label}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return rawBridgeRequest(page, "mutation.apply", {
    contractVersion: "1.0",
    requestId: token,
    idempotencyKey: token,
    tableId,
    schemaRevision: snapshot.payload.snapshot.schemaRevision,
    operations,
    actor: { type: "user", id: "product-e2e", displayName: "Product E2E" },
    expectedRevision: null,
    expectedDigest: null,
  }, 20_000, expectFailure ? ["mutation.apply", "operation.failed"] : ["mutation.apply"]);
}

async function createSimpleTable(page, displayName, fieldName = "label") {
  const tableId = await createEmptyTable(page, displayName);
  const field = await createV2Field(page, tableId, fieldName, "text");
  await closeFieldSettingsDrawer(page);
  return { tableId, field };
}

async function waitForCreateTableSubmission(
  page,
  submit,
  timeoutMs = 30_000,
) {
  const nameInput = page.getByTestId("create-table-name-input");
  try {
    await nameInput.waitFor({ state: "hidden", timeout: timeoutMs });
  } catch (error) {
    const evidence = {
      timeoutMs,
      inputVisible: await nameInput.isVisible().catch(() => false),
      alerts: await page.locator(".create-table-modal [role='alert']")
        .allTextContents(),
      errorText: await page.getByTestId("create-table-error")
        .allTextContents(),
      submitVisible: await submit.isVisible().catch(() => false),
      submitDisabled: await submit.isDisabled().catch(() => null),
      submitText: await submit.allTextContents(),
      cause: error instanceof Error ? error.message : String(error),
    };
    throw new Error(
      `create table submission did not complete before timeout: ${JSON.stringify(evidence)}`,
    );
  }
}

async function waitForJsonFile(filePath, predicate, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let value = null;
  while (Date.now() < deadline) {
    try {
      value = JSON.parse(await fs.readFile(filePath, "utf8"));
      if (predicate(value)) return value;
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`JSON evidence did not reach expected state: ${filePath}: ${JSON.stringify(value)}`);
}

async function scenario01(page, recorder, network, runtime) {
  await waitForShell(page, recorder);
  const external = network.filter((entry) => {
    try {
      const parsed = new URL(entry.url);
      return !["app.vibetable.local", "plugin.vibetable.local"].includes(parsed.hostname);
    } catch {
      return false;
    }
  });
  recorder.check("page emitted no external HTTP(S) requests", external.length === 0, {
    external,
  });
  const processNetwork = await waitForJsonFile(
    path.join(runtime.evidenceDir, "process-network-observations.json"),
    (value) => value?.samples > 0,
  );
  recorder.check(
    "VibeTable-owned processes used only loopback TCP endpoints",
    ["monitoring", "completed"].includes(processNetwork.status)
      && processNetwork.samples > 0
      && Array.isArray(processNetwork.errors)
      && processNetwork.errors.length === 0
      && Array.isArray(processNetwork.unexpectedProductNonLoopback)
      && processNetwork.unexpectedProductNonLoopback.length === 0,
    { processNetwork },
  );
  recorder.check("startup gate is no longer present", await page.getByTestId("startup-gate").count() === 0);
}

async function scenario02(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  await page.getByTestId("sidebar-new-table").waitFor();
  const target = await createSimpleTable(page, "E2E Relation Target V2", "Label");
  const tableId = await createEmptyTable(page, "E2E Field Settings V2");
  recorder.check(
    "new table is host-owned, empty, and opens the unified field settings drawer",
    tableId.startsWith("tbl_")
      && await page.getByTestId("field-display-name").isVisible(),
    { tableId },
  );
  await closeFieldSettingsDrawer(page);

  const specifications = [
    ["Title", "text"],
    ["Notes", "editor"],
    ["Amount", "number"],
    ["Enabled", "bool"],
    ["Due date", "date"],
    ["Observed at", "dateTime"],
    ["Local time", "time"],
    ["Email", "email"],
    ["Website", "url"],
    ["Status", "select"],
    ["Tags", "multiSelect"],
    ["Parent", "relation"],
    ["Files", "file"],
    ["Location", "geoPoint"],
    ["Payload", "json"],
    ["Upper title", "formula"],
    ["Parent label", "lookup"],
  ];
  const created = [];
  for (const [displayName, logicalType] of specifications) {
    created.push(await createV2Field(page, tableId, displayName, logicalType, (draft) => {
      if (logicalType === "select" || logicalType === "multiSelect") {
        draft.select.options = [
          { optionId: "", label: "Draft", color: "#64748b", order: 10, state: "active" },
          { optionId: "", label: "Active", color: "#16a34a", order: 20, state: "active" },
          { optionId: "", label: "Obsolete", color: "#dc2626", order: 30, state: "active" },
        ];
      }
      if (logicalType === "relation") {
        draft.relation.targetTableId = target.tableId;
        draft.relation.displayFieldId = target.field.fieldId;
      }
      if (logicalType === "formula") {
        const title = created.find((field) => field.definition?.logicalType === "text");
        draft.formula = {
          language: "cel-v1",
          source: `upper(${title.physicalName})`,
          resultType: "text",
        };
      }
      if (logicalType === "lookup") {
        const relation = created.find((field) => field.definition?.logicalType === "relation");
        draft.lookup = {
          relationFieldId: relation.fieldId,
          targetFieldId: target.field.fieldId,
          aggregate: "first",
          resultType: "text",
        };
      }
      return draft;
    }));
  }
  recorder.check(
    "recommended capabilities created every regular field family with opaque stable identities",
    created.length === specifications.length
      && created.every((field) =>
        field.fieldId?.startsWith("fld_")
        && field.physicalName?.startsWith("f_"))
      && new Set(created.map((field) => field.fieldId)).size === created.length,
    { created: created.map(({ fieldId, physicalName, definition }) => ({
      fieldId,
      physicalName,
      logicalType: definition?.logicalType,
    })) },
  );

  const computed = created.filter((field) =>
    field.definition?.logicalType === "formula"
      || field.definition?.logicalType === "lookup");
  recorder.check(
    "formula and lookup are created through the same v2 planner and retain typed specs",
    computed.length === 2
      && computed.every((field) => field.receipt?.definition?.logicalType === field.definition?.logicalType)
      && computed.find((field) => field.definition?.logicalType === "formula")
        ?.definition?.formula?.language === "cel-v1"
      && computed.find((field) => field.definition?.logicalType === "lookup")
        ?.definition?.lookup?.relationFieldId
        === created.find((field) => field.definition?.logicalType === "relation")?.fieldId,
    { computed },
  );

  await selectTable(page, "E2E Field Settings V2");
  await insertRowFromToolbar(page);
  const titleField = created.find((field) => field.definition?.logicalType === "text");
  const undoCell = page.locator(
    `.tabulator-cell[tabulator-field="${titleField.physicalName}"]`,
  ).first();
  const undoEditor = await beginCellEdit(undoCell);
  await beginBridgeMessageCapture(
    page,
    ["table.editCommitted", "table.editRejected", "operation.failed"],
  );
  await undoEditor.fill("undo-this-data-edit");
  await undoEditor.press("Enter");
  const editCommitted = await waitForCapturedBridgeMessage(page, 30_000);
  if (editCommitted.type !== "table.editCommitted") {
    throw new Error(`initial edit was not committed: ${JSON.stringify(editCommitted)}`);
  }
  await page.waitForFunction(
    ({ field, value }) => document.querySelector(
      `.tabulator-cell[tabulator-field="${field}"]`,
    )?.textContent?.includes(value),
    { field: titleField.physicalName, value: "undo-this-data-edit" },
    { timeout: 30_000 },
  );
  await beginBridgeMessageCapture(
    page,
    ["table.editCommitted", "table.editRejected", "operation.failed"],
  );
  await page.keyboard.press("Control+Z");
  const undoCommitted = await waitForCapturedBridgeMessage(page, 30_000);
  if (undoCommitted.type !== "table.editCommitted") {
    throw new Error(`undo edit was not committed: ${JSON.stringify(undoCommitted)}`);
  }
  // A data/relation refresh can briefly rebuild the Tabulator DOM. Waiting
  // for the cell text to disappear first would mistake that transient absence
  // for a committed undo. Poll the authoritative QueryPort boundary first;
  // only after the value is gone from storage may the renderer assertion pass.
  let rowsAfterUndo = null;
  const undoDeadline = Date.now() + 30_000;
  while (Date.now() < undoDeadline) {
    rowsAfterUndo = await rawBridgeRequest(page, "query.page", {
      tableId,
      query: { filters: [], sorts: [], offset: 0, limit: 100 },
    });
    if (
      rowsAfterUndo.payload?.rows?.length === 1
      && rowsAfterUndo.payload.rows[0]?.[titleField.physicalName]
        !== "undo-this-data-edit"
    ) break;
    await page.waitForTimeout(50);
  }
  await page.waitForFunction(
    ({ field, value }) => !document.querySelector(
      `.tabulator-cell[tabulator-field="${field}"]`,
    )?.textContent?.includes(value),
    { field: titleField.physicalName, value: "undo-this-data-edit" },
    { timeout: 30_000 },
  );
  const computedAfterUndo = await rawBridgeRequest(page, "field.settings.describe", {
    tableId,
    fieldId: computed[0].fieldId,
  });
  recorder.check(
    "Ctrl+Z reverses the data edit but never reverses a committed schema change",
    rowsAfterUndo.payload?.rows?.length === 1
      && rowsAfterUndo.payload.rows[0]?.[titleField.physicalName] !== "undo-this-data-edit"
      && computedAfterUndo.payload?.definition?.identity?.fieldId === computed[0].fieldId,
    { rowsAfterUndo, computedAfterUndo },
  );

  // The remaining assertions intentionally mutate this table through raw
  // bridge requests. Keep the visible grid on a different table so it cannot
  // issue Lookup reads between an out-of-band schema apply and its UI refresh.
  await selectTable(page, "E2E Relation Target V2");

  const status = created.find((field) => field.definition?.logicalType === "select");
  const draftOption = status.definition.select.options.find((option) => option.label === "Draft");
  const activeOption = status.definition.select.options.find((option) => option.label === "Active");
  const obsoleteOption = status.definition.select.options.find((option) => option.label === "Obsolete");
  const selectSnapshot = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const selectMutation = await rawBridgeRequest(page, "mutation.apply", {
    contractVersion: "1.0",
    requestId: `e2e-select-seed-${Date.now()}`,
    idempotencyKey: `e2e-select-seed-${Date.now()}`,
    tableId,
    schemaRevision: selectSnapshot.payload.snapshot.schemaRevision,
    operations: [
      { kind: "insert", recordId: null, values: { [status.physicalName]: draftOption.optionId } },
      { kind: "insert", recordId: null, values: { [status.physicalName]: activeOption.optionId } },
      { kind: "insert", recordId: null, values: { [status.physicalName]: obsoleteOption.optionId } },
    ],
    actor: { type: "user", id: "product-e2e", displayName: "Product E2E" },
    expectedRevision: null,
    expectedDigest: null,
  });
  const relabelled = await applyV2FieldChange(page, tableId, status.fieldId, "update", {
    mutateDraft: (draft) => {
      const option = draft.select.options.find(
        (candidate) => candidate.optionId === activeOption.optionId,
      );
      option.label = "In progress";
      option.color = "#0ea5e9";
      return draft;
    },
  });
  const relabelledRows = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  recorder.check(
    "changing a select option label and color preserves stored stable option IDs",
    relabelled.applied?.type === "field.change.apply"
      && relabelled.planned?.payload?.after?.select?.options?.some(
        (option) => option.optionId === activeOption.optionId
          && option.label === "In progress",
      )
      && relabelledRows.payload?.rows?.some(
        (row) => row[status.physicalName] === activeOption.optionId,
      ),
    { relabelled, relabelledRows },
  );
  const replaced = await applyV2FieldChange(page, tableId, status.fieldId, "update", {
    mutateDraft: (draft) => {
      draft.select.options = draft.select.options.filter(
        (option) => option.optionId !== draftOption.optionId,
      );
      return draft;
    },
    conversionRule: `selectOption:${draftOption.optionId}:replace:${activeOption.optionId}`,
  });
  const replacedStatus = replaced.applied?.payload?.migrationJobId
    ? await waitForFieldMigration(page, replaced.applied.payload.migrationJobId)
    : null;
  const cleared = await applyV2FieldChange(page, tableId, status.fieldId, "update", {
    mutateDraft: (draft) => {
      draft.select.options = draft.select.options.filter(
        (option) => option.optionId !== obsoleteOption.optionId,
      );
      return draft;
    },
    conversionRule: `selectOption:${obsoleteOption.optionId}:clear`,
  });
  const clearedStatus = cleared.applied?.payload?.migrationJobId
    ? await waitForFieldMigration(page, cleared.applied.payload.migrationJobId)
    : null;
  const selectRows = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const selectValues = (selectRows.payload?.rows ?? []).map(
    (row) => row[status.physicalName],
  );
  recorder.check(
    "select option deletion explicitly replaces referenced values or clears them",
    selectMutation.payload?.status === "applied"
      && (!replacedStatus || replacedStatus.payload?.phase === "completed")
      && (!clearedStatus || clearedStatus.payload?.phase === "completed")
      && selectValues.includes(activeOption.optionId)
      && selectValues.some((value) => value === null || value === undefined || value === ""),
    { replaced, replacedStatus, cleared, clearedStatus, selectValues },
  );

  const amount = created.find((field) => field.definition?.logicalType === "number");
  const beforeIdentity = cloneJson(amount.definition.identity);
  const updated = await applyV2FieldChange(page, tableId, amount.fieldId, "update", {
    mutateDraft: (draft) => {
      draft.displayName = "Total amount";
      draft.display.displayScale = 4;
      return draft;
    },
  });
  recorder.check(
    "same-type display update keeps field and physical identities stable",
    updated.applied?.type === "field.change.apply"
      && updated.planned?.payload?.classes?.every((item) => item === "display" || item === "metadata")
      && updated.planned?.payload?.after?.identity?.fieldId === beforeIdentity.fieldId
      && updated.planned?.payload?.after?.identity?.physicalName === beforeIdentity.physicalName,
    { beforeIdentity, updated },
  );

  const retired = await applyV2FieldChange(page, tableId, amount.fieldId, "retire");
  const recycle = await rawBridgeRequest(page, "field.recycleBin.list", { tableId });
  const restored = await applyV2FieldChange(page, tableId, amount.fieldId, "restore");
  const restoredDescribe = await rawBridgeRequest(page, "field.settings.describe", {
    tableId,
    fieldId: amount.fieldId,
  });
  recorder.check(
    "retire and restore preserve field identity through the recycle bin",
    retired.applied?.type === "field.change.apply"
      && recycle.payload?.fields?.some((field) => field.identity?.fieldId === amount.fieldId)
      && restored.applied?.type === "field.change.apply"
      && restoredDescribe.payload?.definition?.identity?.fieldId === beforeIdentity.fieldId
      && restoredDescribe.payload?.definition?.identity?.physicalName === beforeIdentity.physicalName,
    { retired, recycle, restored, restoredDescribe },
  );

  const legacyWrite = await rawBridgeRequest(
    page,
    "schema.apply",
    { definition: { tableId }, expectedRevision: 0 },
    20_000,
    ["operation.failed"],
  );
  recorder.check(
    "renderer cannot reach the retired generic schema write route",
    legacyWrite.type === "operation.failed",
    { legacyWrite },
  );
  return;
}

async function rawBridgeRequest(
  page,
  type,
  payload,
  timeoutMs = 20_000,
  expectedResponseTypes = [type],
) {
  return page.evaluate(
    ({ type: requestType, payload: requestPayload, timeout, responseTypes }) =>
      new Promise((resolve, reject) => {
        const requestId = `e2e-${crypto.randomUUID()}`;
        const diagnostics = window.__vibetableE2EBridgeDiagnostics;
        if (diagnostics && responseTypes.includes("operation.failed")) {
          diagnostics.expectedFailures[requestId] = true;
        }
        const timer = setTimeout(() => {
          window.chrome.webview.removeEventListener("message", handler);
          reject(new Error(`bridge timeout for ${requestType}`));
        }, timeout);
        const handler = (event) => {
          let message = event.data;
          if (typeof message === "string") {
            try { message = JSON.parse(message); } catch { return; }
          }
          if (!message || message.requestId !== requestId) return;
          if (!responseTypes.includes(message.type) && message.type !== "operation.failed") return;
          clearTimeout(timer);
          window.chrome.webview.removeEventListener("message", handler);
          resolve(message);
        };
        window.chrome.webview.addEventListener("message", handler);
        window.chrome.webview.postMessage({
          type: requestType,
          requestId,
          payload: requestPayload,
        });
      }),
    { type, payload, timeout: timeoutMs, responseTypes: expectedResponseTypes },
  );
}

async function installBridgeDiagnostics(page) {
  await page.evaluate(() => {
    if (window.__vibetableE2EBridgeDiagnostics?.installed) return;

    const diagnostics = {
      installed: true,
      installedAt: new Date().toISOString(),
      requests: [],
      roundTrips: [],
      failures: [],
      pending: {},
      expectedFailures: {},
    };
    const webview = window.chrome.webview;
    const originalPostMessage = webview.postMessage.bind(webview);
    webview.postMessage = (...args) => {
      const candidate = args[0];
      let message = candidate;
      if (typeof candidate === "string") {
        try { message = JSON.parse(candidate); } catch { message = null; }
      }
      if (message?.requestId && message?.type) {
        const requestPayload = message.payload;
        const payloadShape = requestPayload
          && typeof requestPayload === "object"
          && !Array.isArray(requestPayload)
          ? Object.fromEntries(Object.entries(requestPayload).map(([key, value]) => [
            key,
            typeof value === "string"
              ? { kind: "string", length: value.length }
              : Array.isArray(value)
                ? { kind: "array", length: value.length }
                : { kind: value === null ? "null" : typeof value },
          ]))
          : null;
        const request = {
          requestId: message.requestId,
          requestType: message.type,
          payloadShape,
          startedAt: new Date().toISOString(),
          startedMonotonicMs: performance.now(),
        };
        diagnostics.requests.push(request);
        diagnostics.pending[message.requestId] = request;
      }
      return originalPostMessage(...args);
    };
    webview.addEventListener("message", (event) => {
      let message = event.data;
      if (typeof message === "string") {
        try { message = JSON.parse(message); } catch { return; }
      }
      const request = message?.requestId
        ? diagnostics.pending[message.requestId]
        : null;
      if (!request) return;
      const roundTrip = {
        requestId: request.requestId,
        requestType: request.requestType,
        payloadShape: request.payloadShape,
        responseType: message.type ?? null,
        code: message.payload?.code ?? null,
        message: message.payload?.message ?? null,
        startedAt: request.startedAt,
        finishedAt: new Date().toISOString(),
        durationMs: Math.round((performance.now() - request.startedMonotonicMs) * 100) / 100,
      };
      diagnostics.roundTrips.push(roundTrip);
      if (
        message.type === "operation.failed"
        && !diagnostics.expectedFailures[message.requestId]
      ) {
        diagnostics.failures.push(roundTrip);
      }
      delete diagnostics.expectedFailures[message.requestId];
      delete diagnostics.pending[message.requestId];
    });
    window.__vibetableE2EBridgeDiagnostics = diagnostics;
  });
}

async function readBridgeDiagnostics(page) {
  return page.evaluate(() => {
    const diagnostics = window.__vibetableE2EBridgeDiagnostics;
    if (!diagnostics) return null;
    const now = performance.now();
    return {
      installedAt: diagnostics.installedAt,
      requests: diagnostics.requests.map((request) => ({
        requestId: request.requestId,
        requestType: request.requestType,
        payloadShape: request.payloadShape,
        startedAt: request.startedAt,
      })),
      roundTrips: diagnostics.roundTrips,
      failures: diagnostics.failures,
      pending: Object.values(diagnostics.pending).map((request) => ({
        requestId: request.requestId,
        requestType: request.requestType,
        payloadShape: request.payloadShape,
        startedAt: request.startedAt,
        pendingMs: Math.round((now - request.startedMonotonicMs) * 100) / 100,
      })),
    };
  });
}

async function beginBridgeMessageCapture(page, responseTypes) {
  await page.evaluate((types) => {
    window.__vibetableE2EBridgeCapture = { types, message: null };
    window.chrome.webview.addEventListener("message", function handler(event) {
      let message = event.data;
      if (typeof message === "string") {
        try { message = JSON.parse(message); } catch { return; }
      }
      if (!message || !types.includes(message.type)) return;
      window.__vibetableE2EBridgeCapture.message = message;
      window.chrome.webview.removeEventListener("message", handler);
    });
  }, responseTypes);
}

async function waitForCapturedBridgeMessage(page, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const message = await page.evaluate(
      () => window.__vibetableE2EBridgeCapture?.message ?? null,
    );
    if (message) return message;
    await page.waitForTimeout(50);
  }
  throw new Error("captured bridge response timed out");
}

async function beginRawBridgeRequest(
  page,
  type,
  payload,
  expectedResponseTypes = [type],
) {
  return page.evaluate(
    ({ requestType, requestPayload, responseTypes }) => {
      const requestId = `e2e-${crypto.randomUUID()}`;
      window.__vibetableE2ERawRequests ??= {};
      window.__vibetableE2ERawRequests[requestId] = {
        responseTypes,
        message: null,
      };
      window.chrome.webview.addEventListener("message", function handler(event) {
        let message = event.data;
        if (typeof message === "string") {
          try { message = JSON.parse(message); } catch { return; }
        }
        if (!message || message.requestId !== requestId) return;
        if (!responseTypes.includes(message.type) && message.type !== "operation.failed") return;
        window.__vibetableE2ERawRequests[requestId].message = message;
        window.chrome.webview.removeEventListener("message", handler);
      });
      window.chrome.webview.postMessage({
        type: requestType,
        requestId,
        payload: requestPayload,
      });
      return requestId;
    },
    {
      requestType: type,
      requestPayload: payload,
      responseTypes: expectedResponseTypes,
    },
  );
}

async function waitForRawBridgeRequest(page, requestId, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const message = await page.evaluate(
      (id) => window.__vibetableE2ERawRequests?.[id]?.message ?? null,
      requestId,
    );
    if (message) return message;
    await page.waitForTimeout(25);
  }
  throw new Error(`bridge response timeout for ${requestId}`);
}

async function waitForQueryPage(page, payload, predicate, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let response = null;
  while (Date.now() < deadline) {
    response = await rawBridgeRequest(page, "query.page", payload);
    if (response.type === "query.page" && predicate(response.payload)) return response;
    await page.waitForTimeout(50);
  }
  throw new Error(`query.page did not reach the expected state: ${JSON.stringify(response)}`);
}

function sha256(buffer) {
  return createHash("sha256").update(buffer).digest("hex");
}

function canonicalJson(value) {
  if (Array.isArray(value)) return value.map(canonicalJson);
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, canonicalJson(value[key])]),
    );
  }
  return value;
}

function canonicalJsonText(value) {
  return JSON.stringify(canonicalJson(value));
}

function canonicalJsonSet(values) {
  return values.map(canonicalJsonText).sort();
}

function parseCsv(text) {
  const rows = [];
  let row = [];
  let field = "";
  let quoted = false;
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index];
    if (quoted) {
      if (character === '"' && text[index + 1] === '"') {
        field += '"';
        index += 1;
      } else if (character === '"') {
        quoted = false;
      } else {
        field += character;
      }
      continue;
    }
    if (character === '"') {
      quoted = true;
    } else if (character === ",") {
      row.push(field);
      field = "";
    } else if (character === "\n") {
      row.push(field.replace(/\r$/u, ""));
      rows.push(row);
      row = [];
      field = "";
    } else {
      field += character;
    }
  }
  if (quoted) throw new Error("export CSV ended inside a quoted field");
  if (field.length > 0 || row.length > 0) {
    row.push(field.replace(/\r$/u, ""));
    rows.push(row);
  }
  return rows;
}

async function setProductLocale(page, locale) {
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-general").click();
  const language = page.getByTestId("language-select");
  await language.waitFor({ state: "visible" });
  await language.click();
  const label = locale === "en-US" ? "English" : "简体中文";
  await page.locator(".n-base-select-option:visible").filter({ hasText: label }).click();
  await page.locator("html").waitFor({ state: "attached" });
  await page.waitForFunction(
    (expected) => document.documentElement.lang === expected,
    locale,
  );
  await page.getByTestId("nav-tables").click();
}

async function listFilesRecursively(root) {
  const discovered = [];
  let entries;
  try {
    entries = await fs.readdir(root, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return discovered;
    throw error;
  }
  for (const entry of entries) {
    const candidate = path.join(root, entry.name);
    if (entry.isDirectory()) discovered.push(...await listFilesRecursively(candidate));
    else if (entry.isFile()) discovered.push(candidate);
  }
  return discovered;
}

async function waitForPreviewArtifact(runtime, expectedHash, expectedSize, timeoutMs = 30_000) {
  const previewRoot = path.join(
    runtime.evidenceDir,
    "runtime",
    "local-data",
    "attachment-preview",
  );
  const deadline = Date.now() + timeoutMs;
  let observed = [];
  while (Date.now() < deadline) {
    observed = [];
    for (const candidate of await listFilesRecursively(previewRoot)) {
      const bytes = await fs.readFile(candidate);
      const evidence = {
        path: path.relative(runtime.evidenceDir, candidate),
        size: bytes.length,
        sha256: sha256(bytes),
      };
      observed.push(evidence);
      if (evidence.sha256 === expectedHash && evidence.size === expectedSize) {
        return { ...evidence, absolutePath: candidate, bytes };
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`attachment preview artifact was not materialized: ${JSON.stringify(observed)}`);
}

async function requestStorageProof(runtime, tableId, timeoutMs = 30_000) {
  const requestPath = path.join(runtime.evidenceDir, "storage-proof-request.json");
  const resultPath = path.join(runtime.evidenceDir, "storage-proof-result.json");
  await fs.writeFile(
    requestPath,
    `${JSON.stringify({ tableId, requestedAt: new Date().toISOString() }, null, 2)}\n`,
    "utf8",
  );
  const deadline = Date.now() + timeoutMs;
  let result = null;
  while (Date.now() < deadline) {
    try {
      result = JSON.parse(await fs.readFile(resultPath, "utf8"));
      if (result.status === "completed") return result;
      if (result.status === "failed") {
        throw new Error(`storage proof failed: ${JSON.stringify(result)}`);
      }
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`storage proof timed out: ${JSON.stringify(result)}`);
}

async function waitForAttachmentList(page, params, predicate, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let lastResponse;
  while (Date.now() < deadline) {
    lastResponse = await rawBridgeRequest(page, "file.list", params);
    if (
      lastResponse.type === "file.list"
      && Array.isArray(lastResponse.payload?.attachments)
      && predicate(lastResponse.payload.attachments)
    ) {
      return lastResponse;
    }
    await page.waitForTimeout(100);
  }
  throw new Error(`attachment list did not reach the expected state: ${JSON.stringify(lastResponse)}`);
}

function invalidFormulaDefinition() {
  return {
    contractVersion: "1.0",
    tableId: "tbl_e2e_invalid_formula",
    physicalName: "e2e_invalid_formula",
    displayName: "E2E Invalid Formula",
    kind: "base",
    schemaRevision: "schema_0000",
    archivePolicy: { mode: "none", fieldId: null, archivedValue: null },
    fields: [{
      fieldId: "fld_total",
      physicalName: "total",
      displayName: "total",
      kind: "formula",
      dataType: "formula",
      storageType: "number",
      nullable: true,
      defaultValue: null,
      constraints: [],
      editor: { kind: "formula", config: {} },
      readOnly: true,
      formula: {
        language: "cel-v1",
        source: "",
        resultType: "float",
        version: 1,
        status: "ready",
      },
      relation: null,
      lookup: null,
      attachmentPolicy: null,
    }],
    indexes: [],
  };
}

async function scenario03(page, recorder) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Typed Field Error");
  await fillNInput(page, "field-display-name", "");
  recorder.check(
    "unified drawer blocks an unnamed field before planning",
    await page.getByTestId("field-plan-button").isDisabled(),
  );
  await closeFieldSettingsDrawer(page);

  const described = await rawBridgeRequest(page, "field.settings.describe", { tableId });
  const capability = described.payload.capabilities.find(
    (item) => item.logicalType === "file" && item.userCreatable,
  );
  const recommended = cloneJson(capability.recommended);
  const invalidDraft = {
    displayName: "Broken files",
    help: "",
    logicalType: "file",
    value: recommended.value,
    constraints: recommended.constraints,
    storage: recommended.storage,
    display: recommended.display,
    file: { ...recommended.file, maxFiles: 0 },
  };
  const v2Response = await rawBridgeRequest(page, "field.change.plan", {
    action: "create",
    tableId,
    fieldId: described.payload.fieldId,
    expectedSchemaRevision: described.payload.schemaRevision,
    expectedDataRevision: described.payload.dataRevision,
    draft: invalidDraft,
    actor: { id: "product-e2e", kind: "user" },
    conversionRule: "",
    confirmation: "",
    backupReceipt: "",
  }, 20_000);
  recorder.check(
    "server rejects invalid v2 field intent with a typed stable diagnostic",
    v2Response.type === "field.change.plan"
      && v2Response.payload?.error?.code === "field.contract.invalid"
      && v2Response.payload?.error?.path === "file",
    { response: v2Response },
  );
  const legacy = await rawBridgeRequest(
    page,
    "schema.validate",
    { definition: invalidFormulaDefinition(), expectedRevision: 0 },
    20_000,
    ["operation.failed"],
  );
  recorder.check(
    "retired generic schema validation route is absent from the renderer boundary",
    legacy.type === "operation.failed",
    { legacy },
  );

  const defaultsTable = await createSimpleTable(
    page,
    "E2E Defaults Future Inserts",
    "Marker",
  );
  const oldInsert = await applyProductMutation(page, defaultsTable.tableId, [{
    kind: "insert",
    recordId: null,
    values: { [defaultsTable.field.physicalName]: "old" },
  }], "e2e-default-old");
  const futureDefault = await createV2Field(
    page,
    defaultsTable.tableId,
    "Future default",
    "text",
    (draft) => {
      draft.value.default = {
        enabled: true,
        value: "future-only",
        source: "user",
        defaultsVersion: 1,
      };
      return draft;
    },
  );
  const newInsert = await applyProductMutation(page, defaultsTable.tableId, [{
    kind: "insert",
    recordId: null,
    values: { [defaultsTable.field.physicalName]: "new" },
  }], "e2e-default-new");
  const defaultRows = await rawBridgeRequest(page, "query.page", {
    tableId: defaultsTable.tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const oldRow = defaultRows.payload?.rows?.find(
    (row) => row[defaultsTable.field.physicalName] === "old",
  );
  const newRow = defaultRows.payload?.rows?.find(
    (row) => row[defaultsTable.field.physicalName] === "new",
  );
  recorder.check(
    "enabled defaults affect only future inserts and never backfill existing rows",
    oldInsert.payload?.status === "applied"
      && newInsert.payload?.status === "applied"
      && oldRow?.[futureDefault.physicalName] == null
      && newRow?.[futureDefault.physicalName] === "future-only",
    { oldRow, newRow, futureDefault },
  );

  const constrainedTableId = await createEmptyTable(page, "E2E Required Unique");
  const constrained = await createV2Field(
    page,
    constrainedTableId,
    "External key",
    "text",
    (draft) => {
      draft.value.required = true;
      draft.constraints.unique.enabled = true;
      return draft;
    },
  );
  await closeFieldSettingsDrawer(page);
  const uniqueInsert = await applyProductMutation(page, constrainedTableId, [{
    kind: "insert",
    recordId: null,
    values: { [constrained.physicalName]: "only-once" },
  }], "e2e-unique-first");
  const duplicateInsert = await applyProductMutation(page, constrainedTableId, [{
    kind: "insert",
    recordId: null,
    values: { [constrained.physicalName]: "only-once" },
  }], "e2e-unique-duplicate", true);
  const missingRequired = await applyProductMutation(page, constrainedTableId, [{
    kind: "insert",
    recordId: null,
    values: {},
  }], "e2e-required-missing", true);
  const constrainedRows = await rawBridgeRequest(page, "query.page", {
    tableId: constrainedTableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  recorder.check(
    "required and unique constraints reject missing and duplicate writes atomically",
    uniqueInsert.payload?.status === "applied"
      && duplicateInsert.payload?.error?.code === "mutation.validation.failed"
      && missingRequired.payload?.error?.code === "mutation.field.invalid_value"
      && constrainedRows.payload?.rows?.length === 1,
    { uniqueInsert, duplicateInsert, missingRequired, constrainedRows },
  );

  const presenceTable = await createSimpleTable(page, "E2E Presence Matrix", "Marker");
  const presenceFields = {};
  for (const [name, type] of [
    ["Empty text", "text"],
    ["False boolean", "bool"],
    ["Zero number", "number"],
    ["Origin", "geoPoint"],
    ["Null JSON", "json"],
    ["Empty tags", "multiSelect"],
  ]) {
    presenceFields[type] = await createV2Field(
      page,
      presenceTable.tableId,
      name,
      type,
    );
  }
  const matrixInsert = await applyProductMutation(page, presenceTable.tableId, [{
    kind: "insert",
    recordId: null,
    values: {
      [presenceTable.field.physicalName]: "explicit",
      [presenceFields.text.physicalName]: "",
      [presenceFields.bool.physicalName]: false,
      [presenceFields.number.physicalName]: 0,
      [presenceFields.geoPoint.physicalName]: { lat: 0, lon: 0 },
      [presenceFields.json.physicalName]: null,
      [presenceFields.multiSelect.physicalName]: [],
    },
  }], "e2e-presence-matrix");
  const matrixRows = await rawBridgeRequest(page, "query.page", {
    tableId: presenceTable.tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const matrix = matrixRows.payload?.rows?.find(
    (row) => row[presenceTable.field.physicalName] === "explicit",
  );
  recorder.check(
    "mutation and query preserve explicit blank, false, zero, origin, JSON null, and empty collection",
    matrixInsert.payload?.status === "applied"
      && Object.hasOwn(matrix ?? {}, presenceFields.text.physicalName)
      && matrix[presenceFields.text.physicalName] === ""
      && matrix[presenceFields.bool.physicalName] === false
      && matrix[presenceFields.number.physicalName] === 0
      && matrix[presenceFields.geoPoint.physicalName]?.lat === 0
      && matrix[presenceFields.geoPoint.physicalName]?.lon === 0
      && Object.hasOwn(matrix, presenceFields.json.physicalName)
      && matrix[presenceFields.json.physicalName] === null
      && Array.isArray(matrix[presenceFields.multiSelect.physicalName])
      && matrix[presenceFields.multiSelect.physicalName].length === 0,
    { matrixInsert, matrix },
  );
  return;
}

async function scenario04(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const jsonTable = await createSingleFieldTable(
    page,
    "E2E JSON Round Trip",
    "Payload",
    "json",
  );
  const tableId = jsonTable.tableId;
  const jsonField = jsonTable.field.physicalName;
  await selectTable(page, "E2E JSON Round Trip");
  await insertRowFromToolbar(page);
  let jsonCell = page.locator(`.tabulator-cell[tabulator-field="${jsonField}"]`).first();
  await jsonCell.waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    (field) => document.querySelector(
      `.tabulator-cell[tabulator-field="${field}"]`,
    )?.getAttribute("aria-haspopup") === "dialog",
    jsonField,
    { timeout: 30_000 },
  );

  const keyboardContract = await jsonCell.evaluate((element) => ({
    focusableByKeyboard: element.tabIndex === 0,
    tabIndex: element.tabIndex,
    ariaLabel: element.getAttribute("aria-label"),
    shortcuts: element.getAttribute("aria-keyshortcuts"),
    hasPopup: element.getAttribute("aria-haspopup"),
  }));
  await jsonCell.press("Enter");
  await page.getByTestId("json-editor-modal").waitFor();
  recorder.check(
    "structured JSON cell is keyboard-focusable and Enter opens its trapped modal",
    keyboardContract.focusableByKeyboard
      && keyboardContract.hasPopup === "dialog"
      && keyboardContract.shortcuts === "Enter Space Shift+F10"
      && typeof keyboardContract.ariaLabel === "string"
      && keyboardContract.ariaLabel.length > 0,
    { keyboardContract },
  );
  await page.keyboard.press("Escape");
  await page.getByTestId("json-editor-modal").waitFor({ state: "hidden" });
  await page.waitForFunction(
    (element) => document.activeElement === element,
    await jsonCell.elementHandle(),
    { timeout: 1_000 },
  ).catch(() => null);
  const focusRestoration = await jsonCell.evaluate((element) => ({
    documentHasFocus: document.hasFocus(),
    restored: document.activeElement === element,
    activeTag: document.activeElement?.tagName ?? null,
  }));
  recorder.check(
    "Escape closes the structured modal and restores focus when the renderer owns OS focus",
    !focusRestoration.documentHasFocus || focusRestoration.restored,
    { focusRestoration },
  );
  await jsonCell.press("Shift+F10");
  const keyboardContextMenu = page.locator(".n-dropdown-menu:visible").last();
  await keyboardContextMenu.waitFor();
  recorder.check(
    "Shift+F10 opens the structured cell context menu without pointer input",
    await keyboardContextMenu.isVisible(),
  );
  await page.keyboard.press("Escape");

  await jsonCell.click();
  await jsonCell.press("Enter");
  await page.getByTestId("json-editor-modal").waitFor();
  const expectedEditorValue = {
    nested: { value: 7 },
    items: [1, 2, 3],
    enabled: true,
  };
  await page.getByTestId("json-editor-input").fill(JSON.stringify(expectedEditorValue));
  await page.getByTestId("json-editor-save").click();
  await page.getByTestId("json-editor-modal").waitFor({ state: "hidden", timeout: 30_000 });
  const editorQuery = await waitForQueryPage(page, {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  }, (payload) => payload?.rows?.[0]?.[jsonField]?.nested?.value === 7);
  const editorValue = editorQuery.payload?.rows?.[0]?.[jsonField];
  recorder.check("structured JSON editor committed a typed object through the UI",
    editorQuery.type === "query.page"
      && canonicalJsonText(editorValue) === canonicalJsonText(expectedEditorValue),
  {
    editorValue,
    expectedEditorValue,
    normalizedEditorValue: canonicalJsonText(editorValue),
  });

  await page.context().grantPermissions(
    ["clipboard-read", "clipboard-write"],
    { origin: "https://app.vibetable.local" },
  );
  await page.evaluate(async (value) => navigator.clipboard.writeText(value),
    '{"nested":{"value":8},"items":[4,5],"enabled":false}');
  jsonCell = page.locator(`.tabulator-cell[tabulator-field="${jsonField}"]`).first();
  await jsonCell.click();
  await page.keyboard.press("Control+V");
  await page.getByTestId("paste-panel").waitFor({ timeout: 30_000 });
  const ack = page.getByTestId("paste-ack");
  if (await ack.isVisible().catch(() => false)) await ack.click();
  await page.getByTestId("paste-confirm").click();
  const pasteSummary = page.getByTestId("paste-summary").filter({ hasText: /1/ });
  await pasteSummary.waitFor({ timeout: 30_000 });
  const pasteSummaryText = await pasteSummary.innerText();
  await page.getByTestId("paste-close").click();
  await page.getByTestId("paste-panel").waitFor({ state: "hidden", timeout: 30_000 });
  recorder.check("JSON paste completed through preview and confirmation",
    /1/u.test(pasteSummaryText), { pasteSummaryText });

  const expectedImportedValue = {
    nested: { value: 9, label: "import" },
    items: [1, { code: "A" }],
    enabled: true,
  };
  const importedJson = JSON.stringify(expectedImportedValue).replaceAll('"', '""');
  await fs.writeFile(
    path.join(runtime.controlsDir, "import-source.csv"),
    `${jsonField}\n"${importedJson}"\n`,
    "utf8",
  );
  let importConfirmed;
  const importDialog = new Promise((resolve) => { importConfirmed = resolve; });
  page.once("dialog", async (dialog) => {
    await dialog.accept();
    importConfirmed(dialog.message());
  });
  await chooseToolbarMore(page, "import");
  await Promise.race([
    importDialog,
    new Promise((_, reject) => setTimeout(
      () => reject(new Error("JSON import confirmation did not appear")),
      60_000,
    )),
  ]);
  const importOutcome = await waitForImportSuccess(page, 1);
  recorder.check("JSON import completed with one explicitly reported committed row",
    importOutcome === "Imported 1 row(s)." || importOutcome === "已导入 1 行。",
    { importOutcome });
  await waitForVisibleRowCount(page, 2, 60_000);

  const headerFilter = page.locator(
    `.tabulator-col[tabulator-field="${jsonField}"] .tabulator-header-filter input`,
  );
  await headerFilter.click();
  await headerFilter.pressSequentially("8");
  await waitForVisibleRowCount(page, 1);
  recorder.check("JSON filter narrowed two rows to the unique matching object",
    await page.locator(".tabulator-row:visible").count() === 1);
  await headerFilter.press("Control+A");
  await headerFilter.press("Backspace");
  await waitForVisibleRowCount(page, 2);
  recorder.check("clearing the JSON filter restored both rows",
    await page.locator(".tabulator-row:visible").count() === 2);

  const expectedPastedValue = {
    nested: { value: 8 },
    items: [4, 5],
    enabled: false,
  };
  const expectedFinalValues = canonicalJsonSet([
    expectedPastedValue,
    expectedImportedValue,
  ]);
  const authoritative = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const authoritativeValues = (authoritative.payload?.rows ?? []).map((row) => row[jsonField]);
  recorder.check(
    "editor, paste, and native-picker import produced the exact normalized JSON values",
    authoritative.type === "query.page"
      && JSON.stringify(canonicalJsonSet(authoritativeValues))
        === JSON.stringify(expectedFinalValues),
    {
      expectedFinalValues,
      authoritativeValues,
      normalizedAuthoritativeValues: canonicalJsonSet(authoritativeValues),
    },
  );

  await setProductLocale(page, "en-US");
  jsonCell = page.locator(`.tabulator-cell[tabulator-field="${jsonField}"]`).first();
  const englishFilter = page.locator(
    `.tabulator-col[tabulator-field="${jsonField}"] .tabulator-header-filter input`,
  );
  await englishFilter.waitFor();
  await page.evaluate((field) => {
    window.__vibetableE2EJsonField = field;
  }, jsonField);
  await page.waitForFunction(
    () => document.querySelector(
      `.tabulator-col[tabulator-field="${window.__vibetableE2EJsonField}"] .tabulator-header-filter input`,
    )?.getAttribute("placeholder") === "Filter…",
  );
  const englishGridLabels = {
    placeholder: await englishFilter.getAttribute("placeholder"),
    ariaLabel: await jsonCell.getAttribute("aria-label"),
  };
  await setProductLocale(page, "zh-CN");
  jsonCell = page.locator(`.tabulator-cell[tabulator-field="${jsonField}"]`).first();
  const chineseFilter = page.locator(
    `.tabulator-col[tabulator-field="${jsonField}"] .tabulator-header-filter input`,
  );
  await page.waitForFunction(
    () => document.querySelector(
      `.tabulator-col[tabulator-field="${window.__vibetableE2EJsonField}"] .tabulator-header-filter input`,
    )?.getAttribute("placeholder") === "筛选…",
  );
  const chineseGridLabels = {
    placeholder: await chineseFilter.getAttribute("placeholder"),
    ariaLabel: await jsonCell.getAttribute("aria-label"),
  };
  recorder.check(
    "an open table rebuilds filter and structured-cell labels immediately in both languages",
    englishGridLabels.placeholder === "Filter…"
      && englishGridLabels.ariaLabel?.startsWith("Edit the structured value")
      && chineseGridLabels.placeholder === "筛选…"
      && chineseGridLabels.ariaLabel?.startsWith("编辑“"),
    { englishGridLabels, chineseGridLabels },
  );

  await chooseToolbarMore(page, "export");
  const exportTarget = path.join(runtime.controlsDir, "export-result.csv");
  const deadline = Date.now() + 60_000;
  let exported = "";
  while (Date.now() < deadline) {
    try {
      exported = await fs.readFile(exportTarget, "utf8");
      if (exported.includes("nested")) break;
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  const exportedRows = parseCsv(exported);
  const payloadIndex = exportedRows[0]?.indexOf(jsonField) ?? -1;
  const exportedValues = payloadIndex < 0
    ? []
    : exportedRows.slice(1)
      .filter((row) => row.some((field) => field.length > 0))
      .map((row) => JSON.parse(row[payloadIndex]));
  recorder.check(
    "native-picker export deep-matches authoritative normalized JSON",
    JSON.stringify(canonicalJsonSet(exportedValues))
      === JSON.stringify(expectedFinalValues),
    {
      expectedFinalValues,
      exportedValues,
      normalizedExportedValues: canonicalJsonSet(exportedValues),
      exported,
    },
  );

  const queried = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const values = (queried.payload?.rows ?? []).map((row) => row[jsonField]);
  recorder.check("authoritative query returned JSON values as objects, not strings",
    values.length === 2
      && values.every((value) =>
        value && typeof value === "object" && Array.isArray(value.items))
      && JSON.stringify(canonicalJsonSet(values)) === JSON.stringify(expectedFinalValues),
  { values, expectedFinalValues });
}

async function scenario05(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const conversionTable = await createSingleFieldTable(
    page,
    "E2E Empty Conversion",
    "Amount",
    "integer",
  );
  const described = await rawBridgeRequest(page, "field.settings.describe", {
    tableId: conversionTable.tableId,
    fieldId: conversionTable.field.fieldId,
  });
  const textCapability = described.payload.capabilities.find(
    (item) => item.logicalType === "text" && item.userCreatable,
  );
  const recommended = cloneJson(textCapability.recommended);
  const textDraft = {
    displayName: "Amount as text",
    help: "",
    logicalType: "text",
    value: recommended.value,
    constraints: recommended.constraints,
    storage: recommended.storage,
    display: recommended.display,
  };
  const intent = {
    action: "convert",
    tableId: conversionTable.tableId,
    fieldId: conversionTable.field.fieldId,
    expectedSchemaRevision: described.payload.schemaRevision,
    expectedDataRevision: described.payload.dataRevision,
    draft: textDraft,
    actor: { id: "product-e2e", kind: "user" },
    conversionRule: "block",
    confirmation: "",
    backupReceipt: "",
  };
  const planned = await rawBridgeRequest(page, "field.change.plan", intent);
  const applied = await rawBridgeRequest(page, "field.change.apply", {
    planId: planned.payload.planId,
    planHash: planned.payload.planHash,
    operationId: `op_e2e_convert_${Date.now()}`,
    actor: { id: "product-e2e", kind: "user" },
    confirmations: [...(planned.payload.confirmations ?? [])],
  });
  const after = await rawBridgeRequest(page, "field.settings.describe", {
    tableId: conversionTable.tableId,
    fieldId: conversionTable.field.fieldId,
  });
  recorder.check(
    "empty-table type conversion completes directly while preserving public identity",
    applied.type === "field.change.apply"
      && !applied.payload?.migrationJobId
      && after.payload?.definition?.logicalType === "text"
      && after.payload?.definition?.identity?.fieldId === conversionTable.field.fieldId
      && after.payload?.definition?.identity?.physicalName === conversionTable.field.physicalName,
    { planned, applied, after },
  );

  const rollbackTable = await createSimpleTable(
    page,
    "E2E Migration Rollback",
    "Raw number",
  );
  const validSeed = await applyProductMutation(page, rollbackTable.tableId, [{
    kind: "insert",
    recordId: null,
    values: { [rollbackTable.field.physicalName]: "42" },
  }], "e2e-migration-valid-seed");
  const rollbackDescribe = await rawBridgeRequest(page, "field.settings.describe", {
    tableId: rollbackTable.tableId,
    fieldId: rollbackTable.field.fieldId,
  });
  const numberCapability = rollbackDescribe.payload.capabilities.find(
    (item) => item.logicalType === "number" && item.userCreatable,
  );
  const numberRecommended = cloneJson(numberCapability.recommended);
  const rollbackPlan = await rawBridgeRequest(page, "field.change.plan", {
    action: "convert",
    tableId: rollbackTable.tableId,
    fieldId: rollbackTable.field.fieldId,
    expectedSchemaRevision: rollbackDescribe.payload.schemaRevision,
    expectedDataRevision: rollbackDescribe.payload.dataRevision,
    draft: {
      displayName: "Parsed number",
      help: "",
      logicalType: "number",
      value: numberRecommended.value,
      constraints: numberRecommended.constraints,
      storage: numberRecommended.storage,
      display: numberRecommended.display,
    },
    actor: { id: "product-e2e", kind: "user" },
    conversionRule: "block",
    confirmation: "",
    backupReceipt: "",
  });
  await fs.writeFile(
    path.join(runtime.controlsDir, "migration-fault.phase"),
    "copying\n",
    "utf8",
  );
  const faultedApply = await rawBridgeRequest(page, "field.change.apply", {
    planId: rollbackPlan.payload.planId,
    planHash: rollbackPlan.payload.planHash,
    operationId: `op_e2e_rollback_${Date.now()}`,
    actor: { id: "product-e2e", kind: "user" },
    confirmations: [...(rollbackPlan.payload.confirmations ?? [])],
  });
  const rollbackStatus = await waitForFieldMigration(
    page,
    faultedApply.payload.migrationJobId,
  );
  const rollbackAfter = await rawBridgeRequest(page, "field.settings.describe", {
    tableId: rollbackTable.tableId,
    fieldId: rollbackTable.field.fieldId,
  });
  const rollbackRows = await rawBridgeRequest(page, "query.page", {
    tableId: rollbackTable.tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  recorder.check(
    "a fault during a real non-empty migration rolls back schema and data authority",
    validSeed.payload?.status === "applied"
      && rollbackPlan.payload?.createsMigration === true
      && rollbackPlan.payload?.canApply === true
      && faultedApply.payload?.migrationJobId
      && rollbackStatus.payload?.phase === "rolled_back"
      && rollbackAfter.payload?.definition?.logicalType === "text"
      && rollbackAfter.payload?.definition?.identity?.fieldId === rollbackTable.field.fieldId
      && rollbackRows.payload?.rows?.[0]?.[rollbackTable.field.physicalName] === "42",
    { rollbackPlan, faultedApply, rollbackStatus, rollbackAfter, rollbackRows },
  );
  return;
}

async function scenario06(page, recorder) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const authors = await createSimpleTable(page, "E2E Authors V2", "Name");
  const articleTableId = await createEmptyTable(page, "E2E Articles V2");
  await closeFieldSettingsDrawer(page);
  const relation = await createV2Field(
    page,
    articleTableId,
    "Author",
    "relation",
    (draft) => {
      draft.relation.targetTableId = authors.tableId;
      draft.relation.displayFieldId = authors.field.fieldId;
      return draft;
    },
  );
  const cascade = await applyV2FieldChange(
    page,
    articleTableId,
    relation.fieldId,
    "update",
    {
      mutateDraft: (draft) => {
        draft.relation.deletePolicy = "cascade";
        return draft;
      },
    },
  );
  recorder.check(
    "cascade relation plan exposes direction, impact, and danger classification before apply",
    cascade.planned?.type === "field.change.plan"
      && cascade.planned.payload?.classes?.includes("danger")
      && cascade.planned.payload?.confirmations?.includes("cascade")
      && cascade.planned.payload?.impact?.records >= 0
      && Array.isArray(cascade.planned.payload?.impact?.dependencies)
      && cascade.planned.payload?.warnings?.some(
        (warning) => warning.details?.direction === "targetToSource",
      )
      && cascade.planned.payload?.steps?.some(
        (step) => step.details?.direction === "targetToSource",
      ),
    { cascade: cascade.planned },
  );
  return;
}

async function selectTable(page, displayName) {
  const name = page.getByTestId("sidebar-table-name").filter({ hasText: displayName });
  await name.waitFor();
  await name.locator("xpath=ancestor::button").click();
  await page.getByTestId("table-summary").waitFor({ timeout: 30_000 });
}

async function createSingleFieldTable(page, displayName, fieldName, type) {
  const tableId = await createEmptyTable(page, displayName);
  const logicalType = type === "integer" ? "number" : type === "shortText" ? "text" : type;
  const field = await createV2Field(page, tableId, fieldName, logicalType, (draft) => {
    if (type === "integer") {
      draft.storage.options.onlyInt = true;
      draft.display.displayScale = 0;
    }
    return draft;
  });
  await closeFieldSettingsDrawer(page);
  return { tableId, field };
}

async function scenario07(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const attachmentTable = await createSingleFieldTable(
    page,
    "E2E Attachments",
    "Attachments",
    "file",
  );
  const tableId = attachmentTable.tableId;
  const attachmentField = attachmentTable.field.physicalName;
  await selectTable(page, "E2E Attachments");
  await insertRowFromToolbar(page);
  const cell = page.locator(`.tabulator-cell[tabulator-field="${attachmentField}"]`).first();
  await cell.waitFor({ timeout: 30_000 });
  const schema = await rawBridgeRequest(page, "schema.describe", {
    collection: tableId,
    requestGeneration: 7007,
    accepts: [
      "vibetable.relation-capabilities.v1",
      "vibetable.lookup-query.v1",
    ],
  });
  const attachmentColumn = schema.payload?.schema?.columns?.find(
    (column) => column.name === attachmentField,
  );
  const queried = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const recordId = queried.payload?.rows?.[0]?.id;
  if (
    schema.type !== "schema.describe"
    || attachmentColumn?.kind !== "attachment"
    || !attachmentColumn.fieldId
    || !recordId
  ) {
    throw new Error(`attachment schema or record identity was unavailable: ${JSON.stringify({
      schema,
      queried,
    })}`);
  }
  const attachmentParams = {
    tableId,
    recordId,
    fieldId: attachmentColumn.fieldId,
  };
  await cell.dblclick();
  const panel = page.getByTestId("attachment-panel");
  await panel.waitFor();

  const original = (await fs.readFile(
    path.join(runtime.controlsDir, "attachment-source.txt"),
    "utf8",
  )).trim();
  const replacement = (await fs.readFile(
    path.join(runtime.controlsDir, "attachment-replacement-source.txt"),
    "utf8",
  )).trim();
  await fs.copyFile(
    original,
    path.join(runtime.evidenceDir, "attachment-original.txt"),
  );
  await fs.copyFile(
    replacement,
    path.join(runtime.evidenceDir, "attachment-replacement.txt"),
  );
  const originalBytes = await fs.readFile(original);
  const replacementBytes = await fs.readFile(replacement);
  const expectedOriginalHash = sha256(originalBytes);
  const expectedReplacementHash = sha256(replacementBytes);
  await page.getByTestId("attachment-upload").click();
  await panel.waitFor({ state: "hidden", timeout: 30_000 });
  const uploaded = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1,
  );
  const uploadedFile = uploaded.payload.attachments[0];
  recorder.check("uploaded attachment metadata matches the native file bytes",
    uploadedFile.originalName === path.basename(original)
      && uploadedFile.sha256 === expectedOriginalHash
      && uploadedFile.size === originalBytes.length,
  {
    uploadedFile,
    expectedOriginalHash,
    expectedSize: originalBytes.length,
  });
  const restart = await requestSidecarKill(runtime, "verify attachment survives sidecar restart");
  recorder.check("attachment restart terminated only the exact sidecar child",
    restart.processName === "vibetable-pb.exe", { restart });
  await waitForTableRecovery(page, "E2E Attachments", 1);
  const recovered = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1,
    60_000,
  );
  const recoveredFile = recovered.payload.attachments[0];
  recorder.check("attachment name, hash, and content length survived sidecar restart",
    recoveredFile.originalName === uploadedFile.originalName
      && recoveredFile.storedName === uploadedFile.storedName
      && recoveredFile.sha256 === expectedOriginalHash
      && recoveredFile.size === originalBytes.length,
  {
    uploadedFile,
    recoveredFile,
    expectedOriginalHash,
    expectedSize: originalBytes.length,
  });
  await cell.dblclick();
  await panel.waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("attachment-preview-0").waitFor({ timeout: 30_000 });
  await beginBridgeMessageCapture(page, ["operation.failed"]);
  await page.getByTestId("attachment-preview-0").click();
  let previewArtifact;
  try {
    previewArtifact = await waitForPreviewArtifact(
      runtime,
      expectedOriginalHash,
      originalBytes.length,
    );
  } catch (error) {
    const previewFailure = await page.evaluate(
      () => window.__vibetableE2EBridgeCapture?.message ?? null,
    );
    throw new Error(
      `${error instanceof Error ? error.message : String(error)}; `
      + `bridgeFailure=${JSON.stringify(previewFailure)}`,
    );
  }
  const preservedPreviewPath = path.join(
    runtime.evidenceDir,
    "attachment-preview-verified.txt",
  );
  await fs.writeFile(preservedPreviewPath, previewArtifact.bytes);
  recorder.check(
    "native attachment preview materialized the exact managed bytes",
    previewArtifact.sha256 === expectedOriginalHash
      && previewArtifact.size === originalBytes.length
      && sha256(await fs.readFile(preservedPreviewPath)) === expectedOriginalHash,
    {
      previewArtifact: {
        path: previewArtifact.path,
        size: previewArtifact.size,
        sha256: previewArtifact.sha256,
      },
      preservedPreviewPath: path.basename(preservedPreviewPath),
      expectedOriginalHash,
      expectedSize: originalBytes.length,
    },
  );
  await page.getByTestId("attachment-replace-0").click();
  await panel.waitFor({ state: "hidden", timeout: 30_000 });
  const replaced = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1
      && attachments[0]?.sha256 === expectedReplacementHash,
  );
  recorder.check("attachment replacement committed the replacement bytes",
    replaced.payload.attachments[0]?.originalName === path.basename(replacement)
      && replaced.payload.attachments[0]?.sha256 === expectedReplacementHash
      && replaced.payload.attachments[0]?.size === replacementBytes.length,
  {
    replacement: replaced.payload.attachments[0],
    expectedReplacementHash,
    expectedSize: replacementBytes.length,
  });

  // Closing the modal restores its trigger, but Tabulator may retain the
  // neighbouring identity cell as the active range after realtime refresh.
  // Select the attachment field explicitly so history is scoped correctly.
  await cell.click();
  const attachmentHistoryProbe = await rawBridgeRequest(
    page,
    "history.queryRequested",
    {
      table: tableId,
      scope: "cell",
      itemId: recordId,
      field: attachmentField,
      limit: 50,
      offset: 0,
      actions: [],
    },
    20_000,
    ["history.pageLoaded"],
  );
  if (attachmentHistoryProbe.type !== "history.pageLoaded") {
    throw new Error(
      `attachment history query failed: ${JSON.stringify(attachmentHistoryProbe)}`,
    );
  }
  const originalRevision = attachmentHistoryProbe.payload.changeSets.find((changeSet) =>
    changeSet.scalarChanges?.some((change) =>
      change.field === attachmentField
        && String(change.after ?? "").includes(uploadedFile.storedName)),
  )?.rootRevisionId;
  if (!originalRevision) {
    throw new Error(
      `original attachment revision was not returned: ${JSON.stringify(attachmentHistoryProbe.payload)}`,
    );
  }
  const originalPreviewProbe = await rawBridgeRequest(
    page,
    "history.previewRestoreRequested",
    {
      collection: tableId,
      itemId: recordId,
      targetRevision: originalRevision,
      scope: "cell",
      field: attachmentField,
    },
    20_000,
    ["history.restorePreviewReady"],
  );
  recorder.check(
    "attachment restore preview identifies the original managed file",
    originalPreviewProbe.type === "history.restorePreviewReady"
      && originalPreviewProbe.payload?.canApply === true
      && originalPreviewProbe.payload?.restorableFields?.includes(attachmentField)
      && originalPreviewProbe.payload?.scalarChanges?.some((change) =>
        change.field === attachmentField
          && String(change.after ?? "").includes(uploadedFile.storedName)),
    { originalRevision, originalPreviewProbe },
  );
  const historyDrawerStartedAt = performance.now();
  await page.getByTestId("toolbar-history").click();
  await page.getByTestId("history-timeline").waitFor({ timeout: 30_000 });
  runtime.recordUiTiming(
    "history.drawer.initialLoad",
    performance.now() - historyDrawerStartedAt,
    { scope: "cell", scenario: "07-attachment-history" },
  );
  await page.getByTestId(`history-entry-${originalRevision}`).click();
  const restore = page.getByTestId(`history-preview-${originalRevision}`);
  await restore.waitFor();
  await restore.click();
  await page.getByTestId("restore-preview").waitFor();
  await beginBridgeMessageCapture(
    page,
    ["history.restoreApplied", "operation.failed"],
  );
  await page.getByTestId("restore-confirm").click();
  const appliedMessage = await waitForCapturedBridgeMessage(page);
  recorder.check(
    "attachment restore UI receives a committed restore revision",
    appliedMessage.type === "history.restoreApplied"
      && appliedMessage.payload?.restoredToRevision === originalRevision
      && typeof appliedMessage.payload?.newRevisionId === "string",
    { appliedMessage, originalRevision },
  );
  await page.getByTestId("restore-preview").waitFor({ state: "hidden", timeout: 30_000 });
  const restored = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1
      && attachments[0]?.sha256 === expectedOriginalHash,
  );
  recorder.check("attachment revision restored the original name, hash, and content length",
    restored.payload.attachments[0]?.originalName === path.basename(original)
      && restored.payload.attachments[0]?.sha256 === expectedOriginalHash
      && restored.payload.attachments[0]?.size === originalBytes.length,
  {
    restored: restored.payload.attachments[0],
    expectedOriginalHash,
    expectedSize: originalBytes.length,
  });
}

async function scenario08(page, recorder) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const staleTable = await createSimpleTable(page, "E2E Stale Conflict", "Value");
  const tableId = staleTable.tableId;
  const valueField = staleTable.field.physicalName;
  await selectTable(page, "E2E Stale Conflict");
  await insertRowFromToolbar(page);
  const cell = page.locator(`.tabulator-cell[tabulator-field="${valueField}"]`).first();
  await cell.waitFor({ timeout: 30_000 });

  const queried = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const pageResult = queried.payload;
  const row = pageResult?.rows?.[0];
  if (!row?.id || !row.__vibetableDigest || !pageResult?.snapshot?.schemaRevision) {
    throw new Error(`query.page omitted stale-edit guards: ${JSON.stringify(queried)}`);
  }
  // Commit the competing closed-bridge mutation first, then submit the exact
  // renderer-facing table mutation envelope based on the old page snapshot.
  // The UI behavior under test is the localized non-blocking conflict notice; keeping an
  // unrelated Tabulator editor open while injecting a raw bridge envelope
  // would test editor teardown rather than stale-write handling.
  const competitorRequestId = await beginRawBridgeRequest(page, "mutation.apply", {
    contractVersion: "1.0",
    requestId: "e2e-stale-competitor",
    idempotencyKey: "e2e-stale-competitor",
    tableId,
    schemaRevision: pageResult.snapshot.schemaRevision,
    operations: [{
      kind: "update",
      recordId: row.id,
      values: { [valueField]: "competitor-write" },
      expectedDigest: row.__vibetableDigest,
    }],
    actor: { type: "user", id: "e2e-competitor", displayName: "E2E competitor" },
    expectedRevision: null,
    expectedDigest: null,
  });
  const competitor = await waitForRawBridgeRequest(page, competitorRequestId);
  recorder.check("competing edit committed from the same closed product bridge",
    competitor.payload?.status === "applied", { competitor });
  // Let the competitor's realtime invalidation finish its authoritative table
  // refresh before starting the deliberately stale request. Otherwise the
  // workspace generation can change while UpdateCellAsync is in flight and
  // correctly discard the now-stale notification itself, making this conflict
  // assertion race the refresh rather than exercise the conflict boundary.
  await page.waitForFunction(
    ({ field, value }) => Array.from(
      document.querySelectorAll(`.tabulator-cell[tabulator-field="${field}"]`),
    ).some((node) => node.textContent?.includes(value)),
    { field: valueField, value: "competitor-write" },
    { timeout: 30_000 },
  );
  await beginBridgeMessageCapture(page, ["table.editRejected"]);
  await beginRawBridgeRequest(page, "table.updateCellRequested", {
    table: tableId,
    rowKey: row.id,
    column: valueField,
    oldValue: "",
    newValue: "stale-user-write",
    expectedDigest: row.__vibetableDigest,
    schemaRevision: pageResult.snapshot.schemaRevision,
  });
  const rejected = await waitForCapturedBridgeMessage(page);
  recorder.check(
    "stale renderer mutation was rejected by the product table boundary",
    rejected.type === "table.editRejected"
      && rejected.payload?.kind === "edit_conflict",
    { rejected },
  );
  const conflict = page.getByTestId("edit-rejection-notice");
  await conflict.waitFor({ timeout: 30_000 });
  const conflictText = await conflict.innerText();
  const localizedConflictVisible = conflictText.trim().length > 0
    && await page.getByTestId("edit-rejection-reload").isVisible()
    && !(await page.getByTestId("table-error-overlay").isVisible().catch(() => false));
  recorder.check("stale UI edit produced an explicit, user-visible conflict",
    localizedConflictVisible ||
    /changed|conflict|stale|digest|变更|冲突/i.test(conflictText), { conflictText });
}

async function requestSidecarKill(runtime, reason) {
  const requestPath = path.join(runtime.evidenceDir, "fault-request.json");
  const resultPath = path.join(runtime.evidenceDir, "fault-result.json");
  await fs.writeFile(requestPath, `${JSON.stringify({
    action: "kill-sidecar",
    reason,
    requestedAt: new Date().toISOString(),
  }, null, 2)}\n`, "utf8");
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    try {
      const result = JSON.parse(await fs.readFile(resultPath, "utf8"));
      if (result.status !== "completed") {
        throw new Error(`sidecar fault request failed: ${JSON.stringify(result)}`);
      }
      return result;
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("Python orchestrator did not acknowledge the sidecar fault request");
}

async function waitForMutationBarrier(runtime, timeoutMs = 60_000) {
  const readyPath = path.join(runtime.controlsDir, "mutation-barrier.ready.json");
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const ready = JSON.parse(await fs.readFile(readyPath, "utf8"));
      if (
        Number.isInteger(ready.pid)
        && ready.pid > 0
        && ready.point === "after_record"
      ) {
        return ready;
      }
      lastError = new Error(`invalid mutation barrier payload: ${JSON.stringify(ready)}`);
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`mutation barrier was not reached after the first transactional record write: ${lastError}`);
}

async function chooseToolbarMore(page, key) {
  const labels = {
    import: /导入数据|Import data/i,
    export: /导出数据|Export data/i,
  };
  if (!labels[key]) throw new Error(`missing toolbar menu label mapping: ${key}`);
  await page.getByTestId("toolbar-more").click();
  const option = page.locator(".n-dropdown-option-body")
    .filter({ hasText: labels[key] })
    .last();
  await option.waitFor();
  await option.click();
}

async function waitForTableRecovery(page, displayName, expectedRows, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const retry = page.getByTestId("connection-retry");
      if (await retry.isVisible().catch(() => false)) await retry.click();
      const name = page.getByTestId("sidebar-table-name").filter({ hasText: displayName });
      if (await name.isVisible().catch(() => false)) {
        await name.locator("xpath=ancestor::button").click();
      }
      await page.waitForTimeout(750);
      const count = await page.locator(".tabulator-row").count();
      if (count === expectedRows && await page.getByTestId("connection-pill").isVisible()) {
        return count;
      }
      lastError = new Error(`observed ${count} rows, expected ${expectedRows}`);
    } catch (error) {
      lastError = error;
    }
  }
  throw lastError ?? new Error("table did not recover");
}

async function waitForStableGridState(
  page,
  {
    expectedRows,
    matchingCell = null,
    expectedMatchingCells = null,
    timeoutMs = 30_000,
    stableForMs = 1_000,
  },
) {
  const deadline = Date.now() + timeoutMs;
  let stableSince = null;
  let lastEvidence = null;
  const recentSamples = [];
  while (Date.now() < deadline) {
    const observedAt = Date.now();
    const rowCount = await page.locator(".tabulator-row").count();
    const matchingCellCount = matchingCell === null
      ? null
      : await matchingCell.count();
    lastEvidence = {
      observedAt,
      rowCount,
      matchingCellCount,
    };
    recentSamples.push(lastEvidence);
    if (recentSamples.length > 20) recentSamples.shift();
    const reachedExpectedState = rowCount === expectedRows
      && (
        expectedMatchingCells === null
        || matchingCellCount === expectedMatchingCells
      );
    if (reachedExpectedState) {
      stableSince ??= observedAt;
      if (observedAt - stableSince >= stableForMs) {
        return {
          ...lastEvidence,
          stableForMs,
          recentSamples,
        };
      }
    } else {
      stableSince = null;
    }
    await page.waitForTimeout(100);
  }
  throw new Error(`grid did not reach a stable expected state: ${JSON.stringify({
    expectedRows,
    expectedMatchingCells,
    timeoutMs,
    stableForMs,
    lastEvidence,
    recentSamples,
  })}`);
}

async function waitForStableGridRowCount(page, expectedRows, timeoutMs = 30_000) {
  const evidence = await waitForStableGridState(page, {
    expectedRows,
    timeoutMs,
  });
  return evidence.rowCount;
}

async function waitForActiveTableBackend(page, tableId, expectedRows, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  let lastResponse;
  while (Date.now() < deadline) {
    try {
      lastResponse = await rawBridgeRequest(page, "query.page", {
        tableId,
        query: { filters: [], sorts: [], offset: 0, limit: 100 },
      });
      if (
        lastResponse.type === "query.page"
        && lastResponse.payload?.rows?.length === expectedRows
        && lastResponse.payload?.snapshot?.schemaRevision
      ) {
        return lastResponse.payload;
      }
    } catch {
      // The host is intentionally restarting. Keep the selected table intact
      // and retry only the closed read contract; never click/reselect the UI.
    }
    await page.waitForTimeout(250);
  }
  throw new Error(`active table backend did not recover: ${JSON.stringify(lastResponse)}`);
}

async function scenario09(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const importTable = await createSimpleTable(page, "E2E Atomic Import", "Value");
  const tableId = importTable.tableId;
  const valueField = importTable.field.physicalName;
  await selectTable(page, "E2E Atomic Import");

  const rows = [valueField];
  // The release criterion is a 1k-row atomic paste/import. MutationKernel
  // deliberately caps one transaction at 1000 operations, so exercise that
  // boundary instead of an unsupported request rejected before the barrier.
  for (let index = 0; index < 1_000; index += 1) rows.push(`row-${index}`);
  await fs.writeFile(
    path.join(runtime.controlsDir, "import-source.csv"),
    `${rows.join("\n")}\n`,
    "utf8",
  );
  let dialogAccepted;
  const accepted = new Promise((resolve) => { dialogAccepted = resolve; });
  page.once("dialog", async (dialog) => {
    await dialog.accept();
    dialogAccepted(dialog.message());
  });
  await chooseToolbarMore(page, "import");
  const confirmation = await Promise.race([
    accepted,
    new Promise((_, reject) => setTimeout(
      () => reject(new Error("import preview confirmation did not appear")),
      60_000,
    )),
  ]);
  const barrier = await waitForMutationBarrier(runtime);
  const fault = await requestSidecarKill(runtime, "interrupt active 1k-row import");
  recorder.check("the exact sidecar was killed after its first uncommitted transactional record write",
    fault.processName === "vibetable-pb.exe"
      && fault.pid === barrier.pid
      && barrier.point === "after_record",
  {
    fault,
    barrier,
    confirmation,
  });
  const rowCount = await waitForTableRecovery(page, "E2E Atomic Import", 0);
  recorder.check("failed import exposed no partially committed records in the UI", rowCount === 0);
  const historyDeadline = Date.now() + 30_000;
  let history;
  do {
    history = await rawBridgeRequest(page, "history.queryRequested", {
      collection: tableId,
      scope: "table",
      actions: [],
      limit: 100,
      offset: 0,
    }, 5_000, ["history.pageLoaded"]);
    if (history.type === "history.pageLoaded") break;
    await page.waitForTimeout(250);
  } while (Date.now() < historyDeadline);
  const historyPage = history.payload;
  recorder.check("failed import exposed no partially committed audit entries",
    history.type === "history.pageLoaded"
      && historyPage?.collection === tableId
      && historyPage?.scope === "table"
      && historyPage?.total === 0
      && Array.isArray(historyPage?.changeSets)
      && historyPage.changeSets.length === 0
      && typeof historyPage?.capabilityHash === "string"
      && historyPage.capabilityHash.length > 0
      && typeof historyPage?.schemaRevision === "string"
      && historyPage.schemaRevision.length > 0,
  { history });
  const storageProof = await requestStorageProof(
    runtime,
    tableId,
  );
  recorder.check(
    "read-only database proof found zero records, audit, idempotency, and outbox rows",
    storageProof.database?.readOnly === true
      && storageProof.counts?.records === 0
      && storageProof.counts?.audit === 0
      && storageProof.counts?.idempotency === 0
      && storageProof.counts?.outbox === 0,
    { storageProof },
  );
}

async function scenario10(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const realtimeTable = await createSimpleTable(page, "E2E Realtime Recovery", "Value");
  const tableId = realtimeTable.tableId;
  const valueField = realtimeTable.field.physicalName;
  await selectTable(page, "E2E Realtime Recovery");
  await insertRowFromToolbar(page);
  await page.locator(".tabulator-row").first().waitFor({ timeout: 30_000 });
  const baselineRowCount = await waitForStableGridRowCount(page, 1);
  recorder.check(
    "baseline row is visible exactly once",
    baselineRowCount === 1,
    { baselineRowCount },
  );

  const fault = await requestSidecarKill(runtime, "exercise realtime disconnect and catch-up");
  recorder.check("only the exact child PocketBase process was terminated", fault.processName === "vibetable-pb.exe", {
    fault,
  });
  const recovered = await waitForActiveTableBackend(
    page,
    tableId,
    1,
  );
  const activeSidebarTable = page
    .getByTestId("sidebar-table-name")
    .filter({ hasText: "E2E Realtime Recovery" })
    .locator("xpath=ancestor::button");
  recorder.check(
    "the original table selection stayed active through sidecar restart",
    await activeSidebarTable.getAttribute("aria-current") === "page"
      && (await page.getByTestId("toolbar-table-title").innerText())
        .includes("E2E Realtime Recovery"),
  );
  const mutation = await rawBridgeRequest(page, "mutation.apply", {
    contractVersion: "1.0",
    requestId: "e2e-realtime-after-restart",
    idempotencyKey: "e2e-realtime-after-restart",
    tableId,
    schemaRevision: recovered.snapshot.schemaRevision,
    operations: [{
      kind: "insert",
      recordId: null,
      values: { [valueField]: "after-reconnect" },
    }],
    actor: { type: "user", id: "e2e-realtime", displayName: "E2E realtime" },
    expectedRevision: null,
    expectedDigest: null,
  });
  recorder.check(
    "post-restart mutation committed through the closed product bridge",
    mutation.type === "mutation.apply" && mutation.payload?.status === "applied",
    { mutation },
  );
  const newCell = page.locator(`.tabulator-cell[tabulator-field="${valueField}"]`)
    .filter({ hasText: "after-reconnect" });
  await newCell.waitFor({ timeout: 30_000 });
  const stableGrid = await waitForStableGridState(page, {
    expectedRows: 2,
    matchingCell: newCell,
    expectedMatchingCells: 1,
    stableForMs: 1_500,
  });
  recorder.check(
    "reconnected SSE refreshed the still-active grid exactly once without table re-selection",
    stableGrid.rowCount === 2 && stableGrid.matchingCellCount === 1,
    { stableGrid },
  );
}

async function scenario11(page, recorder) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  await createSimpleTable(page, "E2E Plugin Target", "value");
  await selectTable(page, "E2E Plugin Target");
  await page.getByTestId("nav-plugins").click();
  await page.getByTestId("plugin-install-folder").click();
  const installPlan = page.getByTestId("plugin-install-plan");
  await installPlan.waitFor({ timeout: 30_000 });
  recorder.check("plugin permission and install plan was shown before approval",
    (await installPlan.innerText()).includes("E2E"), {
      installPlan: await installPlan.innerText(),
    });
  await page.getByTestId("plugin-install-commit").click();
  const pluginRow = page.locator("button.plugin-row")
    .filter({ hasText: "com.vibetable.e2e.mutation-boundary" });
  await pluginRow.waitFor({ timeout: 60_000 });
  await pluginRow.click();

  const actions = page.locator(".action-row");
  await actions.filter({ hasText: "allowed-plan" }).locator("button.run-button").click();
  await page.getByTestId("plugin-action-start").click();
  const confirmation = page.getByTestId("plugin-confirmation");
  await confirmation.waitFor({ timeout: 30_000 });
  const confirmationText = await confirmation.innerText();
  const confirmationTargetCount = await confirmation.locator("dd").first().innerText();
  const confirmationApprove = page.getByTestId("plugin-confirm-approve");
  recorder.check("authorized mutation plan required explicit confirmation",
    /FINAL WRITE CONFIRMATION/i.test(confirmationText)
      && confirmationTargetCount.trim() === "1"
      && await confirmationApprove.isVisible()
      && await confirmationApprove.isEnabled(),
  {
    confirmation: confirmationText,
    confirmationTargetCount,
  });
  await confirmationApprove.click();
  await page.locator(".result-card").waitFor({ timeout: 30_000 });
  await page.getByTestId("plugin-action-close").click();

  await page.getByTestId("nav-tables").click();
  const afterAuthorized = await waitForTableRecovery(page, "E2E Plugin Target", 1);
  recorder.check("approved plugin mutation became one visible record", afterAuthorized === 1);

  await page.getByTestId("nav-plugins").click();
  await pluginRow.click();
  await actions.filter({ hasText: "denied-plan" }).locator("button.run-button").click();
  await page.getByTestId("plugin-action-start").click();
  const denied = page.getByTestId("plugin-task-error");
  await denied.waitFor({ timeout: 30_000 });
  const deniedText = await denied.innerText();
  recorder.check("undeclared field mutation was rejected by the capability boundary",
    /forbidden|permission|declared|field/i.test(deniedText), { deniedText });
  const auditResponse = await rawBridgeRequest(page, "plugin.audit.list", {
    projectKey: "local:default",
    pluginId: "com.vibetable.e2e.mutation-boundary",
  });
  const auditEvents = Array.isArray(auditResponse.payload) ? auditResponse.payload : [];
  const deniedAudit = auditEvents.find((event) =>
    event.errorCode
    || /denied|rejected|failed|error/i.test(String(event.outcome)));
  recorder.check("denied plugin mutation emitted an auditable rejection event",
    Boolean(deniedAudit), { auditEvents });
  await page.getByTestId("plugin-action-close").click();
  await page.getByTestId("nav-tables").click();
  const afterDenied = await waitForTableRecovery(page, "E2E Plugin Target", 1);
  recorder.check("rejected plugin mutation did not create a second record", afterDenied === 1);
}

async function scenario12(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const purgeTable = await createSingleFieldTable(
    page,
    "E2E Purge Guard",
    "Attachments",
    "file",
  );
  const retired = await applyV2FieldChange(
    page,
    purgeTable.tableId,
    purgeTable.field.fieldId,
    "retire",
  );
  const withoutBackup = await applyV2FieldChange(
    page,
    purgeTable.tableId,
    purgeTable.field.fieldId,
    "purge",
    { confirmation: "Attachments" },
  );
  recorder.check(
    "permanent purge is rejected without a verified backup receipt",
    retired.applied?.type === "field.change.apply"
      && withoutBackup.applied?.type === "field.change.apply"
      && withoutBackup.applied.payload?.error?.code === "field.purge.backup_required",
    { retired, withoutBackup },
  );

  const backup = await rawBridgeRequest(page, "backup.create", {
    name: `e2e_before_purge_${Date.now()}.zip`,
  });
  const purged = await applyV2FieldChange(
    page,
    purgeTable.tableId,
    purgeTable.field.fieldId,
    "purge",
    {
      confirmation: "Attachments",
      backupReceipt: backup.payload?.receipt,
    },
  );
  const recycle = await rawBridgeRequest(page, "field.recycleBin.list", {
    tableId: purgeTable.tableId,
  });
  recorder.check(
    "verified backup plus frozen confirmations permits irreversible purge",
    typeof backup.payload?.receipt === "string"
      && backup.payload.receipt.startsWith("vbr1.")
      && purged.applied?.type === "field.change.apply"
      && !recycle.payload?.fields?.some(
        (field) => field.identity?.fieldId === purgeTable.field.fieldId,
      ),
    { backup, purged, recycle },
  );
  return;
}

const scenarios = {
  "01-offline-first-start": scenario01,
  "02-all-field-schema": scenario02,
  "03-schema-errors": scenario03,
  "04-json-round-trip": scenario04,
  "05-formula-lifecycle": scenario05,
  "06-relation-fanout": scenario06,
  "07-attachment-history": scenario07,
  "08-stale-conflict": scenario08,
  "09-atomic-import-scale": scenario09,
  "10-sse-reconnect": scenario10,
  "11-plugin-mutation": scenario11,
  "12-backup-consistency": scenario12,
};

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const evidenceDir = path.resolve(args["evidence-dir"]);
  await fs.mkdir(evidenceDir, { recursive: true });
  const recorder = makeRecorder();
  const consoleEntries = [];
  const network = [];
  const pageErrors = [];
  const result = {
    scenario: args.scenario,
    status: "failed",
    startedAt: new Date().toISOString(),
    transport: "playwright-core.connectOverCDP",
    browserLaunchCalled: false,
    assertions: recorder.assertions,
    console: consoleEntries,
    network,
    pageErrors,
    uiTimings: [],
  };
  let browser;
  let context;
  let page;
  try {
    browser = await chromium.connectOverCDP(args["cdp-url"]);
    context = browser.contexts()[0];
    if (!context) throw new Error("WebView2 exposed no browser context over CDP");
    await context.tracing.start({ screenshots: true, snapshots: true, sources: false });
    const observedPages = new WeakSet();
    const observePage = (candidate) => {
      if (observedPages.has(candidate)) return;
      observedPages.add(candidate);
      candidate.on("console", (message) => consoleEntries.push({
        type: message.type(),
        text: message.text(),
        location: message.location(),
      }));
      candidate.on("pageerror", (error) => pageErrors.push({
        name: error.name,
        message: error.message,
        stack: error.stack ?? null,
      }));
      candidate.on("request", (request) => network.push({
        method: request.method(),
        resourceType: request.resourceType(),
        url: request.url(),
      }));
      candidate.on("requestfailed", (request) => network.push({
        method: request.method(),
        resourceType: request.resourceType(),
        url: request.url(),
        failure: request.failure()?.errorText ?? "unknown",
      }));
    };
    for (const candidate of context.pages()) observePage(candidate);
    context.on("page", observePage);
    page = await locateProductPage(browser);
    observePage(page);
    await installBridgeDiagnostics(page);
    const implementation = scenarios[args.scenario];
    if (implementation) {
      await implementation(page, recorder, network, {
        evidenceDir,
        controlsDir: path.resolve(args["controls-dir"]),
        recordUiTiming(name, durationMs, details = {}) {
          result.uiTimings.push({
            name,
            durationMs: Math.round(durationMs * 100) / 100,
            details,
          });
        },
      });
    }
    else throw new Error(`unknown product scenario: ${args.scenario}`);
    await page.waitForTimeout(250);
    result.bridgeDiagnostics = await readBridgeDiagnostics(page);
    assertCleanBridgeDiagnostics(recorder, result.bridgeDiagnostics);
    assertCleanRendererDiagnostics(recorder, consoleEntries, pageErrors, network);
    result.status = "passed";
  } catch (error) {
    result.error = {
      name: error?.name ?? "Error",
      code: error?.code ?? "SCENARIO_FAILED",
      message: error?.message ?? String(error),
      capability: error?.capability,
      dependency: error?.dependency,
      probes: error?.probes,
      stack: error?.stack,
    };
  } finally {
    result.finishedAt = new Date().toISOString();
    result.durationMs = Date.parse(result.finishedAt) - Date.parse(result.startedAt);
    if (page) {
      if (!result.bridgeDiagnostics) {
        try {
          result.bridgeDiagnostics = await readBridgeDiagnostics(page);
        } catch (error) {
          result.bridgeDiagnosticsError = String(error);
        }
      }
      try {
        await page.screenshot({
          path: path.join(evidenceDir, `${args.scenario}.png`),
          fullPage: true,
        });
      } catch (error) {
        result.screenshotError = String(error);
      }
    }
    if (context) {
      try {
        await context.tracing.stop({
          path: path.join(evidenceDir, `${args.scenario}-trace.zip`),
        });
      } catch (error) {
        result.traceError = String(error);
      }
    }
    await fs.writeFile(
      path.join(evidenceDir, `${args.scenario}-result.json`),
      `${JSON.stringify(result, null, 2)}\n`,
      "utf8",
    );
  }
  // Keep stdout below the anonymous-pipe buffer: the Python fault controller
  // polls this process while it services kill requests and only drains stdout
  // after exit. Full evidence is already persisted in the result JSON above.
  fsSync.writeSync(1, `${JSON.stringify({
    scenario: result.scenario,
    status: result.status,
  })}\n`);
  // connectOverCDP is attached to a WebView2 owned by the WPF process.
  // Browser.close() sends a browser-close command to that owned renderer and
  // can hang indefinitely after a sidecar recovery. Evidence is already
  // flushed above; the Python orchestrator owns exact process-tree cleanup.
  process.exit(result.status === "passed" ? 0 : 1);
}

await main();
