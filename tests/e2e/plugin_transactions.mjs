import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication, nativePluginInput } from "./plugin_applications.mjs";
import { nativeModel } from "./plugin_models.mjs";

const PLUGIN = "com.vibetable.examples.transaction-conformance";

export async function requestTransactionNativeInput(page, button, input) {
  await button.click();
  await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
  return input();
}

export function assertAppliedTransaction(value, preview) {
  if (value?.status !== "applied" || value.receipt?.status !== "applied"
    || value.requestId !== preview.requestId || value.workspaceId !== preview.workspaceId
    || value.installationId !== preview.installationId
    || JSON.stringify(value.newIds) !== JSON.stringify(preview.newIds)
    || !value.receipt.changeSetId || value.receipt.rows.length !== 3)
    throw new Error("transaction receipt does not prove the exact three-table plan");
  for (const [entity, ids] of Object.entries(value.newIds)) {
    const tableId = preview.model.bindings[entity].tableId;
    for (const recordId of Object.values(ids)) {
      if (!value.receipt.rows.some(row => row.tableId === tableId && row.recordId === recordId && row.operation === "insert"))
        throw new Error("transaction receipt lacks a planned created row");
    }
  }
}

/** Runs against the real WPF/WebView2 Host with synthetic, newly constructed data. */
export async function runPluginTransactions(page, recorder, runtime, helpers) {
  await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  await installApplication(page, runtime, "transaction-conformance", PLUGIN, "open-transactions");
  let frame = await openApplication(page, PLUGIN, "open-transactions");
  async function finish(expected = "succeeded") {
    await frame.locator("#status").filter({ hasText: /^(succeeded|failed)$/ }).waitFor({ timeout: 120000 });
    const value = JSON.parse(await frame.locator("#result").innerText());
    if (await frame.locator("#status").innerText() !== expected) throw new Error(`transaction action failed: ${JSON.stringify(value)}`);
    return value;
  }
  async function action(id, expected = "succeeded") {
    await frame.locator(`#${id}`).click();
    return finish(expected);
  }
  async function confirm(id, approve, stage, expected = "succeeded") {
    const native = await requestTransactionNativeInput(page, frame.locator(`#${id}`),
      () => nativePluginInput(runtime, "click", approve ? "plugin-confirm-approve" : "plugin-confirm-reject", approve ? "approved" : "rejected"));
    recorder.check(`${stage} uses owned native input`, native.status === "completed", { native });
    await fs.copyFile(path.join(runtime.evidenceDir, `48-native-${approve ? "approved" : "rejected"}.png`), path.join(runtime.evidenceDir, `56-${stage}.png`));
    return finish(expected);
  }
  await frame.locator("#model").click();
  await nativeModel(runtime, "click", "plugin-model-confirm", "create");
  const model = await finish();
  recorder.check("transaction model has exactly three canonical entities", model.status === "completed" && Object.keys(model.bindings).length === 3, { model });
  const configured = await confirm("configure", true, "rules");
  recorder.check("native rule registration is acknowledged", configured?.status === "configured", { configured });
  const preview = await action("preview");
  const rows = async () => {
    const result = {};
    for (const [entity, binding] of Object.entries(model.bindings)) {
      const reply = await helpers.rawBridgeRequest(page, "query.page", { tableId: binding.tableId, query: { filters: [], sorts: [], offset: 0, limit: 20 } });
      if (!Array.isArray(reply.payload?.rows)) throw new Error(`transaction query failed: ${JSON.stringify(reply)}`);
      result[entity] = reply.payload.rows;
    }
    return result;
  };
  recorder.check("persistent preview writes no business rows", preview.status === "prepared" && Object.values(await rows()).every(value => value.length === 0), { preview });
  const cancelled = await confirm("submit", false, "cancel");
  recorder.check("native rejection leaves all three tables empty", cancelled === null && Object.values(await rows()).every(value => value.length === 0), { cancelled });
  const applied = await confirm("submit", true, "commit");
  assertAppliedTransaction(applied, preview);
  const committed = await rows();
  recorder.check("one native confirmation atomically creates all three rows", Object.values(committed).every(value => value.length === 1), { applied, committed });
  const replay = await action("submit");
  assertAppliedTransaction(replay, preview);
  recorder.check("same business request returns the original receipt without another write", replay.receipt.changeSetId === applied.receipt.changeSetId && JSON.stringify(await rows()) === JSON.stringify(committed), { replay });
  await page.getByTestId("plugin-app-return").click();
  frame = await openApplication(page, PLUGIN, "open-transactions");
  await frame.locator("#request").fill(preview.requestId);
  const recovered = await action("recover");
  assertAppliedTransaction(recovered, preview);
  recorder.check("reopened plugin recovers the original committed request", recovered.receipt.changeSetId === applied.receipt.changeSetId, { recovered });
  const excess = await action("excess");
  recorder.check("second request has its own durable identity", excess.status === "prepared" && excess.requestId !== preview.requestId, { excess });
  const rejected = await confirm("submit", true, "balance-rejected", "failed");
  recorder.check("additional allocation cannot exceed authoritative balance", /balance_violation/.test(rejected.code + rejected.message) && JSON.stringify(await rows()) === JSON.stringify(committed), { rejected });
  await frame.locator("#request").fill("synthetic-never-admitted");
  const unknown = await action("recover");
  recorder.check("missing request stays unknown and cannot write", unknown.status === "unknown" && JSON.stringify(await rows()) === JSON.stringify(committed), { unknown });
  await fs.writeFile(path.join(runtime.evidenceDir, "56-transactions.json"), JSON.stringify({ model, preview, applied, replay, recovered, excess, rejected, unknown, committed }, null, 2));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "56-transactions.png"), fullPage: true });
}
