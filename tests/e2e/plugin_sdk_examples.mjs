import fs from "node:fs/promises";
import path from "node:path";
import { nativePluginInput } from "./plugin_applications.mjs";

// Real CLI-built examples execute in isolated surfaces; their data still comes from Go.
export async function exerciseSdkExamples(page, recorder, runtime, projectKey, helpers) {
  const { createSimpleTable, createV2Field, applyProductMutation, rawBridgeRequest, selectTable,
    beginBridgeMessageCapture, waitForCapturedBridgeMessage } = helpers;
  const sourceControl = path.join(runtime.controlsDir, "plugin-source.txt");
  const originalSource = await fs.readFile(sourceControl, "utf8");
  try {
    await page.getByTestId("nav-tables").click();
    const table = await createSimpleTable(page, "E2E SDK Examples", "Text");
    const field = table.field.physicalName;
    const second = await createV2Field(page, table.tableId, "More text", "text");
    const secondField = second.physicalName;
    const initial = "  Vibe   Table  ";
    const secondInitial = "  Same   Row  ";
    const otherSecond = " keep   second ";
    const seed = await applyProductMutation(page, table.tableId, [initial, " keep "].map((value, index) => ({
      kind: "insert", recordId: null, values: { [field]: value, [secondField]: index === 0 ? secondInitial : otherSecond },
    })), "sdk-example-seed");
    const [selectedId, otherId] = seed.payload.affectedRows.map(row => row.recordId);
    const readRows = async () => (await rawBridgeRequest(page, "query.page", {
      tableId: table.tableId, query: { filters: [{ field: "id", operator: "in", value: [selectedId, otherId] }], sorts: [], offset: 0, limit: 100 },
    })).payload.rows;
    const valueOf = (rows, id, fieldName = field) => rows.find(row => row.id === id)?.[fieldName];
    async function install(example) {
      await fs.writeFile(sourceControl, `${path.join(runtime.controlsDir, `${example}.vtplugin`)}\n`);
      await page.getByTestId("nav-plugins").click();
      await page.getByTestId("plugin-install-package").click();
      await page.getByTestId("plugin-install-plan").waitFor({ timeout: 30000 });
      await beginBridgeMessageCapture(page, ["plugin.install.commit", "operation.failed"]);
      await page.getByTestId("plugin-install-commit").click();
      const committed = await waitForCapturedBridgeMessage(page, 30000);
      recorder.check(`native SDK ${example} package commits before catalog enable`, committed.type === "plugin.install.commit", { committed });
      const row = page.locator("button.plugin-row").filter({ hasText: `com.vibetable.examples.${example}` });
      await row.waitFor({ timeout: 60000 }); await row.click();
      await page.waitForFunction(() => document.querySelector('[data-testid="plugin-toggle"]')?.classList.contains("enabled"));
    }
    await applyProductMutation(page, table.tableId, Array.from({ length: 548 }, (_, index) => ({
      kind: "insert", recordId: null, values: { [field]: `row-${String(index).padStart(3, "0")}` },
    })), "sdk-example-550-seed");
    await install("data-overview");
    await page.locator(".action-row").filter({ hasText: "open-overview" }).locator("button.run-button").click();
    const overview = page.frameLocator(".surface-shell iframe");
    await overview.locator("#status").filter({ hasText: "已统计" }).waitFor({ timeout: 30000 });
    const metrics = () => overview.locator("#summary dl").evaluate(list => Object.fromEntries([...list.querySelectorAll("dt")].map(term => [term.textContent, term.nextElementSibling?.textContent])));
    const complete = await metrics();
    recorder.check("real overview surface reads all 550 rows through three pages", complete["筛选后记录"] === "550" && complete["读取页数"] === "3" && complete["完整计算"] === "是", { complete });
    await overview.locator("#field").selectOption(table.field.fieldId);
    await overview.locator("#contains").fill("row-"); await overview.locator("#refresh").click();
    await overview.locator("#summary dd").filter({ hasText: /^548$/u }).waitFor({ timeout: 30000 });
    const filtered = await metrics();
    recorder.check("real overview applies Go filters and preserves authority total", filtered["筛选后记录"] === "548" && filtered["权威总行数"] === "550", { filtered });
    await overview.locator("#field").evaluate(field => {
      const invalid = document.createElement("option"); invalid.value = "fld_unauthorized"; invalid.textContent = "unauthorized";
      field.append(invalid); field.value = invalid.value; field.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await overview.locator("#status").filter({ hasText: "字段不可读取" }).waitFor({ timeout: 30000 });
    recorder.check("actual overview refuses a field outside authorized metadata", (await overview.locator("#status").innerText()).includes("字段不可读取"));
    await page.getByRole("button", { name: "关闭插件页面", exact: true }).click();
    await install("normalize-text");
    for (const decision of ["rejected", "cancelled", "conflict", "approved"]) {
      await page.getByTestId("nav-tables").click(); await selectTable(page, "E2E SDK Examples");
      await page.getByTestId("view-keyword").locator("input").fill("Vibe");
      const cell = page.locator(`.tabulator-cell[tabulator-field="${field}"]`).filter({ hasText: "Vibe" }).first();
      await cell.waitFor({ timeout: 30000 });
      await beginBridgeMessageCapture(page, ["plugin.interaction.requested"]);
      // The row context menu binds exactly this real row; Plugin Center has no selected-row context.
      await cell.click({ button: "right" });
      await page.locator(".n-dropdown-option").filter({ hasText: "规范化选中文本" }).click();
      const runner = page.getByTestId("plugin-headless-runner");
      await runner.locator(".host-confirmation").waitFor({ timeout: 30000 });
      const pending = (await waitForCapturedBridgeMessage(page, 30000)).payload.snapshot;
      const taskId = await runner.locator(".task-status code").innerText();
      const before = await rawBridgeRequest(page, "plugin.task.get", { taskId });
      const preview = pending.pendingConfirmation.preview;
      const changes = preview.sampleRows[0]?.values;
      recorder.check(`native normalization ${decision} merges two text fields into one guarded selected-row update`, before.payload?.runId === pending.runId
        && before.payload.collection === table.tableId && preview.affectedCount === 1 && preview.sampleRows.length === 1
        && preview.sampleRows[0].primaryKey === selectedId && changes?.[table.field.fieldId] === "Vibe Table"
        && changes?.[second.fieldId] === "Same Row"
        && Object.keys(changes).sort().join() === [table.field.fieldId, second.fieldId].sort().join(), { pending, before });
      if (decision === "cancelled") await page.getByTestId("plugin-headless-cancel").click();
      else {
        if (decision === "conflict") await applyProductMutation(page, table.tableId, [{ kind: "update", recordId: selectedId, values: { [field]: "changed elsewhere" } }], "sdk-concurrent-change");
        const native = await nativePluginInput(runtime, "click", `plugin-confirm-${decision === "rejected" ? "reject" : "approve"}`, decision === "rejected" ? "rejected" : "approved");
        recorder.check(`native normalization ${decision} has owned Host input`, native.status === "completed", { native });
      }
      const until = Date.now() + 30000; let settled;
      do {
        settled = (await rawBridgeRequest(page, "plugin.task.get", { taskId })).payload;
        if (["succeeded", "failed", "cancelled", "aborted"].includes(settled?.state)) break;
        await page.waitForTimeout(100);
      } while (Date.now() < until);
      const rows = await readRows();
      const expected = decision === "conflict" ? "changed elsewhere" : decision === "approved" ? "Vibe Table" : initial;
      const expectedSecond = decision === "approved" ? "Same Row" : secondInitial;
      recorder.check(`native normalization ${decision} preserves both selected fields and the unselected row`, valueOf(rows, selectedId) === expected && valueOf(rows, otherId) === " keep "
        && valueOf(rows, selectedId, secondField) === expectedSecond && valueOf(rows, otherId, secondField) === otherSecond
        && (decision === "approved" ? settled?.state === "succeeded" : decision === "conflict" ? settled?.state === "failed" && settled.error?.code === "mutation.digest_conflict" : settled?.state === "cancelled"), { settled, rows });
      await page.getByTestId("plugin-headless-close").waitFor({ timeout: 30000 }); await page.getByTestId("plugin-headless-close").click();
      const audit = (await rawBridgeRequest(page, "plugin.audit.list", { projectKey, pluginId: "com.vibetable.examples.normalize-text" })).payload;
      const terminals = audit.filter(event => event.eventType === "action" && event.runId === settled.runId);
      recorder.check(`native normalization ${decision} has one durable receipt for one authorized row`, terminals.length === 1
        && terminals[0].targetCount === 1 && terminals[0].details?.taskId === taskId
        && terminals[0].outcome === (decision === "approved" ? "succeeded" : decision === "conflict" ? "failed" : decision), { terminals, taskId });
      if (decision === "conflict") await applyProductMutation(page, table.tableId, [{ kind: "update", recordId: selectedId, values: { [field]: initial } }], "sdk-reset-synthetic-value");
    }
  } finally { await fs.writeFile(sourceControl, originalSource, "utf8"); }
}
