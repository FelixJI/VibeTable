import fs from "node:fs/promises";
import { parseArgs } from "node:util";
import { pathToFileURL } from "node:url";

import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";
import { locateProductPage } from "./packaged_runtime_probe.mjs";
import {
  beginRawBridgeRequestInPage,
  readRawBridgeRequestTerminalInPage,
  readRawBridgeRequestElapsedInPage,
  releaseRawBridgeRequestInPage,
} from "./bridge_raw_request.mjs";

export async function measureRpcLatency(page, tableId) {
  if (typeof tableId !== "string" || !tableId.startsWith("tbl_")) {
    throw new Error("RPC baseline requires the first table identity");
  }
  const samples = {};
  let schemaRevision;
  for (const method of ["schema.getTable", "query.page"]) {
    samples[method] = [];
    for (let index = 0; index < 30; index += 1) {
      const requestPayload = method === "schema.getTable"
        ? { tableId }
        : { tableId, query: { filters: [], sorts: [], offset: 0, limit: 100 } };
      const requestId = await page.evaluate(beginRawBridgeRequestInPage, {
        requestType: method, requestPayload,
      });
      try {
        const terminal = await page.waitForFunction(
          readRawBridgeRequestTerminalInPage, { requestId }, { timeout: 20_000 },
        );
        let response;
        try {
          response = await terminal.jsonValue();
        } finally {
          await terminal.dispose();
        }
        if (response?.type !== method) throw new Error(`${method} did not succeed`);
        const payload = response.payload;
        if (method === "schema.getTable") {
          if (payload?.tableId !== tableId || !Array.isArray(payload.fields)
            || typeof payload.schemaRevision !== "string" || !payload.schemaRevision) {
            throw new Error("schema.getTable returned an invalid table schema");
          }
          schemaRevision ??= payload.schemaRevision;
          if (payload.schemaRevision !== schemaRevision) {
            throw new Error("RPC baseline schema changed during sampling");
          }
        } else if (!Array.isArray(payload?.rows) || payload.rows.length !== 0
          || payload.snapshot?.table !== tableId
          || payload.snapshot?.schemaRevision !== schemaRevision) {
          throw new Error("query.page did not return the same empty table snapshot");
        }
        const elapsed = await page.evaluate(readRawBridgeRequestElapsedInPage, { requestId });
        if (!Number.isFinite(elapsed) || elapsed < 0) {
          throw new Error(`${method} did not expose a valid page-local duration`);
        }
        samples[method].push(elapsed);
      } finally {
        await page.evaluate(releaseRawBridgeRequestInPage, { requestId });
      }
    }
  }
  return { status: "passed", tableId, samples };
}

async function main() {
  const { values } = parseArgs({ options: {
    "cdp-url": { type: "string" },
    "table-id": { type: "string" },
    "json-report": { type: "string" },
  } });
  if (!values["cdp-url"] || !values["table-id"] || !values["json-report"]) {
    throw new Error("RPC probe requires cdp-url, table-id and json-report");
  }
  const browser = await chromium.connectOverCDP(values["cdp-url"]);
  const page = await locateProductPage({ browser });
  const result = await measureRpcLatency(page, values["table-id"]);
  await fs.writeFile(values["json-report"], `${JSON.stringify(result)}\n`, "utf8");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await main();
    process.exit(0);
  } catch {
    // The parent records failure; no raw bridge payloads or local paths escape.
    process.exit(1);
  }
}
