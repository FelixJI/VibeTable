import fs from "node:fs/promises";
import fsSync from "node:fs";
import { performance } from "node:perf_hooks";
import process from "node:process";
import { pathToFileURL } from "node:url";

import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";


function parseArgs(argv) {
  const result = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name?.startsWith("--") || value === undefined) {
      throw new Error(`invalid argument list near ${name ?? "<end>"}`);
    }
    result[name.slice(2)] = value;
  }
  for (const required of ["cdp-url", "json-report"]) {
    if (!result[required]) throw new Error(`--${required} is required`);
  }
  return result;
}


function requestedReportPath(argv) {
  const index = argv.indexOf("--json-report");
  return index >= 0 && typeof argv[index + 1] === "string" ? argv[index + 1] : undefined;
}


export async function requireStableFirstTable({
  expectedTableId,
  sample,
  stableForMs = 1_000,
  pollMs = 50,
  now = () => performance.now(),
  sleep = (duration) => new Promise((resolve) => setTimeout(resolve, duration)),
}) {
  if (!expectedTableId?.startsWith("tbl_")) {
    throw new Error(`invalid first table identity: ${expectedTableId}`);
  }
  if (!Number.isInteger(stableForMs) || stableForMs <= 0) {
    throw new Error(`invalid stable window: ${stableForMs}`);
  }
  if (!Number.isInteger(pollMs) || pollMs <= 0) {
    throw new Error(`invalid stable poll interval: ${pollMs}`);
  }
  let stableStarted;
  while (true) {
    const observed = await sample();
    if (observed.tableId !== expectedTableId || observed.activeTableId !== expectedTableId) {
      throw new Error(
        `first table identity changed: expected ${expectedTableId}, `
        + `observed ${observed.tableId}/${observed.activeTableId}`,
      );
    }
    if (observed.errorOverlayVisible) {
      throw new Error("first table exposed a visible error overlay");
    }
    if (!observed.tableSummaryVisible) {
      throw new Error("first table summary is not visible");
    }
    if (observed.rowCount !== 0) {
      throw new Error(`first table row count changed from zero: ${observed.rowCount}`);
    }
    const observedAt = now();
    stableStarted ??= observedAt;
    if (observedAt - stableStarted >= stableForMs) {
      return {
        status: "passed",
        tableId: expectedTableId,
        sameTableIdentity: true,
        tableSummaryVisible: true,
        errorOverlayVisible: false,
        rowCount: 0,
        stableWindowMs: stableForMs,
      };
    }
    await sleep(pollMs);
  }
}


export async function locateProductPage({
  browser,
  timeoutMs = 60_000,
  now = () => performance.now(),
  sleep = (duration) => new Promise((resolve) => setTimeout(resolve, duration)),
}) {
  const started = now();
  while (now() - started < timeoutMs) {
    for (const context of browser.contexts()) {
      for (const page of context.pages()) {
        if (page.url().startsWith("https://app.vibetable.local/")) return page;
      }
    }
    await sleep(100);
  }
  throw new Error("WebView2 CDP target never navigated to app.vibetable.local");
}


async function createFirstTable(page) {
  const displayName = "Runtime baseline";
  await page.getByTestId("nav-tables").click();
  await page.getByTestId("sidebar-new-table").click();
  const nameInput = page.getByTestId("create-table-name-input");
  await nameInput.locator("input").fill(displayName);
  await page.getByTestId("create-table-submit").click();
  await nameInput.waitFor({ state: "hidden", timeout: 30_000 });
  const row = page.locator(".table-row").filter({
    has: page.getByTestId("sidebar-table-name").filter({ hasText: displayName }),
  }).last();
  await row.waitFor({ state: "visible", timeout: 30_000 });
  const tableId = (await row.locator("small").innerText()).trim();
  if (!tableId.startsWith("tbl_")) {
    throw new Error(`host did not expose an opaque first table identity: ${tableId}`);
  }
  await page.getByTestId("field-display-name").waitFor({ timeout: 30_000 });
  const confirmation = page.waitForEvent("dialog", { timeout: 2_000 })
    .then(async (dialog) => {
      await dialog.accept();
      return dialog.message();
    })
    .catch(() => null);
  await page.getByTestId("field-close-button").click();
  await confirmation;
  await page.getByTestId("field-display-name").waitFor({ state: "hidden" });
  await page.getByTestId("table-summary").waitFor({ state: "visible", timeout: 30_000 });
  return tableId;
}


async function runProbe(cdpUrl) {
  const browser = await chromium.connectOverCDP(cdpUrl);
  const page = await locateProductPage({ browser });
  const tableId = await createFirstTable(page);
  return requireStableFirstTable({
    expectedTableId: tableId,
    sample: async () => {
      const activeTableId = (
        await page.locator(".table-row.table-item--active small").innerText()
      ).trim();
      return {
        tableId,
        activeTableId,
        tableSummaryVisible: await page.getByTestId("table-summary").isVisible(),
        errorOverlayVisible: await page.getByTestId("table-error-overlay").isVisible(),
        rowCount: await page.locator(".tabulator-row:visible").count(),
      };
    },
  });
}


export async function runCli(
  argv,
  {
    probe = runProbe,
    writeReport = (path, content) => fs.writeFile(path, content, "utf8"),
    writeStatus = (status) => fsSync.writeSync(1, `${JSON.stringify({ status })}\n`),
  } = {},
) {
  let report;
  let reportPath = requestedReportPath(argv);
  try {
    const args = parseArgs(argv);
    reportPath = args["json-report"];
    report = {
      contractVersion: "1.0",
      evidenceKind: "packaged-runtime-ui-probe",
      ...(await probe(args["cdp-url"])),
      errors: [],
    };
  } catch (error) {
    report = {
      contractVersion: "1.0",
      evidenceKind: "packaged-runtime-ui-probe",
      status: "failed",
      tableId: null,
      sameTableIdentity: false,
      tableSummaryVisible: false,
      errorOverlayVisible: null,
      rowCount: null,
      stableWindowMs: 1_000,
      errors: [
        {
          code: "FIRST_TABLE_PROBE_FAILED",
          message: error instanceof Error ? error.message : String(error),
        },
      ],
    };
  }
  if (reportPath) {
    await writeReport(reportPath, `${JSON.stringify(report, null, 2)}\n`);
  }
  writeStatus(report.status);
  return report.status === "passed" ? 0 : 1;
}


if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exit(await runCli(process.argv.slice(2)));
}
