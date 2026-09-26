import fs from "node:fs/promises";
import path from "node:path";

async function open(page) {
  await page.getByTestId("nav-help").click();
  await page.getByTestId("commands-panel").waitFor({ state: "visible" });
  await page.waitForFunction(() => !document.querySelector('[data-testid="shortcut-new"]')?.disabled);
}
async function close(page) {
  await page.getByTestId("shortcuts-dialog").locator(".n-card-header__close").click();
  await page.getByTestId("commands-panel").waitFor({ state: "hidden" });
}
async function saved(page, label) {
  await page.getByTestId("shortcut-save").click();
  const row = page.getByTestId("shortcut-row").filter({ hasText: label });
  await row.waitFor({ state: "visible" });
  await page.waitForFunction(() => !document.querySelector('[data-testid="shortcut-new"]')?.disabled);
  return row.getAttribute("data-shortcut-id");
}
async function exported(page, recorder, runtime, control) {
  const output = path.join(runtime.controlsDir, `command-${control}.csv`);
  await fs.writeFile(path.join(runtime.controlsDir, "export-target.txt"), output, "utf8");
  await page.evaluate(() => {
    window.__hostCommandEvents = [];
    window.__hostCommandListener = event => {
      const message = typeof event.data === "string" ? JSON.parse(event.data) : event.data;
      if (message?.type === "task.changed") {
        window.__hostCommandEvents.push(message.payload);
        if (window.__hostCommandEvents.length > 10) window.__hostCommandEvents.shift();
      }
    };
    window.chrome.webview.addEventListener("message", window.__hostCommandListener);
  });
  try {
    await page.getByTestId(control).click();
    await page.waitForFunction(() => {
      const node = document.querySelector('[data-testid="commands-notice"]');
      return node?.textContent?.includes(".csv") || !!document.querySelector('[data-testid="commands-error"]');
    }, undefined, { timeout: 60_000 });
    const diagnostic = await page.evaluate(() => ({
      error: document.querySelector('[data-testid="commands-error"]')?.textContent,
      tasks: window.__hostCommandEvents,
    }));
    if (diagnostic.error) throw new Error(`Host command export failed: ${JSON.stringify(diagnostic)}`);
  } finally {
    await page.evaluate(() => {
      window.chrome.webview.removeEventListener("message", window.__hostCommandListener);
      delete window.__hostCommandEvents;
      delete window.__hostCommandListener;
    });
  }
  const content = await fs.readFile(output, "utf8");
  recorder.check(`${control} produces the actual filtered export through a fresh Host grant`,
    content.includes("preserved") && content.split(/\r?\n/).filter(Boolean).length === 2,
    { file: path.basename(output), content });
}
export async function seedHostCommands(page, recorder, runtime) {
  await open(page);
  await exported(page, recorder, runtime, "command-run");
  await page.getByTestId("shortcut-label").locator("input").fill("Pinned export");
  const exportId = await saved(page, "Pinned export");
  await page.getByTestId("shortcut-label").locator("input").fill("Pinned query");
  const editedId = await saved(page, "Pinned query");
  recorder.check("editing the shortcut preserves its stable identity", exportId === editedId && !!exportId);
  await page.getByTestId("shortcut-new").click();
  await page.getByTestId("shortcut-target").click();
  await page.locator(".n-base-select-option:visible").filter({ hasText: /HTTPS/ }).click();
  await page.getByTestId("shortcut-label").locator("input").fill("Example help");
  await page.getByTestId("shortcut-url").locator("input").fill("https://example.com/help");
  const urlId = await saved(page, "Example help");
  await fs.writeFile(path.join(runtime.controlsDir, "command-https-cancel.txt"), "cancel", "utf8");
  await page.getByTestId("shortcut-launch").click();
  await page.waitForFunction(() => document.querySelector('[data-testid="commands-notice"]')?.textContent?.includes("取消"));
  recorder.check("the real Host HTTPS confirmation cancel is reported without a launch success",
    (await page.getByTestId("commands-notice").innerText()).includes("取消"));
  await page.getByTestId("shortcuts-dialog").evaluate(node => node.scrollIntoView({ block: "start" }));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "host-commands-created.png"), fullPage: true });
  await close(page);
  return { exportId, urlId };
}
export async function verifyHostCommandsReopen(page, recorder, state) {
  await open(page);
  recorder.check("real workspace reopen retains both device shortcut definitions",
    await page.getByTestId("shortcut-row").count() === 2
      && await page.locator(`[data-shortcut-id="${state.exportId}"]`).count() === 1
      && await page.locator(`[data-shortcut-id="${state.urlId}"]`).count() === 1);
  await close(page);
}
export async function resumeHostCommands(page, recorder, runtime, state) {
  if (!state?.exportId || !state?.urlId) throw new Error("Missing seed shortcut identities");
  await open(page);
  const exportRow = page.locator(`[data-shortcut-id="${state.exportId}"]`);
  recorder.check("second real Host restores the edited shortcut and HTTPS definition",
    (await exportRow.innerText()).includes("Pinned query")
      && (await page.locator(`[data-shortcut-id="${state.urlId}"]`).locator("small").innerText()) === "https://example.com/help");
  await exportRow.click();
  await exported(page, recorder, runtime, "shortcut-launch");
  await page.getByTestId("shortcuts-dialog").evaluate(node => node.scrollIntoView({ block: "start" }));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "host-commands-reopened.png"), fullPage: true });
  for (const id of [state.exportId, state.urlId]) {
    await page.locator(`[data-shortcut-id="${id}"]`).click();
    await page.getByTestId("shortcut-delete").click();
    await page.locator(".n-popconfirm:visible").getByRole("button", { name: /确认|确定|Confirm|OK/i }).press("Enter");
    await page.locator(`[data-shortcut-id="${id}"]`).waitFor({ state: "detached" });
  }
  await close(page); await open(page);
  recorder.check("UI deletion persists after the command panel reopens", await page.getByTestId("shortcut-row").count() === 0);
  await close(page);
}
