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
    autoDate: /^(自动日期|Automatic date)$/u,
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
    autoDate: { zh: "自动日期", en: "Automatic date" },
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
    autoDate: /^(?:\u81ea\u52a8\u65e5\u671f|Automatic date)$/u,
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
    autoDate: { zh: "\u81ea\u52a8\u65e5\u671f", en: "Automatic date" },
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

async function createSimpleTable(page, displayName, fieldName = "label") {
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", displayName);
  await fillNInput(page, "create-table-field-name-0", fieldName);
  await page.getByTestId("create-table-submit").click();
  await page.getByTestId("create-table-name-input").waitFor({
    state: "hidden",
    timeout: 30_000,
  });
  await page.getByTestId("sidebar-table-name").filter({ hasText: displayName }).waitFor();
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
  await createSimpleTable(page, "E2E Relation Target", "label");

  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E All Field Families");

  const fields = [
    ["quantity", "integer"],
    ["title", "shortText"],
    ["status", "select"],
    ["metadata", "json"],
    ["double_quantity", "formula"],
    ["attachments", "file"],
    ["parent", "relation"],
    ["parent_label", "lookup"],
    ["created_at", "autoDate"],
    ["tags", "multiSelect"],
  ];
  for (let index = 0; index < fields.length; index += 1) {
    if (index > 0) await page.getByTestId("create-table-add-field").click();
    const [name, type] = fields[index];
    await fillNInput(page, `create-table-field-name-${index}`, name);
    await selectNValue(page, `create-table-field-type-${index}`, type);
  }

  const scalar = page.locator('section[data-field-type="integer"]');
  await scalar.locator('.toggles input[type="checkbox"]').nth(0).check();
  await scalar.locator('.toggles input[type="checkbox"]').nth(1).uncheck();
  await scalar.locator('.toggles input[type="checkbox"]').nth(2).check();
  await scalar.locator('.config-grid input[type="number"]').nth(0).fill("0");
  await scalar.locator('.config-grid input[type="number"]').nth(1).fill("1000");

  const titleField = page.locator('section[data-field-type="shortText"]');
  await titleField.locator('.toggles input[type="checkbox"]').nth(0).check();
  await titleField.locator('.toggles input[type="checkbox"]').nth(1).uncheck();
  await titleField.locator('.config-grid input[type="number"]').nth(0).fill("3");
  await titleField.locator('.config-grid input[type="number"]').nth(1).fill("80");
  await titleField.locator('.config-grid input[type="text"]').fill("^[A-Za-z0-9 ].+$");

  const statusField = page.locator('section[data-field-type="select"]');
  await statusField.locator('.toggles input[type="checkbox"]').nth(0).check();
  await statusField.locator('.toggles input[type="checkbox"]').nth(1).uncheck();
  await page.getByTestId("field-enum-option-value-2-0").fill('"draft"');
  await page.getByTestId("field-enum-option-display-2-0").fill("Draft");
  await page.getByTestId("field-enum-add-option-2").click();
  await page.getByTestId("field-enum-option-value-2-1").fill('"active"');
  await page.getByTestId("field-enum-option-display-2-1").fill("Active");
  await page.getByTestId("field-enum-add-option-2").click();
  await page.getByTestId("field-enum-option-value-2-2").fill('"archived"');
  await page.getByTestId("field-enum-option-display-2-2").fill("Archived");
  await statusField.locator('label.single input').fill('"draft"');

  await page.getByTestId("field-json-schema-3").fill(
    '{"type":"object","required":["source"],"properties":{"source":{"type":"string"}}}',
  );
  await page.getByTestId("field-formula-source-4").fill("quantity * 2");
  await page.locator('section[data-field-type="formula"] select').selectOption("integer");

  const attachment = page.locator('section[data-field-type="file"]');
  await page.getByTestId("field-attachment-max-files-5").fill("3");
  await attachment.locator('.config-grid input[type="number"]').nth(1).fill("1048576");
  await attachment.locator(".config-grid label.wide input").first()
    .fill("image/png, application/pdf");
  await page.getByTestId("field-attachment-thumbnails-5").fill("320x240, 640x480");
  recorder.check(
    "managed attachment policy is protected and bounded before schema apply",
    await attachment.locator('.config-grid input[type="checkbox"]').isChecked(),
  );

  await page.getByTestId("field-relation-target-6").fill("tbl_e2e_relation_target");
  const relation = page.locator('section[data-field-type="relation"]');
  await relation.locator("select").nth(0).selectOption("one");
  await relation.locator("select").nth(1).selectOption("restrict");

  await page.getByTestId("field-lookup-relation-7").fill("fld_parent");
  await page.locator('section[data-field-type="lookup"] .config-grid input')
    .nth(1)
    .fill("fld_label");
  await page.locator('section[data-field-type="lookup"] select').first()
    .selectOption("first");
  await page.getByTestId("field-lookup-output-type-7").selectOption("shortText");

  await page.getByTestId("field-enum-option-value-9-0").fill("true");
  await page.getByTestId("field-enum-option-display-9-0").fill("Enabled");
  await page.getByTestId("field-enum-add-option-9").click();
  await page.getByTestId("field-enum-option-value-9-1").fill("2");
  await page.getByTestId("field-enum-option-display-9-1").fill("Priority two");
  await page.getByTestId("field-enum-add-option-9").click();
  await page.getByTestId("field-enum-option-value-9-2").fill('"done"');
  await page.getByTestId("field-enum-option-display-9-2").fill("Done");
  await page.getByTestId("field-enum-min-selected-9").fill("1");
  await page.getByTestId("field-enum-max-selected-9").fill("2");

  await page.getByTestId("create-table-add-index")
    .evaluate((node) => node.click());
  await fillNInput(page, "create-table-index-name-0", "idx_title");
  await selectNOptionsByText(page, "create-table-index-fields-0", ["title"]);
  await page.getByTestId("create-table-index-type-0")
    .locator(".n-base-selection")
    .evaluate((node) => node.click());
  const uniqueIndexOption = page.locator(".n-base-select-option:visible")
    .filter({ hasText: /(?:\u552f\u4e00\u7d22\u5f15|Unique index)/u })
    .first();
  await uniqueIndexOption.waitFor();
  await uniqueIndexOption.evaluate((node) => node.click());
  await closeVisibleNSelectMenu(page);
  await page.getByTestId("create-table-add-index")
    .evaluate((node) => node.click());
  await fillNInput(page, "create-table-index-name-1", "idx_quantity_status");
  await selectNOptionsByText(
    page,
    "create-table-index-fields-1",
    ["quantity", "status"],
  );

  const kinds = await page.locator("section[data-field-type]").evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-field-type")));
  recorder.check("schema editor exposed every product field family and constraint carrier",
    JSON.stringify(kinds) === JSON.stringify(fields.map(([, type]) => type)), {
    dataTypes: kinds,
    productKinds: ["scalar", "json", "formula", "attachment", "relation", "lookup", "system"],
    constraints: [
      "required", "nullable", "unique", "range", "length", "pattern",
      "enumValueDisplay", "singleMultiSelectBounds", "default",
      "jsonSchema", "attachment", "thumbnail",
      "relation", "lookup", "regularIndex", "uniqueIndex", "compositeIndex",
    ],
  });
  await closeVisibleNSelectMenu(page);
  const submit = page.getByTestId("create-table-submit");
  await submit.waitFor({ state: "visible" });
  await submit.click();
  await waitForCreateTableSubmission(page, submit);
  const createdTable = page.getByTestId("sidebar-table-name")
    .filter({ hasText: "E2E All Field Families" });
  await createdTable.waitFor();
  recorder.check("schema.validate and schema.apply completed through the UI",
    await createdTable.isVisible()
      && !(await page.getByTestId("create-table-name-input").isVisible()));
  const persistedSchema = await rawBridgeRequest(page, "schema.getTable", {
    tableId: "tbl_e2e_all_field_families",
  });
  const definition = persistedSchema.payload;
  const persistedFields = new Map(
    (definition?.fields ?? []).map((field) => [field.physicalName, field]),
  );
  const findConstraint = (fieldName, kind) =>
    persistedFields.get(fieldName)?.constraints?.find((item) => item.kind === kind);
  const quantity = persistedFields.get("quantity");
  const title = persistedFields.get("title");
  const status = persistedFields.get("status");
  const metadata = persistedFields.get("metadata");
  const formula = persistedFields.get("double_quantity");
  const attachments = persistedFields.get("attachments");
  const parent = persistedFields.get("parent");
  const parentLabel = persistedFields.get("parent_label");
  const createdAt = persistedFields.get("created_at");
  const quantityRange = findConstraint("quantity", "range");
  const titleLength = findConstraint("title", "length");
  const titlePattern = findConstraint("title", "pattern");
  const statusEnum = findConstraint("status", "enum");
  const tagsEnum = findConstraint("tags", "enum");
  const statusDefault = findConstraint("status", "default");
  const metadataSchema = findConstraint("metadata", "jsonSchema");
  const attachmentConstraint = findConstraint("attachments", "attachment");
  recorder.check(
    "authoritative normalized schema persisted every configured field constraint and policy",
    persistedSchema.type === "schema.getTable"
      && definition?.tableId === "tbl_e2e_all_field_families"
      && quantity?.nullable === false
      && Boolean(findConstraint("quantity", "required")?.value)
      && Boolean(findConstraint("quantity", "unique")?.value)
      && quantityRange?.min === 0
      && quantityRange?.max === 1000
      && title?.nullable === false
      && Boolean(findConstraint("title", "required")?.value)
      && titleLength?.minLength === 3
      && titleLength?.maxLength === 80
      && titlePattern?.pattern === "^[A-Za-z0-9 ].+$"
      && status?.nullable === false
      && Boolean(findConstraint("status", "required")?.value)
      && JSON.stringify(statusEnum?.options?.map((item) => item.value))
        === JSON.stringify(["draft", "active", "archived"])
      && JSON.stringify(statusEnum?.options?.map((item) => item.displayName))
        === JSON.stringify(["Draft", "Active", "Archived"])
      && statusEnum?.multiple === false
      && statusEnum?.minSelected === 1
      && statusEnum?.maxSelected === 1
      && statusDefault?.value === "draft"
      && status?.defaultValue === "draft"
      && metadataSchema?.schema?.type === "object"
      && metadataSchema?.schema?.required?.[0] === "source"
      && formula?.formula?.source === "quantity * 2"
      && formula?.formula?.resultType === "integer"
      && attachments?.attachmentPolicy?.maxFiles === 3
      && attachments?.attachmentPolicy?.maxBytesPerFile === 1048576
      && JSON.stringify(attachments?.attachmentPolicy?.allowedMimeTypes)
        === JSON.stringify(["image/png", "application/pdf"])
      && attachments?.attachmentPolicy?.protected === true
      && JSON.stringify(attachments?.attachmentPolicy?.thumbnailVariants)
        === JSON.stringify(["320x240", "640x480"])
      && attachmentConstraint?.policy?.protected === true
      && parent?.relation?.targetTableId === "tbl_e2e_relation_target"
      && parent?.relation?.cardinality === "one"
      && parent?.relation?.deletePolicy === "restrict"
      && parentLabel?.lookup?.relationFieldId === "fld_parent"
      && parentLabel?.lookup?.targetFieldId === "fld_label"
      && parentLabel?.lookup?.aggregate === "first"
      && createdAt?.dataType === "autoDate"
      && createdAt?.kind === "system"
      && createdAt?.readOnly === true
      && tagsEnum?.multiple === true
      && tagsEnum?.minSelected === 1
      && tagsEnum?.maxSelected === 2
      && JSON.stringify(tagsEnum?.options)
        === JSON.stringify([
          { value: true, displayName: "Enabled" },
          { value: 2, displayName: "Priority two" },
          { value: "done", displayName: "Done" },
        ])
      && definition?.indexes?.length === 2
      && definition.indexes[0]?.name === "idx_title"
      && definition.indexes[0]?.unique === true
      && JSON.stringify(definition.indexes[0]?.fieldIds) === JSON.stringify(["fld_title"])
      && definition.indexes[1]?.name === "idx_quantity_status"
      && definition.indexes[1]?.unique === false
      && JSON.stringify(definition.indexes[1]?.fieldIds)
        === JSON.stringify(["fld_quantity", "fld_status"]),
    { persistedSchema },
  );
  await createdTable.click();
  await page.locator(".tabulator").waitFor({ timeout: 30_000 });
  const appliedFields = await page.locator(".tabulator-col[tabulator-field]")
    .evaluateAll((nodes) => nodes
      .map((node) => node.getAttribute("tabulator-field"))
      .filter((field) => field && field !== "__vt_row_number"));
  recorder.check(
    "applied schema renders every configured product field",
    fields.every(([name]) => appliedFields.includes(name)),
    { expectedFields: fields.map(([name]) => name), appliedFields },
  );
  const emptyState = page.locator(".tabulator-placeholder:visible");
  await emptyState.waitFor({ timeout: 10_000 });
  const emptyStateText = (await emptyState.innerText()).trim();
  const activeLocale = await page.evaluate(
    () => localStorage.getItem("vt:locale") ?? "zh-CN",
  );
  const expectedEmptyState = activeLocale === "en-US"
    ? "No records yet — use + to add the first row"
    : "暂无记录，使用“+”添加第一行";
  recorder.check(
    "new empty table shows a localized actionable empty state",
    emptyStateText === expectedEmptyState,
    { activeLocale, emptyStateText, expectedEmptyState },
  );
  const typedEnumMutation = await rawBridgeRequest(page, "mutation.apply", {
    contractVersion: "1.0",
    requestId: "e2e-typed-enum-insert",
    idempotencyKey: "e2e-typed-enum-insert",
    tableId: definition.tableId,
    schemaRevision: definition.schemaRevision,
    operations: [{
      kind: "insert",
      recordId: null,
      values: {
        quantity: 42,
        title: "Typed Enum Row",
        status: "draft",
        tags: [true, 2],
      },
    }],
    actor: { type: "user", id: "e2e-schema", displayName: "E2E schema" },
    expectedRevision: null,
    expectedDigest: null,
  });
  recorder.check(
    "typed boolean and number enum values committed through mutation.apply",
    typedEnumMutation.type === "mutation.apply"
      && typedEnumMutation.payload?.status === "applied",
    { typedEnumMutation },
  );
  const typedEnumQuery = await rawBridgeRequest(page, "query.page", {
    tableId: definition.tableId,
    query: {
      filters: [{ field: "tags", operator: "contains", value: true, logic: "AND" }],
      sorts: [],
      offset: 0,
      limit: 100,
    },
  });
  const typedEnumRow = typedEnumQuery.payload?.rows?.[0];
  recorder.check(
    "query.page filtered and returned typed enum values without string collapse",
    typedEnumQuery.type === "query.page"
      && typedEnumQuery.payload?.rows?.length === 1
      && typedEnumRow?.title === "Typed Enum Row"
      && Array.isArray(typedEnumRow?.tags)
      && typedEnumRow.tags.length === 2
      && typedEnumRow.tags[0] === true
      && typedEnumRow.tags[1] === 2,
    { typedEnumQuery },
  );
  const lookupSchemaProbe = await rawBridgeRequest(page, "schema.describe", {
    collection: definition.tableId,
    requestGeneration: 2002,
    accepts: [
      "vibetable.relation-capabilities.v1",
      "vibetable.lookup-query.v1",
    ],
  });
  const lookupCatalogProbe = await rawBridgeRequest(page, "lookup.list", {
    collection: definition.tableId,
  });
  const lookupDefinitions = lookupCatalogProbe.payload?.definitions ?? [];
  const authoritativeLookupQuery = await rawBridgeRequest(page, "lookup.query", {
    contract: "vibetable.lookup-query.v1",
    collection: definition.tableId,
    fieldRefs: lookupDefinitions.map((item) => item.fieldKey),
    query: { filters: [], sorts: [], groups: [], offset: 0, limit: 100 },
    requestGeneration: 2002,
    schemaRevision: lookupSchemaProbe.payload?.schema?.schemaRevision,
    permissionRevision: lookupSchemaProbe.payload?.schema?.permissionRevision,
    lookupRevision: lookupSchemaProbe.payload?.schema?.lookupRevision,
  });
  recorder.check(
    "authoritative Lookup projection succeeds with Lookup-only field refs",
    authoritativeLookupQuery.type === "lookup.query"
      && authoritativeLookupQuery.payload?.rows?.length === 1,
    {
      lookupDefinitions,
      lookupSchemaProbe,
      lookupCatalogProbe,
      authoritativeLookupQuery,
    },
  );
  await page.waitForTimeout(100);
  const unexpectedErrorMessages = await page.locator(".n-message--error-type:visible").allInnerTexts();
  recorder.check(
    "successful all-field schema flow leaves no error toast",
    unexpectedErrorMessages.length === 0,
    { unexpectedErrorMessages },
  );

  const previousViewport = page.viewportSize();
  await page.setViewportSize({ width: 720, height: 720 });
  const compactTableSwitcher = page.getByTestId("compact-table-switcher");
  await compactTableSwitcher.waitFor({ state: "visible" });
  await compactTableSwitcher.click();
  const compactTarget = page.locator(".n-dropdown-option:visible")
    .filter({ hasText: "E2E Relation Target" })
    .first();
  await compactTarget.waitFor();
  await compactTarget.click();
  await page.waitForFunction(
    () => document.querySelector('[data-testid="compact-table-switcher"]')
      ?.textContent?.includes("E2E Relation Target"),
  );
  recorder.check(
    "narrow real WebView2 keeps table switching available from the compact toolbar",
    await compactTableSwitcher.isVisible()
      && (await compactTableSwitcher.innerText()).includes("E2E Relation Target")
      && !(await page.getByTestId("toolbar-table-title").isVisible()),
    {
      viewport: page.viewportSize(),
      compactLabel: await compactTableSwitcher.innerText(),
    },
  );
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "02-narrow-table-switcher.png"),
    fullPage: true,
  });
  if (previousViewport) {
    await page.setViewportSize(previousViewport);
  }
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
      if (message.type === "operation.failed") diagnostics.failures.push(roundTrip);
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
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E Invalid Formula");
  await fillNInput(page, "create-table-field-name-0", "total");
  await selectNValue(page, "create-table-field-type-0", "formula");
  const localError = page.getByTestId("field-error-0");
  await localError.waitFor();
  // The exact stable field path is intentionally tucked behind the
  // user-facing "details" disclosure. Exercise that real affordance before
  // asserting the path instead of relying on hidden DOM text.
  await localError.locator("summary").click();
  const localText = await localError.innerText();
  recorder.check("client blocks invalid formula at the field path", localText.includes("fields[0].formula.source"), {
    localText,
    submitDisabled: await page.getByTestId("create-table-submit").isDisabled(),
  });

  const response = await rawBridgeRequest(page, "schema.validate", {
    definition: invalidFormulaDefinition(),
    expectedRevision: 0,
  });
  recorder.check(
    "server rejects the same invalid definition with a stable code and field path",
    response.type === "schema.validate"
      && response.payload?.error?.code === "schema.field.invalid_formula"
      && response.payload?.error?.path === "fields[0].formula.source",
    { response },
  );
}

async function scenario04(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E JSON Round Trip");
  await fillNInput(page, "create-table-field-name-0", "payload");
  await selectNValue(page, "create-table-field-type-0", "json");
  await page.getByTestId("create-table-submit").click();
  await page.getByTestId("create-table-name-input").waitFor({ state: "hidden", timeout: 30_000 });
  await selectTable(page, "E2E JSON Round Trip");
  await insertRowFromToolbar(page);
  let jsonCell = page.locator('.tabulator-cell[tabulator-field="payload"]').first();
  await jsonCell.waitFor({ timeout: 30_000 });

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
    tableId: "tbl_e2e_json_round_trip",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  }, (payload) => payload?.rows?.[0]?.payload?.nested?.value === 7);
  const editorValue = editorQuery.payload?.rows?.[0]?.payload;
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
  jsonCell = page.locator('.tabulator-cell[tabulator-field="payload"]').first();
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
    '.tabulator-col[tabulator-field="payload"] .tabulator-header-filter input',
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
  const expectedImportedValue = {
    nested: { value: 9, label: "import" },
    items: [1, { code: "A" }],
    enabled: true,
  };
  const expectedFinalValues = canonicalJsonSet([
    expectedPastedValue,
    expectedImportedValue,
  ]);
  const authoritative = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_json_round_trip",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const authoritativeValues = (authoritative.payload?.rows ?? []).map((row) => row.payload);
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
  jsonCell = page.locator('.tabulator-cell[tabulator-field="payload"]').first();
  const englishFilter = page.locator(
    '.tabulator-col[tabulator-field="payload"] .tabulator-header-filter input',
  );
  await englishFilter.waitFor();
  await page.waitForFunction(
    () => document.querySelector(
      '.tabulator-col[tabulator-field="payload"] .tabulator-header-filter input',
    )?.getAttribute("placeholder") === "Filter…",
  );
  const englishGridLabels = {
    placeholder: await englishFilter.getAttribute("placeholder"),
    ariaLabel: await jsonCell.getAttribute("aria-label"),
  };
  await setProductLocale(page, "zh-CN");
  jsonCell = page.locator('.tabulator-cell[tabulator-field="payload"]').first();
  const chineseFilter = page.locator(
    '.tabulator-col[tabulator-field="payload"] .tabulator-header-filter input',
  );
  await page.waitForFunction(
    () => document.querySelector(
      '.tabulator-col[tabulator-field="payload"] .tabulator-header-filter input',
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
  const payloadIndex = exportedRows[0]?.indexOf("payload") ?? -1;
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
    tableId: "tbl_e2e_json_round_trip",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const values = (queried.payload?.rows ?? []).map((row) => row.payload);
  recorder.check("authoritative query returned JSON values as objects, not strings",
    values.length === 2
      && values.every((value) =>
        value && typeof value === "object" && Array.isArray(value.items))
      && JSON.stringify(canonicalJsonSet(values)) === JSON.stringify(expectedFinalValues),
  { values, expectedFinalValues });
}

async function scenario05(page, recorder) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E Formula Lifecycle");
  await fillNInput(page, "create-table-field-name-0", "quantity");
  await selectNValue(page, "create-table-field-type-0", "integer");
  await page.getByTestId("create-table-add-field").click({ force: true });
  await fillNInput(page, "create-table-field-name-1", "doubled");
  await selectNValue(page, "create-table-field-type-1", "formula");
  await page.locator('section[data-field-type="formula"] select').selectOption("integer");
  await page.getByTestId("field-formula-preview-row-1").fill('{"quantity":3}');
  await page.getByTestId("field-formula-source-1").fill("quantity * 2");
  const livePreview = page.getByTestId("field-formula-preview-1");
  await livePreview.locator("code").filter({ hasText: "6" }).waitFor({ timeout: 30_000 });
  recorder.check("formula preview used the authoritative service before schema save",
    await livePreview.getAttribute("data-state") === "ready");
  await page.getByTestId("create-table-submit").click();
  await page.getByTestId("create-table-name-input").waitFor({ state: "hidden", timeout: 30_000 });

  await selectTable(page, "E2E Formula Lifecycle");
  await insertRowFromToolbar(page);
  let quantity = page.locator('.tabulator-cell[tabulator-field="quantity"]').first();
  await quantity.waitFor({ timeout: 30_000 });
  let quantityEditor = await beginCellEdit(quantity);
  await quantityEditor.fill("2");
  await quantityEditor.press("Enter");
  let doubled = page.locator('.tabulator-cell[tabulator-field="doubled"]').first();
  await doubled.filter({ hasText: "4" }).waitFor({ timeout: 30_000 });
  quantity = page.locator('.tabulator-cell[tabulator-field="quantity"]').first();
  quantityEditor = await beginCellEdit(quantity);
  await quantityEditor.fill("5");
  await quantityEditor.press("Enter");
  doubled = page.locator('.tabulator-cell[tabulator-field="doubled"]').first();
  await doubled.filter({ hasText: "10" }).waitFor({ timeout: 30_000 });
  const recomputedFormulaValue = await doubled.innerText();
  recorder.check("saved formula recomputed after dependency edit",
    recomputedFormulaValue.trim() === "10", { recomputedFormulaValue });
  const visibleRows = await page.locator(".tabulator-row:visible").count();
  const summaryText = await page.getByTestId("table-summary").innerText();
  const summaryMatch = summaryText.match(/\d+/u);
  const summaryRows = summaryMatch ? Number.parseInt(summaryMatch[0], 10) : Number.NaN;
  recorder.check(
    "visible grid row count matched the product status bar",
    visibleRows === 1 && summaryRows === visibleRows,
    { visibleRows, summaryRows, summaryText },
  );

  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E Formula Cycle");
  await fillNInput(page, "create-table-field-name-0", "a");
  await selectNValue(page, "create-table-field-type-0", "formula");
  await page.getByTestId("field-formula-source-0").fill("");
  await page.locator('section[data-field-type="formula"] select').selectOption("integer");
  await page.getByTestId("create-table-add-field").click();
  await fillNInput(page, "create-table-field-name-1", "b");
  await selectNValue(page, "create-table-field-type-1", "formula");
  await page.getByTestId("field-formula-source-1").fill("");
  await page.locator('section[data-field-type="formula"] select').nth(1).selectOption("integer");
  await page.getByTestId("field-formula-source-0").fill("b + 1");
  await page.getByTestId("field-formula-source-1").fill("a + 1");
  await page.getByTestId("create-table-submit").click();
  const cycleSurface = page.locator(
    '[data-testid="create-table-error"], [data-testid="field-error-0"], [data-testid="field-error-1"], [data-state="error"]',
  ).filter({ hasText: /cycle|循环|cyclic/i }).first();
  await cycleSurface.waitFor({ timeout: 30_000 });
  const cycleError = await cycleSurface.innerText();
  recorder.check("cyclic formula was rejected on the product schema surface",
    /cycle|循环|cyclic/i.test(cycleError), { cycleError });
}

async function scenario06(page, recorder) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  await createSimpleTable(page, "E2E Authors", "name");
  await selectTable(page, "E2E Authors");
  await insertRowFromToolbar(page);
  let authorCell = page.locator('.tabulator-cell[tabulator-field="name"]').first();
  await authorCell.waitFor({ timeout: 30_000 });
  let authorEditor = await beginCellEdit(authorCell);
  await authorEditor.fill("Before");
  await authorEditor.press("Enter");
  await authorCell.filter({ hasText: "Before" }).waitFor({ timeout: 30_000 });

  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E Articles");
  await selectNValue(page, "create-table-field-type-0", "relation");
  await fillNInput(page, "create-table-field-name-0", "author");
  await page.getByTestId("field-relation-target-0").fill("tbl_e2e_authors");
  await page.getByTestId("create-table-add-field").click();
  await selectNValue(page, "create-table-field-type-1", "formula");
  await fillNInput(page, "create-table-field-name-1", "author_label");
  await page.getByTestId("field-formula-preview-row-1")
    .fill('{"author":{"name":"Before"}}');
  await page.getByTestId("field-formula-source-1").fill("author.name");
  await page.locator('section[data-field-type="formula"] select').selectOption("shortText");
  await page.getByTestId("field-formula-preview-1").locator("code")
    .filter({ hasText: "Before" }).waitFor({ timeout: 30_000 });
  await page.getByTestId("create-table-submit").click();
  await page.getByTestId("create-table-name-input").waitFor({ state: "hidden", timeout: 30_000 });
  await selectTable(page, "E2E Articles");
  await insertRowFromToolbar(page);

  const relationCell = page.locator('.tabulator-cell[tabulator-field="author"]').first();
  await relationCell.waitFor({ timeout: 30_000 });
  const schemaProbe = await rawBridgeRequest(page, "schema.describe", {
    collection: "tbl_e2e_articles",
    requestGeneration: 6006,
    accepts: [
      "vibetable.relation-capabilities.v1",
      "vibetable.lookup-query.v1",
    ],
  });
  const lookupProbe = await rawBridgeRequest(page, "lookup.list", {
    collection: "tbl_e2e_articles",
  });
  const schemaPayload = schemaProbe?.payload;
  const lookupPayload = lookupProbe?.payload;
  const columnRelationIds = schemaPayload?.schema?.columns
    ?.map((column) => column.relationId)
    .filter(Boolean) ?? [];
  const descriptors = schemaPayload?.schema?.normalizedRelations ?? [];
  const normalizedRelationKeys = descriptors.map((relation) => relation.relationId);
  const descriptor = descriptors.find(
    (relation) => relation.relationId === "tbl_e2e_articles.fld_author",
  );
  recorder.check("packaged schema.describe returned the valid, indexable relation descriptor",
    schemaProbe?.type === "schema.describe"
      && lookupProbe?.type === "lookup.list"
      && schemaPayload?.contract === "vibetable.schema-describe.v1"
      && schemaPayload?.collection === "tbl_e2e_articles"
      && schemaPayload?.requestGeneration === 6006
      && schemaPayload?.schema?.lookupRevision === lookupPayload?.lookupRevision
      && columnRelationIds.length === 1
      && columnRelationIds[0] === "tbl_e2e_articles.fld_author"
      && normalizedRelationKeys.length === 1
      && normalizedRelationKeys[0] === columnRelationIds[0]
      && descriptor?.state === "valid"
      && descriptor?.fieldRef === "author"
      && descriptor?.relatedCollection === "tbl_e2e_authors",
  {
    schemaProbe,
    lookupProbe,
    schemaLookupRevision: schemaPayload?.schema?.lookupRevision,
    listLookupRevision: lookupPayload?.lookupRevision,
    schemaCollection: schemaPayload?.collection,
    schemaRequestGeneration: schemaPayload?.requestGeneration,
    columnRelationIds,
    normalizedRelationKeys,
    descriptor,
  });
  await relationCell.dblclick();
  const relationEditor = page.locator(".relation-editor");
  await relationEditor.waitFor();
  await relationEditor.locator("input").first().fill("Before");
  const candidate = relationEditor.locator(".relation-editor__candidate")
    .filter({ hasText: "Before" });
  await candidate.waitFor({ timeout: 30_000 });
  await candidate.click();
  const formulaCell = page.locator('.tabulator-cell[tabulator-field="author_label"]').first();
  await formulaCell.filter({ hasText: "Before" }).waitFor({ timeout: 30_000 });
  const relationFormulaValue = await formulaCell.innerText();
  recorder.check("relation selection computed the cross-record formula",
    relationFormulaValue.includes("Before"), { relationFormulaValue });

  await selectTable(page, "E2E Authors");
  authorCell = page.locator('.tabulator-cell[tabulator-field="name"]').first();
  authorEditor = await beginCellEdit(authorCell);
  await authorEditor.fill("After");
  await authorEditor.press("Enter");
  await authorCell.filter({ hasText: "After" }).waitFor({ timeout: 30_000 });
  await selectTable(page, "E2E Articles");
  const updatedFormulaCell = page.locator('.tabulator-cell[tabulator-field="author_label"]').first()
    .filter({ hasText: "After" });
  await updatedFormulaCell.waitFor({ timeout: 60_000 });
  const updatedFormulaValue = await updatedFormulaCell.innerText();
  recorder.check("target update fanned out and refreshed the dependent formula in the UI",
    updatedFormulaValue.includes("After"), { updatedFormulaValue });
}

async function selectTable(page, displayName) {
  const name = page.getByTestId("sidebar-table-name").filter({ hasText: displayName });
  await name.waitFor();
  await name.locator("xpath=ancestor::button").click();
  await page.getByTestId("table-summary").waitFor({ timeout: 30_000 });
}

async function createSingleFieldTable(page, displayName, fieldName, type) {
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", displayName);
  await fillNInput(page, "create-table-field-name-0", fieldName);
  await selectNValue(page, "create-table-field-type-0", type);
  await page.getByTestId("create-table-submit").click();
  await page.getByTestId("create-table-name-input").waitFor({
    state: "hidden",
    timeout: 30_000,
  });
}

async function scenario07(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  await createSingleFieldTable(page, "E2E Attachments", "attachments", "file");
  await selectTable(page, "E2E Attachments");
  await insertRowFromToolbar(page);
  const cell = page.locator('.tabulator-cell[tabulator-field="attachments"]').first();
  await cell.waitFor({ timeout: 30_000 });
  const schema = await rawBridgeRequest(page, "schema.describe", {
    collection: "tbl_e2e_attachments",
    requestGeneration: 7007,
    accepts: [
      "vibetable.relation-capabilities.v1",
      "vibetable.lookup-query.v1",
    ],
  });
  const attachmentColumn = schema.payload?.schema?.columns?.find(
    (column) => column.name === "attachments",
  );
  const queried = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_attachments",
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
    tableId: "tbl_e2e_attachments",
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
  await page.getByTestId("attachment-preview-0").click();
  const previewArtifact = await waitForPreviewArtifact(
    runtime,
    expectedOriginalHash,
    originalBytes.length,
  );
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
      table: "tbl_e2e_attachments",
      scope: "cell",
      itemId: recordId,
      field: "attachments",
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
      change.field === "attachments"
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
      collection: "tbl_e2e_attachments",
      itemId: recordId,
      targetRevision: originalRevision,
      scope: "cell",
      field: "attachments",
    },
    20_000,
    ["history.restorePreviewReady"],
  );
  recorder.check(
    "attachment restore preview identifies the original managed file",
    originalPreviewProbe.type === "history.restorePreviewReady"
      && originalPreviewProbe.payload?.canApply === true
      && originalPreviewProbe.payload?.restorableFields?.includes("attachments")
      && originalPreviewProbe.payload?.scalarChanges?.some((change) =>
        change.field === "attachments"
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
  await createSimpleTable(page, "E2E Stale Conflict", "value");
  await selectTable(page, "E2E Stale Conflict");
  await insertRowFromToolbar(page);
  const cell = page.locator('.tabulator-cell[tabulator-field="value"]').first();
  await cell.waitFor({ timeout: 30_000 });

  const queried = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_stale_conflict",
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
    tableId: "tbl_e2e_stale_conflict",
    schemaRevision: pageResult.snapshot.schemaRevision,
    operations: [{
      kind: "update",
      recordId: row.id,
      values: { value: "competitor-write" },
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
    (value) => Array.from(
      document.querySelectorAll('.tabulator-cell[tabulator-field="value"]'),
    ).some((node) => node.textContent?.includes(value)),
    "competitor-write",
    { timeout: 30_000 },
  );
  await beginBridgeMessageCapture(page, ["table.editRejected"]);
  await beginRawBridgeRequest(page, "table.updateCellRequested", {
    table: "tbl_e2e_stale_conflict",
    rowKey: row.id,
    column: "value",
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
  await createSimpleTable(page, "E2E Atomic Import", "value");
  await selectTable(page, "E2E Atomic Import");

  const rows = ["value"];
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
      collection: "tbl_e2e_atomic_import",
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
      && historyPage?.collection === "tbl_e2e_atomic_import"
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
    "tbl_e2e_atomic_import",
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
  await createSimpleTable(page, "E2E Realtime Recovery", "value");
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
    "tbl_e2e_realtime_recovery",
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
    tableId: "tbl_e2e_realtime_recovery",
    schemaRevision: recovered.snapshot.schemaRevision,
    operations: [{
      kind: "insert",
      recordId: null,
      values: { value: "after-reconnect" },
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
  const newCell = page.locator('.tabulator-cell[tabulator-field="value"]')
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
  await page.getByTestId("sidebar-new-table").click();
  await fillNInput(page, "create-table-name-input", "E2E Backup Consistency");
  const fields = [
    ["value", "integer"],
    ["doubled", "formula"],
    ["attachments", "file"],
  ];
  for (let index = 0; index < fields.length; index += 1) {
    if (index > 0) await page.getByTestId("create-table-add-field").click();
    await fillNInput(page, `create-table-field-name-${index}`, fields[index][0]);
    await selectNValue(page, `create-table-field-type-${index}`, fields[index][1]);
  }
  await page.getByTestId("field-formula-preview-row-1").fill('{"value":7}');
  await page.getByTestId("field-formula-source-1").fill("value * 2");
  await page.locator('section[data-field-type="formula"] select').selectOption("integer");
  const formulaPreview = page.getByTestId("field-formula-preview-1");
  await formulaPreview.locator("code").filter({ hasText: "14" })
    .waitFor({ timeout: 30_000 });
  recorder.check("backup schema formula preview was authoritative before save",
    await formulaPreview.getAttribute("data-state") === "ready");
  await page.getByTestId("field-attachment-max-files-2").fill("2");
  await page.getByTestId("create-table-submit").click();
  await page.getByTestId("create-table-name-input").waitFor({ state: "hidden", timeout: 30_000 });
  await selectTable(page, "E2E Backup Consistency");
  await insertRowFromToolbar(page);

  const valueCell = page.locator('.tabulator-cell[tabulator-field="value"]').first();
  await valueCell.waitFor({ timeout: 30_000 });
  let valueEditor = await beginCellEdit(valueCell);
  await valueEditor.fill("7");
  await valueEditor.press("Enter");
  const formulaCell = page.locator('.tabulator-cell[tabulator-field="doubled"]').first();
  await formulaCell.filter({ hasText: "14" }).waitFor({ timeout: 30_000 });

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
    path.join(runtime.evidenceDir, "backup-original.txt"),
  );
  await fs.copyFile(
    replacement,
    path.join(runtime.evidenceDir, "backup-replacement.txt"),
  );
  const originalBytes = await fs.readFile(original);
  const replacementBytes = await fs.readFile(replacement);
  const expectedOriginalHash = sha256(originalBytes);
  const expectedReplacementHash = sha256(replacementBytes);
  const schema = await rawBridgeRequest(page, "schema.describe", {
    collection: "tbl_e2e_backup_consistency",
    requestGeneration: 12_012,
    accepts: [
      "vibetable.relation-capabilities.v1",
      "vibetable.lookup-query.v1",
    ],
  });
  const attachmentColumn = schema.payload?.schema?.columns?.find(
    (column) => column.name === "attachments",
  );
  const initialQuery = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_backup_consistency",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const recordId = initialQuery.payload?.rows?.[0]?.id;
  if (
    schema.type !== "schema.describe"
    || attachmentColumn?.kind !== "attachment"
    || !attachmentColumn.fieldId
    || !recordId
  ) {
    throw new Error(`backup attachment schema or record identity was unavailable: ${JSON.stringify({
      schema,
      initialQuery,
    })}`);
  }
  const attachmentParams = {
    tableId: "tbl_e2e_backup_consistency",
    recordId,
    fieldId: attachmentColumn.fieldId,
  };
  const attachmentCell = page.locator('.tabulator-cell[tabulator-field="attachments"]').first();
  await attachmentCell.dblclick();
  let panel = page.getByTestId("attachment-panel");
  await page.getByTestId("attachment-upload").click();
  await panel.waitFor({ state: "hidden", timeout: 30_000 });
  const originalAttachmentResponse = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1
      && attachments[0]?.sha256 === expectedOriginalHash,
  );
  const beforeBackupAttachment = originalAttachmentResponse.payload.attachments[0];
  await attachmentCell.dblclick();
  await panel.waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("attachment-preview-0").waitFor({ timeout: 30_000 });
  await panel.locator("header button").click();

  const beforeBackupQuery = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_backup_consistency",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const beforeBackupRow = beforeBackupQuery.payload?.rows?.[0];
  const beforeBackupHistory = await rawBridgeRequest(page, "history.queryRequested", {
    collection: "tbl_e2e_backup_consistency",
    scope: "table",
    actions: [],
    limit: 100,
    offset: 0,
  }, 20_000, ["history.pageLoaded"]);
  recorder.check("pre-backup record, formula, attachment, and audit snapshot is complete",
    beforeBackupQuery.type === "query.page"
      && beforeBackupRow?.id === recordId
      && String(beforeBackupRow?.value) === "7"
      && String(beforeBackupRow?.doubled) === "14"
      && beforeBackupAttachment?.originalName === path.basename(original)
      && beforeBackupAttachment?.sha256 === expectedOriginalHash
      && beforeBackupAttachment?.size === originalBytes.length
      && beforeBackupHistory.type === "history.pageLoaded"
      && beforeBackupHistory.payload?.collection === "tbl_e2e_backup_consistency"
      && Array.isArray(beforeBackupHistory.payload?.changeSets)
      && beforeBackupHistory.payload.changeSets.length > 0
      && beforeBackupHistory.payload.total === beforeBackupHistory.payload.changeSets.length,
  {
    beforeBackupRow,
    beforeBackupAttachment,
    beforeBackupHistory: beforeBackupHistory.payload,
    expectedOriginalHash,
    expectedOriginalSize: originalBytes.length,
  });

  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-backup").click();
  await page.getByTestId("backup-create").click();
  await page.getByTestId("backup-status").waitFor({ timeout: 60_000 });
  const restoreButton = page.locator('[data-testid^="backup-restore-"]').first();
  await restoreButton.waitFor();
  const restoreTestId = await restoreButton.getAttribute("data-testid");
  recorder.check("backup was created through the product UI with a listed archive", Boolean(restoreTestId), {
    restoreTestId,
    status: await page.getByTestId("backup-status").innerText(),
  });

  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E Backup Consistency");
  valueEditor = await beginCellEdit(valueCell);
  await valueEditor.fill("9");
  await valueEditor.press("Enter");
  await formulaCell.filter({ hasText: "18" }).waitFor({ timeout: 30_000 });
  await attachmentCell.dblclick();
  panel = page.getByTestId("attachment-panel");
  await page.getByTestId("attachment-replace-0").click();
  await panel.waitFor({ state: "hidden", timeout: 30_000 });
  const replacementAttachmentResponse = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1
      && attachments[0]?.sha256 === expectedReplacementHash,
  );
  const changedQuery = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_backup_consistency",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const changedRow = changedQuery.payload?.rows?.[0];
  recorder.check("post-backup mutation changed record, formula, and attachment bytes",
    changedQuery.type === "query.page"
      && changedRow?.id === recordId
      && String(changedRow?.value) === "9"
      && String(changedRow?.doubled) === "18"
      && replacementAttachmentResponse.payload.attachments[0]?.originalName === path.basename(replacement)
      && replacementAttachmentResponse.payload.attachments[0]?.sha256 === expectedReplacementHash
      && replacementAttachmentResponse.payload.attachments[0]?.size === replacementBytes.length,
  {
    changedRow,
    replacementAttachment: replacementAttachmentResponse.payload.attachments[0],
    expectedReplacementHash,
    expectedReplacementSize: replacementBytes.length,
  });

  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-backup").click();
  await page.locator(`[data-testid="${restoreTestId}"]`).click();
  await page.getByTestId("backup-restore-confirmation").waitFor();
  await page.getByTestId("backup-restore-confirm").click();
  await page.getByTestId("backup-status").waitFor();

  await page.getByTestId("nav-tables").click();
  await waitForTableRecovery(page, "E2E Backup Consistency", 1, 90_000);
  const afterRestoreQuery = await rawBridgeRequest(page, "query.page", {
    tableId: "tbl_e2e_backup_consistency",
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const afterRestoreRow = afterRestoreQuery.payload?.rows?.[0];
  recorder.check("record and stored formula returned exactly to the backup snapshot",
    afterRestoreQuery.type === "query.page"
      && afterRestoreRow?.id === beforeBackupRow?.id
      && afterRestoreRow?.value === beforeBackupRow?.value
      && afterRestoreRow?.doubled === beforeBackupRow?.doubled,
  { beforeBackupRow, afterRestoreRow });
  const restoredAttachmentCell = page.locator('.tabulator-cell[tabulator-field="attachments"]').first();
  await restoredAttachmentCell.dblclick();
  panel = page.getByTestId("attachment-panel");
  await panel.waitFor({ state: "visible", timeout: 30_000 });
  const restoredAttachmentResponse = await waitForAttachmentList(
    page,
    attachmentParams,
    (attachments) => attachments.length === 1
      && attachments[0]?.sha256 === expectedOriginalHash,
  );
  const afterRestoreAttachment = restoredAttachmentResponse.payload.attachments[0];
  await page.getByTestId("attachment-preview-0").waitFor({
    state: "visible",
    timeout: 30_000,
  });
  const restoredAttachmentText = await panel.innerText();
  recorder.check("attachment name, hash, and content length returned exactly to the backup snapshot",
    restoredAttachmentText.includes("backup-original")
      && await page.getByTestId("attachment-preview-0").isVisible()
      && afterRestoreAttachment?.originalName === beforeBackupAttachment?.originalName
      && afterRestoreAttachment?.storedName === beforeBackupAttachment?.storedName
      && afterRestoreAttachment?.sha256 === expectedOriginalHash
      && afterRestoreAttachment?.size === originalBytes.length,
  {
      restoredAttachmentText,
      beforeBackupAttachment,
      afterRestoreAttachment,
      expectedOriginalHash,
      expectedOriginalSize: originalBytes.length,
  });
  await panel.locator("header button").click();
  const historyDrawerStartedAt = performance.now();
  await page.getByTestId("toolbar-history").click();
  await page.getByTestId("history-timeline").waitFor({ timeout: 30_000 });
  runtime.recordUiTiming(
    "history.drawer.initialLoad",
    performance.now() - historyDrawerStartedAt,
    { scope: "table", scenario: "12-backup-consistency" },
  );
  const afterRestoreHistory = await rawBridgeRequest(page, "history.queryRequested", {
    collection: "tbl_e2e_backup_consistency",
    scope: "table",
    actions: [],
    limit: 100,
    offset: 0,
  }, 20_000, ["history.pageLoaded"]);
  const allowedBackupRestoreAuditActions = new Set([]);
  const beforeChangeSetIds = new Set(
    beforeBackupHistory.payload.changeSets.map((changeSet) => changeSet.changeSetId),
  );
  const afterChangeSets = afterRestoreHistory.payload?.changeSets ?? [];
  const afterSnapshotChangeSets = afterChangeSets.filter(
    (changeSet) => beforeChangeSetIds.has(changeSet.changeSetId),
  );
  const addedChangeSets = afterChangeSets.filter(
    (changeSet) => !beforeChangeSetIds.has(changeSet.changeSetId),
  );
  const beforeAuditSnapshot = canonicalJsonText({
    collection: beforeBackupHistory.payload.collection,
    scope: beforeBackupHistory.payload.scope,
    itemId: beforeBackupHistory.payload.itemId ?? null,
    field: beforeBackupHistory.payload.field ?? null,
    changeSets: beforeBackupHistory.payload.changeSets,
    total: beforeBackupHistory.payload.total,
    capabilityHash: beforeBackupHistory.payload.capabilityHash,
    schemaRevision: beforeBackupHistory.payload.schemaRevision,
    hasMore: beforeBackupHistory.payload.hasMore,
  });
  const afterAuditSnapshot = canonicalJsonText({
    collection: afterRestoreHistory.payload?.collection,
    scope: afterRestoreHistory.payload?.scope,
    itemId: afterRestoreHistory.payload?.itemId ?? null,
    field: afterRestoreHistory.payload?.field ?? null,
    changeSets: afterSnapshotChangeSets,
    total: (afterRestoreHistory.payload?.total ?? -1) - addedChangeSets.length,
    capabilityHash: afterRestoreHistory.payload?.capabilityHash,
    schemaRevision: afterRestoreHistory.payload?.schemaRevision,
    hasMore: afterRestoreHistory.payload?.hasMore,
  });
  recorder.check("audit snapshot returned byte-for-byte equivalent normalized history after restore",
    afterRestoreHistory.type === "history.pageLoaded"
      && beforeAuditSnapshot === afterAuditSnapshot
      && addedChangeSets.every(
        (changeSet) => allowedBackupRestoreAuditActions.has(changeSet.action),
      )
      && afterRestoreHistory.payload?.total
        === beforeBackupHistory.payload.total + addedChangeSets.length,
  {
    beforeBackupHistory: beforeBackupHistory.payload,
    afterRestoreHistory: afterRestoreHistory.payload,
    beforeAuditSnapshot,
    afterAuditSnapshot,
    addedChangeSets,
    allowedBackupRestoreAuditActions: [...allowedBackupRestoreAuditActions],
  });

  // Visual acceptance is part of the product gate, not a source-only token
  // check. Exercise the user-facing theme control and capture the real
  // packaged WebView2 table, modal, and popover in dark mode.
  const historyClose = page.locator(".n-drawer-header__close").last();
  if (await historyClose.isVisible()) await historyClose.click();
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-general").click();
  await page.getByTestId("theme-select").click();
  await page.locator(".n-base-select-option")
    .filter({ hasText: /^(深色|Dark)$/u })
    .click();
  await page.locator("html.dark").waitFor({ timeout: 10_000 });
  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E Backup Consistency");
  const darkCell = page.locator(".tabulator-row .tabulator-cell").first();
  await darkCell.waitFor({ state: "visible", timeout: 10_000 });
  const [darkRootEvidence, darkTableSurface, darkCellSurface] = await Promise.all([
    page.locator("html").evaluate((element) => ({
      rootDark: element.classList.contains("dark"),
      colorScheme: getComputedStyle(element).colorScheme,
    })),
    page.locator(".tabulator-tableholder").evaluate(collectBrowserSurfaceEvidence),
    darkCell.evaluate(collectBrowserSurfaceEvidence),
  ]);
  const darkThemeEvidence = {
    root: darkRootEvidence,
    table: darkTableSurface,
    cell: darkCellSurface,
  };
  recorder.check("dark table uses the packaged semantic theme rather than light fallbacks",
    darkThemeEvidence.root.rootDark
      && darkThemeEvidence.root.colorScheme.includes("dark")
      && darkThemeEvidence.table.visible
      && darkThemeEvidence.table.effectiveBackground[3] >= 0.999
      && darkThemeEvidence.table.backgroundLuminance < 0.25
      && darkThemeEvidence.cell.visible
      && darkThemeEvidence.cell.effectiveBackground[3] >= 0.999
      && darkThemeEvidence.cell.backgroundLuminance < 0.25
      && darkThemeEvidence.cell.contrast >= 4.5,
  darkThemeEvidence);
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "12-dark-table.png"),
    fullPage: true,
  });

  await page.getByTestId("sidebar-new-table").click();
  await page.getByTestId("create-table-name-input").waitFor();
  const darkDialog = page.locator('[role="dialog"]')
    .filter({ has: page.getByTestId("create-table-name-input") });
  await page.waitForFunction(
    (dialog) => {
      const opacity = Number.parseFloat(getComputedStyle(dialog).opacity);
      return Number.isFinite(opacity) && opacity >= 0.999;
    },
    await darkDialog.elementHandle(),
  );
  const darkModalEvidence = {
    role: await darkDialog.getAttribute("role"),
    ariaModal: await darkDialog.getAttribute("aria-modal"),
    surface: await darkDialog.evaluate(collectBrowserSurfaceEvidence),
  };
  recorder.check("dark create-table modal has dialog semantics and a non-light surface",
    darkModalEvidence.role === "dialog"
      && darkModalEvidence.ariaModal === "true"
      && darkModalEvidence.surface.visible
      && darkModalEvidence.surface.effectiveBackground[3] >= 0.999
      && darkModalEvidence.surface.backgroundLuminance < 0.25
      && darkModalEvidence.surface.contrast >= 4.5,
  darkModalEvidence);
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "12-dark-modal.png"),
    fullPage: true,
  });
  await page.getByTestId("create-table-cancel").click();

  await page.getByTestId("toolbar-more").click();
  const darkPopover = page.locator(".n-dropdown-menu").last();
  await darkPopover.waitFor();
  await page.waitForFunction(
    (popover) => {
      const opacity = Number.parseFloat(getComputedStyle(popover).opacity);
      return Number.isFinite(opacity) && opacity >= 0.999;
    },
    await darkPopover.elementHandle(),
  );
  const darkPopoverEvidence = await darkPopover.evaluate(collectBrowserSurfaceEvidence);
  recorder.check("dark toolbar popover is visible and avoids a light surface",
    darkPopoverEvidence.visible
      && darkPopoverEvidence.effectiveBackground[3] >= 0.999
      && darkPopoverEvidence.backgroundLuminance < 0.25
      && darkPopoverEvidence.contrast >= 4.5,
  darkPopoverEvidence);
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "12-dark-popover.png"),
    fullPage: true,
  });
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
