import process from "node:process";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    values[argv[index]?.replace(/^--/, "")] = argv[index + 1];
  }
  for (const required of ["cdp-url", "workspace-id", "display-name"]) {
    if (!values[required]) throw new Error(`missing --${required}`);
  }
  return values;
}

async function productPage(browser) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    for (const context of browser.contexts()) {
      for (const page of context.pages()) {
        if (page.url().startsWith("https://app.vibetable.local/")) return page;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("legacy packaged host product page was not exposed over CDP");
}

const values = args(process.argv.slice(2));
const browser = await chromium.connectOverCDP(values["cdp-url"]);
try {
  const page = await productPage(browser);
  const switcher = page.getByTestId("workspace-switcher");
  await switcher.locator(".switcher-trigger").click();
  const options = await page.locator(".n-dropdown-option").allTextContents();
  try {
    if (!options.some((option) => option.includes(values["display-name"]))) {
      throw new Error("legacy Host bootstrap did not advertise the workspace");
    }
    const response = await page.evaluate(({ workspaceId }) => new Promise((resolve, reject) => {
      const operationId = crypto.randomUUID();
      const requestId = `legacy-host-${operationId}`;
      const wire = { scope: "global", operationId, sequence: Date.now() };
      const timeout = setTimeout(() => {
        window.chrome.webview.removeEventListener("message", handler);
        reject(new Error("legacy workspace.open bridge request timed out"));
      }, 60_000);
      function handler(event) {
        let message = event.data;
        if (typeof message === "string") {
          try { message = JSON.parse(message); } catch { return; }
        }
        if (message?.requestId !== requestId) return;
        clearTimeout(timeout);
        window.chrome.webview.removeEventListener("message", handler);
        resolve(message);
      }
      window.chrome.webview.addEventListener("message", handler);
      window.chrome.webview.postMessage({
        type: "workspace.v2.request",
        requestId,
        wire,
        payload: {
          method: "workspace.open",
          params: { workspaceId, openMode: "writable" },
          wire,
        },
      });
    }), { workspaceId: values["workspace-id"] });
    if (response?.payload?.ok !== true) {
      throw new Error(`legacy workspace.open failed: ${JSON.stringify(response)}`);
    }
    process.stdout.write(`${JSON.stringify({ status: "passed", session: response.payload.result })}\n`);
  } catch (error) {
    const diagnostic = await page.evaluate(() => ({
      session: window.__vibetableE2EBridgeDiagnostics?.workspaceSession ?? null,
      text: document.body.innerText.slice(0, 4000),
    }));
    process.stderr.write(`${JSON.stringify({ options, diagnostic })}\n`);
    throw error;
  }
} finally {
  await browser.close();
}
