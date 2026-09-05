import fs from "node:fs/promises";
import fsSync from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";
import process from "node:process";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";
import {
  acknowledgeExpectedSidecarRecoveryFailure,
  beginSidecarRecoveryNotificationFailureWindowInPage,
  releaseSidecarRecoveryNotificationFailureWindowInPage,
  settleSidecarRecoveryNotificationFailureWindowInPage,
  SidecarRecoveryContractError,
  SidecarRecoveryReadWindow,
} from "./bridge_failure_policy.mjs";
import {
  installBridgeDiagnosticsInPage,
  readBridgeDiagnosticsInPage,
} from "./bridge_diagnostics_instrumentation.mjs";
import { beginImportFaultOutcomeCapture, waitForFailedImportUi } from "./import_fault_outcome.mjs";
import {
  captureDialogFocusLeaseInPage,
  hasDialogFocusLeaseRestoredFocusInPage,
  readDialogFocusLeaseEvidenceInPage,
} from "./dialog_focus_terminal.mjs";
import {
  beginRawBridgeRequestInPage,
  isAppliedMutationResponse,
  postRawBridgeNotificationInPage,
  readRawBridgeRequestTerminalInPage,
  releaseRawBridgeRequestInPage,
  requestLifecycleWorkspaceV2InPage,
  requestWorkspaceV2InPage,
} from "./bridge_raw_request.mjs";
import {
  beginWorkspaceActivationCapture,
  waitForCapturedBridgeMessage,
} from "./bridge_capture_wait.mjs";
import { runScenario18RecoveryBoundary } from "./scenario18_recovery_boundary.mjs";
import { installTableMutationReceiptCaptureInPage } from "./table_mutation_receipt_capture.mjs";
import { activateWorkspaceAndWaitForDatabaseOpened } from "./workspace_activation_readiness.mjs";
import { waitForWorkspaceSearchRebuildTerminal } from "./workspace_search_terminal.mjs";
import { installWorkspaceV2MethodTerminalCaptureInPage } from "./workspace_v2_method_terminal.mjs";
import {
  collectBrowserSurfaceEvidence,
  sampleConnectedThemeSurfaces,
} from "./theme_surface_probe.mjs";

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
  for (const required of ["cdp-url", "scenario", "evidence-dir", "controls-dir", "data-root"]) {
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
      if (!passed) {
        let serialized = "";
        try {
          serialized = JSON.stringify(details);
        } catch {
          serialized = "<unserializable details>";
        }
        const suffix = serialized ? `: ${serialized.slice(0, 4_000)}` : "";
        throw new Error(`assertion failed: ${name}${suffix}`);
      }
    },
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

async function waitForShell(page, recorder, { requireDatabaseOpened = false } = {}) {
  await page.getByTestId("nav-home").waitFor({ state: "visible", timeout: 60_000 });
  const workspaceCenter = page.getByTestId("workspace-center");
  await workspaceCenter.waitFor({ state: "visible", timeout: 60_000 });
  recorder.check(
    "fresh device starts in Workspace Center before product data services",
    await page.getByTestId("home-view").isHidden(),
  );
  await page.getByTestId("workspace-create").click();
  const createModal = page.getByTestId("workspace-flow-modal");
  await createModal.waitFor({ state: "visible" });
  await createModal.locator("input").first().fill("E2E Product Workspace");
  await page.getByTestId("workspace-flow-confirm").click();
  const openCreatedWorkspace = workspaceCenter.getByRole("button", {
    name: /E2E Product Workspace/,
  });
  const creationOutcome = await Promise.race([
    openCreatedWorkspace.waitFor({ state: "visible", timeout: 60_000 })
      .then(() => ({ kind: "created" })),
    page.getByTestId("workspace-operation-error")
      .waitFor({ state: "visible", timeout: 60_000 })
      .then(async () => ({
        kind: "failed",
        message: await page.getByTestId("workspace-operation-error").innerText(),
        bridgeFailure: await page.evaluate(() => {
          const failures = window.__vibetableE2EBridgeDiagnostics?.failures ?? [];
          return failures.findLast((item) => item.requestType === "workspace.create") ?? null;
        }),
      })),
  ]);
  if (creationOutcome.kind === "failed") {
    throw new Error(`workspace creation failed: ${JSON.stringify(creationOutcome)}`);
  }
  recorder.check(
    "fresh-device workspace creation is projected into Workspace Center",
    await openCreatedWorkspace.isVisible(),
  );
  const waitForActivation = (timeoutMs) => Promise.race([
    workspaceCenter.waitFor({ state: "hidden", timeout: timeoutMs })
      .then(() => ({ kind: "opened" })),
    page.getByTestId("workspace-operation-error")
      .waitFor({ state: "visible", timeout: timeoutMs })
      .then(async () => ({
        kind: "failed",
        message: await page.getByTestId("workspace-operation-error").innerText(),
      })),
  ]);
  let databaseOpened = null;
  if (requireDatabaseOpened) {
    databaseOpened = await activateWorkspaceAndWaitForDatabaseOpened({
      beginCapture: (expectation) => beginWorkspaceActivationCapture(page, expectation),
      activate: () => openCreatedWorkspace.click(),
      waitForActivation,
    });
  } else {
    await openCreatedWorkspace.click();
    const activationOutcome = await waitForActivation(60_000);
    if (activationOutcome.kind === "failed") {
      throw new Error(`workspace activation failed: ${activationOutcome.message}`);
    }
  }
  await page.getByTestId("home-view").waitFor({ state: "visible" });
  await page.getByTestId("workspace-switcher").waitFor({ state: "visible" });
  recorder.check("real WebView2 renderer reached the home workspace",
    page.url().startsWith("https://app.vibetable.local/"), {
    url: page.url(),
  });
  return databaseOpened;
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

async function selectVisibleNOption(page, testId, label) {
  const select = page.getByTestId(testId);
  await select.locator(".n-base-selection").click();
  const option = page.locator(".n-base-select-option:visible")
    .filter({ hasText: label })
    .first();
  await option.waitFor({ state: "visible", timeout: 10_000 });
  await option.click();
  await option.waitFor({ state: "hidden", timeout: 10_000 });
}

async function selectVisibleNOptions(page, testId, labels) {
  const select = page.getByTestId(testId);
  const selection = select.locator(".n-base-selection");
  for (const label of labels) {
    const selectedChip = select.getByText(label, { exact: true }).first();
    if (await selectedChip.isVisible()) {
      continue;
    }

    let openMenu = page.locator(".n-base-select-menu:visible").first();
    if (!(await openMenu.count())) {
      await selection.waitFor({ state: "visible", timeout: 10_000 });
      await selection.click();
      openMenu = page.locator(".n-base-select-menu:visible").first();
      await openMenu.waitFor({ state: "visible", timeout: 10_000 });
    }

    const option = openMenu
      .locator(".n-base-select-option")
      .filter({ hasText: label })
      .first();
    await option.waitFor({ state: "visible", timeout: 10_000 });
    await option.click();
    await selectedChip.waitFor({ state: "visible", timeout: 10_000 });
  }
  const openMenu = page.locator(".n-base-select-menu:visible").first();
  if (await openMenu.count()) {
    await page.keyboard.press("Escape");
    await openMenu.waitFor({ state: "hidden" });
  }
}

async function addVisibleNTagOption(page, testId, label) {
  const select = page.getByTestId(testId);
  const selection = select.locator(".n-base-selection");
  await selection.waitFor({ state: "visible", timeout: 10_000 });
  await selection.click();
  const input = select.locator("input");
  await input.fill(label);
  const option = page.locator(".n-base-select-option:visible")
    .filter({ hasText: label })
    .first();
  await option.waitFor({ state: "visible", timeout: 10_000 });
  await option.click();
  await select.getByText(label, { exact: true }).first()
    .waitFor({ state: "visible", timeout: 10_000 });
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

async function confirmImportPreview(page, timeoutMs = 60_000) {
  const panel = page.getByTestId("import-preview-panel");
  await panel.waitFor({ state: "visible", timeout: timeoutMs });
  const confirmation = await panel.innerText();
  await page.getByTestId("import-confirm").click();
  return confirmation;
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
  let relationPair;
  if (logicalType === "relation") {
    const sourceSchema = await rawBridgeRequest(page, "schema.describe", {
      collection: tableId,
      requestGeneration: 0,
      accepts: [
        "vibetable.relation-capabilities.v1",
        "vibetable.lookup-query.v1",
      ],
    });
    const sourceDisplayFieldId = sourceSchema.payload?.schema?.primaryDisplayFieldId
      || sourceSchema.payload?.schema?.columns?.find(
        (column) => column.fieldId && column.kind !== "system",
      )?.fieldId;
    if (!sourceDisplayFieldId) {
      throw new Error(`relation source display field is unavailable for ${tableId}`);
    }
    relationPair = {
      reciprocalDisplayName: `${displayName} 来源`,
      reciprocalCardinality: "many",
      sourceDisplayFieldId,
    };
  }
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
    ...(relationPair ? { relationPair } : {}),
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
    contractVersion: "2.0",
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
  await page.evaluate(() => {
    window.__vibetableE2eEditSchemaRejections = [];
    window.chrome?.webview?.addEventListener("message", (event) => {
      let value = event.data;
      if (typeof value === "string") {
        try {
          value = JSON.parse(value);
        } catch {
          return;
        }
      }
      if (value?.type === "table.editRejected" && value?.payload?.operation === "editSchema") {
        window.__vibetableE2eEditSchemaRejections.push(value);
      }
    });
  });
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
        };
      }
      if (logicalType === "lookup") {
        const relation = created.find((field) => field.definition?.logicalType === "relation");
        draft.lookup = {
          path: [{ relationFieldId: relation.fieldId }],
          targetFieldId: target.field.fieldId,
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
        ?.definition?.lookup?.path?.[0]?.relationFieldId
        === created.find((field) => field.definition?.logicalType === "relation")?.fieldId,
    { computed },
  );

  await selectTable(page, "E2E Field Settings V2");
  const titleField = created.find((field) => field.definition?.logicalType === "text");
  const undoSeed = await applyProductMutation(page, tableId, [{
    kind: "insert",
    recordId: null,
    values: {},
  }], "e2e-undo-seed");
  if (undoSeed.payload?.status !== "applied") {
    throw new Error(`undo seed row was not committed: ${JSON.stringify(undoSeed)}`);
  }
  await waitForVisibleRowCount(page, 1);
  await page.evaluate(() => {
    window.__vibetableE2eEditCommitted = false;
    window.__vibetableE2eEditOutcome = null;
    window.chrome?.webview?.addEventListener("message", (event) => {
      let value = event.data;
      if (typeof value === "string") {
        try {
          value = JSON.parse(value);
        } catch {
          return;
        }
      }
      if (value?.type === "table.editCommitted") {
        window.__vibetableE2eEditCommitted = true;
      }
      if (["table.editCommitted", "table.editRejected", "operation.failed"].includes(value?.type)) {
        window.__vibetableE2eEditOutcome = value;
      }
    });
  });
  const undoCell = page.locator(
    `.tabulator-cell[tabulator-field="${titleField.physicalName}"]`,
  ).first();
  let undoEditor;
  try {
    undoEditor = await beginCellEdit(undoCell);
  } catch (error) {
    const editSchemaRejections = await page.evaluate(
      () => window.__vibetableE2eEditSchemaRejections,
    );
    throw new Error(
      `${error.message}; editSchemaRejections=${JSON.stringify(editSchemaRejections)}`,
    );
  }
  await undoEditor.fill("undo-this-data-edit");
  await undoEditor.press("Enter");
  try {
    await waitForQueryPage(
      page,
      {
        tableId,
        query: { filters: [], sorts: [], offset: 0, limit: 100 },
      },
      (payload) => payload?.rows?.length === 1
        && payload.rows[0]?.[titleField.physicalName] === "undo-this-data-edit",
    );
  } catch (error) {
    const editOutcome = await page.evaluate(() => window.__vibetableE2eEditOutcome);
    throw new Error(`${error.message}; editOutcome=${JSON.stringify(editOutcome)}`);
  }
  await page.waitForFunction(
    ({ field, value }) => document.querySelector(
      `.tabulator-cell[tabulator-field="${field}"]`,
    )?.textContent?.includes(value),
    { field: titleField.physicalName, value: "undo-this-data-edit" },
    { timeout: 30_000 },
  );
  // QueryPort and the optimistic grid can both expose the new value before
  // the host's commit notification creates the renderer history entry. Wait
  // for that public transport boundary before exercising Ctrl+Z.
  await page.waitForFunction(() => window.__vibetableE2eEditCommitted === true);
  // Enter can leave the detached editor as document.activeElement for one
  // renderer turn. Clicking the committed cell would start a contenteditable
  // Tabulator editor again, and the product intentionally ignores global
  // shortcuts while an editor owns focus. Move focus outside the grid first.
  await page.getByTestId("toolbar-table-title").click();
  await page.waitForFunction(() => {
    const active = document.activeElement;
    const tag = active?.tagName.toLowerCase();
    return tag !== "input"
      && tag !== "textarea"
      && tag !== "select"
      && !(active instanceof HTMLElement && active.isContentEditable);
  });
  const undoShortcutHandled = await page.evaluate(() => {
    const event = new KeyboardEvent("keydown", {
      key: "z",
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    });
    document.dispatchEvent(event);
    return event.defaultPrevented;
  });
  recorder.check("Ctrl+Z is accepted by the table shortcut layer", undoShortcutHandled);
  // A data/relation refresh can briefly rebuild the Tabulator DOM. Waiting
  // for the cell text to disappear first would mistake that transient absence
  // for a committed undo. Poll the authoritative QueryPort boundary first;
  // only after the value is gone from storage may the renderer assertion pass.
  const rowsAfterUndoResponse = await waitForQueryPage(
    page,
    {
      tableId,
      query: { filters: [], sorts: [], offset: 0, limit: 100 },
    },
    (payload) => payload?.rows?.length === 1
      && payload.rows[0]?.[titleField.physicalName] !== "undo-this-data-edit",
  );
  const rowsAfterUndo = rowsAfterUndoResponse.payload;
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
    rowsAfterUndo?.rows?.length === 1
      && rowsAfterUndo.rows[0]?.[titleField.physicalName] !== "undo-this-data-edit"
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
    contractVersion: "2.0",
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
  await acknowledgeExpectedBridgeFailure(page, legacyWrite);
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
        const operationId = crypto.randomUUID();
        const requestId = `e2e-${operationId}`;
        const wirePort = window.__vibetableE2EWorkspaceWirePort;
        if (!wirePort) {
          reject(new Error(`workspace wire E2E port unavailable for ${requestType}`));
          return;
        }
        let scope;
        try {
          scope = wirePort.reserve(operationId);
        } catch (error) {
          reject(error);
          return;
        }
        const envelope = {
          type: requestType,
          requestId,
          payload: requestPayload,
          scope,
        };
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
        window.chrome.webview.postMessage(envelope);
      }),
    { type, payload, timeout: timeoutMs, responseTypes: expectedResponseTypes },
  );
}

async function rawWorkspaceV2Request(
  page,
  method,
  params,
) {
  return page.evaluate(
    requestWorkspaceV2InPage,
    { method, params },
  );
}

async function rawLifecycleWorkspaceV2Request(
  page,
  method,
  params,
  timeoutMs = 20_000,
) {
  return page.evaluate(
    requestLifecycleWorkspaceV2InPage,
    { method, params, timeoutMs },
  );
}

async function installBridgeDiagnostics(page) {
  await page.evaluate(installBridgeDiagnosticsInPage);
}

async function readBridgeDiagnostics(page) {
  return page.evaluate(readBridgeDiagnosticsInPage);
}

async function waitForBridgeDiagnosticsToSettle(
  page,
  { timeoutMs = 10_000, quietMs = 250 } = {},
) {
  const deadline = Date.now() + timeoutMs;
  let quietSince = null;
  let diagnostics = null;
  while (Date.now() < deadline) {
    diagnostics = await readBridgeDiagnostics(page);
    const failures = diagnostics?.failures ?? [];
    const pending = diagnostics?.pending ?? [];
    if (failures.length > 0) return diagnostics;
    if (pending.length === 0) {
      quietSince ??= Date.now();
      if (Date.now() - quietSince >= quietMs) return diagnostics;
    } else {
      quietSince = null;
    }
    await page.waitForTimeout(50);
  }
  return diagnostics ?? await readBridgeDiagnostics(page);
}

async function acknowledgeExpectedBridgeFailure(page, response) {
  const requestId = response?.requestId;
  if (typeof requestId !== "string") {
    throw new Error(`expected bridge failure has no requestId: ${JSON.stringify(response)}`);
  }
  const acknowledged = await page.evaluate((id) => {
    const diagnostics = window.__vibetableE2EBridgeDiagnostics;
    if (!diagnostics) return false;
    const index = diagnostics.failures.findIndex((failure) => failure.requestId === id);
    if (index < 0) return false;
    diagnostics.acknowledgedFailures ??= [];
    diagnostics.acknowledgedFailures.push(...diagnostics.failures.splice(index, 1));
    return true;
  }, requestId);
  if (!acknowledged) {
    throw new Error(`expected bridge failure was not recorded: ${requestId}`);
  }
}

async function acknowledgeExpectedBridgeFailureByCodeIfPresent(page, code) {
  const failure = await page.evaluate((expectedCode) => {
    const diagnostics = window.__vibetableE2EBridgeDiagnostics;
    return diagnostics?.failures.find((item) => item.code === expectedCode) ?? null;
  }, code);
  if (failure) await acknowledgeExpectedBridgeFailure(page, failure);
  return failure;
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

async function beginWorkspaceV2MethodCapture(page, method) {
  await page.evaluate(installWorkspaceV2MethodTerminalCaptureInPage, method);
}

async function beginWritableWorkspaceBootstrapCapture(
  page,
  minimumExclusiveEpoch,
  failureMethod = null,
) {
  await page.evaluate(({ minimumEpoch, expectedFailureMethod }) => {
    window.__vibetableE2EBridgeCapture = {
      types: ["workspace.v2.bootstrap"],
      message: null,
      error: null,
    };
    window.chrome.webview.addEventListener("message", function handler(event) {
      let message = event.data;
      if (typeof message === "string") {
        try { message = JSON.parse(message); } catch { return; }
      }
      if (expectedFailureMethod
        && message?.type === "workspace.v2.response"
        && message.payload?.method === expectedFailureMethod
        && message.payload?.ok === false) {
        window.__vibetableE2EBridgeCapture.error = {
          method: expectedFailureMethod,
          code: message.payload?.error?.code ?? "unknown",
          message: message.payload?.error?.message ?? "workspace operation failed",
        };
        window.chrome.webview.removeEventListener("message", handler);
        return;
      }
      const session = message?.payload?.session;
      if (
        message?.type !== "workspace.v2.bootstrap"
        || session?.state !== "openedWritable"
        || session?.writable !== true
        || !Number.isInteger(session?.sessionEpoch)
        || session.sessionEpoch <= minimumEpoch
      ) {
        return;
      }
      window.__vibetableE2EBridgeCapture.message = message;
      window.chrome.webview.removeEventListener("message", handler);
    });
  }, { minimumEpoch: minimumExclusiveEpoch, expectedFailureMethod: failureMethod });
}

async function beginRawBridgeRequest(
  page,
  type,
  payload,
) {
  return page.evaluate(
    beginRawBridgeRequestInPage,
    {
      requestType: type,
      requestPayload: payload,
    },
  );
}

async function postRawBridgeNotification(page, type, payload) {
  await page.evaluate(
    postRawBridgeNotificationInPage,
    {
      requestType: type,
      requestPayload: payload,
    },
  );
}

async function observeRawBridgeRequest(page, requestId, timeoutMs) {
  // Observation expiry must not cancel the in-page request listener. Recovery
  // ownership later settles the same requestId against its real terminal.
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const result = await page.evaluate(
      readRawBridgeRequestTerminalInPage,
      { requestId },
    );
    if (result) return result;
    await page.waitForTimeout(25);
  }
  return null;
}

async function releaseRawBridgeRequest(page, requestId) {
  await page.evaluate(releaseRawBridgeRequestInPage, { requestId });
}

function attachCleanupFailure(primaryError, cleanupError, message) {
  if (!(primaryError instanceof Error)) return false;
  primaryError.cause = primaryError.cause === undefined
    ? cleanupError
    : new AggregateError([primaryError.cause, cleanupError], message);
  return true;
}

async function waitForRawBridgeRequest(page, requestId, timeoutMs = 20_000) {
  let primaryError = null;
  try {
    const result = await observeRawBridgeRequest(page, requestId, timeoutMs);
    if (result) return result;
    throw new Error(`bridge response timeout for ${requestId}`);
  } catch (error) {
    primaryError = error;
    throw error;
  } finally {
    try {
      await releaseRawBridgeRequest(page, requestId);
    } catch (cleanupError) {
      if (!attachCleanupFailure(
        primaryError,
        cleanupError,
        `bridge request cleanup also failed: ${requestId}`,
      )) {
        throw cleanupError;
      }
    }
  }
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
  const previewRoot = path.join(runtime.dataRoot, "attachment-preview");
  const deadline = Date.now() + timeoutMs;
  let observed = [];
  while (Date.now() < deadline) {
    observed = [];
    for (const candidate of await listFilesRecursively(previewRoot)) {
      if (/^\.vibetable-attachment-.*\.part$/u.test(path.basename(candidate))) continue;
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

async function waitForPublishedPreviewArtifact(runtime, expectedHash, expectedSize, page) {
  const previewResult = await waitForCapturedBridgeMessage(page, 30_000);
  const previewArtifact = await waitForPreviewArtifact(runtime, expectedHash, expectedSize);
  return { previewArtifact, previewResult };
}

async function requestStorageProof(runtime, tableId, timeoutMs = 30_000) {
  const requestPath = path.join(runtime.evidenceDir, "storage-proof-request.json");
  const resultPath = path.join(runtime.evidenceDir, "storage-proof-result.json");
  const requestId = crypto.randomUUID();
  await fs.writeFile(
    requestPath,
    `${JSON.stringify({
      requestId,
      tableId,
      requestedAt: new Date().toISOString(),
    }, null, 2)}\n`,
    "utf8",
  );
  const deadline = Date.now() + timeoutMs;
  let result = null;
  while (Date.now() < deadline) {
    try {
      result = JSON.parse(await fs.readFile(resultPath, "utf8"));
      if (result.requestId !== requestId) {
        await new Promise((resolve) => setTimeout(resolve, 50));
        continue;
      }
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
    contractVersion: "2.0",
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
  await acknowledgeExpectedBridgeFailure(page, legacy);

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

function controlOwnJsonReconcileInPage({ operation, tableId, field }) {
  const key = "__vibetableE2EOwnJsonReconcile";
  if (operation !== "start") return window[key]?.[operation]() ?? null;
  const webview = window.chrome.webview;
  const originalPostMessage = webview.postMessage;
  const state = { heldRequestId: null, forwards: 0, released: null, error: null };
  let held = null;
  let deadline = null;
  let rowKey = null;
  let revision = null;
  let schemaRevision = null;
  let reconciled = false;
  let refreshes = 0;
  let datasetReady = false;
  let schemaReady = false;
  const parse = (value) => {
    if (typeof value !== "string") return value;
    try { return JSON.parse(value); } catch { return null; }
  };
  const forward = (reason) => {
    if (!held) return;
    const pending = held;
    held = null;
    clearTimeout(deadline);
    state.released = reason;
    state.forwards += 1;
    return originalPostMessage.apply(pending.receiver, pending.args);
  };
  function postMessage(...args) {
    const message = parse(args[0]);
    if (rowKey === null && message?.type === "table.updateCellRequested"
      && message.payload?.table === tableId && message.payload?.column === field) {
      rowKey = message.payload.rowKey;
    }
    if (rowKey !== null && message?.type === "events.reconcile"
      && message.payload?.tableId === tableId) {
      if (held) {
        state.error ??= "controlled reconcile received a competing request";
      } else if (state.heldRequestId === null) {
        state.heldRequestId = message.requestId;
        held = { receiver: this, args };
        deadline = setTimeout(() => {
          state.error ??= "controlled reconcile hold exceeded 8 seconds";
          forward("deadline");
        }, 8_000);
        return;
      } else {
        state.error ??= "controlled reconcile received a competing request";
      }
    }
    if (rowKey !== null && message?.type === "table.selected" && message.payload?.table === tableId) {
      refreshes += 1;
      if (state.released !== "after-selection" || refreshes !== 1) {
        state.error ??= "controlled reconcile observed an unexpected table refresh";
      }
    }
    return originalPostMessage.apply(this, args);
  }
  const receive = (event) => {
    const message = parse(event.data);
    const payload = message?.payload;
    if (message?.type === "table.editCommitted" && payload?.rowKey === rowKey
      && payload.column === field && payload.storedValue?.nested?.value === 7) {
      revision = payload.revision;
    }
    if (message?.type === "events.reconcile" && message.requestId === state.heldRequestId) {
      reconciled = payload?.action === "refresh-data";
      if (!reconciled) state.error ??= "controlled reconcile did not return refresh-data";
    }
    if (state.released === "after-selection" && refreshes === 1 && payload?.table === tableId) {
      if (message?.type === "table.datasetReady" && payload.revision?.dataRevision >= revision?.dataRevision
        && payload.rows?.some((row) => row.rowKey === rowKey && row[field]?.nested?.value === 7)) {
        datasetReady = true;
        schemaRevision = payload.revision.schemaRevision;
      }
      if (message?.type === "table.editSchemaLoaded" && payload.schemaRevision === schemaRevision) {
        schemaReady = true;
      }
    }
  };
  const ready = (afterRefresh) => {
    if (state.error) throw new Error(state.error);
    const cell = document.querySelector(
      `.grid-wrapper[aria-busy="false"] .tabulator-cell[tabulator-field="${field}"]`,
    );
    return Boolean(cell && revision && (afterRefresh
      ? reconciled && refreshes === 1 && datasetReady && schemaReady
      : held));
  };
  webview.postMessage = postMessage;
  webview.addEventListener("message", receive);
  window[key] = {
    "selection-ready": () => ready(false),
    "refresh-ready": () => ready(true),
    release: () => {
      if (!held || state.error) throw new Error(state.error ?? "controlled reconcile was not held");
      forward("after-selection");
    },
    stop: () => {
      try { forward("cleanup"); } finally {
        clearTimeout(deadline);
        webview.removeEventListener("message", receive);
        if (webview.postMessage === postMessage) webview.postMessage = originalPostMessage;
        else state.error ??= "controlled reconcile postMessage owner changed";
        delete window[key];
      }
      return { ...state, reconciled, refreshes, datasetReady, schemaReady };
    },
  };
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
  const focusLease = await page.evaluate(captureDialogFocusLeaseInPage, {
    operation: "capture",
    target: "json",
  });
  await page.keyboard.press("Escape");
  await page.getByTestId("json-editor-modal").waitFor({ state: "hidden" });
  let focusRestorationObserved = false;
  let focusRestoration = null;
  let focusRestorationWaitError = null;
  try {
    const restoredFocus = await page.waitForFunction(
      hasDialogFocusLeaseRestoredFocusInPage,
      {
        operation: "has-restored-focus",
        capture: focusLease,
        field: jsonField,
        occurrence: 0,
      },
      { timeout: 10_000 },
    );
    try {
      focusRestoration = await restoredFocus.jsonValue();
    } finally {
      await restoredFocus.dispose();
    }
    focusRestorationObserved = focusRestoration?.restored === true;
  } catch (error) {
    focusRestorationWaitError = error instanceof Error
      ? { name: error.name, message: error.message }
      : { name: "UnknownError", message: String(error) };
  }
  const focusLeaseEvidence = await page.evaluate(
    readDialogFocusLeaseEvidenceInPage,
    { operation: "read-evidence", capture: focusLease },
  );
  if (focusRestoration === null) {
    focusRestoration = await page.evaluate(() => ({
      documentHasFocus: document.hasFocus(),
      restored: false,
      activeTag: document.activeElement?.tagName ?? null,
      activeClass: document.activeElement instanceof HTMLElement
        ? document.activeElement.className
        : null,
      activeField: document.activeElement instanceof HTMLElement
        ? document.activeElement.getAttribute("tabulator-field")
        : null,
      activeRole: document.activeElement instanceof HTMLElement
        ? document.activeElement.getAttribute("role")
        : null,
      activeTestId: document.activeElement instanceof HTMLElement
        ? document.activeElement.getAttribute("data-testid")
        : null,
    }));
  }
  jsonCell = page.locator(".grid-host > .tabulator-mount.tabulator")
    .locator(`.tabulator-cell[tabulator-field="${jsonField}"]`)
    .nth(0);
  await jsonCell.waitFor({ timeout: 10_000 });
  recorder.check(
    "Escape closes the structured modal and restores focus when the renderer owns OS focus",
    focusRestorationObserved
      && focusRestoration.documentHasFocus
      && focusLeaseEvidence.terminal?.state === "restored"
      && focusRestoration.restored,
    { focusLeaseEvidence, focusRestoration, focusRestorationWaitError },
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
  await page.evaluate(controlOwnJsonReconcileInPage, { operation: "start", tableId, field: jsonField });
  try {
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
    await page.waitForFunction(
      controlOwnJsonReconcileInPage,
      { operation: "selection-ready" },
      { timeout: 30_000 },
    );
    jsonCell = page.locator(`.tabulator-cell[tabulator-field="${jsonField}"]`).first();
    await jsonCell.click();
    const selectedBeforeRefresh = await jsonCell.evaluate((cell) => ({
      range: cell.getAttribute("data-range"),
      selected: cell.getAttribute("aria-selected"),
    }));
    recorder.check("JSON Payload range selection is active before its reconcile refresh",
      selectedBeforeRefresh.range === "0" && selectedBeforeRefresh.selected === "true",
      { selectedBeforeRefresh },
    );
    await page.evaluate(controlOwnJsonReconcileInPage, { operation: "release" });
    await page.waitForFunction(
      controlOwnJsonReconcileInPage,
      { operation: "refresh-ready" },
      { timeout: 30_000 },
    );
    const rangeSelection = await jsonCell.evaluate((cell) => ({
      range: cell.getAttribute("data-range"),
      selected: cell.getAttribute("aria-selected"),
    }));
    recorder.check("JSON Payload range selection survives its own reconcile refresh",
      rangeSelection.range === "0" && rangeSelection.selected === "true",
      { rangeSelection },
    );
    await page.keyboard.press("Control+V");
    await page.getByTestId("paste-panel").waitFor({ timeout: 30_000 });
  } finally {
    const controlledReconcile = await page.evaluate(
      controlOwnJsonReconcileInPage,
      { operation: "stop" },
    );
    recorder.check("JSON reconcile control forwarded exactly one original request and restored the bridge",
      controlledReconcile?.error === null
        && controlledReconcile.forwards === 1
        && controlledReconcile.released === "after-selection"
        && controlledReconcile.reconciled
        && controlledReconcile.refreshes === 1
        && controlledReconcile.datasetReady
        && controlledReconcile.schemaReady,
      { controlledReconcile },
    );
  }
  const ack = page.getByTestId("paste-ack");
  if (await ack.isVisible().catch(() => false)) await ack.click();
  await page.getByTestId("paste-confirm").click();
  await page.locator('[data-testid="paste-panel"][data-phase="applied"]').waitFor({
    timeout: 30_000,
  });
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
  await chooseToolbarMore(page, "import");
  const importConfirmation = await confirmImportPreview(page);
  const importOutcome = await waitForImportSuccess(page, 1);
  recorder.check("JSON import completed with one explicitly reported committed row",
    importOutcome === "Imported 1 row(s)." || importOutcome === "已导入 1 行。",
    { importConfirmation, importOutcome });
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

  await chooseToolbarMore(page, "export-csv");
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
  await createV2Field(page, articleTableId, "Title", "text");
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
  const bridgeBeforeSidecarRestart = await waitForBridgeDiagnosticsToSettle(page);
  const failuresBeforeSidecarRestart = bridgeBeforeSidecarRestart?.failures ?? [];
  const pendingBeforeSidecarRestart = bridgeBeforeSidecarRestart?.pending ?? [];
  recorder.check(
    "attachment sidecar restart begins from a quiescent bridge",
    bridgeBeforeSidecarRestart !== null
      && failuresBeforeSidecarRestart.length === 0
      && pendingBeforeSidecarRestart.length === 0,
    {
      failures: failuresBeforeSidecarRestart,
      pending: pendingBeforeSidecarRestart,
    },
  );
  const recoveryFailureOwnerToken = `attachment-recovery-${crypto.randomUUID()}`;
  await page.evaluate(beginSidecarRecoveryNotificationFailureWindowInPage, {
    ownerToken: recoveryFailureOwnerToken,
    tableId,
  });
  let recoveryPrimaryError = null;
  try {
    const restart = await requestSidecarKill(runtime, "verify attachment survives sidecar restart");
    recorder.check("attachment restart terminated only the exact sidecar child",
      restart.processName === "vibetable-pb.exe", { restart });
    await waitForTableRecovery(
      page,
      "E2E Attachments",
      tableId,
      1,
      60_000,
      recoveryFailureOwnerToken,
    );
  } catch (error) {
    recoveryPrimaryError = error;
    throw error;
  } finally {
    try {
      await page.evaluate(releaseSidecarRecoveryNotificationFailureWindowInPage, {
        ownerToken: recoveryFailureOwnerToken,
      });
    } catch (cleanupError) {
      if (!attachCleanupFailure(
        recoveryPrimaryError,
        cleanupError,
        "sidecar recovery notification failure window cleanup also failed",
      )) {
        throw cleanupError;
      }
    }
  }
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
  await beginBridgeMessageCapture(page, ["file.previewRequested"]);
  await page.getByTestId("attachment-preview-0").click();
  const { previewArtifact, previewResult } = await waitForPublishedPreviewArtifact(
    runtime,
    expectedOriginalHash,
    originalBytes.length,
    page,
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
  recorder.check(
    "native attachment preview reaches one correlated capability outcome",
    previewResult.type === "file.previewRequested"
      && typeof previewResult.requestId === "string"
      && (previewResult.payload?.outcome === "opened"
        || (previewResult.payload?.outcome === "unavailable"
          && previewResult.payload?.reason === "PREVIEW_HANDLER_UNAVAILABLE")),
    { previewResult },
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
  const attachmentHistoryReply = await rawWorkspaceV2Request(
    page,
    "history.query",
    {
      collection: tableId,
      scope: "cell",
      itemId: recordId,
      field: attachmentField,
      search: "",
      dateFrom: null,
      dateTo: null,
      actorId: null,
      recordId: null,
      limit: 50,
      offset: 0,
      actions: [],
    },
  );
  const attachmentHistoryProbe = attachmentHistoryReply.result;
  const originalRevision = attachmentHistoryProbe.changeSets.find((changeSet) =>
    changeSet.scalarChanges?.some((change) =>
      change.field === attachmentField
        && String(change.after ?? "").includes(uploadedFile.storedName)),
  )?.rootRevisionId;
  if (!originalRevision) {
    throw new Error(
      `original attachment revision was not returned: ${JSON.stringify(attachmentHistoryProbe)}`,
    );
  }
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
  const restorePreview = page.getByTestId("restore-preview");
  await restorePreview.waitFor();
  const restorePreviewText = await restorePreview.innerText();
  recorder.check(
    "attachment restore preview identifies the original managed file through the product UI",
    restorePreviewText.includes(attachmentField)
      && restorePreviewText.includes(uploadedFile.storedName)
      && await page.getByTestId("restore-confirm").isEnabled(),
    { originalRevision, restorePreviewText },
  );
  await beginBridgeMessageCapture(
    page,
    ["workspace.v2.response", "operation.failed"],
  );
  await page.getByTestId("restore-confirm").click();
  const appliedMessage = await waitForCapturedBridgeMessage(page);
  recorder.check(
    "attachment restore UI receives a coordinated Workspace V2 mutation receipt",
    appliedMessage.type === "workspace.v2.response"
      && appliedMessage.payload?.method === "history.applyRestore"
      && appliedMessage.payload?.ok === true
      && appliedMessage.payload?.result?.restoredToRevision === originalRevision
      && typeof appliedMessage.payload?.result?.newRevisionId === "string"
      && Number.isInteger(appliedMessage.payload?.result?.mutationRevision)
      && appliedMessage.payload.result.mutationRevision > 0,
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
    contractVersion: "2.0",
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
    isAppliedMutationResponse(competitor), { competitor });
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
  await postRawBridgeNotification(page, "table.updateCellRequested", {
    table: tableId,
    rowKey: row.id,
    column: valueField,
    oldValue: row[valueField],
    newValue: "stale-user-write",
    expectedDigest: row.__vibetableDigest,
    schemaRevision: pageResult.snapshot.schemaRevision,
  });
  const rejected = await waitForCapturedBridgeMessage(page);
  recorder.check(
    "stale renderer mutation was rejected by the product table boundary",
    rejected.type === "table.editRejected"
      && rejected.requestId === null
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

async function requestPackagedProcessKill(runtime, action, reason) {
  const requestPath = path.join(runtime.evidenceDir, "fault-request.json");
  const resultPath = path.join(runtime.evidenceDir, "fault-result.json");
  const requestId = crypto.randomUUID();
  await fs.writeFile(requestPath, `${JSON.stringify({
    requestId,
    action,
    reason,
    requestedAt: new Date().toISOString(),
  }, null, 2)}\n`, "utf8");
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    try {
      const result = JSON.parse(await fs.readFile(resultPath, "utf8"));
      if (result.requestId !== requestId) {
        await new Promise((resolve) => setTimeout(resolve, 50));
        continue;
      }
      if (result.status !== "completed") {
        throw new Error(`${action} fault request failed: ${JSON.stringify(result)}`);
      }
      return result;
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`Python orchestrator did not acknowledge the ${action} fault request`);
}

async function requestSidecarKill(runtime, reason) {
  return requestPackagedProcessKill(runtime, "kill-sidecar", reason);
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
    "export-csv": /导出 CSV|Export CSV/i,
    "export-xlsx": /导出 XLSX|Export XLSX/i,
    refresh: /刷新|Refresh/i,
  };
  if (!labels[key]) throw new Error(`missing toolbar menu label mapping: ${key}`);
  await page.getByTestId("toolbar-more").click();
  const option = page.locator(".n-dropdown-option-body")
    .filter({ hasText: labels[key] })
    .last();
  await option.waitFor();
  await option.click();
}

async function waitForTableRecovery(
  page,
  displayName,
  tableId,
  expectedRows,
  timeoutMs = 60_000,
  recoveryFailureOwnerToken = null,
) {
  const deadline = Date.now() + timeoutMs;
  const recoveryReads = new SidecarRecoveryReadWindow({
    deadlineAt: deadline,
    observeTerminal: (requestId, observationMs) =>
      observeRawBridgeRequest(page, requestId, observationMs),
    releaseRequest: requestId => releaseRawBridgeRequest(page, requestId),
    acknowledge: response => acknowledgeExpectedBridgeFailure(page, response),
  });
  let lastError;
  let primaryError = null;
  try {
    while (Date.now() < deadline) {
      try {
        const retry = page.getByTestId("connection-retry");
        if (await retry.isVisible().catch(() => false)) await retry.click();
        const name = page.getByTestId("sidebar-table-name").filter({ hasText: displayName });
        if (await name.isVisible().catch(() => false)) {
          const tableButton = name.locator("xpath=ancestor::button");
          if (await tableButton.getAttribute("aria-current") !== "page") {
            await tableButton.click();
          }
        }
        await page.waitForTimeout(750);
        const count = await page.locator(".tabulator-row").count();
        const requestId = await beginRawBridgeRequest(page, "query.page", {
          tableId,
          query: { filters: [], sorts: [], offset: 0, limit: 100 },
        });
        recoveryReads.own(requestId);
        const backend = await recoveryReads.observe(requestId);
        if (backend === null) {
          lastError = new Error(
            `query.page observation expired during sidecar recovery: ${requestId}`,
          );
          continue;
        }
        const backendRows = backend.type === "query.page"
          && Array.isArray(backend.payload?.rows)
          ? backend.payload.rows.length
          : null;
        if (
          count === expectedRows
          && backendRows === expectedRows
          && typeof backend.payload?.snapshot?.schemaRevision === "string"
        ) {
          const errorOverlay = page.getByTestId("table-error-overlay");
          if (await errorOverlay.isVisible().catch(() => false)) {
            await name.locator("xpath=ancestor::button").click();
            await errorOverlay.waitFor({ state: "hidden", timeout: 10_000 });
            await page.waitForTimeout(250);
          }
          const recoveredCount = await page.locator(".tabulator-row").count();
          if (recoveredCount === expectedRows) {
            await recoveryReads.settle();
            if (recoveryFailureOwnerToken !== null) {
              const failureWindow = await page.evaluate(
                settleSidecarRecoveryNotificationFailureWindowInPage,
                { ownerToken: recoveryFailureOwnerToken, deadlineAt: deadline },
              );
              if (failureWindow.state !== "settled") {
                throw new SidecarRecoveryContractError(
                  `sidecar recovery notification failure window did not settle: ${failureWindow.state}`,
                );
              }
            }
            return recoveredCount;
          }
        }
        lastError = new Error(`table did not recover: ${JSON.stringify({
          tableId,
          observedUiRows: count,
          observedBackendRows: backendRows,
          expectedRows,
          backendType: backend.type,
        })}`);
      } catch (error) {
        if (error instanceof SidecarRecoveryContractError) throw error;
        lastError = error;
      }
    }
    throw lastError ?? new Error("table did not recover");
  } catch (error) {
    primaryError = error;
    throw error;
  } finally {
    try {
      await recoveryReads.close();
    } catch (cleanupError) {
      if (!attachCleanupFailure(
        primaryError,
        cleanupError,
        "sidecar recovery cleanup also failed",
      )) {
        throw cleanupError;
      }
    }
  }
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
    } catch {
      // The host is intentionally restarting. Keep the selected table intact
      // and retry only the closed read contract; never click/reselect the UI.
      await page.waitForTimeout(250);
      continue;
    }
    if (
      lastResponse.type === "query.page"
      && lastResponse.payload?.rows?.length === expectedRows
      && lastResponse.payload?.snapshot?.schemaRevision
    ) {
      return lastResponse.payload;
    }
    await acknowledgeExpectedSidecarRecoveryFailure(
      lastResponse,
      response => acknowledgeExpectedBridgeFailure(page, response),
    );
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
  await chooseToolbarMore(page, "import");
  const importFaultCapture = await beginImportFaultOutcomeCapture(page);
  let importFaultPrimaryError = null;
  let confirmation;
  let barrier;
  let fault;
  let rowCount;
  try {
    confirmation = await confirmImportPreview(page);
    const importTask = await importFaultCapture.waitForCreatedTask();
    barrier = await waitForMutationBarrier(runtime);
    const faultDeadline = Date.now() + 60_000;
    await importFaultCapture.openFaultWindow({ deadlineAt: faultDeadline });
    fault = await requestSidecarKill(runtime, "interrupt active 1k-row import");
    const verifiedFault = fault.status === "completed"
      && fault.processName === "vibetable-pb.exe"
      && fault.pid === barrier.pid
      && barrier.point === "after_record";
    recorder.check("the exact sidecar was killed after its first uncommitted transactional record write",
      verifiedFault,
    { fault, barrier, confirmation, importTask });
    const failedUi = await waitForFailedImportUi(page, faultDeadline);
    recorder.check("faulted import reports a non-busy, dismissible failure and enables a new import",
      failedUi.errorLength > 0
        && failedUi.confirmEnabled
        && failedUi.cancelEnabled
        && failedUi.newImportAvailable
        && failedUi.cancelTaskAvailable === false,
      failedUi,
    );
    rowCount = await waitForTableRecovery(
      page,
      "E2E Atomic Import",
      tableId,
      0,
      Math.max(1, faultDeadline - Date.now()),
    );
    const outcome = await importFaultCapture.settle({
      deadlineAt: faultDeadline,
      fault,
      barrier,
    });
    if (outcome.kind === "expected-bridge-failure") {
      await acknowledgeExpectedBridgeFailure(page, outcome.failure);
    }
  } catch (error) {
    importFaultPrimaryError = error;
    throw error;
  } finally {
    try {
      await importFaultCapture.release();
    } catch (cleanupError) {
      if (!attachCleanupFailure(
        importFaultPrimaryError,
        cleanupError,
        "import fault outcome capture cleanup also failed",
      )) throw cleanupError;
    }
  }
  recorder.check("failed import exposed no partially committed records in the UI", rowCount === 0);
  const historyDeadline = Date.now() + 30_000;
  let history;
  do {
    try {
      history = await rawWorkspaceV2Request(page, "history.query", {
        collection: tableId,
        scope: "table",
        itemId: null,
        field: null,
        search: "",
        dateFrom: null,
        dateTo: null,
        actorId: null,
        actions: [],
        recordId: null,
        limit: 100,
        offset: 0,
      });
      break;
    } catch {
      history = null;
    }
    await page.waitForTimeout(250);
  } while (Date.now() < historyDeadline);
  const historyPage = history?.result;
  recorder.check("failed import exposed no partially committed audit entries",
    history !== null
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

  const fault = await requestPackagedProcessKill(
    runtime,
    "kill-sidecar",
    "exercise realtime disconnect and catch-up",
  );
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
    contractVersion: "2.0",
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

  const backendSourceSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.workspaceSession ?? null,
  );
  const backendFault = await requestPackagedProcessKill(
    runtime,
    "kill-backend",
    "exercise supported workspace reopen after packaged backend exit",
  );
  recorder.check(
    "only the exact packaged Python backend process was terminated",
    backendFault.processName === "vibetable-backend.exe",
    { backendFault },
  );

  await beginWritableWorkspaceBootstrapCapture(
    page,
    backendSourceSession.sessionEpoch,
    "workspace.open",
  );
  const closed = await rawLifecycleWorkspaceV2Request(
    page,
    "workspace.close",
    { reason: "user" },
    60_000,
  );
  recorder.check(
    "the faulted backend session closed through the supported workspace lifecycle",
    closed.result?.state === "closed" && closed.result?.workspaceId === null,
    { closed },
  );
  await openWorkspaceCenterFromSwitcher(page);
  const workspaceCenter = page.getByTestId("workspace-center");
  const workspace = workspaceCenter.getByRole("button", { name: /E2E Product Workspace/ });
  await workspace.waitFor({ state: "visible", timeout: 30_000 });
  await workspace.click();
  const recoveredBootstrap = await waitForCapturedBridgeMessage(page, 60_000);
  const recoveredSession = recoveredBootstrap.payload.session;
  recorder.check(
    "reopening after packaged backend exit published a fresh writable session epoch",
    recoveredSession.workspaceId === backendSourceSession.workspaceId
      && recoveredSession.state === "openedWritable"
      && recoveredSession.writable === true
      && recoveredSession.sessionEpoch > backendSourceSession.sessionEpoch,
    { backendSourceSession, recoveredSession },
  );

  const retentionBeforeStaleWrite = (
    await rawWorkspaceV2Request(page, "retention.get", {})
  ).result;
  const staleRepositoryLimit = retentionBeforeStaleWrite.repositoryLimitBytes === 1024 ** 3
    ? 2 * 1024 ** 3
    : 1024 ** 3;
  const staleBackendWrite = await requestWithStaleWorkspaceScope(
    page,
    "retention.update",
    {
      expectedRevision: retentionBeforeStaleWrite.policyRevision,
      snapshotDays: retentionBeforeStaleWrite.snapshotDays,
      snapshotCount: retentionBeforeStaleWrite.snapshotCount,
      snapshotBuckets: retentionBeforeStaleWrite.snapshotBuckets,
      fileRevisionDays: retentionBeforeStaleWrite.fileRevisionDays,
      fileRevisionCount: retentionBeforeStaleWrite.fileRevisionCount,
      fileRevisionBuckets: retentionBeforeStaleWrite.fileRevisionBuckets,
      repositoryLimitBytes: staleRepositoryLimit,
    },
    backendSourceSession,
  );
  recorder.check(
    "the retired backend epoch cannot write into the recovered session",
    (staleBackendWrite.type === "operation.failed"
        || staleBackendWrite.type === "workspace.v2.response")
      && staleBackendWrite.payload?.error?.code === "workspace.session_stale",
    { staleBackendWrite },
  );
  await acknowledgeExpectedBridgeFailure(page, staleBackendWrite);
  const retentionAfterStaleWrite = (
    await rawWorkspaceV2Request(page, "retention.get", {})
  ).result;
  recorder.check(
    "the rejected old-epoch write left the recovered workspace policy unchanged",
    retentionAfterStaleWrite.policyRevision === retentionBeforeStaleWrite.policyRevision
      && retentionAfterStaleWrite.repositoryLimitBytes
        === retentionBeforeStaleWrite.repositoryLimitBytes,
    { retentionBeforeStaleWrite, retentionAfterStaleWrite },
  );

  const afterBackendRecovery = await rawBridgeRequest(
    page,
    "mutation.apply",
    {
      contractVersion: "2.0",
      requestId: "e2e-after-backend-recovery",
      idempotencyKey: "e2e-after-backend-recovery",
      tableId,
      schemaRevision: recovered.snapshot.schemaRevision,
      operations: [{
        kind: "insert",
        recordId: null,
        values: { [valueField]: "after-backend-recovery" },
      }],
      actor: { type: "user", id: "e2e-backend-recovered", displayName: "E2E backend recovered" },
      expectedRevision: null,
      expectedDigest: null,
    },
  );
  recorder.check(
    "the fresh backend session accepts a new product mutation",
    afterBackendRecovery.type === "mutation.apply"
      && afterBackendRecovery.payload?.status === "applied",
    { afterBackendRecovery },
  );
  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E Realtime Recovery");
  const recoveredCell = page.locator(`.tabulator-cell[tabulator-field="${valueField}"]`)
    .filter({ hasText: "after-backend-recovery" });
  await recoveredCell.waitFor({ timeout: 30_000 });
  recorder.check(
    "the recovered-session write is visible exactly once after backend recovery",
    await recoveredCell.count() === 1,
    { recoveredCellCount: await recoveredCell.count() },
  );
}

async function scenario11(page, recorder, _network, runtime) {
  const databaseOpened = await waitForShell(page, recorder, { requireDatabaseOpened: true });
  const projectKey = databaseOpened.payload.projectKey.trim();
  await page.getByTestId("nav-tables").click();
  const pluginTable = await createSimpleTable(page, "E2E Plugin Target", "value");
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

  const toggle = page.getByTestId("plugin-toggle");
  await page.waitForFunction(() => {
    const lifecycleToggle = document.querySelector('[data-testid="plugin-toggle"]');
    const runButton = document.querySelector(".action-row button.run-button");
    return lifecycleToggle instanceof HTMLButtonElement
      && runButton instanceof HTMLButtonElement
      && lifecycleToggle.classList.contains("enabled")
      && !lifecycleToggle.disabled
      && !runButton.disabled;
  });
  await toggle.click();
  await page.waitForFunction(() => {
    const lifecycleToggle = document.querySelector('[data-testid="plugin-toggle"]');
    const runButton = document.querySelector(".action-row button.run-button");
    return lifecycleToggle instanceof HTMLButtonElement
      && runButton instanceof HTMLButtonElement
      && !lifecycleToggle.classList.contains("enabled")
      && !lifecycleToggle.disabled
      && runButton.disabled;
  });
  recorder.check("installed plugin can be disabled through its lifecycle UI",
    !(await toggle.getAttribute("class")).includes("enabled")
      && await page.locator(".action-row button.run-button").first().isDisabled());
  await toggle.click();
  await page.waitForFunction(() => {
    const lifecycleToggle = document.querySelector('[data-testid="plugin-toggle"]');
    const runButton = document.querySelector(".action-row button.run-button");
    return lifecycleToggle instanceof HTMLButtonElement
      && runButton instanceof HTMLButtonElement
      && lifecycleToggle.classList.contains("enabled")
      && !lifecycleToggle.disabled
      && !runButton.disabled;
  });
  recorder.check("disabled plugin can be explicitly enabled again",
    (await toggle.getAttribute("class")).includes("enabled")
      && await page.locator(".action-row button.run-button").first().isEnabled());

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
  const afterAuthorized = await waitForTableRecovery(
    page,
    "E2E Plugin Target",
    pluginTable.tableId,
    1,
  );
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
    projectKey,
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
  const afterDenied = await waitForTableRecovery(
    page,
    "E2E Plugin Target",
    pluginTable.tableId,
    1,
  );
  recorder.check("rejected plugin mutation did not create a second record", afterDenied === 1);
  await page.getByTestId("nav-plugins").click();
  await pluginRow.click();
  const sourceControl = path.join(runtime.controlsDir, "plugin-source.txt");
  const installedSource = (await fs.readFile(sourceControl, "utf8")).trim();
  const invalidUpgrade = path.join(runtime.controlsDir, "invalid-plugin-upgrade");
  await fs.cp(installedSource, invalidUpgrade, { recursive: true });
  await fs.writeFile(path.join(invalidUpgrade, "manifest.json"), "{ invalid manifest\n", "utf8");
  await fs.writeFile(sourceControl, `${invalidUpgrade}\n`, "utf8");
  await beginBridgeMessageCapture(page, ["operation.failed"]);
  await page.getByTestId("plugin-upgrade").click();
  const upgradeFailure = page.locator(".global-error[role='alert']");
  await upgradeFailure.waitFor({ state: "visible", timeout: 30_000 });
  const invalidUpgradeFailure = await waitForCapturedBridgeMessage(page, 30_000);
  await acknowledgeExpectedBridgeFailure(page, invalidUpgradeFailure);
  recorder.check("invalid upgrade source is rejected without replacing the installation",
    (await upgradeFailure.innerText()).trim().length > 0
      && await page.getByTestId("plugin-install-plan").isHidden()
      && (await page.locator(".status-strip").innerText()).includes("1.0.0"),
  { message: await upgradeFailure.innerText() });
}

async function submitWorkspaceSearch(page, { keyboard = false } = {}) {
  const submit = page.getByTestId("workspace-search-submit");
  await submit.waitFor({ state: "visible" });
  await beginWorkspaceV2MethodCapture(page, "workspaceSearch.query");
  if (keyboard) {
    const input = page.getByTestId("workspace-search-input").locator("input");
    await input.focus();
    await input.press("Enter");
  } else {
    await submit.click();
  }
  const response = await waitForCapturedBridgeMessage(page, 30_000);
  if (response.payload?.ok !== true) {
    throw new Error(`WorkspaceSearch query failed: ${JSON.stringify(response)}`);
  }
  return response.payload.result;
}

async function rebuildWorkspaceSearchAndWaitForTerminal(page, timeout = 120_000) {
  await beginWorkspaceV2MethodCapture(page, "workspaceSearch.rebuild");
  await page.getByTestId("workspace-search-rebuild").click();
  const response = await waitForCapturedBridgeMessage(page, 30_000);
  const accepted = response.payload?.result;
  if (response.payload?.ok !== true
    || accepted?.state !== "building"
    || !Number.isInteger(accepted.generation)) {
    throw new Error(`WorkspaceSearch rebuild was not accepted: ${JSON.stringify(response)}`);
  }

  return waitForWorkspaceSearchRebuildTerminal(page, accepted, timeout);
}

async function scenario12(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Backup Consistency");
  await closeFieldSettingsDrawer(page);
  const valueField = await createV2Field(
    page,
    tableId,
    "Value",
    "number",
    (draft) => {
      draft.storage.options.onlyInt = true;
      draft.display.displayScale = 0;
      return draft;
    },
  );
  const formulaField = await createV2Field(
    page,
    tableId,
    "Doubled",
    "formula",
    (draft) => {
      draft.formula = {
        language: "cel-v1",
        source: `${valueField.physicalName} * 2.0`,
      };
      return draft;
    },
  );
  const attachmentField = await createV2Field(
    page,
    tableId,
    "Attachments",
    "file",
    (draft) => {
      draft.file.maxFiles = 2;
      return draft;
    },
  );
  recorder.check(
    "backup schema was planned through Schema v2 with opaque stable field identities",
    valueField.fieldId?.startsWith("fld_")
      && formulaField.fieldId?.startsWith("fld_")
      && attachmentField.fieldId?.startsWith("fld_")
      && formulaField.definition?.formula?.source === `${valueField.physicalName} * 2.0`
      && formulaField.definition?.formula?.resultType === "number"
      && attachmentField.definition?.file?.maxFiles === 2,
    { valueField, formulaField, attachmentField },
  );
  await selectTable(page, "E2E Backup Consistency");
  await chooseToolbarMore(page, "refresh");
  await page.locator(
    `.tabulator-col[tabulator-field="${valueField.physicalName}"]`,
  ).waitFor({ timeout: 30_000 });
  const seed = await applyProductMutation(page, tableId, [{
    kind: "insert",
    recordId: null,
    values: {},
  }], "e2e-backup-seed");
  recorder.check(
    "backup seed row committed through the product mutation boundary",
    seed.type === "mutation.apply" && seed.payload?.status === "applied",
    { seed },
  );
  await waitForVisibleRowCount(page, 1);

  const valueCell = page.locator(
    `.tabulator-cell[tabulator-field="${valueField.physicalName}"]`,
  ).first();
  await valueCell.waitFor({ timeout: 30_000 });
  let valueEditor = await beginCellEdit(valueCell);
  await valueEditor.fill("7");
  await valueEditor.press("Enter");
  const formulaCell = page.locator(
    `.tabulator-cell[tabulator-field="${formulaField.physicalName}"]`,
  ).first();
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
    collection: tableId,
    requestGeneration: 12_012,
    accepts: [
      "vibetable.relation-capabilities.v1",
      "vibetable.lookup-query.v1",
    ],
  });
  const attachmentColumn = schema.payload?.schema?.columns?.find(
    (column) => column.name === attachmentField.physicalName,
  );
  const initialQuery = await rawBridgeRequest(page, "query.page", {
    tableId,
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
    tableId,
    recordId,
    fieldId: attachmentColumn.fieldId,
  };
  const attachmentCell = page.locator(
    `.tabulator-cell[tabulator-field="${attachmentField.physicalName}"]`,
  ).first();
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
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const beforeBackupRow = beforeBackupQuery.payload?.rows?.[0];
  const beforeBackupHistoryReply = await rawWorkspaceV2Request(page, "history.query", {
    collection: tableId,
    scope: "table",
    itemId: null,
    field: null,
    search: "",
    dateFrom: null,
    dateTo: null,
    actorId: null,
    actions: [],
    recordId: null,
    limit: 100,
    offset: 0,
  });
  const beforeBackupHistory = beforeBackupHistoryReply.result;
  recorder.check("pre-backup record, formula, attachment, and audit snapshot is complete",
    beforeBackupQuery.type === "query.page"
      && beforeBackupRow?.id === recordId
      && String(beforeBackupRow?.[valueField.physicalName]) === "7"
      && String(beforeBackupRow?.[formulaField.physicalName]) === "14"
      && beforeBackupAttachment?.originalName === path.basename(original)
      && beforeBackupAttachment?.sha256 === expectedOriginalHash
      && beforeBackupAttachment?.size === originalBytes.length
      && beforeBackupHistory?.collection === tableId
      && Array.isArray(beforeBackupHistory?.changeSets)
      && beforeBackupHistory.changeSets.length > 0
      && beforeBackupHistory.total === beforeBackupHistory.changeSets.length,
  {
    beforeBackupRow,
    beforeBackupAttachment,
    beforeBackupHistory,
    expectedOriginalHash,
    expectedOriginalSize: originalBytes.length,
  });

  await page.getByTestId("nav-search").click();
  const searchWorkspace = page.getByTestId("workspace-search-view");
  await searchWorkspace.waitFor({ state: "visible", timeout: 30_000 });
  const beforeSnapshotSearchLifecycle = await rebuildWorkspaceSearchAndWaitForTerminal(page);
  const beforeSnapshotSearchState = beforeSnapshotSearchLifecycle.state;
  await page.getByTestId("workspace-search-input").locator("input").fill("backup-original");
  const beforeSnapshotSearch = await submitWorkspaceSearch(page);
  const beforeSnapshotSearchStatus = await rawWorkspaceV2Request(
    page,
    "workspaceSearch.status",
    {},
  );
  recorder.check("snapshot source has a ready search generation containing the attachment",
    beforeSnapshotSearchState === "ready"
      && beforeSnapshotSearch.hits.some((hit) => hit.kind === "attachment")
      && Number.isInteger(beforeSnapshotSearchStatus.result?.generation)
      && beforeSnapshotSearchStatus.result.generation > 0,
  { beforeSnapshotSearch, beforeSnapshotSearchStatus: beforeSnapshotSearchStatus.result });

  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-versions").click();
  const existingSnapshotIds = await page.locator(".snapshot-row").evaluateAll(
    (rows) => rows.map((row) => row.id),
  );
  await page.getByTestId("snapshot-create").click();
  await page.waitForFunction(
    (existing) => [...document.querySelectorAll(".snapshot-row")]
      .some((row) => row.id && !existing.includes(row.id)),
    existingSnapshotIds,
    { timeout: 60_000 },
  );
  const createdSnapshotId = await page.locator(".snapshot-row").evaluateAll(
    (rows, existing) => rows.find((row) => row.id && !existing.includes(row.id))?.id ?? null,
    existingSnapshotIds,
  );
  const createdSnapshot = page.locator(`[id="${createdSnapshotId}"]`);
  await createdSnapshot.click();
  recorder.check("backup was created through the product UI with a listed archive",
    typeof createdSnapshotId === "string" && createdSnapshotId.startsWith("snapshot-"),
  {
    createdSnapshotId,
    timelineText: await createdSnapshot.innerText(),
  });
  const snapshotStorageProof = await requestStorageProof(
    runtime,
    tableId,
  );
  recorder.check("backup boundary published a verified external audit-ledger prefix",
    snapshotStorageProof.auditLedger?.verified === true
      && snapshotStorageProof.auditLedger?.count > 0
      && typeof snapshotStorageProof.auditLedger?.anchorHash === "string"
      && snapshotStorageProof.auditLedger.anchorHash.startsWith("sha256:"),
  { snapshotStorageProof });

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
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const changedRow = changedQuery.payload?.rows?.[0];
  recorder.check("post-backup mutation changed record, formula, and attachment bytes",
    changedQuery.type === "query.page"
      && changedRow?.id === recordId
      && String(changedRow?.[valueField.physicalName]) === "9"
      && String(changedRow?.[formulaField.physicalName]) === "18"
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
  await page.getByTestId("settings-nav-versions").click();
  await page.locator(`[id="${createdSnapshotId}"]`).click();
  await page.getByTestId("snapshot-restore-open").click();
  const restoreAdvance = page.getByTestId("snapshot-restore-preview");
  await restoreAdvance.click();
  const restorePlan = page.locator(".snapshot-restore-modal .plan-summary");
  const restoreFailure = page.locator(".protection-settings .n-alert--error");
  await Promise.race([
    restorePlan.waitFor({ timeout: 30_000 }),
    restoreFailure.waitFor({ timeout: 30_000 }).then(async () => {
      throw new Error(`snapshot restore preview failed: ${await restoreFailure.innerText()}`);
    }),
  ]);
  const restoreSourceSessionEpoch = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.workspaceSession?.sessionEpoch ?? 0,
  );
  await beginWritableWorkspaceBootstrapCapture(page, restoreSourceSessionEpoch);
  await restoreAdvance.click();
  await page.locator(".snapshot-restore-modal").waitFor({ state: "hidden" });
  const restoredBootstrap = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check(
    "snapshot restore published a fresh writable Workspace V2 session before follow-up reads",
    restoredBootstrap.type === "workspace.v2.bootstrap"
      && restoredBootstrap.payload?.session?.state === "openedWritable"
      && restoredBootstrap.payload.session.writable === true
      && Number.isInteger(restoredBootstrap.payload.session.sessionEpoch)
      && restoredBootstrap.payload.session.sessionEpoch > restoreSourceSessionEpoch,
    {
      restoreSourceSessionEpoch,
      restoredSession: restoredBootstrap.payload?.session,
    },
  );

  const postRestoreInitialSearch = await rawWorkspaceV2Request(
    page,
    "workspaceSearch.status",
    {},
  );
  await page.getByTestId("nav-search").click();
  await searchWorkspace.waitFor({ state: "visible", timeout: 30_000 });
  const restoredSearchLifecycle = await rebuildWorkspaceSearchAndWaitForTerminal(page);
  const restoredSearchState = restoredSearchLifecycle.state;
  await page.getByTestId("workspace-search-input").locator("input").fill("backup-original");
  const restoredSearch = await submitWorkspaceSearch(page);
  const restoredSearchStatus = await rawWorkspaceV2Request(
    page,
    "workspaceSearch.status",
    {},
  );
  recorder.check("snapshot restore invalidates derived search and rebuilds a newer usable generation",
    ["building", "degraded", "ready"].includes(postRestoreInitialSearch.result?.state)
      && restoredSearchState === "ready"
      && restoredSearchStatus.result?.generation
        > beforeSnapshotSearchStatus.result?.generation
      && restoredSearch.hits.some((hit) => hit.kind === "attachment"),
  {
    postRestoreInitialSearch: postRestoreInitialSearch.result,
    beforeSnapshotSearchStatus: beforeSnapshotSearchStatus.result,
    restoredSearchStatus: restoredSearchStatus.result,
    restoredSearch,
  });

  await page.getByTestId("nav-tables").click();
  await waitForTableRecovery(
    page,
    "E2E Backup Consistency",
    tableId,
    1,
    90_000,
  );
  const afterRestoreQuery = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 100 },
  });
  const afterRestoreRow = afterRestoreQuery.payload?.rows?.[0];
  recorder.check("record and stored formula returned exactly to the backup snapshot",
    afterRestoreQuery.type === "query.page"
      && afterRestoreRow?.id === beforeBackupRow?.id
      && afterRestoreRow?.[valueField.physicalName]
        === beforeBackupRow?.[valueField.physicalName]
      && afterRestoreRow?.[formulaField.physicalName]
        === beforeBackupRow?.[formulaField.physicalName],
  { beforeBackupRow, afterRestoreRow });
  const restoredStorageProof = await requestStorageProof(
    runtime,
    tableId,
  );
  const snapshotLedgerCount = snapshotStorageProof.auditLedger.count;
  const appendedLedgerRecords = restoredStorageProof.auditLedger.records.slice(
    snapshotLedgerCount,
  );
  const preservedPostSnapshotMutations = appendedLedgerRecords.filter(
    (record) => record.sourceEpoch === "business-v2"
      && record.payload?.type === "workspace.v2.business-mutation",
  );
  const restoreLedgerEvents = appendedLedgerRecords.filter(
    (record) => record.sourceEpoch.startsWith("snapshot-restore:")
      && record.payload?.type === "workspace.snapshotRestored",
  );
  const preservedSnapshotAnchor = restoredStorageProof.auditLedger
    .records[snapshotLedgerCount - 1]?.hash;
  recorder.check("external audit ledger preserved the snapshot prefix and appended post-snapshot mutations plus restore epoch",
    restoredStorageProof.auditLedger?.verified === true
      && restoredStorageProof.auditLedger.count > snapshotLedgerCount
      && preservedSnapshotAnchor === snapshotStorageProof.auditLedger.anchorHash
      && preservedPostSnapshotMutations.length > 0
      && restoreLedgerEvents.length === 1
      && preservedPostSnapshotMutations.every(
        (record) => record.ledgerSequence < restoreLedgerEvents[0].ledgerSequence,
      ),
  {
    snapshotLedger: snapshotStorageProof.auditLedger,
    restoredLedger: restoredStorageProof.auditLedger,
    appendedLedgerRecords,
    preservedPostSnapshotMutations,
    restoreLedgerEvents,
  });
  // This assertion targets table history. Run it before the explicit
  // attachment-cell interaction, which correctly changes the toolbar action
  // to a different cell-history query.
  const historyDrawerStartedAt = performance.now();
  await page.getByTestId("toolbar-history").click();
  await page.getByTestId("history-timeline").waitFor({ timeout: 30_000 });
  await waitForBridgeDiagnosticsToSettle(page, { timeoutMs: 2_000, quietMs: 50 });
  const retiredHistoryField = await acknowledgeExpectedBridgeFailureByCodeIfPresent(
    page,
    "history.field_not_found",
  );
  recorder.check(
    "restored history retired any stale cell field and converged on table scope",
    retiredHistoryField === null || retiredHistoryField.requestType === "history.query",
    { retiredHistoryField },
  );
  runtime.recordUiTiming(
    "history.drawer.initialLoad",
    performance.now() - historyDrawerStartedAt,
    { scope: "table", scenario: "12-backup-consistency" },
  );
  const afterRestoreHistoryReply = await rawWorkspaceV2Request(page, "history.query", {
    collection: tableId,
    scope: "table",
    itemId: null,
    field: null,
    search: "",
    dateFrom: null,
    dateTo: null,
    actorId: null,
    actions: [],
    recordId: null,
    limit: 100,
    offset: 0,
  });
  const afterRestoreHistory = afterRestoreHistoryReply.result;
  const beforeChangeSetIds = new Set(
    beforeBackupHistory.changeSets.map((changeSet) => changeSet.changeSetId),
  );
  const afterChangeSets = afterRestoreHistory?.changeSets ?? [];
  const preservedChangeSets = afterChangeSets.filter(
    (changeSet) => beforeChangeSetIds.has(changeSet.changeSetId),
  );
  const addedChangeSets = afterChangeSets.filter(
    (changeSet) => !beforeChangeSetIds.has(changeSet.changeSetId),
  );
  const preservedChangeSetIds = new Set(
    preservedChangeSets.map((changeSet) => changeSet.changeSetId),
  );
  const addedScalarChanges = addedChangeSets.flatMap(
    (changeSet) => changeSet.scalarChanges ?? [],
  );
  const postSnapshotValuePreserved = addedScalarChanges.some(
    (change) => change.field === valueField.physicalName && String(change.after) === "9",
  );
  const postSnapshotAttachmentPreserved = addedScalarChanges.some(
    (change) => change.field === attachmentField.physicalName
      && String(change.after ?? "").includes(
        replacementAttachmentResponse.payload.attachments[0].storedName,
      ),
  );
  recorder.check("append-only history preserved the snapshot prefix and post-snapshot mutations after restore",
    beforeBackupHistory.changeSets.every(
        (changeSet) => preservedChangeSetIds.has(changeSet.changeSetId),
      )
      && postSnapshotValuePreserved
      && postSnapshotAttachmentPreserved
      && afterRestoreHistory.total
        === beforeBackupHistory.total + addedChangeSets.length
      && addedChangeSets.length >= 2,
  {
    beforeBackupHistory,
    afterRestoreHistory,
    addedChangeSets,
    postSnapshotValuePreserved,
    postSnapshotAttachmentPreserved,
  });
  const historyClose = page.locator(".n-drawer-header__close").last();
  if (await historyClose.isVisible()) await historyClose.click();

  const restoredAttachmentCell = page.locator(
    `.tabulator-cell[tabulator-field="${attachmentField.physicalName}"]`,
  ).first();
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

  // Visual acceptance is part of the product gate, not a source-only token
  // check. Exercise the user-facing theme control and capture the real
  // packaged WebView2 table, modal, and popover in dark mode.
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-general").click();
  await page.getByTestId("theme-select").click();
  await page.locator(".n-base-select-option")
    .filter({ hasText: /^(深色|Dark)$/u })
    .click();
  await page.locator("html.dark").waitFor({ timeout: 10_000 });
  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E Backup Consistency");
  const darkThemeEvidence = await sampleConnectedThemeSurfaces(page);
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

async function scenario13(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-storage").click();
  const settings = page.getByTestId("storage-settings");
  await settings.waitFor({ state: "visible", timeout: 30_000 });

  const verify = page.getByTestId("repository-verify");
  await verify.waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForFunction(() => {
    const button = document.querySelector('[data-testid="repository-verify"]');
    return button instanceof HTMLButtonElement && !button.disabled;
  }, null, { timeout: 30_000 });
  recorder.check("repository verification is available through the packaged settings UI",
    await verify.isEnabled(), { disabled: await verify.isDisabled() });
  await verify.click();
  const verification = settings.locator(".n-alert").last();
  await verification.waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("repository verification returns a visible user-facing result",
    (await verification.innerText()).trim().length > 0,
    { text: await verification.innerText() });

  const initialRetentionReply = await rawWorkspaceV2Request(page, "retention.get", {});
  const initialRetention = initialRetentionReply.result;
  const limit = page.getByTestId("retention-repository-limit").locator("input");
  await limit.fill("1");
  await limit.press("Tab");
  const save = page.getByTestId("retention-save");
  recorder.check("retention policy becomes dirty through the real Settings control",
    await save.isEnabled(), { saveDisabled: await save.isDisabled() });
  await beginWorkspaceV2MethodCapture(page, "retention.update");
  await save.click();
  const retentionUpdate = await waitForCapturedBridgeMessage(page, 30_000);
  recorder.check("retention policy update round-trips through the packaged UI",
    retentionUpdate.type === "workspace.v2.response"
      && retentionUpdate.payload?.method === "retention.update"
      && retentionUpdate.payload?.ok === true
      && ["1", "1.0"].includes(await limit.inputValue()),
  { limit: await limit.inputValue(), retentionUpdate });

  let stalePolicyFailure = "";
  try {
    await rawWorkspaceV2Request(page, "retention.update", {
      expectedRevision: initialRetention.policyRevision,
      snapshotDays: initialRetention.snapshotDays,
      snapshotCount: initialRetention.snapshotCount,
      snapshotBuckets: initialRetention.snapshotBuckets,
      fileRevisionDays: initialRetention.fileRevisionDays,
      fileRevisionCount: initialRetention.fileRevisionCount,
      fileRevisionBuckets: initialRetention.fileRevisionBuckets,
      repositoryLimitBytes: initialRetention.repositoryLimitBytes,
    });
  } catch (error) {
    stalePolicyFailure = String(error);
  }
  await acknowledgeExpectedBridgeFailureByCodeIfPresent(
    page,
    "retention.policy_revision_stale",
  );
  const retainedPolicy = (await rawWorkspaceV2Request(page, "retention.get", {})).result;
  recorder.check("a stale retention policy revision fails closed without overwriting the UI update",
    stalePolicyFailure.includes("retention.policy_revision_stale")
      && retainedPolicy.policyRevision === initialRetention.policyRevision + 1
      && retainedPolicy.repositoryLimitBytes === 1024 ** 3,
  { stalePolicyFailure, initialRetention, retainedPolicy });

  const retentionPlan = page.getByTestId("retention-plan-preview");
  await retentionPlan.waitFor({ state: "visible", timeout: 30_000 });
  await beginWorkspaceV2MethodCapture(page, "retention.plan");
  await retentionPlan.click();
  const retentionPreview = await waitForCapturedBridgeMessage(page, 30_000);
  const cleanupIsEmpty = retentionPreview.type === "workspace.v2.response"
    && retentionPreview.payload?.method === "retention.plan"
    && retentionPreview.payload?.ok === true
    && retentionPreview.payload?.result?.reclaimableBytes === 0
    && Array.isArray(retentionPreview.payload?.result?.blockedReasons)
    && retentionPreview.payload.result.blockedReasons.length === 0;
  const apply = page.getByTestId("retention-plan-apply");
  await apply.waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("retention cleanup preview produces an explicit empty plan before applying it",
    cleanupIsEmpty && await apply.isEnabled(),
  { applyDisabled: await apply.isDisabled(), retentionPreview });
  if (!cleanupIsEmpty) {
    throw new Error("refusing to apply a non-empty retention cleanup plan");
  }

  const sync = page.getByTestId("workspace-storage-sync");
  recorder.check("direct workspace does not fabricate a replica synchronize path",
    !(await sync.isVisible()), { syncVisible: await sync.isVisible() });

  await beginWorkspaceV2MethodCapture(page, "retention.apply");
  await apply.click();
  const retentionApply = await waitForCapturedBridgeMessage(page, 30_000);
  const deletedObjects = retentionApply.payload?.result?.deletedObjects;
  const reclaimedBytes = retentionApply.payload?.result?.reclaimedBytes;
  await apply.waitFor({ state: "hidden", timeout: 30_000 });
  recorder.check("retention cleanup applies the verified empty plan through the packaged UI",
    retentionApply.type === "workspace.v2.response"
      && retentionApply.payload?.method === "retention.apply"
      && retentionApply.payload?.ok === true
      && deletedObjects === 0
      && reclaimedBytes === 0,
  { retentionApply, planCleared: !(await apply.isVisible()) });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "13-protection-policy.png"), fullPage: true });
}

async function scenario14(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-files").click();
  const workspace = page.getByTestId("file-workspace");
  await workspace.waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("document-import").click();
  const row = page.locator('[data-testid^="document-row-"]').filter({
    hasText: "document-diff-source.txt",
  });
  await row.waitFor({ state: "visible", timeout: 30_000 });

  const listed = await rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "and",
    filters: [{ field: "displayName", operator: "eq", value: "document-diff-source.txt" }],
    sort: [{ field: "relativePath", direction: "asc" }],
    limit: 50,
    cursor: null,
  });
  const document = listed.result?.documents?.find(
    (item) => item.relativePath === "document-diff-source.txt",
  );
  recorder.check("host-only picker imports a real TXT into file history",
    typeof document?.documentId === "string"
      && typeof document?.effectiveRevisionId === "string",
  { document });
  const firstTree = await rawWorkspaceV2Request(page, "fileHistory.readTree", {
    documentId: document.documentId,
  });
  const historicalRevisionId = firstTree.result?.effectiveRevisionId;
  const restored = await rawWorkspaceV2Request(page, "fileHistory.restore", {
    documentId: document.documentId,
    expectedEffectiveRevisionId: historicalRevisionId,
    historicalRevisionId,
  });
  const effectiveRevisionId = restored.result?.revisionId;
  recorder.check("real restore creates a second immutable revision for comparison",
    typeof effectiveRevisionId === "string"
      && effectiveRevisionId !== historicalRevisionId,
  { historicalRevisionId, effectiveRevisionId, restored: restored.result });

  const oldHandleAttribute = await row.getAttribute("data-testid");
  await workspace.locator(".file-toolbar > button").first().click();
  await page.waitForFunction(
    ({ oldHandle, name }) => {
      const current = [...document.querySelectorAll('[data-testid^="document-row-"]')]
        .find((candidate) => candidate.textContent?.includes(name));
      return current?.getAttribute("data-testid") !== oldHandle;
    },
    { oldHandle: oldHandleAttribute, name: "document-diff-source.txt" },
  );
  const refreshedRow = page.locator('[data-testid^="document-row-"]').filter({
    hasText: "document-diff-source.txt",
  });
  const refreshedHandleAttribute = await refreshedRow.getAttribute("data-testid");
  const entryHandle = refreshedHandleAttribute?.replace(/^document-row-/u, "");
  await refreshedRow.click();
  await workspace.locator(".inspector-tabs button").nth(1).click();
  await page.getByTestId("file-revision-tree").waitFor({ state: "visible" });
  const compare = page.getByTestId("compare-revision").first();
  await compare.waitFor({ state: "visible", timeout: 30_000 });
  await beginBridgeMessageCapture(page, ["document.diffCompleted"]);
  await compare.click();
  const completed = await waitForCapturedBridgeMessage(page, 30_000);
  const resultAlert = page.getByTestId("diff-result");
  await resultAlert.waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("FileRevisionTree uses the closed operation and renders a localized result",
    completed.payload?.outcome === "identical"
      && completed.payload?.historicalRevisionId === historicalRevisionId
      && completed.payload?.effectiveRevisionId === effectiveRevisionId
      && (await resultAlert.innerText()).trim().length > 0,
  { completed, text: await resultAlert.innerText() });

  const staleAdvance = await rawWorkspaceV2Request(page, "fileHistory.restore", {
    documentId: document.documentId,
    expectedEffectiveRevisionId: effectiveRevisionId,
    historicalRevisionId,
  });
  const stale = await rawBridgeRequest(
    page,
    "document.diffRequested",
    {
      entryHandle,
      operationId: crypto.randomUUID(),
      historicalRevisionId,
      expectedEffectiveRevisionId: effectiveRevisionId,
    },
    30_000,
    ["document.diffCompleted"],
  );
  recorder.check("materialization CAS fails closed when the revision has advanced",
    stale.type === "document.diffCompleted"
      && stale.payload?.outcome === "failure"
      && stale.payload?.failure === "stale",
  { stale, staleAdvance: staleAdvance.result });

  let rawMaterializeFailure = "";
  try {
    await rawWorkspaceV2Request(page, "fileHistory.materializeDiffPair", {
      documentId: document.documentId,
      historicalRevisionId,
      expectedEffectiveRevisionId: effectiveRevisionId,
      pathGrant: `host-path-grant://${crypto.randomUUID()}`,
    });
  } catch (error) {
    rawMaterializeFailure = String(error);
  }
  recorder.check("renderer cannot invoke raw diff materialization",
    rawMaterializeFailure.includes("workspace.method_not_public"),
  { rawMaterializeFailure });
  await page.screenshot({ path: path.join(runtime.evidenceDir, "14-document-diff.png"), fullPage: true });
}

async function requestWithStaleWorkspaceScope(page, method, params, staleSession) {
  return page.evaluate(
    ({ rpcMethod, rpcParams, session }) => new Promise((resolve, reject) => {
      const operationId = crypto.randomUUID();
      const requestId = `e2e-${operationId}`;
      const wire = {
        scope: "workspace",
        workspaceId: session.workspaceId,
        sessionEpoch: session.sessionEpoch,
        operationId,
        sequence: Date.now() * 1_000,
      };
      const timer = setTimeout(() => {
        window.chrome.webview.removeEventListener("message", handler);
        reject(new Error(`stale workspace request timed out for ${rpcMethod}`));
      }, 20_000);
      const handler = (event) => {
        let message = event.data;
        if (typeof message === "string") {
          try { message = JSON.parse(message); } catch { return; }
        }
        if (!message || message.requestId !== requestId) return;
        clearTimeout(timer);
        window.chrome.webview.removeEventListener("message", handler);
        resolve(message);
      };
      window.chrome.webview.addEventListener("message", handler);
      window.chrome.webview.postMessage({
        type: "workspace.v2.request",
        requestId,
        scope: wire,
        wire,
        payload: { method: rpcMethod, params: rpcParams, wire },
      });
    }),
    { rpcMethod: method, rpcParams: params, session: staleSession },
  );
}

async function openWorkspaceCenterFromSwitcher(page) {
  const switcher = page.getByTestId("workspace-switcher");
  await switcher.locator(".switcher-trigger").click();
  await page.locator(".n-dropdown-option").last().click();
  await page.getByTestId("workspace-center").waitFor({ state: "visible", timeout: 30_000 });
}

async function switchWorkspaceByName(page, name, minimumEpoch) {
  await beginWritableWorkspaceBootstrapCapture(page, minimumEpoch, "workspace.switch");
  const switcher = page.getByTestId("workspace-switcher");
  await switcher.locator(".switcher-trigger").click();
  await page.locator(".n-dropdown-option").filter({ hasText: name }).click();
  return waitForCapturedBridgeMessage(page, 60_000);
}

async function scenario15(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  const originalSession = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.workspaceSession ?? null,
  );
  await openWorkspaceCenterFromSwitcher(page);
  await page.getByTestId("workspace-create").click();
  const modal = page.getByTestId("workspace-flow-modal");
  await modal.locator("input").first().fill("E2E Switch Target");
  await page.getByTestId("workspace-flow-confirm").click();
  const target = page.getByTestId("workspace-center").getByRole("button", {
    name: /E2E Switch Target/,
  });
  await target.waitFor({ state: "visible", timeout: 60_000 });
  await beginWritableWorkspaceBootstrapCapture(
    page,
    originalSession.sessionEpoch,
    "workspace.open",
  );
  await target.click();
  const targetBootstrap = await waitForCapturedBridgeMessage(page, 60_000);
  const targetSession = targetBootstrap.payload.session;
  recorder.check("Workspace Center creates and opens a second real workspace",
    targetSession.workspaceId !== originalSession.workspaceId
      && targetSession.sessionEpoch > originalSession.sessionEpoch,
  { originalSession, targetSession });

  const switchedBootstrap = await switchWorkspaceByName(
    page,
    "E2E Product Workspace",
    targetSession.sessionEpoch,
  );
  const switchedSession = switchedBootstrap.payload.session;
  recorder.check("workspace switcher rotates the writable session epoch",
    switchedSession.workspaceId === originalSession.workspaceId
      && switchedSession.sessionEpoch > targetSession.sessionEpoch,
  { targetSession, switchedSession });

  const stale = await requestWithStaleWorkspaceScope(
    page,
    "snapshot.list",
    { cursor: null, limit: 1 },
    targetSession,
  );
  const staleText = JSON.stringify(stale);
  recorder.check("the retired workspace epoch cannot write or read into the new session",
    (stale.type === "operation.failed" || stale.type === "workspace.v2.response")
      && (stale.payload?.error?.code === "workspace.session_stale"
        || /session[_ .-]?epoch[_ .-]?stale|stale/i.test(staleText)),
  { stale });
  await acknowledgeExpectedBridgeFailure(page, stale);

  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-versions").click();
  const settings = page.getByTestId("snapshot-settings");
  await settings.waitFor({ state: "visible", timeout: 30_000 });
  const existingSnapshotIds = await page.locator(".snapshot-row").evaluateAll(
    (rows) => rows.map((row) => row.id),
  );
  await page.getByTestId("snapshot-create").click();
  await page.waitForFunction(
    (count) => document.querySelectorAll(".snapshot-row").length > count,
    existingSnapshotIds.length,
    { timeout: 60_000 },
  );
  const createdSnapshotId = await page.locator(".snapshot-row").evaluateAll(
    (rows, existingIds) => rows.map((row) => row.id).find((id) => !existingIds.includes(id)),
    existingSnapshotIds,
  );
  recorder.check("snapshot creation exposes a new immutable timeline identity",
    typeof createdSnapshotId === "string" && createdSnapshotId.startsWith("snapshot-"),
  { existingSnapshotIds, createdSnapshotId });
  const snapshot = page.locator(`[id="${createdSnapshotId}"]`);
  await snapshot.click();
  await page.getByTestId("snapshot-export-open").click();
  await beginWorkspaceV2MethodCapture(page, "snapshot.export");
  await page.getByTestId("snapshot-export-apply").click();
  const exportTerminal = await waitForCapturedBridgeMessage(page, 60_000);
  if (exportTerminal.type === "operation.failed") {
    throw new Error(`snapshot export failed: ${JSON.stringify(exportTerminal)}`);
  }
  const exportResponseSummary = {
    type: exportTerminal.type,
    requestId: exportTerminal.requestId,
    method: exportTerminal.payload?.method ?? null,
    ok: exportTerminal.payload?.ok === true,
  };
  if (exportResponseSummary.type !== "workspace.v2.response"
    || exportResponseSummary.method !== "snapshot.export"
    || !exportResponseSummary.ok) {
    throw new Error(`snapshot export returned an invalid terminal: ${JSON.stringify(
      exportResponseSummary,
    )}`);
  }
  const exportDiagnosticsAfter = await readBridgeDiagnostics(page);
  const exportRoundTrip = exportDiagnosticsAfter?.roundTrips
    .find((item) => item.requestId === exportResponseSummary.requestId) ?? null;
  recorder.check("snapshot export completes through its correlated bridge request",
    exportRoundTrip?.responseType === "workspace.v2.response"
      && exportRoundTrip.requestType === "snapshot.export"
      && exportRoundTrip.code === null
      && !exportDiagnosticsAfter.pending.some(
        (item) => item.requestId === exportRoundTrip.requestId,
      ),
  { exportResponseSummary, exportRoundTrip });
  const packagePath = path.join(runtime.controlsDir, "workspace-snapshot.vtsnapshot");
  const packageBytes = await fs.readFile(packagePath);
  recorder.check("snapshot export writes a non-empty package through the host picker",
    packageBytes.length > 0, { packageSize: packageBytes.length });

  const sourceEpoch = switchedSession.sessionEpoch;
  await beginWritableWorkspaceBootstrapCapture(
    page,
    sourceEpoch,
    "snapshot.openAsNewWorkspace",
  );
  await page.getByTestId("snapshot-open-as-new").click();
  const openedBootstrap = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check("snapshot open-as-new creates a distinct writable workspace",
    openedBootstrap.payload.session.workspaceId !== switchedSession.workspaceId
      && openedBootstrap.payload.session.sessionEpoch > sourceEpoch,
  { sourceSession: switchedSession, openedSession: openedBootstrap.payload.session });

  await switchWorkspaceByName(
    page,
    "E2E Product Workspace",
    openedBootstrap.payload.session.sessionEpoch,
  );
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-versions").click();
  await fs.writeFile(packagePath, "corrupt snapshot package\n", "utf8");
  const corruptRoundTripStart = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.roundTrips.length ?? 0,
  );
  await page.getByTestId("snapshot-import").click();
  await page.waitForFunction(
    ({ start }) => window.__vibetableE2EBridgeDiagnostics?.roundTrips
      .slice(start)
      .some((item) => item.requestType === "snapshot.inspectPackage"),
    { start: corruptRoundTripStart },
    { timeout: 30_000 },
  );
  const corruptRoundTrip = await page.evaluate(
    ({ start }) => window.__vibetableE2EBridgeDiagnostics?.roundTrips
      .slice(start)
      .find((item) => item.requestType === "snapshot.inspectPackage") ?? null,
    { start: corruptRoundTripStart },
  );
  const corruptFailure = page.getByTestId("snapshot-operation-error");
  await corruptFailure.waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("a damaged snapshot package fails visibly before import",
    corruptRoundTrip?.code === "snapshot.package_invalid"
      && (await corruptFailure.innerText()).trim().length > 0,
  { corruptRoundTrip, message: await corruptFailure.innerText() });
  await acknowledgeExpectedBridgeFailure(page, corruptRoundTrip);

  await fs.writeFile(packagePath, packageBytes);
  await page.getByTestId("snapshot-import").click();
  await page.getByTestId("snapshot-import-apply").waitFor({ state: "visible", timeout: 30_000 });
  const beforeImport = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.workspaceSession?.sessionEpoch ?? 0,
  );
  await beginWritableWorkspaceBootstrapCapture(page, beforeImport, "snapshot.import");
  await page.getByTestId("snapshot-import-apply").click();
  const importedBootstrap = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check("the restored package imports as another writable workspace through the UI",
    importedBootstrap.payload.session.workspaceId !== switchedSession.workspaceId
      && importedBootstrap.payload.session.sessionEpoch > beforeImport,
  { importedSession: importedBootstrap.payload.session });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "15-workspace-snapshot-package.png"),
    fullPage: true,
  });
}

async function scenario16(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const seeded = await createSimpleTable(page, "E2E Dashboard Data", "Region");
  const dashboardSeed = await applyProductMutation(page, seeded.tableId, [
    { kind: "insert", recordId: null, values: { [seeded.field.physicalName]: "North" } },
    { kind: "insert", recordId: null, values: { [seeded.field.physicalName]: "South" } },
  ], "dashboard-seed");
  recorder.check("Dashboard E2E starts from authoritative PocketBase records",
    dashboardSeed.payload?.status === "applied", { dashboardSeed });

  await page.getByTestId("nav-dashboard").click();
  const workspace = page.getByTestId("dashboard-workspace");
  await workspace.waitFor({ state: "visible", timeout: 30_000 });

  await page.getByTestId("dashboard-create").click();
  const modal = page.getByTestId("dashboard-create-modal");
  await modal.waitFor({ state: "visible", timeout: 10_000 });
  const createSubmit = page.getByTestId("dashboard-create-submit");
  await page.getByTestId("dashboard-create-name").locator("input").fill("");
  recorder.check("Dashboard rejects an empty name before persistence",
    await createSubmit.isDisabled());
  await page.getByTestId("dashboard-create-name").locator("input").fill("E2E Dashboard");
  await page.getByTestId("dashboard-create-template-blank").click();
  await createSubmit.click();

  const save = page.getByTestId("dashboard-save");
  await save.waitFor({ state: "visible", timeout: 10_000 });
  recorder.check("a blank Dashboard draft is created through the product UI",
    await save.isEnabled());

  async function addPanel(name, typeLabel, dimensions = []) {
    await page.getByTestId("dashboard-add-panel").click();
    await page.getByTestId("dashboard-panel-editor").waitFor({ state: "visible" });
    await fillNInput(page, "dashboard-panel-name", name);
    await selectVisibleNOption(page, "dashboard-panel-type", typeLabel);
    await selectVisibleNOption(page, "dashboard-panel-collection", seeded.tableId);
    if (/Record list|明细列表/u.test(String(typeLabel))) {
      await page.getByTestId("dashboard-panel-fields").waitFor({ state: "visible" });
      await selectVisibleNOptions(page, "dashboard-panel-fields", ["Region"]);
    } else if (dimensions.length > 0) {
      await page.getByTestId("dashboard-panel-dimensions").waitFor({ state: "visible" });
      await selectVisibleNOptions(page, "dashboard-panel-dimensions", dimensions);
    }
    const submit = page.getByTestId("dashboard-panel-submit");
    await submit.waitFor({ state: "visible" });
    await page.waitForFunction(
      () => !document.querySelector('[data-testid="dashboard-panel-submit"]')?.hasAttribute("disabled"),
      undefined,
      { timeout: 20_000 },
    );
    await submit.click();
    const panel = workspace.locator(".dashboard-panel").filter({ hasText: name });
    await panel.waitFor({ state: "visible", timeout: 30_000 });
    return panel;
  }

  const barPanel = await addPanel("Regional bars", /^(柱状图|Bar chart)$/u, ["Region"]);
  const dataPanel = await addPanel("Regional records", /^(明细列表|Record list)$/u);
  const metricPanel = await addPanel("Record count", /^(单指标|Metric)$/u);
  const piePanel = await addPanel("Regional share", /^(饼图|Pie chart)$/u, ["Region"]);
  await dataPanel.getByText("North", { exact: true }).waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    () => [...document.querySelectorAll(".dashboard-panel")].every((panel) =>
      !panel.textContent?.includes("正在加载") && !panel.textContent?.includes("Loading")),
    undefined,
    { timeout: 30_000 },
  );
  recorder.check("Dashboard renders four typed panel families over authoritative rows",
    (await dataPanel.innerText()).includes("North")
      && (await dataPanel.innerText()).includes("South")
      && /2/u.test(await metricPanel.innerText())
      && await barPanel.getByTestId("dashboard-chart-selection").isVisible()
      && await piePanel.getByTestId("dashboard-chart-selection").isVisible(),
  {
    recordPanel: await dataPanel.innerText(),
    metricPanel: await metricPanel.innerText(),
    panelCount: await workspace.locator(".dashboard-panel").count(),
  });

  const layoutItem = workspace.locator(".grid-stack-item").filter({ hasText: "Regional records" });
  const layoutBefore = {
    x: await layoutItem.getAttribute("gs-x"),
    height: await layoutItem.getAttribute("gs-h"),
  };
  await layoutItem.press("Alt+ArrowRight");
  await layoutItem.press("Alt+Shift+ArrowDown");
  const layoutAfter = {
    x: await layoutItem.getAttribute("gs-x"),
    height: await layoutItem.getAttribute("gs-h"),
  };
  recorder.check("Dashboard keyboard layout moves and resizes the selected panel",
    layoutAfter.x !== layoutBefore.x && layoutAfter.height !== layoutBefore.height,
  { layoutBefore, layoutAfter });

  const dataPanelId = await dataPanel.evaluate((node) =>
    node.closest("[data-panel-id]")?.getAttribute("data-panel-id") ?? null,
  );
  if (!dataPanelId) throw new Error("Dashboard record panel id is unavailable");

  await page.getByTestId("dashboard-configure").click();
  await page.getByTestId("dashboard-settings").waitFor({ state: "visible" });
  await page.getByTestId("dashboard-add-filter").click();
  await fillNInput(page, "dashboard-filter-label-0", "Region");
  await fillNInput(page, "dashboard-filter-key-0", "region");
  await selectVisibleNOption(page, "dashboard-filter-type-0", /^(枚举多选|Enum multi-select)$/u);
  const filterTargets = page.getByTestId("dashboard-filter-targets-0");
  await filterTargets.locator(".n-tag")
    .filter({ hasText: "Regional bars" })
    .locator(".n-tag__close")
    .click();
  await selectVisibleNOptions(page, "dashboard-filter-targets-0", ["Regional records"]);
  await selectVisibleNOption(page, `dashboard-filter-binding-0-${dataPanelId}`, "Region");
  await page.getByTestId("dashboard-add-interaction").click();
  const interactionSource = page.getByTestId("dashboard-interaction-source-0");
  const interactionSourceField = page.getByTestId("dashboard-interaction-source-field-0");
  const interactionTargets = page.getByTestId("dashboard-interaction-targets-0");
  recorder.check("Dashboard settings derives a visual chart-to-record interaction by default",
    (await interactionSource.innerText()).includes("Regional bars")
      && (await interactionSourceField.innerText()).includes("Region")
      && (await interactionTargets.innerText()).includes("Regional records"),
  {
    source: await interactionSource.innerText(),
    sourceField: await interactionSourceField.innerText(),
    targets: await interactionTargets.innerText(),
  });
  await selectVisibleNOption(page, "dashboard-interaction-target-field-0", "Region");
  const settingsSubmit = page.getByTestId("dashboard-settings-submit");
  await page.waitForFunction(
    () => !document.querySelector('[data-testid="dashboard-settings-submit"]')?.hasAttribute("disabled"),
    undefined,
    { timeout: 20_000 },
  );
  await settingsSubmit.click();
  await save.click();

  const persisted = workspace.locator('[data-testid^="dashboard-select-"]').filter({
    hasText: "E2E Dashboard",
  });
  await persisted.waitFor({ state: "visible", timeout: 30_000 });
  const dashboardId = (await persisted.getAttribute("data-testid"))
    ?.replace("dashboard-select-", "");
  if (!dashboardId) throw new Error("persisted Dashboard id is unavailable");
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("nav-dashboard").click();
  await workspace.waitFor({ state: "visible", timeout: 30_000 });
  await persisted.waitFor({ state: "visible", timeout: 30_000 });
  await persisted.click();
  await page.getByTestId("dashboard-refresh").click();
  const reopenedPanel = workspace.locator(".dashboard-panel").filter({ hasText: "Regional records" });
  await reopenedPanel.waitFor({ state: "visible", timeout: 30_000 });
  const reopenedBar = workspace.locator(".dashboard-panel").filter({ hasText: "Regional bars" });
  const reopenedMetric = workspace.locator(".dashboard-panel").filter({ hasText: "Record count" });
  const reopened = await rawBridgeRequest(page, "dashboard.readRequested", {
    dashboardId,
  }, 20_000, ["dashboard.loaded"]);
  const persistedFilter = reopened.payload?.config?.globalFilters?.find((filter) =>
    filter.key === "region",
  );
  const persistedDataPanelId = reopened.payload?.dashboard?.panels?.find((panel) =>
    panel.name === "Regional records",
  )?.id;
  recorder.check("Dashboard persists the visual global-filter definition and explicit field binding",
    persistedFilter?.label === "Region"
      && persistedFilter?.type === "enum"
      && persistedFilter?.targetPanels?.length === 1
      && persistedFilter.targetPanels[0] === persistedDataPanelId
      && persistedFilter?.fieldBindings?.[persistedDataPanelId] === seeded.field.physicalName
      && !Object.prototype.hasOwnProperty.call(persistedFilter, "value"),
  { persistedFilter, draftDataPanelId: dataPanelId, persistedDataPanelId, field: seeded.field.physicalName });

  const filterQueryStart = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.roundTrips.length ?? 0,
  );
  await addVisibleNTagOption(page, "dashboard-filter-value-region", "North");
  await page.waitForFunction(
    ({ start }) => window.__vibetableE2EBridgeDiagnostics?.roundTrips
      .slice(start)
      .filter((item) => item.requestType === "dashboard.queryRequested"
        && item.responseType === "dashboard.queryLoaded").length >= 4,
    { start: filterQueryStart },
    { timeout: 30_000 },
  );
  await page.waitForFunction(
    () => {
      const panels = [...document.querySelectorAll(".dashboard-panel")];
      const panel = panels
        .find((item) => item.textContent?.includes("Regional records"));
      const metric = panels.find((item) => item.textContent?.includes("Record count"));
      const metricValue = metric?.querySelector(".metric-panel strong")?.textContent?.trim();
      return panel?.textContent?.includes("North")
        && !panel?.textContent?.includes("South")
        && metricValue === "2";
    },
    undefined,
    { timeout: 30_000 },
  );
  const globallyFilteredText = await reopenedPanel.innerText();
  const metricText = await reopenedMetric.innerText();
  const metricValue = await reopenedMetric.locator(".metric-panel strong").innerText();
  recorder.check("the real FilterBar limits only its explicitly bound record panel",
    globallyFilteredText.includes("North")
      && !globallyFilteredText.includes("South")
      && metricValue.trim() === "2",
  { globallyFilteredText, metricText, metricValue });
  await page.getByTestId("dashboard-filters-clear").click();
  await page.waitForFunction(
    () => {
      const panel = [...document.querySelectorAll(".dashboard-panel")]
        .find((item) => item.textContent?.includes("Regional records"));
      return panel?.textContent?.includes("North") && panel?.textContent?.includes("South");
    },
    undefined,
    { timeout: 30_000 },
  );
  recorder.check("clearing global filters restores all authoritative record rows",
    (await reopenedPanel.innerText()).includes("North")
      && (await reopenedPanel.innerText()).includes("South"));

  const chartSelection = reopenedBar.getByTestId("dashboard-chart-selection");
  await chartSelection.waitFor({ state: "visible", timeout: 30_000 });
  await chartSelection.selectOption({ label: "North" });
  const drilldown = page.getByTestId("dashboard-drilldown");
  await drilldown.waitFor({ state: "visible", timeout: 30_000 });
  await drilldown.getByText("North", { exact: true }).first().waitFor({ timeout: 30_000 });
  await page.waitForFunction(
    () => {
      const panel = [...document.querySelectorAll(".dashboard-panel")]
        .find((item) => item.textContent?.includes("Regional records"));
      return panel?.textContent?.includes("North") && !panel?.textContent?.includes("South");
    },
    undefined,
    { timeout: 30_000 },
  );
  const linkedText = await reopenedPanel.innerText();
  recorder.check("the saved Dashboard reopens and chart selection drives linked filtering plus drilldown",
    await persisted.getAttribute("aria-selected") === "true"
      && linkedText.includes("North") && !linkedText.includes("South")
      && (await drilldown.innerText()).includes("North"),
  { dashboardName: "E2E Dashboard", linkedText, drilldownText: await drilldown.innerText() });

  await page.keyboard.press("Escape");
  await page.getByTestId("dashboard-edit").click();
  await page.getByTestId("dashboard-configure").click();
  const conflictSettings = page.getByTestId("dashboard-settings");
  await conflictSettings.waitFor({ state: "visible", timeout: 10_000 });
  await conflictSettings.locator("textarea").fill("local E2E edit before competing save");
  await page.getByTestId("dashboard-settings-submit").click();
  await page.waitForFunction(
    () => !document.querySelector('[data-testid="dashboard-save"]')?.hasAttribute("disabled"),
    undefined,
    { timeout: 10_000 },
  );
  const current = await rawBridgeRequest(page, "dashboard.readRequested", {
    dashboardId,
  }, 20_000, ["dashboard.loaded"]);
  const server = current.payload;
  const competing = await rawBridgeRequest(page, "dashboard.saveRequested", {
    dashboardId: server.dashboard.id,
    expectedRevision: server.revision,
    idempotencyKey: crypto.randomUUID(),
    name: server.dashboard.name,
    note: "competing E2E save",
    panels: server.dashboard.panels.map((panel) => ({
      clientId: panel.id,
      panelId: panel.id,
      name: panel.name,
      type: panel.type,
      position: panel.position,
      note: panel.note ?? null,
      icon: panel.icon ?? null,
      color: panel.color ?? null,
      showHeader: panel.showHeader !== false,
      options: panel.options,
      query: panel.query,
    })),
    deletedPanelIds: [],
    config: server.config,
  }, 20_000, ["dashboard.saved"]);
  recorder.check("a competing Dashboard commit advances the authoritative revision",
    competing.type === "dashboard.saved"
      && typeof competing.payload?.workspace?.revision === "string"
      && competing.payload.workspace.revision !== server.revision,
  {
    before: server.revision,
    after: competing.payload?.workspace?.revision,
    responseType: competing.type,
    error: competing.type === "operation.failed" ? competing.payload : undefined,
  });
  await save.click();
  const conflict = page.getByTestId("dashboard-conflict-error");
  await conflict.waitFor({ state: "visible", timeout: 30_000 });
  const conflictText = await conflict.innerText();
  const conflictReloadStart = await page.evaluate(
    () => window.__vibetableE2EBridgeDiagnostics?.roundTrips.length ?? 0,
  );
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByTestId("dashboard-reload-conflict").click();
  await page.waitForFunction(
    ({ start }) => window.__vibetableE2EBridgeDiagnostics?.roundTrips
      .slice(start)
      .some((item) => item.requestType === "dashboard.readRequested"
        && item.responseType === "dashboard.loaded") ?? false,
    { start: conflictReloadStart },
    { timeout: 30_000 },
  );
  await conflict.waitFor({ state: "hidden", timeout: 30_000 });
  await acknowledgeExpectedBridgeFailureByCodeIfPresent(page, "dashboard_edit_conflict");
  while (await acknowledgeExpectedBridgeFailureByCodeIfPresent(page, "DASHBOARD_CANCELLED")) {
    // Rapid Dashboard reloads intentionally cancel superseded panel reads.
  }
  const editAfterReload = page.getByTestId("dashboard-edit");
  await editAfterReload.waitFor({ state: "visible", timeout: 30_000 });
  await editAfterReload.click();
  const configureAfterReload = page.getByTestId("dashboard-configure");
  await configureAfterReload.waitFor({ state: "visible", timeout: 30_000 });
  await configureAfterReload.click();
  const reloadedSettings = page.getByTestId("dashboard-settings");
  await reloadedSettings.waitFor({ state: "visible", timeout: 30_000 });
  const reloadedNote = await page.getByTestId("dashboard-settings-note")
    .locator("textarea")
    .inputValue();
  recorder.check("Dashboard exposes a CAS conflict and reloads the winning revision",
    conflictText.trim().length > 0 && reloadedNote === "competing E2E save",
  {
    conflictText,
    reloadedNote,
    winningRevision: competing.payload?.workspace?.revision,
  });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "16-dashboard-lifecycle.png"),
    fullPage: true,
  });
}

async function scenario17(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-plugins").click();
  await page.getByTestId("plugin-install-folder").click();
  const pluginInstallPlan = page.getByTestId("plugin-install-plan");
  await pluginInstallPlan.waitFor({ timeout: 30_000 });
  const pluginInstallPlanText = await pluginInstallPlan.innerText();
  await page.getByTestId("plugin-install-commit").click();
  const interfacePlugin = page.locator("button.plugin-row")
    .filter({ hasText: "com.vibetable.e2e.mutation-boundary" });
  await interfacePlugin.waitFor({ timeout: 60_000 });
  recorder.check("Interface packaged journey installs the real mutation-boundary plugin",
    await interfacePlugin.isVisible(), { installPlan: pluginInstallPlanText });
  await page.getByTestId("nav-tables").click();
  const seeded = await createSimpleTable(page, "E2E Interface Data", "Subject");
  await applyProductMutation(page, seeded.tableId, [{
    kind: "insert", recordId: null, values: { [seeded.field.physicalName]: "Existing request" },
  }], "interface-seed");
  await page.getByTestId("nav-interfaces").click();
  const workspace = page.getByTestId("interface-workspace");
  await workspace.waitFor({ state: "visible", timeout: 30_000 });

  page.once("dialog", (dialog) => dialog.accept("E2E Interface"));
  await page.getByTestId("interface-create").click();
  const nameInput = workspace.locator(".surface-name input");
  await nameInput.fill("E2E Interface");
  await page.getByTestId("interface-add-text").click();
  const element = workspace.locator('[data-testid^="interface-builder-element-"]').first();
  await element.waitFor({ state: "visible", timeout: 10_000 });
  const inspectorText = workspace.locator(".inspector-panel label").filter({ hasText: "文本" })
    .locator("input");
  await inspectorText.fill("E2E Interface Content");
  const saveButton = page.getByTestId("interface-save");
  const saveDisabled = await saveButton.isDisabled();
  if (saveDisabled) {
    const diagnostic = await page.getByTestId("interface-diagnostics").textContent()
      .catch(() => null);
    throw new Error(`Interface save is disabled: ${diagnostic ?? "no diagnostic rendered"}`);
  }
  await beginBridgeMessageCapture(page, ["interface.committed"]);
  await saveButton.click();
  const initialCommit = await waitForCapturedBridgeMessage(page, 30_000);
  const persistedEntry = {
    interfaceId: initialCommit.payload?.definition?.interfaceId,
    name: initialCommit.payload?.definition?.name,
  };
  if (!persistedEntry?.interfaceId) {
    throw new Error(`Interface save did not commit a definition: ${JSON.stringify(initialCommit)}`);
  }
  const persisted = page.getByTestId(`interface-select-${persistedEntry.interfaceId}`);
  await persisted.waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("nav-files").click();
  await page.getByTestId("nav-interfaces").click();
  await persisted.waitFor({ state: "visible", timeout: 30_000 });
  await persisted.click();
  const loaded = await rawBridgeRequest(page, "interface.loadRequested", {
    interfaceId: persistedEntry.interfaceId,
  }, 20_000, ["interface.loaded"]);
  const interfaceId = loaded.payload?.definition?.interfaceId;
  const definition = {
    contractVersion: "1.0",
    interfaceId,
    name: "E2E Interface",
    bindings: [{
      bindingId: "requests",
      query: {
        contractVersion: "1.0", tableId: seeded.tableId,
        fields: [seeded.field.physicalName], filters: [], sorts: [], cursor: null, pageSize: 100,
      },
      variables: [],
    }],
    actions: [
      { actionId: "create", kind: "record.create", bindingId: "requests", targetPageId: null, pluginId: null, pluginActionId: null, requiresConfirmation: false },
      { actionId: "update", kind: "record.update", bindingId: "requests", targetPageId: null, pluginId: null, pluginActionId: null, requiresConfirmation: true },
      { actionId: "refresh", kind: "binding.refresh", bindingId: "requests", targetPageId: null, pluginId: null, pluginActionId: null, requiresConfirmation: false },
      { actionId: "details", kind: "navigate", bindingId: null, targetPageId: "details", pluginId: null, pluginActionId: null, requiresConfirmation: false },
      { actionId: "plugin-action", kind: "plugin", bindingId: null, targetPageId: null, pluginId: "com.vibetable.e2e.mutation-boundary", pluginActionId: "allowed-plan", requiresConfirmation: true },
    ],
    pages: [
      { pageId: "main", title: "Requests", elements: [
        { elementId: "list", kind: "record-list", bindingId: "requests", actionId: null, text: "Requests", width: "full", children: [] },
        { elementId: "update", kind: "form", bindingId: "requests", actionId: "update", text: "Update request", width: "half", children: [] },
        { elementId: "form", kind: "form", bindingId: "requests", actionId: "create", text: "New request", width: "half", children: [] },
        { elementId: "refresh", kind: "button", bindingId: null, actionId: "refresh", text: "Refresh", width: "third", children: [] },
        { elementId: "navigate", kind: "navigation", bindingId: null, actionId: "details", text: "Open details", width: "third", children: [] },
        { elementId: "plugin-action", kind: "button", bindingId: "requests", actionId: "plugin-action", text: "Run plugin", width: "third", children: [] },
      ] },
      { pageId: "details", title: "Details", elements: [
        { elementId: "detail-text", kind: "text", bindingId: null, actionId: null, text: "E2E Interface Details", width: "full", children: [] },
      ] },
    ],
  };
  const committed = await rawBridgeRequest(page, "interface.commitRequested", {
    definition, expectedRevision: loaded.payload?.revision, idempotencyKey: crypto.randomUUID(),
  }, 20_000, ["interface.committed"]);
  recorder.check("Interface definition commits pages, binding and closed actions atomically",
    committed.payload?.definition?.actions?.length === 5
      && committed.payload?.definition?.pages?.length === 2,
  { committed });
  await persisted.click();
  await page.getByTestId("interface-run").click();
  const runtimeSurface = page.getByTestId("interface-runtime");
  await runtimeSurface.waitFor({ state: "visible", timeout: 10_000 });
  await runtimeSurface.getByText("Existing request", { exact: true }).waitFor({ timeout: 30_000 });
  const updateForm = page.getByTestId("interface-runtime-update");
  await updateForm.locator("input").fill("Updated through Interface");
  page.once("dialog", (dialog) => dialog.accept());
  await updateForm.getByRole("button", { name: "提交" }).click();
  await runtimeSurface.getByText("操作已完成", { exact: true }).waitFor({ timeout: 30_000 });
  await runtimeSurface.getByText("Updated through Interface", { exact: true }).waitFor({ timeout: 30_000 });
  recorder.check("Interface runtime performs a confirmed CAS record update and refreshes the binding",
    !(await runtimeSurface.innerText()).includes("Existing request"),
  { runtimeText: await runtimeSurface.innerText() });
  const form = page.getByTestId("interface-runtime-form");
  await form.locator("input").fill("Created through Interface");
  await form.getByRole("button", { name: "提交" }).click();
  await runtimeSurface.getByText("操作已完成", { exact: true }).waitFor({ timeout: 30_000 });
  await runtimeSurface.getByText("Created through Interface", { exact: true }).waitFor({ timeout: 30_000 });

  const pluginAction = page.getByTestId("interface-runtime-plugin-action").getByRole("button");
  page.once("dialog", (dialog) => dialog.accept());
  await pluginAction.click();
  const pluginConfirmation = page.getByTestId("plugin-confirmation");
  await pluginConfirmation.waitFor({ timeout: 30_000 });
  await page.getByTestId("plugin-confirm-reject").click();
  await runtimeSurface.getByText("插件操作已拒绝", { exact: true }).waitFor({ timeout: 30_000 });
  recorder.check("Interface plugin action exposes and records an explicit broker rejection",
    !(await runtimeSurface.innerText()).includes("插件动作执行失败"));

  page.once("dialog", (dialog) => dialog.accept());
  await pluginAction.click();
  await pluginConfirmation.waitFor({ timeout: 30_000 });
  await page.getByTestId("plugin-task-cancel").click();
  await runtimeSurface.getByText("插件操作已取消", { exact: true }).waitFor({ timeout: 30_000 });
  recorder.check("Interface plugin action cancels the authoritative in-flight task",
    !(await runtimeSurface.innerText()).includes("操作已完成"));

  page.once("dialog", (dialog) => dialog.accept());
  await pluginAction.click();
  await pluginConfirmation.waitFor({ timeout: 30_000 });
  const confirmationText = await pluginConfirmation.innerText();
  await page.getByTestId("plugin-confirm-approve").click();
  await runtimeSurface.getByText("操作已完成", { exact: true }).waitFor({ timeout: 30_000 });
  recorder.check("Interface plugin action waits for broker approval and terminal success",
    /FINAL WRITE CONFIRMATION/i.test(confirmationText), { confirmationText });

  await page.getByTestId("interface-runtime-navigate").getByRole("button").click();
  const runtimeText = await runtimeSurface.innerText();
  recorder.check("Interface runtime creates a record, refreshes its binding and navigates pages",
    runtimeText.includes("E2E Interface Details"),
  { interfaceName: "E2E Interface", runtimeText });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "17-interface-lifecycle.png"),
    fullPage: true,
  });
}

async function scenario18(page, recorder, _network, runtime) {
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Search Records");
  const titleField = await createV2Field(page, tableId, "Title", "text");
  const bodyField = await createV2Field(page, tableId, "Body", "editor");
  const attachmentField = await createV2Field(page, tableId, "Attachment", "file");
  await closeFieldSettingsDrawer(page);
  const seeded = { tableId, field: titleField };
  const recordSeed = await applyProductMutation(page, seeded.tableId, [{
    kind: "insert", recordId: null,
    values: {
      [titleField.physicalName]: "E2E searchable record",
      [bodyField.physicalName]: "A durable E2E body indexed from the authoritative record",
    },
  }], "search-seed");
  const seededRow = recordSeed.payload?.affectedRows?.[0];
  const seededRecordId = seededRow?.recordId;

  await selectTable(page, "E2E Search Records");
  await chooseToolbarMore(page, "refresh");
  const titleCell = page.locator(
    `.tabulator-cell[tabulator-field="${titleField.physicalName}"]`,
  ).first();
  await titleCell.waitFor({ state: "visible", timeout: 30_000 });
  await titleCell.click();
  await page.getByTestId("toolbar-content-record").click();
  const profileConfig = page.getByTestId("content-profile-config");
  await profileConfig.waitFor({ state: "visible", timeout: 30_000 });
  await selectVisibleNOption(page, "content-profile-title", "Title");
  await selectVisibleNOption(page, "content-profile-body", "Body");
  await selectVisibleNOptions(page, "content-profile-searchable", ["Title", "Body"]);
  await page.getByTestId("content-profile-save").click();
  const contentPanel = page.getByTestId("content-record-panel");
  await contentPanel.waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("content-edit").click();
  const contentInputs = contentPanel.locator(".content-main .n-input input, .content-main .n-input textarea");
  await contentInputs.nth(0).fill("E2E content record edited");
  await contentInputs.nth(1).fill("Durable violet body saved through the content reading layout.");
  await page.getByTestId("content-record-save").click();
  const contentSaved = await waitForQueryPage(page, {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  }, (payload) => payload?.rows?.[0]?.[titleField.physicalName]
      === "E2E content record edited"
    && payload.rows[0]?.[bodyField.physicalName]
      === "Durable violet body saved through the content reading layout.");
  recorder.check("ContentProfile and record edits flow through the packaged content UI",
    (await contentPanel.innerText()).includes("E2E content record edited")
      && (await contentPanel.innerText()).includes("Durable violet body"),
  { tableId, seededRecordId, contentSaved: contentSaved.payload?.rows?.[0] });
  await page.locator(".n-drawer-header__close").last().click();

  const schema = await rawBridgeRequest(page, "schema.describe", {
    collection: tableId,
    requestGeneration: 18_001,
    accepts: ["vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1"],
  });
  const attachmentColumn = schema.payload?.schema?.columns?.find(
    (column) => column.name === attachmentField.physicalName,
  );
  if (!seededRecordId || !attachmentColumn?.fieldId) {
    throw new Error(`search attachment identity unavailable: ${JSON.stringify({ recordSeed, schema })}`);
  }
  const attachmentCell = page.locator(
    `.tabulator-cell[tabulator-field="${attachmentField.physicalName}"]`,
  ).first();
  await attachmentCell.dblclick();
  await page.getByTestId("attachment-upload").click();
  await waitForAttachmentList(
    page,
    { tableId, recordId: seededRecordId, fieldId: attachmentColumn.fieldId },
    (attachments) => attachments.length === 1
      && attachments[0]?.originalName === "e2e-search-attachment-original.txt",
  );

  await page.getByTestId("nav-files").click();
  const documentControl = path.join(runtime.controlsDir, "document-source.txt");
  await fs.writeFile(
    documentControl,
    `${path.join(runtime.controlsDir, "content-reference-a.md")}\n`,
    "utf8",
  );
  await page.getByTestId("document-import").click();
  const markdownRow = page.locator('[data-testid^="document-row-"]').filter({
    hasText: "content-reference-a.md",
  });
  await markdownRow.waitFor({ state: "visible", timeout: 30_000 });
  await fs.writeFile(
    documentControl,
    `${path.join(runtime.controlsDir, "content-reference-b.json")}\n`,
    "utf8",
  );
  await page.getByTestId("document-import").click();
  const jsonRow = page.locator('[data-testid^="document-row-"]').filter({
    hasText: "content-reference-b.json",
  });
  await jsonRow.waitFor({ state: "visible", timeout: 30_000 });
  const andDocuments = await rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "and",
    filters: [
      { field: "displayName", operator: "contains", value: "content-reference" },
      { field: "extension", operator: "eq", value: "md" },
    ],
    sort: [{ field: "relativePath", direction: "asc" }],
    limit: 50,
    cursor: null,
  });
  const orDocuments = await rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "or",
    filters: [
      { field: "displayName", operator: "eq", value: "content-reference-b.json" },
      { field: "extension", operator: "eq", value: "md" },
    ],
    sort: [{ field: "relativePath", direction: "asc" }],
    limit: 50,
    cursor: null,
  });
  const documents = orDocuments.result?.documents ?? [];
  const markdownDocument = documents.find(
    (item) => item.relativePath === "content-reference-a.md",
  );
  const jsonDocument = documents.find(
    (item) => item.relativePath === "content-reference-b.json",
  );
  recorder.check("FileDocument metadata AND/OR preserves mandatory summary semantics",
    andDocuments.result?.documents?.length === 1
      && andDocuments.result.documents[0]?.documentId === markdownDocument?.documentId
      && documents.length === 2
      && documents.every((item) => item.relativePath
        && item.displayName
        && item.extension
        && item.mimeType
        && Number.isInteger(item.sizeBytes)
        && item.sizeBytes > 0
        && item.effectiveRevisionCreatedAt
        && ["active", "deleted"].includes(item.status)),
  { andDocuments: andDocuments.result, orDocuments: orDocuments.result });

  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E Search Records");
  await titleCell.waitFor({ state: "visible", timeout: 30_000 });
  await titleCell.click();
  await page.getByTestId("toolbar-content-record").click();
  await contentPanel.waitFor({ state: "visible", timeout: 30_000 });
  await selectVisibleNOption(page, "content-link-document", "content-reference-a.md");
  await page.getByTestId("content-link-create").click();
  const linkedMarkdown = contentPanel.locator(".link-card").filter({
    hasText: "content-reference-a.md",
  });
  await linkedMarkdown.waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("RecordDocumentLink is created through the content UI without copying identity",
    (await linkedMarkdown.innerText()).includes("正常"),
  { markdownDocument });
  await page.locator(".n-drawer-header__close").last().click();

  await page.getByTestId("nav-files").click();
  await markdownRow.waitFor({ state: "visible", timeout: 30_000 });
  await markdownRow.click({ button: "right" });
  await page.getByTestId("document-unlink").click();
  await page.getByTestId("document-unlink-confirm").click();
  const deletedDocuments = await rawWorkspaceV2Request(page, "fileHistory.queryDocuments", {
    logic: "and",
    filters: [{ field: "displayName", operator: "eq", value: "content-reference-a.md" }],
    sort: [{ field: "relativePath", direction: "asc" }],
    limit: 50,
    cursor: null,
  });
  recorder.check("unlink deletes only the FileHistory document and leaves the explicit link broken",
    deletedDocuments.result?.documents?.length === 1
      && deletedDocuments.result.documents[0]?.status === "deleted",
  { deletedDocuments: deletedDocuments.result });

  await page.getByTestId("nav-tables").click();
  await selectTable(page, "E2E Search Records");
  await titleCell.waitFor({ state: "visible", timeout: 30_000 });
  await titleCell.click();
  await page.getByTestId("toolbar-content-record").click();
  await contentPanel.waitFor({ state: "visible", timeout: 30_000 });
  const brokenLink = contentPanel.locator(".link-card").filter({
    hasText: "content-reference-a.md",
  });
  await brokenLink.getByText("关联已断开", { exact: true }).waitFor({ timeout: 30_000 });
  await selectVisibleNOption(page, "content-link-document", "content-reference-b.json");
  await brokenLink.getByTestId("content-link-repair").click();
  const repairedLink = contentPanel.locator(".link-card").filter({
    hasText: "content-reference-b.json",
  });
  await repairedLink.getByText("正常", { exact: true }).waitFor({ timeout: 30_000 });
  const persistedLinks = await rawBridgeRequest(page, "recordDocumentLink.list", {
    tableId,
    recordId: seededRecordId,
  });
  recorder.check("broken RecordDocumentLink repairs to a second authority document",
    persistedLinks.payload?.items?.length === 1
      && persistedLinks.payload.items[0]?.link?.documentId === jsonDocument?.documentId,
  { persistedLinks, jsonDocument });
  await page.locator(".n-drawer-header__close").last().click();

  await page.getByTestId("nav-search").click();
  const workspace = page.getByTestId("workspace-search-view");
  await workspace.waitFor({ state: "visible", timeout: 30_000 });

  const rebuiltIndex = await rebuildWorkspaceSearchAndWaitForTerminal(page);
  const indexState = rebuiltIndex.state;
  recorder.check("WorkspaceSearch rebuild completes durably without a silent fallback",
    indexState === "ready", { rebuiltIndex });

  await page.getByTestId("workspace-search-input").locator("input").fill("E2E");
  await submitWorkspaceSearch(page, { keyboard: true });
  const results = page.getByTestId("workspace-search-result");
  await results.first().waitFor({ state: "visible", timeout: 30_000 });
  const kinds = await results.evaluateAll((items) => items.map((item) => item.dataset.kind));
  recorder.check("keyboard search returns current record, attachment, and file sources through the UI",
    kinds.includes("record") && kinds.includes("attachment") && kinds.includes("file")
      && kinds.every((kind) => ["record", "attachment", "file"].includes(kind)),
  { kinds });

  await page.getByTestId("workspace-search-scope-history").check();
  await page.getByTestId("workspace-search-input").locator("input").fill("Marigold");
  const markdownSearch = await submitWorkspaceSearch(page, { keyboard: true });
  await results.first().waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("deleted Markdown visible text remains searchable only in history scope",
    markdownSearch.hits.some((hit) => hit.kind === "file"
      && hit.canonicalId === markdownDocument?.documentId),
  { markdownSearch, markdownDocument });
  await page.getByTestId("workspace-search-scope-current").check();
  await page.getByTestId("workspace-search-input").locator("input").fill("Cobalt");
  const jsonSearch = await submitWorkspaceSearch(page, { keyboard: true });
  await results.first().waitFor({ state: "visible", timeout: 30_000 });
  recorder.check("JSON textual values are extracted into WorkspaceSearch",
    jsonSearch.hits.some((hit) => hit.kind === "file"
      && hit.canonicalId === jsonDocument?.documentId),
  { jsonSearch, jsonDocument });
  await page.getByTestId("workspace-search-input").locator("input").fill("E2E");
  await submitWorkspaceSearch(page);

  await page.getByTestId("workspace-search-filters").locator("summary").click();
  await page.getByTestId("workspace-search-filter-table").locator("input").fill(seeded.tableId);
  await submitWorkspaceSearch(page);
  await results.first().waitFor({ state: "visible", timeout: 30_000 });
  const andKinds = await results.evaluateAll((items) => items.map((item) => item.dataset.kind));
  recorder.check("WorkspaceSearch AND applies the table filter without leaking file hits",
    andKinds.length > 0
      && andKinds.every((kind) => ["record", "attachment"].includes(kind))
      && andKinds.includes("record")
      && andKinds.includes("attachment")
      && !andKinds.includes("file"),
  { andKinds });

  await page.getByTestId("workspace-search-filter-extension").locator("input").fill(".json");
  await workspace.locator(".segmented button", { hasText: "OR" }).click();
  await submitWorkspaceSearch(page);
  await results.first().waitFor({ state: "visible", timeout: 30_000 });
  const orKinds = await results.evaluateAll((items) => items.map((item) => item.dataset.kind));
  recorder.check("WorkspaceSearch OR combines table and extension filters across source kinds",
    orKinds.includes("record") && orKinds.includes("file"),
  { orKinds });

  await page.getByTestId("workspace-search-filter-table").locator("input").fill("");
  await page.getByTestId("workspace-search-filter-extension").locator("input").fill("");
  await page.getByTestId("workspace-search-scope-history").check();
  const historyResult = await submitWorkspaceSearch(page);
  const expectedHistoryFileCount = historyResult.hits.filter((hit) => hit.kind === "file").length;
  await page.waitForFunction(
    (expected) => document.querySelectorAll(
      '[data-testid="workspace-search-result"][data-kind="file"]',
    ).length >= expected,
    expectedHistoryFileCount,
    { timeout: 30_000 },
  );
  const historyFiles = page.locator(
    '[data-testid="workspace-search-result"][data-kind="file"]',
  );
  recorder.check("WorkspaceSearch history scope exposes immutable file revisions",
    expectedHistoryFileCount >= 2 && await historyFiles.count() >= expectedHistoryFileCount,
  { expectedHistoryFileCount, historyFileCount: await historyFiles.count(), historyResult });

  await page.getByTestId("workspace-search-scope-current").check();
  await page.getByTestId("workspace-search-filter-extension").locator("input").fill("");
  await workspace.locator(".segmented button", { hasText: "AND" }).click();
  await submitWorkspaceSearch(page);
  const staleRecord = page.locator(
    '[data-testid="workspace-search-result"][data-kind="record"]',
  ).first();
  await staleRecord.waitFor({ state: "visible", timeout: 30_000 });
  if (!seededRecordId) throw new Error(`Search seed did not return a record id: ${JSON.stringify(recordSeed)}`);
  const staleBefore = await rawBridgeRequest(page, "query.page", {
    tableId: seeded.tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  });
  const staleBeforeRow = staleBefore.payload?.rows?.find((row) => row.id === seededRecordId);
  const staleBeforeRevision = Number(staleBefore.payload?.snapshot?.dataRevision);
  if (!staleBeforeRow?.__vibetableDigest
      || !staleBefore.payload?.snapshot?.schemaRevision
      || !Number.isSafeInteger(staleBeforeRevision)) {
    throw new Error(`stale SearchHit setup lacks current authority: ${JSON.stringify(staleBefore)}`);
  }
  const staleToken = `search-stale-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const staleMutation = await rawBridgeRequest(page, "mutation.apply", {
    contractVersion: "2.0",
    requestId: staleToken,
    idempotencyKey: staleToken,
    tableId: seeded.tableId,
    schemaRevision: staleBefore.payload.snapshot.schemaRevision,
    operations: [{
      kind: "update", recordId: seededRecordId,
      values: { [titleField.physicalName]: "E2E searchable record changed" },
      expectedDigest: staleBeforeRow.__vibetableDigest,
    }],
    actor: { type: "user", id: "product-e2e", displayName: "Product E2E" },
    expectedRevision: null,
    expectedDigest: null,
  }, 20_000, ["mutation.apply", "operation.failed"]);
  if (staleMutation.type !== "mutation.apply"
      || staleMutation.payload?.status !== "applied"
      || staleMutation.payload?.error?.code) {
    throw new Error(`stale SearchHit authority update failed: ${JSON.stringify(staleMutation)}`);
  }
  const staleAfter = await rawBridgeRequest(page, "query.page", {
    tableId: seeded.tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  });
  const staleAfterRevision = Number(staleAfter.payload?.snapshot?.dataRevision);
  recorder.check("stale SearchHit setup commits a newer authority revision",
    staleAfter.type === "query.page"
      && Number.isSafeInteger(staleAfterRevision)
      && staleAfterRevision > staleBeforeRevision
      && staleAfter.payload?.rows?.find((row) => row.id === seededRecordId)
        ?.[titleField.physicalName] === "E2E searchable record changed",
  { staleBeforeRevision, staleAfterRevision, staleMutation, staleAfter });
  await staleRecord.focus();
  await staleRecord.press("Enter");
  const staleMessage = page.locator(".n-message").last();
  await staleMessage.waitFor({ state: "visible", timeout: 30_000 });
  const staleMessageText = (await staleMessage.innerText()).trim();
  const staleMessageWasVisible = await staleMessage.isVisible();
  const activeSearchInput = page.getByTestId("workspace-search-input").locator("input");
  await activeSearchInput.focus();
  recorder.check("keyboard opening a stale SearchHit re-resolves authority and stays in Search",
    staleMessageWasVisible
      && staleMessageText.length > 0
      && await workspace.isVisible()
      && await activeSearchInput.isVisible()
      && await activeSearchInput.evaluate((element) => element === document.activeElement),
  { staleMessage: staleMessageText });

  const freshTableName = page.getByTestId("sidebar-table-name")
    .filter({ hasText: "E2E Search Records" });
  const recovery = await runScenario18RecoveryBoundary({
    page,
    tableId,
    injectFault: () => requestSidecarKill(runtime, "verify ContentProfile and repaired link survive restart"),
    awaitBackendRecovery: () => waitForActiveTableBackend(page, tableId, 1, 90_000),
    prepareFreshTable: async () => {
      await page.getByTestId("nav-files").click();
      await page.getByTestId("file-workspace").waitFor({ state: "visible", timeout: 30_000 });
      await page.getByTestId("nav-tables").click();
      await freshTableName.waitFor();
    },
    triggerFreshTable: () => freshTableName.locator("xpath=ancestor::button").click(),
    prepareFreshContent: async () => {
      await titleCell.waitFor({ state: "visible", timeout: 30_000 });
      await titleCell.click();
    },
    triggerFreshContent: () => page.getByTestId("toolbar-content-record").click(),
    readFreshContent: async () => {
      await contentPanel.waitFor({ state: "visible", timeout: 30_000 });
      return contentPanel.innerText();
    },
  });
  const restart = recovery.fault;
  recorder.check("content restart terminated only the exact sidecar child",
    restart.processName === "vibetable-pb.exe", { restart });
  const reopenedText = recovery.content;
  recorder.check("content record and repaired link survive packaged sidecar restart and reopen",
    reopenedText.includes("Durable violet body")
      && reopenedText.includes("content-reference-b.json")
      && reopenedText.includes("正常")
      && !reopenedText.includes("关联已断开"),
  { reopenedText });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "18-workspace-search.png"),
    fullPage: true,
  });
}

async function waitForGalleryProjection(page, expectedCount) {
  const gallery = page.getByTestId("record-gallery-view");
  await gallery.waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForFunction(count => (
    document.querySelectorAll('[data-testid="gallery-card"]').length === count
      && document.querySelectorAll('[data-testid="gallery-cover-placeholder"]').length === count
  ), expectedCount, { timeout: 30_000 });
  return gallery;
}

async function scenario19(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Gallery Records");
  const titleField = await createV2Field(page, tableId, "Title", "text");
  const coverField = await createV2Field(page, tableId, "Cover", "file");
  await closeFieldSettingsDrawer(page);
  await selectTable(page, "E2E Gallery Records");
  await chooseToolbarMore(page, "refresh");
  await page.locator(
    `.tabulator-col[tabulator-field="${titleField.physicalName}"]`,
  ).waitFor({ timeout: 30_000 });
  await page.locator(
    `.tabulator-col[tabulator-field="${coverField.physicalName}"]`,
  ).waitFor({ timeout: 30_000 });
  const seeded = await applyProductMutation(page, tableId, [
    {
      kind: "insert",
      recordId: null,
      values: { [titleField.physicalName]: "Gallery Alpha" },
    },
    {
      kind: "insert",
      recordId: null,
      values: { [titleField.physicalName]: "Gallery Beta" },
    },
  ], "gallery-seed");
  recorder.check("Gallery seed commits two authoritative records",
    seeded.type === "mutation.apply" && seeded.payload?.status === "applied",
  { seeded });
  await chooseToolbarMore(page, "refresh");
  await waitForVisibleRowCount(page, 2);

  await page.getByTestId("view-create").click();
  const createDialog = page.locator(".view-dialog:visible");
  await createDialog.waitFor({ state: "visible", timeout: 10_000 });
  await createDialog.locator(".n-input input").fill("E2E Gallery");
  await page.getByTestId("view-kind-gallery").click();
  await selectVisibleNOption(page, "view-gallery-cover-field", "Cover");
  await selectVisibleNOption(page, "view-gallery-title-field", "Title");
  await page.getByTestId("view-dialog-confirm").click();

  const gallery = await waitForGalleryProjection(page, 2);
  const initialCardText = await page.getByTestId("gallery-card").allInnerTexts();
  recorder.check("Gallery projects both records with the configured empty-cover fallback",
    initialCardText.some(text => text.includes("Gallery Alpha"))
      && initialCardText.some(text => text.includes("Gallery Beta")),
  { initialCardText });

  const listed = await rawBridgeRequest(page, "preset.list", { collection: tableId });
  const persisted = listed.payload?.presets?.find(item => item.name === "E2E Gallery");
  recorder.check("Gallery configuration is persisted through the public preset interface",
    persisted?.collection === tableId
      && persisted.view?.kind === "gallery"
      && persisted.view?.layout === "gallery"
      && persisted.view?.titleField === titleField.physicalName
      && persisted.view?.coverField === coverField.physicalName
      && typeof persisted.revision === "string",
  { persisted });
  if (!persisted?.id || !persisted?.revision) {
    throw new Error(`persisted Gallery preset is unavailable: ${JSON.stringify(listed)}`);
  }

  const persistedTab = page.getByTestId(`view-tab-${persisted.id}`);
  await persistedTab.waitFor({ state: "visible", timeout: 10_000 });
  await page.getByTestId("nav-settings").click();
  await gallery.waitFor({ state: "hidden", timeout: 10_000 });
  await page.getByTestId("nav-tables").click();
  await persistedTab.waitFor({ state: "visible", timeout: 30_000 });
  await persistedTab.click();
  await waitForGalleryProjection(page, 2);
  const reopened = await rawBridgeRequest(page, "preset.list", { collection: tableId });
  recorder.check("Gallery reopens from the same persisted preset after leaving Tables",
    reopened.payload?.presets?.some(item => (
      item.id === persisted.id
        && item.revision === persisted.revision
        && item.view?.kind === "gallery"
    )),
  { reopened });

  const competing = await rawBridgeRequest(page, "preset.save", {
    collection: tableId,
    name: "E2E Gallery Winner",
    view: persisted.view,
    presetId: persisted.id,
    expectedRevision: persisted.revision,
    operationId: crypto.randomUUID(),
  });
  recorder.check("a competing preset save advances the authoritative revision",
    competing.type === "preset.save"
      && competing.payload?.id === persisted.id
      && typeof competing.payload?.revision === "string"
      && competing.payload.revision !== persisted.revision,
  { before: persisted.revision, competing });
  if (competing.type === "operation.failed") {
    throw new Error(`competing Gallery save failed: ${JSON.stringify(competing)}`);
  }

  await page.getByTestId(`view-actions-${persisted.id}`).click();
  const rename = page.locator(".n-dropdown-option-body:visible")
    .filter({ hasText: /重命名|Rename/i })
    .last();
  await rename.waitFor({ state: "visible", timeout: 10_000 });
  await rename.click();
  const renameDialog = page.locator(".view-dialog:visible");
  await renameDialog.waitFor({ state: "visible", timeout: 10_000 });
  await renameDialog.locator(".n-input input").fill("E2E Gallery Local Stale");
  await page.getByTestId("view-dialog-confirm").click();

  const conflictAlert = page.getByTestId("view-operation-error");
  await conflictAlert.waitFor({ state: "visible", timeout: 30_000 });
  const conflictText = await conflictAlert.innerText();
  await page.waitForFunction(() => (
    window.__vibetableE2EBridgeDiagnostics?.roundTrips
      ?.some(roundTrip => (
        roundTrip.requestType === "preset.save"
          && roundTrip.responseType === "preset.save"
          && roundTrip.code === "preset_edit_conflict"
      )) === true
  ), undefined, { timeout: 30_000 });
  const diagnostics = await readBridgeDiagnostics(page);
  const conflictRoundTrip = diagnostics?.roundTrips?.find(
    roundTrip => (
      roundTrip.requestType === "preset.save"
        && roundTrip.responseType === "preset.save"
        && roundTrip.code === "preset_edit_conflict"
    ),
  );
  recorder.check("the stale Gallery rename exposes the typed preset conflict before recovery",
    Boolean(conflictText.trim()) && conflictRoundTrip?.code === "preset_edit_conflict",
  { conflictText, conflictRoundTrip });

  await page.getByTestId("view-reload").click();
  await conflictAlert.waitFor({ state: "hidden", timeout: 30_000 });
  await page.waitForFunction(({ presetId, winner }) => {
    const tab = document.querySelector(`[data-testid="view-tab-${presetId}"]`);
    return tab?.textContent?.includes(winner) && tab.classList.contains("active");
  }, { presetId: persisted.id, winner: "E2E Gallery Winner" }, { timeout: 30_000 });
  await waitForGalleryProjection(page, 2);
  const authoritative = await rawBridgeRequest(page, "preset.list", { collection: tableId });
  const winner = authoritative.payload?.presets?.find(item => item.id === persisted.id);
  recorder.check("explicit reload adopts the winning revision and preserves the Gallery projection",
    winner?.name === "E2E Gallery Winner"
      && winner.revision === competing.payload?.revision
      && winner.view?.kind === "gallery",
  { winner, cardCount: await page.getByTestId("gallery-card").count() });

  await page.screenshot({
    path: path.join(runtime.evidenceDir, "19-gallery-lifecycle.png"),
    fullPage: true,
  });
}

async function waitForKanbanCardInLane(page, optionId, title) {
  const lane = page.locator(
    `[data-testid="kanban-lane"][data-option-id="${optionId}"]`,
  );
  await lane.waitFor({ state: "visible", timeout: 30_000 });
  await lane.getByTestId("kanban-card")
    .filter({ hasText: title })
    .waitFor({ state: "visible", timeout: 30_000 });
  return lane;
}

async function scenario20(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Kanban Records");

  const createFieldThroughUi = async (displayName, typeLabel, optionLabels = []) => {
    const displayNameInput = page.getByTestId("field-display-name");
    if (!(await displayNameInput.isVisible())) {
      await page.getByTestId("toolbar-field-manager").click();
      await displayNameInput.waitFor({ state: "visible", timeout: 30_000 });
    }
    await displayNameInput.locator("input").fill(displayName);
    await selectVisibleNOption(page, "field-logical-type", typeLabel);
    if (optionLabels.length > 0) {
      const optionSection = page.locator(".settings-section").filter({ hasText: "选项" });
      const optionInputs = optionSection.locator('.option-row input:not([type="color"])');
      for (let index = 0; index < optionLabels.length; index += 1) {
        await optionSection.getByRole("button", { name: "添加" }).click();
        await optionInputs.nth(index).waitFor({ state: "visible", timeout: 10_000 });
        await optionInputs.nth(index).fill(optionLabels[index]);
      }
    }
    await page.getByTestId("field-plan-button").click();
    await page.getByTestId("field-change-plan").waitFor({ state: "visible", timeout: 30_000 });
    const applyButton = page.getByTestId("field-apply-button");
    await page.waitForFunction(() => {
      const button = document.querySelector('[data-testid="field-apply-button"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    }, undefined, { timeout: 30_000 });
    await applyButton.click();
    await page.getByTestId("field-change-plan").waitFor({ state: "hidden", timeout: 30_000 });
    await page.getByTestId("field-close-button").click();
    await displayNameInput.waitFor({ state: "hidden", timeout: 10_000 });
  };

  await createFieldThroughUi("Title", "单行文本");
  await createFieldThroughUi("Status", "单选", ["Todo", "Done"]);
  await selectTable(page, "E2E Kanban Records");
  await chooseToolbarMore(page, "refresh");
  const titleHeader = page.locator(".tabulator-col").filter({ hasText: "Title" }).first();
  const statusHeader = page.locator(".tabulator-col").filter({ hasText: "Status" }).first();
  await titleHeader.waitFor({ state: "visible", timeout: 30_000 });
  await statusHeader.waitFor({ state: "visible", timeout: 30_000 });
  const titlePhysicalName = await titleHeader.getAttribute("tabulator-field");
  const statusPhysicalName = await statusHeader.getAttribute("tabulator-field");
  if (!titlePhysicalName || !statusPhysicalName) {
    throw new Error(`Kanban field authority is unavailable: ${JSON.stringify({
      titlePhysicalName,
      statusPhysicalName,
    })}`);
  }

  await page.getByTestId("view-create").click();
  const createDialog = page.locator(".view-dialog:visible");
  await createDialog.waitFor({ state: "visible", timeout: 10_000 });
  await createDialog.locator(".n-input input").fill("E2E Kanban");
  await page.getByTestId("view-kind-kanban").click();
  await selectVisibleNOption(page, "view-kanban-group-field", "Status");
  await selectVisibleNOption(page, "view-kanban-title-field", "Title");
  await page.getByTestId("view-dialog-confirm").click();

  const kanban = page.getByTestId("record-kanban-view");
  await kanban.waitFor({ state: "visible", timeout: 30_000 });
  const todoLane = page.getByTestId("kanban-lane").filter({ hasText: "Todo" }).first();
  const doneLane = page.getByTestId("kanban-lane").filter({ hasText: "Done" }).first();
  await todoLane.waitFor({ state: "visible", timeout: 30_000 });
  await doneLane.waitFor({ state: "visible", timeout: 30_000 });
  const todoOption = { optionId: await todoLane.getAttribute("data-option-id"), label: "Todo" };
  const doneOption = { optionId: await doneLane.getAttribute("data-option-id"), label: "Done" };
  if (!todoOption.optionId || !doneOption.optionId) {
    throw new Error(`Kanban option authority is unavailable: ${JSON.stringify({
      todoOption,
      doneOption,
    })}`);
  }

  const seeded = await applyProductMutation(page, tableId, [
    {
      kind: "insert",
      recordId: null,
      values: {
        [titlePhysicalName]: "Kanban Alpha",
        [statusPhysicalName]: todoOption.optionId,
      },
    },
    {
      kind: "insert",
      recordId: null,
      values: {
        [titlePhysicalName]: "Kanban Beta",
        [statusPhysicalName]: todoOption.optionId,
      },
    },
  ], "kanban-seed");
  recorder.check("Kanban seed commits stable option IDs for two authoritative records",
    seeded.type === "mutation.apply" && seeded.payload?.status === "applied",
  { seeded, todoOptionId: todoOption.optionId });
  await chooseToolbarMore(page, "refresh");
  await waitForKanbanCardInLane(page, todoOption.optionId, "Kanban Alpha");
  const initialText = await kanban.innerText();
  recorder.check("Kanban renders active labels and an empty authoritative target lane",
    initialText.includes("Todo")
      && initialText.includes("Done")
      && !initialText.includes(todoOption.optionId)
      && !initialText.includes(doneOption.optionId)
      && await todoLane.getByTestId("kanban-card").count() === 2
      && await doneLane.getByTestId("kanban-card").count() === 0,
  { initialText, todoOptionId: todoOption.optionId, doneOptionId: doneOption.optionId });

  const alphaCard = todoLane.getByTestId("kanban-card")
    .filter({ hasText: "Kanban Alpha" });
  await alphaCard.dragTo(doneLane.locator(".kanban-cards"));
  await waitForKanbanCardInLane(page, doneOption.optionId, "Kanban Alpha");
  const authoritative = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  });
  const moved = authoritative.payload?.rows?.find(
    row => row[titlePhysicalName] === "Kanban Alpha",
  );
  recorder.check("drag waits for host authority and persists the target optionId rather than its label",
    authoritative.type === "query.page"
      && moved?.[statusPhysicalName] === doneOption.optionId
      && moved?.[statusPhysicalName] !== doneOption.label,
  { moved, authoritative });

  await chooseToolbarMore(page, "refresh");
  await waitForKanbanCardInLane(page, doneOption.optionId, "Kanban Alpha");
  const listed = await rawBridgeRequest(page, "preset.list", { collection: tableId });
  const persisted = listed.payload?.presets?.find(item => item.name === "E2E Kanban");
  recorder.check("Kanban definition persists the selected group and title fields",
    persisted?.collection === tableId
      && persisted.view?.kind === "kanban"
      && persisted.view?.layout === "kanban"
      && persisted.view?.groupField === statusPhysicalName
      && persisted.view?.titleField === titlePhysicalName,
  { persisted });
  if (!persisted?.id) {
    throw new Error(`persisted Kanban preset is unavailable: ${JSON.stringify(listed)}`);
  }

  await page.getByTestId("nav-settings").click();
  await kanban.waitFor({ state: "hidden", timeout: 10_000 });
  await page.getByTestId("nav-tables").click();
  const persistedTab = page.getByTestId(`view-tab-${persisted.id}`);
  await persistedTab.waitFor({ state: "visible", timeout: 30_000 });
  await persistedTab.click();
  await waitForKanbanCardInLane(page, doneOption.optionId, "Kanban Alpha");
  recorder.check("Kanban moved card survives refresh and leaving then reopening Tables",
    await doneLane.getByTestId("kanban-card").filter({ hasText: "Kanban Alpha" }).count() === 1,
  { presetId: persisted.id, doneOptionId: doneOption.optionId });

  await page.screenshot({
    path: path.join(runtime.evidenceDir, "20-kanban-lane-drag.png"),
    fullPage: true,
  });
}

async function waitForCalendarRecordOnDate(page, date, title) {
  const day = page.locator(
    `[data-testid="calendar-day"][data-date="${date}"]`,
  );
  await day.waitFor({ state: "visible", timeout: 30_000 });
  await day.getByTestId("calendar-record")
    .filter({ hasText: title })
    .waitFor({ state: "visible", timeout: 30_000 });
  return day;
}

async function dragCalendarRecordToDate(page, sourceDate, targetDate, title) {
  await page.evaluate(({ sourceDate: source, targetDate: target, title: recordTitle }) => {
    const sourceDay = document.querySelector(
      `[data-testid="calendar-day"][data-date="${source}"]`,
    );
    const targetDay = document.querySelector(
      `[data-testid="calendar-day"][data-date="${target}"]`,
    );
    const record = [...(sourceDay?.querySelectorAll('[data-testid="calendar-record"]') ?? [])]
      .find(element => element.textContent?.includes(recordTitle));
    if (!(record instanceof HTMLElement) || !(targetDay instanceof HTMLElement)
      || record.draggable !== true) {
      throw new Error(`Calendar drag endpoints are unavailable: ${JSON.stringify({
        source,
        target,
        recordTitle,
      })}`);
    }
    const dataTransfer = new DataTransfer();
    const dispatch = (element, type) => element.dispatchEvent(new DragEvent(type, {
      bubbles: true,
      cancelable: true,
      dataTransfer,
    }));
    dispatch(record, "dragstart");
    dispatch(targetDay, "dragenter");
    dispatch(targetDay, "dragover");
    dispatch(targetDay, "drop");
    dispatch(record, "dragend");
  }, { sourceDate, targetDate, title });
}

async function scenario21(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Calendar Records");

  const createFieldThroughUi = async (displayName, typeLabel) => {
    const displayNameInput = page.getByTestId("field-display-name");
    if (!(await displayNameInput.isVisible())) {
      await page.getByTestId("toolbar-field-manager").click();
      await displayNameInput.waitFor({ state: "visible", timeout: 30_000 });
    }
    await displayNameInput.locator("input").fill(displayName);
    await selectVisibleNOption(page, "field-logical-type", typeLabel);
    await page.getByTestId("field-plan-button").click();
    await page.getByTestId("field-change-plan").waitFor({ state: "visible", timeout: 30_000 });
    const applyButton = page.getByTestId("field-apply-button");
    await page.waitForFunction(() => {
      const button = document.querySelector('[data-testid="field-apply-button"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    }, undefined, { timeout: 30_000 });
    await applyButton.click();
    await page.getByTestId("field-change-plan").waitFor({ state: "hidden", timeout: 30_000 });
    await page.getByTestId("field-close-button").click();
    await displayNameInput.waitFor({ state: "hidden", timeout: 10_000 });
  };

  await createFieldThroughUi("Title", "单行文本");
  await createFieldThroughUi("Due Date", "日期");
  await selectTable(page, "E2E Calendar Records");
  await chooseToolbarMore(page, "refresh");
  const titleHeader = page.locator(".tabulator-col").filter({ hasText: "Title" }).first();
  const dateHeader = page.locator(".tabulator-col").filter({ hasText: "Due Date" }).first();
  await titleHeader.waitFor({ state: "visible", timeout: 30_000 });
  await dateHeader.waitFor({ state: "visible", timeout: 30_000 });
  const titlePhysicalName = await titleHeader.getAttribute("tabulator-field");
  const datePhysicalName = await dateHeader.getAttribute("tabulator-field");
  if (!titlePhysicalName || !datePhysicalName) {
    throw new Error(`Calendar field authority is unavailable: ${JSON.stringify({
      titlePhysicalName,
      datePhysicalName,
    })}`);
  }

  await page.getByTestId("view-create").click();
  const createDialog = page.locator(".view-dialog:visible");
  await createDialog.waitFor({ state: "visible", timeout: 10_000 });
  await createDialog.locator(".n-input input").fill("E2E Calendar");
  await page.getByTestId("view-kind-calendar").click();
  await selectVisibleNOption(page, "view-temporal-date-field", "Due Date");
  await selectVisibleNOption(page, "view-temporal-title-field", "Title");
  await page.getByTestId("view-dialog-confirm").click();

  const calendar = page.getByTestId("record-calendar-view");
  await calendar.waitFor({ state: "visible", timeout: 30_000 });
  const sourceDate = "2026-08-12";
  const targetDate = "2026-08-20";
  const seeded = await applyProductMutation(page, tableId, [{
    kind: "insert",
    recordId: null,
    values: {
      [titlePhysicalName]: "Calendar Alpha",
      [datePhysicalName]: sourceDate,
    },
  }], "calendar-seed");
  recorder.check("Calendar seed commits one authoritative date record",
    seeded.type === "mutation.apply" && seeded.payload?.status === "applied",
  { seeded, sourceDate });
  await chooseToolbarMore(page, "refresh");

  const sourceDay = await waitForCalendarRecordOnDate(page, sourceDate, "Calendar Alpha");
  const targetDay = page.locator(
    `[data-testid="calendar-day"][data-date="${targetDate}"]`,
  );
  await targetDay.waitFor({ state: "visible", timeout: 30_000 });
  const record = sourceDay.getByTestId("calendar-record")
    .filter({ hasText: "Calendar Alpha" });
  const recordDraggable = await record.getAttribute("draggable");
  const sourceDayDate = await sourceDay.getAttribute("data-date");
  const targetDayDate = await targetDay.getAttribute("data-date");
  recorder.check("Calendar exposes only an enabled draggable record and a dated drop target",
    recordDraggable === "true"
      && sourceDayDate === sourceDate
      && targetDayDate === targetDate,
  { sourceDate, targetDate, recordDraggable, sourceDayDate, targetDayDate });
  await dragCalendarRecordToDate(page, sourceDate, targetDate, "Calendar Alpha");
  await waitForCalendarRecordOnDate(page, targetDate, "Calendar Alpha");

  const authoritative = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  });
  const moved = authoritative.payload?.rows?.find(
    row => row[titlePhysicalName] === "Calendar Alpha",
  );
  recorder.check("Calendar drag waits for mutation authority and persists the target date",
    authoritative.type === "query.page"
      && String(moved?.[datePhysicalName] ?? "").startsWith(targetDate),
  { moved, authoritative });

  await chooseToolbarMore(page, "refresh");
  await waitForCalendarRecordOnDate(page, targetDate, "Calendar Alpha");
  const listed = await rawBridgeRequest(page, "preset.list", { collection: tableId });
  const persisted = listed.payload?.presets?.find(item => item.name === "E2E Calendar");
  recorder.check("Calendar definition persists the selected date and title fields",
    persisted?.collection === tableId
      && persisted.view?.kind === "calendar"
      && persisted.view?.layout === "calendar"
      && persisted.view?.dateField === datePhysicalName
      && persisted.view?.titleField === titlePhysicalName,
  { persisted });
  if (!persisted?.id) {
    throw new Error(`persisted Calendar preset is unavailable: ${JSON.stringify(listed)}`);
  }

  await page.getByTestId("nav-settings").click();
  await calendar.waitFor({ state: "hidden", timeout: 10_000 });
  await page.getByTestId("nav-tables").click();
  const persistedTab = page.getByTestId(`view-tab-${persisted.id}`);
  await persistedTab.waitFor({ state: "visible", timeout: 30_000 });
  await persistedTab.click();
  await waitForCalendarRecordOnDate(page, targetDate, "Calendar Alpha");
  recorder.check("Calendar moved record survives refresh and leaving then reopening Tables",
    await page.locator(`[data-testid="calendar-day"][data-date="${targetDate}"]`)
      .getByTestId("calendar-record")
      .filter({ hasText: "Calendar Alpha" })
      .count() === 1,
  { presetId: persisted.id, targetDate });

  await page.screenshot({
    path: path.join(runtime.evidenceDir, "21-calendar-date-move.png"),
    fullPage: true,
  });
}

async function waitForTimelineRecordInRange(page, targetDate, title) {
  await page.waitForFunction(({ target, recordTitle }) => {
    const record = [...document.querySelectorAll('[data-testid="timeline-record"]')]
      .find(element => element instanceof HTMLElement
        && element.getClientRects().length > 0
        && element.textContent?.includes(recordTitle));
    const track = record?.closest('[data-testid="timeline-track"]');
    if (!(record instanceof HTMLElement)
      || !(track instanceof HTMLElement)
      || track.getClientRects().length === 0) return false;
    const start = track.dataset.startDate;
    const end = track.dataset.endDate;
    return Boolean(
      record.dataset.date === target
      && start
      && end
      && start <= target
      && target <= end,
    );
  }, { target: targetDate, recordTitle: title }, { timeout: 30_000 });
  return page.getByTestId("timeline-record")
    .filter({ hasText: title })
    .locator('xpath=ancestor::*[@data-testid="timeline-track"]')
    .first();
}

async function dragTimelineRecordToDate(page, title, targetDate) {
  await page.evaluate(({ title: recordTitle, targetDate: target }) => {
    const record = [...document.querySelectorAll('[data-testid="timeline-record"]')]
      .find(element => element.textContent?.includes(recordTitle));
    const track = record?.closest('[data-testid="timeline-track"]');
    if (!(record instanceof HTMLElement) || !(track instanceof HTMLElement)
      || record.draggable !== true) {
      throw new Error(`Timeline drag endpoints are unavailable: ${JSON.stringify({
        recordTitle,
        target,
      })}`);
    }
    const start = track.dataset.startDate;
    const end = track.dataset.endDate;
    const dates = [];
    const cursor = start ? new Date(`${start}T00:00:00.000Z`) : new Date(Number.NaN);
    const last = end ? new Date(`${end}T00:00:00.000Z`) : new Date(Number.NaN);
    while (!Number.isNaN(cursor.valueOf())
      && !Number.isNaN(last.valueOf())
      && cursor <= last
      && dates.length < 10_000) {
      dates.push(cursor.toISOString().slice(0, 10));
      cursor.setUTCDate(cursor.getUTCDate() + 1);
    }
    const targetOffset = dates.indexOf(target);
    if (targetOffset < 0) {
      throw new Error(`Timeline target is outside the rendered range: ${JSON.stringify({
        start,
        end,
        target,
      })}`);
    }
    const bounds = track.getBoundingClientRect();
    const clientX = bounds.left + ((targetOffset + 0.5) / dates.length) * bounds.width;
    const dataTransfer = new DataTransfer();
    const dispatch = (element, type) => element.dispatchEvent(new DragEvent(type, {
      bubbles: true,
      cancelable: true,
      clientX,
      dataTransfer,
    }));
    dispatch(record, "dragstart");
    dispatch(track, "dragenter");
    dispatch(track, "dragover");
    dispatch(track, "drop");
    dispatch(record, "dragend");
  }, { title, targetDate });
}

async function scenario22(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const tableId = await createEmptyTable(page, "E2E Timeline Records");

  const createFieldThroughUi = async (displayName, typeLabel) => {
    const displayNameInput = page.getByTestId("field-display-name");
    if (!(await displayNameInput.isVisible())) {
      await page.getByTestId("toolbar-field-manager").click();
      await displayNameInput.waitFor({ state: "visible", timeout: 30_000 });
    }
    await displayNameInput.locator("input").fill(displayName);
    await selectVisibleNOption(page, "field-logical-type", typeLabel);
    await page.getByTestId("field-plan-button").click();
    await page.getByTestId("field-change-plan").waitFor({ state: "visible", timeout: 30_000 });
    const applyButton = page.getByTestId("field-apply-button");
    await page.waitForFunction(() => {
      const button = document.querySelector('[data-testid="field-apply-button"]');
      return button instanceof HTMLButtonElement && !button.disabled;
    }, undefined, { timeout: 30_000 });
    await applyButton.click();
    await page.getByTestId("field-change-plan").waitFor({ state: "hidden", timeout: 30_000 });
    await page.getByTestId("field-close-button").click();
    await displayNameInput.waitFor({ state: "hidden", timeout: 10_000 });
  };

  await createFieldThroughUi("Title", "单行文本");
  await createFieldThroughUi("Start Date", "日期");
  await selectTable(page, "E2E Timeline Records");
  await chooseToolbarMore(page, "refresh");
  const titleHeader = page.locator(".tabulator-col").filter({ hasText: "Title" }).first();
  const dateHeader = page.locator(".tabulator-col").filter({ hasText: "Start Date" }).first();
  await titleHeader.waitFor({ state: "visible", timeout: 30_000 });
  await dateHeader.waitFor({ state: "visible", timeout: 30_000 });
  const titlePhysicalName = await titleHeader.getAttribute("tabulator-field");
  const datePhysicalName = await dateHeader.getAttribute("tabulator-field");
  if (!titlePhysicalName || !datePhysicalName) {
    throw new Error(`Timeline field authority is unavailable: ${JSON.stringify({
      titlePhysicalName,
      datePhysicalName,
    })}`);
  }

  await page.getByTestId("view-create").click();
  const createDialog = page.locator(".view-dialog:visible");
  await createDialog.waitFor({ state: "visible", timeout: 10_000 });
  await createDialog.locator(".n-input input").fill("E2E Timeline");
  await page.getByTestId("view-kind-timeline").click();
  await selectVisibleNOption(page, "view-temporal-date-field", "Start Date");
  await selectVisibleNOption(page, "view-temporal-title-field", "Title");
  await page.getByTestId("view-dialog-confirm").click();

  const timeline = page.getByTestId("record-timeline-view");
  await timeline.waitFor({ state: "visible", timeout: 30_000 });
  const sourceDate = "2026-08-12";
  const targetDate = "2026-08-16";
  const seeded = await applyProductMutation(page, tableId, [{
    kind: "insert",
    recordId: null,
    values: {
      [titlePhysicalName]: "Timeline Alpha",
      [datePhysicalName]: sourceDate,
    },
  }], "timeline-seed");
  recorder.check("Timeline seed commits one authoritative point-date record",
    seeded.type === "mutation.apply" && seeded.payload?.status === "applied",
  { seeded, sourceDate });
  await chooseToolbarMore(page, "refresh");

  const sourceTrack = await waitForTimelineRecordInRange(
    page,
    sourceDate,
    "Timeline Alpha",
  );
  const record = page.getByTestId("timeline-record").filter({ hasText: "Timeline Alpha" });
  const recordDraggable = await record.getAttribute("draggable");
  const renderedStartDate = await sourceTrack.getAttribute("data-start-date");
  const renderedEndDate = await sourceTrack.getAttribute("data-end-date");
  recorder.check("Timeline exposes an enabled point record and a padded logical-day viewport",
    recordDraggable === "true"
      && typeof renderedStartDate === "string"
      && typeof renderedEndDate === "string"
      && renderedStartDate <= targetDate
      && targetDate <= renderedEndDate,
  { recordDraggable, sourceDate, targetDate, renderedStartDate, renderedEndDate });
  await dragTimelineRecordToDate(page, "Timeline Alpha", targetDate);
  await waitForTimelineRecordInRange(page, targetDate, "Timeline Alpha");

  const authoritative = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  });
  const moved = authoritative.payload?.rows?.find(
    row => row[titlePhysicalName] === "Timeline Alpha",
  );
  recorder.check("Timeline drag waits for mutation authority and persists the target date",
    authoritative.type === "query.page"
      && String(moved?.[datePhysicalName] ?? "").startsWith(targetDate),
  { moved, authoritative });

  await chooseToolbarMore(page, "refresh");
  await waitForTimelineRecordInRange(page, targetDate, "Timeline Alpha");
  const listed = await rawBridgeRequest(page, "preset.list", { collection: tableId });
  const persisted = listed.payload?.presets?.find(item => item.name === "E2E Timeline");
  recorder.check("Timeline definition persists one date field without a range end",
    persisted?.collection === tableId
      && persisted.view?.kind === "timeline"
      && persisted.view?.layout === "timeline"
      && persisted.view?.dateField === datePhysicalName
      && persisted.view?.endDateField == null
      && persisted.view?.titleField === titlePhysicalName,
  { persisted });
  if (!persisted?.id) {
    throw new Error(`persisted Timeline preset is unavailable: ${JSON.stringify(listed)}`);
  }

  await page.getByTestId("nav-settings").click();
  await timeline.waitFor({ state: "hidden", timeout: 10_000 });
  await page.getByTestId("nav-tables").click();
  const persistedTab = page.getByTestId(`view-tab-${persisted.id}`);
  await persistedTab.waitFor({ state: "visible", timeout: 30_000 });
  await persistedTab.click();
  await waitForTimelineRecordInRange(page, targetDate, "Timeline Alpha");
  recorder.check("Timeline moved record survives refresh and leaving then reopening Tables",
    await page.getByTestId("timeline-record")
      .filter({ hasText: "Timeline Alpha" })
      .count() === 1,
  { presetId: persisted.id, targetDate });

  await page.screenshot({
    path: path.join(runtime.evidenceDir, "22-timeline-date-move.png"),
    fullPage: true,
  });
}

function hasExactWorkspaceWire(message) {
  const outer = message?.wire;
  const inner = message?.payload?.wire;
  if (!outer || !inner || typeof outer !== "object" || typeof inner !== "object") return false;
  const outerKeys = Object.keys(outer).sort();
  const innerKeys = Object.keys(inner).sort();
  return typeof message.requestId === "string"
    && message.requestId.length > 0
    && typeof outer.operationId === "string"
    && outer.scope === "global"
    && outerKeys.length === 3
    && innerKeys.length === 3
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu
      .test(outer.operationId)
    && Number.isSafeInteger(outer.sequence)
    && outer.sequence >= 0
    && outerKeys.length === innerKeys.length
    && outerKeys.every((key, index) => key === innerKeys[index] && outer[key] === inner[key]);
}

async function activateDirectoryReplicaWorkspace(page, { method, activate }) {
  const workspaceCenter = page.getByTestId("workspace-center");
  const databaseOpened = await activateWorkspaceAndWaitForDatabaseOpened({
    beginCapture: (expectation) => beginWorkspaceActivationCapture(page, {
      ...expectation, waitForHydration: true,
    }),
    activate,
    waitForActivation: (timeoutMs) => Promise.race([
      workspaceCenter.waitFor({ state: "hidden", timeout: timeoutMs })
        .then(() => ({ kind: "opened" })),
      page.getByTestId("workspace-operation-error")
        .waitFor({ state: "visible", timeout: timeoutMs })
        .then(async () => ({
          kind: "failed",
          message: await page.getByTestId("workspace-operation-error").innerText(),
        })),
    ]),
    method,
  });
  const session = await page.evaluate(
    () => window.__vibetableE2EBridgeCapture?.session ?? null,
  );
  if (!session) throw new Error(`activation capture omitted ${method} session`);
  return { databaseOpened, session };
}

async function readDirectoryReplicaCheckpoint(page, tableId) {
  const query = await rawBridgeRequest(page, "query.page", {
    tableId,
    query: { filters: [], sorts: [], offset: 0, limit: 10 },
  });
  const replicaReply = await rawWorkspaceV2Request(page, "replica.status", {});
  return { query, replica: replicaReply.result };
}

async function scenario23(page, recorder, _network, runtime) {
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  const originalSession = await page.evaluate(
    () => window.__vibetableE2EBridgeCapture?.session ?? null,
  );
  if (!originalSession) throw new Error("initial workspace activation omitted its session");
  await openWorkspaceCenterFromSwitcher(page);
  await page.getByTestId("workspace-create").click();
  const modal = page.getByTestId("workspace-flow-modal");
  const workspaceName = "E2E Directory Replica";
  await modal.locator("input").first().fill(workspaceName);
  await modal.locator('.n-radio-button:has(input[value="other"])').click();
  await modal.locator('.n-radio-button:has(input[value="mirrored"])').click();
  const syncMark = page.getByTestId("workspace-user-marked-sync");
  await syncMark.click();
  recorder.check("Workspace Center exposes the requested mirrored directory topology",
    await modal.locator('input[value="other"]').isChecked()
      && await modal.locator('input[value="mirrored"]').isChecked()
      && await syncMark.getAttribute("aria-checked") === "true",
  { workspaceName });

  await beginWorkspaceV2MethodCapture(page, "workspace.create");
  await page.getByTestId("workspace-flow-confirm").click();
  const createTerminal = await waitForCapturedBridgeMessage(page, 60_000);
  const workspaceId = createTerminal.payload?.result?.workspaceId;
  recorder.check("mirrored workspace creation returns one exact global wire terminal",
    createTerminal.type === "workspace.v2.response"
      && createTerminal.payload?.method === "workspace.create"
      && createTerminal.payload?.ok === true
      && createTerminal.payload?.result?.status === "created"
      && typeof workspaceId === "string"
      && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu
        .test(workspaceId)
      && hasExactWorkspaceWire(createTerminal),
  { createTerminal });
  const card = page.getByTestId("workspace-center").getByRole("button", {
    name: new RegExp(workspaceName),
  });
  await card.waitFor({ state: "visible", timeout: 60_000 });

  const initial = await activateDirectoryReplicaWorkspace(page, {
    method: "workspace.switch",
    activate: () => card.click(),
  });
  const initialSession = initial.session;
  const initialIdentity = workspaceId.replaceAll("-", "");
  recorder.check("new directory replica opens one exact provisional project session",
    initialSession.workspaceId === workspaceId
      && initialSession.sessionEpoch > originalSession.sessionEpoch
      && initialSession.state === "openedProvisional"
      && initialSession.openMode === "provisional"
      && initialSession.writable === false
      && initialSession.provisional === true
      && initial.databaseOpened.payload?.projectKey === `local:${initialIdentity}`
      && initial.databaseOpened.payload?.projectRevision
        === `${initialIdentity}:${initialSession.sessionEpoch}`,
  { originalSession, initial });

  await page.getByTestId("home-view").waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("nav-tables").click();
  const tableName = "E2E Directory Replica Records";
  const table = await createSimpleTable(page, tableName, "Value");
  await selectTable(page, tableName);
  const insertCaptureId = await page.evaluate(
    installTableMutationReceiptCaptureInPage,
    { requestType: "table.insertRowRequested" },
  );
  await page.getByTestId("toolbar-insert-row").click({ timeout: 30_000 });
  const insertReceipt = await waitForCapturedBridgeMessage(page, 30_000, insertCaptureId);
  recorder.check("public row insertion returns one exact committed mutation receipt",
    insertReceipt.type === "table.rowsInserted"
      && insertReceipt.owner?.requestType === "table.insertRowRequested"
      && insertReceipt.owner?.table === table.tableId
      && insertReceipt.owner?.workspaceId === initialSession.workspaceId
      && insertReceipt.owner?.sessionEpoch === initialSession.sessionEpoch
      && Array.isArray(insertReceipt.owner?.valueKeys)
      && insertReceipt.owner.valueKeys.length === 0
      && (typeof insertReceipt.payload?.rowKey === "string"
        || Number.isSafeInteger(insertReceipt.payload?.rowKey))
      && typeof insertReceipt.payload?.revision?.databaseSessionId === "string"
      && insertReceipt.payload.revision.databaseSessionId.length > 0
      && typeof insertReceipt.payload?.revision?.schemaRevision === "string"
      && insertReceipt.payload.revision.schemaRevision.length > 0
      && insertReceipt.owner?.schemaRevision === insertReceipt.payload.revision.schemaRevision
      && Number.isSafeInteger(insertReceipt.payload?.revision?.dataRevision)
      && insertReceipt.payload.revision.dataRevision >= 0,
  { insertReceipt });
  const value = "directory replica survives";
  const cell = page.locator(
    `.tabulator-cell.tabulator-editable[tabulator-field="${table.field.physicalName}"]`,
  ).first();
  await cell.waitFor({ state: "visible", timeout: 30_000 });
  await cell.dblclick();
  const editor = cell.locator("input, textarea").first();
  await editor.waitFor({ state: "visible", timeout: 10_000 });
  await editor.fill(value);
  const editCaptureId = await page.evaluate(
    installTableMutationReceiptCaptureInPage,
    { requestType: "table.updateCellRequested" },
  );
  await editor.press("Enter");
  const editTerminal = await waitForCapturedBridgeMessage(page, 30_000, editCaptureId);
  recorder.check("public cell edit commits the inserted row with an advancing revision",
    editTerminal.type === "table.editCommitted"
      && editTerminal.owner?.requestType === "table.updateCellRequested"
      && editTerminal.owner?.table === table.tableId
      && editTerminal.owner?.workspaceId === initialSession.workspaceId
      && editTerminal.owner?.sessionEpoch === initialSession.sessionEpoch
      && editTerminal.owner?.rowKey === insertReceipt.payload?.rowKey
      && editTerminal.owner?.column === table.field.physicalName
      && editTerminal.owner?.schemaRevision === insertReceipt.payload?.revision?.schemaRevision
      && editTerminal.payload?.rowKey === insertReceipt.payload?.rowKey
      && editTerminal.payload?.column === table.field.physicalName
      && editTerminal.payload?.storedValue === value
      && editTerminal.payload?.currentRow?.[table.field.physicalName] === value
      && editTerminal.payload?.revision?.databaseSessionId
        === insertReceipt.payload?.revision?.databaseSessionId
      && editTerminal.payload?.revision?.schemaRevision
        === insertReceipt.payload?.revision?.schemaRevision
      && Number.isSafeInteger(editTerminal.payload?.revision?.dataRevision)
      && editTerminal.payload.revision.dataRevision
        > insertReceipt.payload?.revision?.dataRevision,
  { insertReceipt, editTerminal });
  await cell.filter({ hasText: value }).waitFor({ state: "visible", timeout: 30_000 });

  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-nav-storage").click();
  await page.getByTestId("storage-settings").waitFor({ state: "visible", timeout: 30_000 });
  await page.getByTestId("workspace-storage-release-cache-preview").click({ timeout: 90_000 });
  await page.getByTestId("workspace-storage-confirmation").locator("input").fill(workspaceName);
  await beginWorkspaceV2MethodCapture(page, "workspace.storage.apply");
  await page.getByTestId("workspace-storage-relocate-apply").click();
  const releaseTerminal = await waitForCapturedBridgeMessage(page, 60_000);
  const releasedStorage = releaseTerminal.payload?.result?.storage;
  recorder.check("public storage release applies the verified replica and exact wire identity",
    releaseTerminal.type === "workspace.v2.response"
      && releaseTerminal.payload?.method === "workspace.storage.apply"
      && releaseTerminal.payload?.ok === true
      && releaseTerminal.payload?.result?.workspaceId === workspaceId
      && releaseTerminal.payload?.result?.status === "applied"
      && releasedStorage?.mode === "mirrored"
      && releasedStorage?.pendingSync === false
      && releasedStorage?.replicaVerified === true
      && hasExactWorkspaceWire(releaseTerminal),
  { releaseTerminal });

  await page.getByTestId("nav-home").click();
  const workspaceCenter = page.getByTestId("workspace-center");
  await workspaceCenter.waitFor({ state: "visible", timeout: 60_000 });
  const reopenCard = workspaceCenter.getByRole("button", { name: new RegExp(workspaceName) });
  const reopened = await activateDirectoryReplicaWorkspace(page, {
    method: "workspace.open",
    activate: () => reopenCard.click(),
  });
  const session = reopened.session;
  const identity = workspaceId.replaceAll("-", "");
  recorder.check("released directory replica reopens the same UUID as one provisional session",
    session.workspaceId === workspaceId
      && session.sessionEpoch > initialSession.sessionEpoch
      && session.state === "openedProvisional"
      && session.openMode === "provisional"
      && session.writable === false
      && session.provisional === true
      && reopened.databaseOpened.payload?.projectKey === `local:${identity}`
      && reopened.databaseOpened.payload?.projectRevision === `${identity}:${session.sessionEpoch}`,
  { initialSession, reopened });

  const beforeRestart = await readDirectoryReplicaCheckpoint(page, table.tableId);
  const beforeRow = beforeRestart.query.payload?.rows?.[0];
  const beforeSnapshot = beforeRestart.query.payload?.snapshot;
  const replica = beforeRestart.replica;
  recorder.check("one query and one status expose the released directory replica",
    beforeRestart.query.type === "query.page"
      && beforeRestart.query.payload?.rows?.length === 1
      && beforeRow?.[table.field.physicalName] === value
      && beforeRow?.id === insertReceipt.payload?.rowKey
      && beforeSnapshot?.table === table.tableId
      && typeof beforeSnapshot?.databaseId === "string"
      && beforeSnapshot.databaseId.length > 0
      && beforeSnapshot?.schemaRevision === editTerminal.payload?.revision?.schemaRevision
      && beforeSnapshot?.dataRevision === editTerminal.payload?.revision?.dataRevision
      && replica.coordinationStrength === "advisory"
      && replica.syncState === "replicated"
      && replica.pendingSync === false,
  { beforeRestart });

  const bridgeBeforeRestart = await waitForBridgeDiagnosticsToSettle(page);
  recorder.check("directory replica restart begins from a quiescent bridge",
    bridgeBeforeRestart !== null
      && (bridgeBeforeRestart.failures ?? []).length === 0
      && (bridgeBeforeRestart.pending ?? []).length === 0,
  { bridgeBeforeRestart });

  await beginBridgeMessageCapture(page, ["database.opened"]);
  const kill = await requestSidecarKill(
    runtime,
    "verify released directory replica survives restart",
  );
  const replacementOpened = await waitForCapturedBridgeMessage(page, 60_000);
  recorder.check("the packaged controller kills one exact sidecar before same-session readiness",
    kill.status === "completed"
      && kill.action === "kill-sidecar"
      && kill.processName === "vibetable-pb.exe"
      && Number.isInteger(kill.pid)
      && kill.pid > 0
      && replacementOpened.type === "database.opened"
      && replacementOpened.payload?.projectKey === `local:${identity}`
      && replacementOpened.payload?.projectRevision === `${identity}:${session.sessionEpoch}`,
  { kill, replacementOpened, session });

  const afterRestart = await readDirectoryReplicaCheckpoint(page, table.tableId);
  const afterRow = afterRestart.query.payload?.rows?.[0];
  const afterSnapshot = afterRestart.query.payload?.snapshot;
  recorder.check("replacement sidecar preserves the exact row, revisions, and replica status",
    afterRestart.query.type === "query.page"
      && afterRestart.query.payload?.rows?.length === 1
      && afterRow?.id === beforeRow.id
      && afterRow?.[table.field.physicalName] === value
      && afterSnapshot?.table === beforeSnapshot.table
      && afterSnapshot?.databaseId === beforeSnapshot.databaseId
      && afterSnapshot?.schemaRevision === beforeSnapshot.schemaRevision
      && afterSnapshot?.dataRevision === beforeSnapshot.dataRevision
      && afterRestart.replica.coordinationStrength === replica.coordinationStrength
      && afterRestart.replica.syncState === replica.syncState
      && afterRestart.replica.pendingSync === replica.pendingSync,
  { beforeRestart, afterRestart });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "23-directory-replica-recovery.png"),
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
  "13-protection-policy": scenario13,
  "14-document-diff": scenario14,
  "15-workspace-snapshot-package": scenario15,
  "16-dashboard-lifecycle": scenario16,
  "17-interface-lifecycle": scenario17,
  "18-workspace-search": scenario18,
  "19-gallery-lifecycle": scenario19,
  "20-kanban-lane-drag": scenario20,
  "21-calendar-date-move": scenario21,
  "22-timeline-date-move": scenario22,
  "23-directory-replica-recovery": scenario23,
};

async function main() {
  const argv = process.argv.slice(2);
  if (argv.length === 1 && argv[0] === "--list-scenarios") {
    fsSync.writeSync(1, `${JSON.stringify(Object.keys(scenarios))}\n`);
    return;
  }
  const args = parseArgs(argv);
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
        dataRoot: path.resolve(args["data-root"]),
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
    result.bridgeDiagnostics = await waitForBridgeDiagnosticsToSettle(page);
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
