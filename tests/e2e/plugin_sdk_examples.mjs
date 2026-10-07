import fs from "node:fs/promises";
import path from "node:path";

// Real CLI-built packages, installed and executed by the packaged Host against
// this scenario's synthetic workspace. No offline mutation adapter is involved.
export async function exerciseSdkExamples(page, recorder, runtime, projectKey, helpers) {
  const { createSimpleTable, applyProductMutation, rawBridgeRequest,
    beginBridgeMessageCapture, waitForCapturedBridgeMessage } = helpers;
  const sourceControl = path.join(runtime.controlsDir, "plugin-source.txt");
  const originalSource = await fs.readFile(sourceControl, "utf8");
  await page.getByTestId("nav-tables").click();
  const table = await createSimpleTable(page, "E2E SDK Examples", "Text");
  const field = table.field.physicalName;
  const initial = "  Vibe   Table  ";
  const seed = await applyProductMutation(page, table.tableId, [initial, " keep "].map(value => ({
    kind: "insert", recordId: null, values: { [field]: value },
  })), "sdk-example-seed");
  const [selectedId, otherId] = seed.payload.affectedRows.map(row => row.recordId);
  const readRows = async () => (await rawBridgeRequest(page, "query.page", {
    tableId: table.tableId, query: { filters: [], sorts: [], offset: 0, limit: 100 },
  })).payload.rows;
  const valueOf = (rows, id) => rows.find(row => row.id === id)?.[field];
  const context = { contract: "vibetable.command-context.v1", projectKey, collection: table.tableId,
    selectedKeys: [selectedId], querySnapshot: null, locale: "zh-CN", theme: "light",
    density: "comfortable", user: {}, hostVersion: "unknown" };
  async function install(example) {
    await fs.writeFile(sourceControl, `${path.join(runtime.controlsDir, `${example}.vtplugin`)}\n`);
    await page.getByTestId("nav-plugins").click();
    await page.getByTestId("plugin-install-package").click();
    await page.getByTestId("plugin-install-plan").waitFor({ timeout: 30_000 });
    await page.getByTestId("plugin-install-commit").click();
    const row = page.locator("button.plugin-row").filter({ hasText: `com.vibetable.examples.${example}` });
    await row.waitFor({ timeout: 60_000 });
    await row.click();
    await page.waitForFunction(() => document.querySelector('[data-testid="plugin-toggle"]')?.classList.contains("enabled"));
    return row;
  }
  async function uninstall(row) {
    await row.click();
    await page.getByTestId("plugin-uninstall").click();
    await page.getByTestId("plugin-uninstall-confirm").click();
    await row.waitFor({ state: "hidden", timeout: 30_000 });
  }
  async function start(example, actionId, input) {
    const response = await rawBridgeRequest(page, "plugin.action.start", {
      projectKey, pluginId: `com.vibetable.examples.${example}`, actionId, input, context,
    });
    recorder.check(`SDK ${example} receives a real Host task identity`,
      response.type === "plugin.action.start" && Boolean(response.payload?.taskId), { response });
    return response.payload;
  }
  async function terminal(taskId) {
    const deadline = Date.now() + 30_000;
    do {
      const response = await rawBridgeRequest(page, "plugin.task.get", { taskId });
      if (["succeeded", "failed", "cancelled", "aborted"].includes(response.payload?.state)) return response.payload;
      await page.waitForTimeout(100);
    } while (Date.now() < deadline);
    throw new Error(`SDK example Host task did not settle: ${taskId}`);
  }
  const overview = await install("data-overview");
  const read = await terminal((await start("data-overview", "open-overview", {})).taskId);
  recorder.check("real data-overview package returns the synthetic table count",
    read.state === "succeeded" && read.result?.table?.data?.count === 2, { read });
  await uninstall(overview);
  const normalize = await install("normalize-text");
  for (const decision of ["rejected", "cancelled", "conflict", "approved"]) {
    await beginBridgeMessageCapture(page, ["plugin.interaction.requested"]);
    const task = await start("normalize-text", "normalize-selection", { field, strategy: "collapse-whitespace" });
    const pending = (await waitForCapturedBridgeMessage(page, 30_000)).payload.snapshot;
    const preview = pending.pendingConfirmation.preview;
    recorder.check(`SDK normalization ${decision} has a precise preview before Go submission`,
      pending.runId === task.runId && preview.affectedCount === 1
        && preview.sampleRows[0].before === initial && preview.sampleRows[0].after === "Vibe Table",
      { preview, task });
    if (decision === "cancelled") {
      await rawBridgeRequest(page, "plugin.task.cancel", { taskId: task.taskId });
    } else {
      if (decision === "conflict") await applyProductMutation(page, table.tableId, [{
        kind: "update", recordId: selectedId, values: { [field]: "changed elsewhere" },
      }], "sdk-concurrent-change");
      await rawBridgeRequest(page, "plugin.interaction.resolve", {
        runId: pending.runId, interactionId: pending.pendingConfirmation.interactionId,
        decision: decision === "rejected" ? "rejected" : "approved",
      });
    }
    const settled = await terminal(task.taskId);
    const rows = await readRows();
    const expected = decision === "conflict" ? "changed elsewhere" : decision === "approved" ? "Vibe Table" : initial;
    recorder.check(`SDK normalization ${decision} preserves the expected rows and terminal`,
      valueOf(rows, selectedId) === expected && valueOf(rows, otherId) === " keep "
        && (decision === "approved" ? settled.state === "succeeded" && settled.result?.metrics?.[0]?.value === 1
          : decision === "cancelled" ? settled.state === "cancelled"
            : settled.state === "failed" && settled.error?.code === (decision === "conflict" ? "mutation.digest_conflict" : "plugin_mutation_rejected")),
      { settled, rows });
    if (decision === "conflict") await applyProductMutation(page, table.tableId, [{
      kind: "update", recordId: selectedId, values: { [field]: initial },
    }], "sdk-reset-synthetic-value");
  }
  await uninstall(normalize);
  await fs.writeFile(sourceControl, originalSource, "utf8");
}
