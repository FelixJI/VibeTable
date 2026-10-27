import fs from "node:fs/promises";
import path from "node:path";
import { nativePluginInput } from "./plugin_applications.mjs";
import { picker } from "./plugin_capability_services.mjs";

/** Executes only against the real packaged WPF/WebView2 page supplied by the product runner. */
export async function runPluginSurfaceOutputs(page, recorder, runtime, helpers) {
  const { waitForShell, createSimpleTable, createV2Field, applyProductMutation, rawBridgeRequest } = helpers;
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  const table = await createSimpleTable(page, "Synthetic Output Conformance", "Unchanged business value");
  const textField = table.field.physicalName;
  await createV2Field(page, table.tableId, "Issued output", "file", draft => ({ ...draft,
    file: { ...draft.file, maxFiles: 4, maxBytesPerFile: 33554432, allowedMimeTypes: [], thumbs: [] },
  }));
  await applyProductMutation(page, table.tableId, [{ kind: "insert", recordId: null, values: { [textField]: "SYNTHETIC-BUSINESS-UNCHANGED" } }], "output-business-seed");
  const business = async () => {
    const response = await rawBridgeRequest(page, "query.page", { tableId: table.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } });
    return response.payload.rows.map(row => ({ id: row.id, value: row[textField] }));
  };
  const before = await business();
  await fs.writeFile(path.join(runtime.controlsDir, "plugin-source.txt"), path.join(runtime.controlsDir, "output-conformance.vtplugin") + "\n");
  await page.getByTestId("nav-plugins").click();
  await page.getByTestId("plugin-install-package").click();
  await page.getByTestId("plugin-install-plan").waitFor({ timeout: 30000 });
  await page.getByTestId("plugin-install-commit").click();
  const plugin = page.locator("button.plugin-row").filter({ hasText: "com.vibetable.examples.output-conformance" });
  await plugin.waitFor({ timeout: 60000 }); await plugin.click();
  await page.waitForFunction(() => document.querySelector('[data-testid="plugin-toggle"]')?.classList.contains("enabled"));
  await page.locator(".action-row").filter({ hasText: "open-output" }).locator("button.run-button").click();
  const frame = page.frameLocator(".surface-shell iframe");
  await frame.locator("#small").waitFor({ timeout: 30000 });
  await fs.writeFile(path.join(runtime.controlsDir, "attachment-target.txt"), "native-dialog\n");
  async function finishAction(id, expected = "succeeded") {
    const deadline = Date.now() + 120000;
    while (Date.now() < deadline) {
      const status = await frame.locator("#status").innerText();
      if (status === expected) return JSON.parse(await frame.locator("#result").innerText());
      if (status === "failed" && expected !== "failed") throw new Error(await frame.locator("#result").innerText());
      await page.waitForTimeout(50);
    }
    throw new Error(`output ${id} did not settle`);
  }
  async function action(id, expected = "succeeded") {
    await frame.locator(`#${id}`).click();
    return finishAction(id, expected);
  }
  async function save(name, kind) {
    const clicking = frame.locator("#save").click();
    const native = await picker(runtime, kind);
    await clicking;
    recorder.check(`native ${name} uses the owned real save dialog`, native.status === "completed" && native.input === "SendInput", { native });
    const receipt = await finishAction("save");
    recorder.check(`native ${name} commit is positively acknowledged`, receipt.state === "succeeded" && receipt.committed === true, { receipt });
    return receipt;
  }
  const small = await action("small");
  const cancelling = frame.locator("#save").click();
  const cancellation = await picker(runtime, "output-small", "cancel");
  await cancelling;
  const cancelled = await finishAction("save");
  recorder.check("real native save cancellation is definite and writes no file", cancellation.status === "completed"
    && cancelled.state === "cancelled" && cancelled.committed === false
    && await fs.stat(path.join(runtime.controlsDir, "small.xlsx")).then(() => false, error => { if (error.code === "ENOENT") return true; throw error; }), { cancellation, cancelled });
  await save("small.xlsx", "output-small");
  const large = await action("large");
  recorder.check("real frame generates >1 MiB XLSX within independent 32 MiB budget", small.bytes < 1048576 && large.bytes > 1048576 && large.bytes <= 33554432, { small, large });
  await save("large.xlsx", "output-large");
  await frame.locator("#archive").click();
  await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
  const approval = await nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved");
  recorder.check("archive requires exact current owned native Host confirmation", approval.status === "completed", { approval });
  await fs.copyFile(path.join(runtime.evidenceDir, "48-native-approved.png"), path.join(runtime.evidenceDir, "55-archive-confirmation.png"));
  const deadline = Date.now() + 30000;
  while (await frame.locator("#status").innerText() === "running" && Date.now() < deadline) await page.waitForTimeout(50);
  const archive = JSON.parse(await frame.locator("#result").innerText());
  recorder.check("canonical workspace attachment archive commits once", archive.status === "succeeded" && archive.artifactId === large.outputId, { archive });
  await page.getByRole("button", { name: "关闭插件页面", exact: true }).click();
  await page.locator(".action-row").filter({ hasText: "open-output" }).locator("button.run-button").click();
  await frame.locator("#artifact").waitFor({ timeout: 30000 });
  await frame.locator("#artifact").fill(archive.artifactId);
  const reopened = await action("reopen");
  recorder.check("opaque artifact reopens immutable declared source", reopened.source.templateVersion === "template-v1" && reopened.artifactId === archive.artifactId, { reopened });
  await save("reprint.xlsx", "output-reprint");
  const next = await action("next"); await save("new-template.xlsx", "output-next");
  recorder.check("new template produces a fresh output instance", next.outputId !== large.outputId && next.source.templateVersion === "template-v2", { next });
  const oversize = await action("oversize", "failed");
  recorder.check("over-budget output is explicitly rejected", oversize.code === "plugin_file_limit", { oversize });
  const after = await business();
  const businessUnchanged = JSON.stringify(before) === JSON.stringify(after);
  recorder.check("saving and reprinting never replay business posting", businessUnchanged, { before, after });
  await fs.writeFile(path.join(runtime.controlsDir, "output-proof.json"), JSON.stringify({ small, large, archive, reopened, next, oversizeCode: oversize.code, businessUnchanged }, null, 2));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "55-output-conformance.png"), fullPage: true });
}
