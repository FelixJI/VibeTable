import fs from "node:fs/promises";
import path from "node:path";
import { nativePluginInput, openApplication } from "./plugin_applications.mjs";
import { picker } from "./plugin_capability_services.mjs";
import { requestTransactionNativeInput, runPluginTransactions } from "./plugin_transactions.mjs";

export function exactSyntheticCents(rows, field) {
  return rows.reduce((sum, row) => {
    const value = row[field];
    if (typeof value !== "string" || !/^-?\d+\.\d{2}$/.test(value)) throw new Error("synthetic money is not fixed-scale decimal text");
    return sum + BigInt(value.replace(".", ""));
  }, 0n).toString();
}

/** Real SDK model -> protected transaction -> shared dataset -> native atomic CSV. */
export async function runPluginDatasets(page, recorder, runtime, helpers) {
  await runPluginTransactions(page, recorder, runtime, helpers);
  const prior = JSON.parse(await fs.readFile(path.join(runtime.evidenceDir, "56-transactions.json"), "utf8"));
  let frame = page.frameLocator(".surface-shell iframe");
  async function finish(expected = "succeeded") {
    await frame.locator("#status").filter({ hasText: /^(succeeded|failed)$/ }).waitFor({ timeout: 120000 });
    const value = JSON.parse(await frame.locator("#result").innerText());
    if (await frame.locator("#status").innerText() !== expected) throw new Error(`dataset action failed: ${JSON.stringify(value)}`);
    return value;
  }
  async function action(id, expected = "succeeded") { await frame.locator(`#${id}`).click(); return finish(expected); }
  const seed = await action("dataset-seed");
  recorder.check("multi-page fixture is prepared through public protected transactions", seed.status === "prepared" && seed.effects.length === 400, { requestId: seed.requestId });
  const approval = await requestTransactionNativeInput(page, frame.locator("#submit"),
    () => nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved"));
  recorder.check("multi-page transaction uses the owned native confirmation", approval.status === "completed", { approval });
  const applied = await finish();
  recorder.check("all 200 lines and 200 allocations committed in the same protected command", applied.status === "applied" && applied.receipt.rows.length === 400, { receipt: applied.receipt });
  const opened = await action("dataset");
  const lines = prior.model.bindings.lines, allocations = prior.model.bindings.allocations;
  const lineSummary = opened.summaries.find(item => item.collection === lines.tableId);
  const allocationSummary = opened.summaries.find(item => item.collection === allocations.tableId);
  recorder.check("same three-table dataset has >one page in both detail sources", opened.sources.length === 3
    && opened.sources.find(item => item.collection === lines.tableId).filteredRows === 201
    && opened.sources.find(item => item.collection === allocations.tableId).filteredRows === 201, { opened });
  recorder.check("line and allocation exact totals both equal authoritative 10.00", lineSummary.summary.sums[0].value === "10.00"
    && allocationSummary.summary.sums[0].value === "10.00" && lineSummary.summary.complete && allocationSummary.summary.complete, { lineSummary, allocationSummary });
  const detail = await action("dataset-pages");
  recorder.check("both pages match the displayed dataset and sum exactly to 1000 cents", detail.datasetId === opened.datasetId
    && JSON.stringify(detail.versions) === JSON.stringify(opened.versions) && detail.pages === 2 && detail.rows.length === 201
    && new Set(detail.rows.map(row => row.id)).size === 201 && exactSyntheticCents(detail.rows, lines.fields.amount) === "1000", { pages: detail.pages });
  async function save(decision) {
    const clicking = frame.locator("#dataset-export").click();
    const native = await picker(runtime, "dataset", decision); await clicking;
    recorder.check(`dataset ${decision} uses owned native file selection`, native.status === "completed" && native.input === "SendInput", { native });
    return finish();
  }
  await fs.writeFile(path.join(runtime.controlsDir, "attachment-target.txt"), "native-dialog\n");
  const cancelled = await save("cancel");
  recorder.check("cancelled dataset save writes no file", cancelled === null
    && await fs.stat(path.join(runtime.controlsDir, "57-dataset.csv")).then(() => false, error => { if (error.code === "ENOENT") return true; throw error; }));
  const saved = await save("select");
  recorder.check("CSV save acknowledges the same displayed dataset and versions", saved.committed && saved.datasetId === opened.datasetId
    && saved.rowsWritten === detail.rows.length && JSON.stringify(saved.versions) === JSON.stringify(opened.versions), { saved });
  await fs.writeFile(path.join(runtime.controlsDir, "57-dataset-proof.json"), JSON.stringify({ fields: ["id", lines.fields.amount, lines.fields.head], amountField: lines.fields.amount, rows: detail.rows, datasetId: opened.datasetId, versions: opened.versions, saved }, null, 2));
  const header = prior.model.bindings.heads;
  const schema = await helpers.rawBridgeRequest(page, "schema.getTable", { tableId: header.tableId });
  const code = schema.payload.fields.find(field => field.identity.fieldId === header.fields.code).identity.physicalName;
  await helpers.applyProductMutation(page, header.tableId, [{ kind: "update", recordId: prior.applied.newIds.heads.h, values: { [code]: "SYNTHETIC-CHANGED" } }], "dataset-concurrent-update");
  const stale = await action("dataset-pages", "failed");
  recorder.check("a concurrent write to another source invalidates all remaining dataset pages", stale.code === "plugin_dataset_stale", { stale });
  const staleExport = await action("dataset-export", "failed");
  recorder.check("stale export never silently reopens or displays a newer snapshot", staleExport.code === "plugin_dataset_stale", { staleExport });
  const refreshed = await action("dataset");
  recorder.check("explicit refresh gets a new dataset and source revision", refreshed.datasetId !== opened.datasetId
    && refreshed.versions[header.tableId].dataRevision > opened.versions[header.tableId].dataRevision, { refreshed });
  await action("dataset-release");
  for (let i = 0; i < 3; i++) {
    await page.getByTestId("plugin-app-return").click();
    frame = await openApplication(page, "com.vibetable.examples.transaction-conformance", "open-transactions");
    await action("dataset");
  }
  recorder.check("repeated remounts reclaim datasets and bounded subscriptions", await frame.locator("#dataset-state").innerText() === "current");
  await fs.writeFile(path.join(runtime.evidenceDir, "57-datasets.json"), JSON.stringify({ model: prior.model, transaction: applied, opened, detail, saved, stale, staleExport, refreshed }, null, 2));
  await page.screenshot({ path: path.join(runtime.evidenceDir, "57-datasets.png"), fullPage: true });
}
