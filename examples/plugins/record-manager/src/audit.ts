import { createSurfaceClient, finishHeadlessRun } from "@vibetable/plugin-sdk";
import { runAudit } from "./audit-action.js";
const api = createSurfaceClient(window.name);
try { finishHeadlessRun(window.name, "succeeded", await runAudit(api)); }
catch (error) { finishHeadlessRun(window.name, "failed", `无界面动作失败：${error instanceof Error ? error.message : String(error)}`); }
finally { api.dispose(); }
