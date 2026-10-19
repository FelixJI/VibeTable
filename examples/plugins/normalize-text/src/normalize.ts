import { createSurfaceClient, finishHeadlessRun } from "@vibetable/plugin-sdk";
import { normalizeSelection } from "./normalize-action.js";

const api = createSurfaceClient(window.name);
try { finishHeadlessRun(window.name, "succeeded", await normalizeSelection(api)); }
catch (error) { finishHeadlessRun(window.name, "failed", `规范化失败：${error instanceof Error ? error.message : String(error)}`); }
finally { api.dispose(); }
