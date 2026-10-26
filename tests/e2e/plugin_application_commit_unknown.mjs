import fs from "node:fs/promises";
import path from "node:path";
import { installApplication, openApplication } from "./plugin_applications.mjs";

async function waitFile(file, predicate, seconds = 20) {
  const until = Date.now() + seconds * 1000;
  while (Date.now() < until) {
    const content = await fs.readFile(file, "utf8").catch(error => { if (error.code === "ENOENT") return null; throw error; });
    if (content !== null && predicate(content)) return content;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error(`acceptance evidence did not arrive: ${path.basename(file)}`);
}

export async function runApplicationCommitUnknown(page, recorder, runtime, helpers) {
  const opened = await helpers.waitForShell(page, recorder, { requireDatabaseOpened: true });
  await page.getByTestId("nav-tables").click();
  // No record mutation precedes this plugin's write: the existing one-shot
  // after_record barrier was armed by the parent before the owned Host launch.
  const table = await helpers.createSimpleTable(page, "Unknown Commit Tasks", "Text");
  await installApplication(page, runtime, "app-upgrade");
  let frame = await openApplication(page);
  await frame.getByRole("link", { name: "详情", exact: true }).click();
  await frame.locator("#draft").fill("unknown synthetic record");
  await helpers.beginBridgeMessageCapture(page, ["plugin.task.changed"]);
  await frame.locator("#save").click();
  const started = await helpers.waitForCapturedBridgeMessage(page, 30000);
  const taskId = started.payload.snapshot.taskId;
  await page.locator(".host-confirmation").waitFor({ timeout: 30000 });
  const approved = await nativePluginInput(runtime, "click", "plugin-confirm-approve", "approved");
  recorder.check("unknown commit was approved by real native input for exactly one Host task", approved.status === "completed" && Boolean(taskId), { approved, started });
  const ready = JSON.parse(await waitFile(path.join(runtime.controlsDir, "mutation-barrier.ready.json"), text => JSON.parse(text).point === "after_record"));
  recorder.check("actual Go write reached the existing transaction barrier", ready.point === "after_record" && Number.isInteger(ready.pid ?? ready.processId), { ready });
  const oldToken = await frame.evaluate(() => window.name);
  // 原生常驻“停止插件”入口已移除：用同一渲染器恢复路径（seal → drain ≤6s →
  // rebuild）封存会话；屏障挂起的写入在 drain 后保持 sticky-unknown，后续
  // 断言不受影响。封存证据即下方的 sealed-sessions trace（无原生输入面）。
  await runtime.refreshPluginProductPage();
  await waitFile(path.join(path.dirname(runtime.dataRoot), "vibetable-trace.log"), text => text.includes("Renderer recovery sealed plugin sessions;"));
  await fs.writeFile(path.join(runtime.controlsDir, "mutation-barrier.release"), "release\n");
  page = await runtime.refreshPluginProductPage();
  await page.getByTestId("nav-home").waitFor({ timeout: 60000 });
  const terminal = await helpers.rawBridgeRequest(page, "plugin.task.get", { taskId });
  recorder.check("lost commit receipt is unknown rather than rolled back or replayed", terminal.payload.state === "aborted" && terminal.payload.error?.code === "plugin_commit_unknown", { terminal });
  const query = () => helpers.rawBridgeRequest(page, "query.page", { tableId: table.tableId, query: { filters: [], sorts: [], offset: 0, limit: 10 } });
  const observed = await query();
  recorder.check("authority read is bounded after unknown and does not invent a rollback", observed.payload.rows.length <= 1, { observed });
  await page.getByTestId("nav-tables").click();
  await helpers.selectTable(page, "Unknown Commit Tasks");
  frame = await openApplication(page);
  const stale = await frame.evaluate(async token => { try { await call("context.read", {}, token); return "accepted"; } catch(error) { return error.code; } }, oldToken);
  recorder.check("unknown run cannot resurrect the old execution token", stale === "plugin_source_denied", { stale });
  const later = await helpers.rawBridgeRequest(page, "plugin.task.get", { taskId });
  const unchanged = await query();
  recorder.check("navigation preserves unknown and never automatically repeats its write", later.payload.state === "aborted" && later.payload.error?.code === "plugin_commit_unknown" && JSON.stringify(observed.payload.rows) === JSON.stringify(unchanged.payload.rows), { later, unchanged });
  // 恢复后的外壳可用性以真实侧栏入口验证（原生恢复行仅存在于故障层）。
  recorder.check("unknown commit recovery leaves the product shell usable",
    await page.getByTestId("nav-plugins").isVisible());
  await page.getByTestId("plugin-app-return").click();
  return { taskId, projectKey: opened.payload.projectKey };
}
