import { createSurfaceClient, type DataDescription, type SurfaceMutationPreview, type SurfaceTransport } from "@vibetable/plugin-sdk";
import { observeTransport } from "./transport.js";
const browser = globalThis as typeof globalThis & { chrome?: { webview?: SurfaceTransport } };
if (!browser.chrome?.webview) throw new Error("native frame transport unavailable");
const observed = observeTransport(browser.chrome.webview);
const api = createSurfaceClient(window.name, observed.transport);
observed.requireBaseline(1);
const results: Record<string, unknown> = {};
const events: unknown[] = [], tasks: unknown[] = [], markerEvents: unknown[] = [];
let description: DataDescription, field: string, attachment: string, relation: string;
let preview: SurfaceMutationPreview;
let held: Awaited<ReturnType<typeof api.file.pickRead>>;
let subscription: Awaited<ReturnType<typeof api.data.subscribe>>;
const query = () => api.data.query({ contract: "vibetable.plugin-query.v2", collection: description.collection, fields: ["id", field, relation], pageSize: 200 });
const code = async (operation: () => Promise<unknown>) => { try { await operation(); return "accepted"; } catch (error) { return String((error as { code?: string }).code ?? error); } };
async function rawForeignRead(handle: string, attachmentRead = false) {
  const browser = globalThis as typeof globalThis & { chrome?: { webview?: SurfaceTransport } };
  const channel = browser.chrome?.webview, requestId = crypto.randomUUID();
  if (!channel) throw new Error("native frame transport unavailable");
  return new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => { channel.removeEventListener("message", receive); reject(new Error("foreign read deadline")); }, 5000);
    const receive = (event: { data: unknown }) => {
      const value: unknown = typeof event.data === "string" ? JSON.parse(event.data) : event.data;
      if (typeof value !== "object" || value === null || !("requestId" in value) || value.requestId !== requestId) return;
      clearTimeout(timer); channel.removeEventListener("message", receive);
      const error = "error" in value ? value.error : null;
      resolve(typeof error === "object" && error !== null && "code" in error && typeof error.code === "string" ? error.code : "accepted");
    };
    channel.addEventListener("message", receive);
    channel.postMessage({ contract: "vibetable.plugin-api.v1", requestId, surfaceToken: window.name, method: attachmentRead ? "attachments.read" : "file.read", params: attachmentRead ? { attachmentId: handle, offset: 0 } : { grantId: handle } });
  });
}
async function execute(action: string, value: string = "") {
  if (action === "read") {
    if (!held) return { cancelled: true };
    const grantId = held.grantId, content = new TextDecoder().decode(await held.read());
    return { grantId, content, reused: await code(() => held!.read()) };
  }
  if (action === "pickRead") { held = await api.file.pickRead(); return held ? { grantId: held.grantId, displayName: held.displayName } : null; }
  if (action === "write") { const grant = await api.file.pickWrite({ suggestedName: "52-output.bin", mediaType: "application/octet-stream" }); if (!grant) return null; await grant.write(new TextEncoder().encode("VIBETABLE_483_LITERAL_FILE\n")); return { displayName: grant.displayName }; }
  if (action === "previewImport") { const task = await api.data.previewImport({ collection: description.collection, schemaRevision: description.schemaRevision, columnMapping: [{ sourceColumn: "Text", targetField: field }] }); if (!task) return null; const progress: unknown[] = []; const result = await task.result(undefined, item => progress.push(item)); if (result.planId) preview = { planId: result.planId, affectedCount: result.affectedCount }; return { taskId: task.taskId, status: await task.status(), progress, result }; }
  if (action === "export") { const task = await api.data.export({ collection: description.collection, fields: [field], format: "csv", suggestedName: "52-export.csv" }); if (!task) return null; return { result: await task.result(), status: await task.status() }; }
  if (action === "prepare") {
    const rows = await query(), first = rows.items[0];
    const operations = value === "create" ? [{ kind: "create" as const, values: { [field]: "plugin-created" } }]
      : value === "relation" ? [{ kind: "update" as const, primaryKey: String(first.id), expectedDigest: rows.rowGuards[String(first.id)], values: { [relation]: String(rows.items[1].id) } }]
      : value === "delete" ? [{ kind: "delete" as const, primaryKey: String(first.id), expectedDigest: rows.rowGuards[String(first.id)] }]
      : [{ kind: "update" as const, primaryKey: String(first.id), expectedDigest: rows.rowGuards[String(first.id)], values: { [field]: "plugin-updated" } }];
    preview = await api.mutation.preview({ contract: "vibetable.surface-mutation-plan.v1", collection: description.collection, schemaRevision: description.schemaRevision, operations }); return preview;
  }
  if (action === "denyCollection") return code(() => api.data.query({ contract: "vibetable.plugin-query.v2", collection: value, fields: ["id"], pageSize: 1 }));
  if (action === "denyField") return code(() => api.data.query({ contract: "vibetable.plugin-query.v2", collection: description.collection, fields: ["field_not_granted"], pageSize: 1 }));
  if (action === "invalidBatch") return code(async () => api.mutation.preview({ contract: "vibetable.surface-mutation-plan.v1", collection: description.collection, schemaRevision: description.schemaRevision, operations: [{ kind: "create", values: { [field]: "MUST_NOT_COMMIT" } }, { kind: "create", values: { unknown_field: "invalid" } }] }));
  if (action === "submit") return api.mutation.submit(preview.planId);
  if (action === "snapshot") return { rows: await query(), events, markerEvents, tasks: await api.tasks.list() };
  if (action === "attach") { const rows = await query(), first = rows.items[0]; const result = await api.attachments.previewChange({ collection: description.collection, recordId: String(first.id), fieldId: attachment, expectedDigest: rows.rowGuards[String(first.id)], addFile: true }); if (result) preview = result; return result; }
  if (action === "attachments") { const rows = await query(), first = rows.items[0]; const list = await api.attachments.list({ collection: description.collection, recordId: String(first.id), fieldId: attachment }); return { list, contents: await Promise.all(list.map(async item => new TextDecoder().decode(await api.attachments.read(item.attachmentId)))) }; }
  if (action === "cancelTask") { const list = await api.tasks.list(); const current = list.find(item => item.state === "queued" || item.state === "running"); if (!current) throw new Error("no current cancellable task"); return { taskId: current.taskId, receipt: await api.tasks.cancel(current.taskId) }; }
  if (action === "foreignAttachment") return rawForeignRead(value, true);
  if (action === "foreignRead") return rawForeignRead(value);
  if (action === "staleContext") { const old = createSurfaceClient(value, observed.transport); try { return await code(() => old.context.read()); } finally { old.dispose(); observed.requireBaseline(1); } }
  if (action === "listeners") {
    const counts: number[] = [];
    for (let index = 0; index < 12; index++) {
      const client = createSurfaceClient(window.name, observed.transport);
      observed.requireBaseline(2);
      try { await client.context.read(); } finally { client.dispose(); }
      observed.requireBaseline(1); counts.push(observed.count());
    }
    return { baseline: 1, counts };
  }
  if (action === "ui") return { context: await api.context.current(), notification: await api.ui.notifications.show("合成能力通知"), capabilities: api.ui.capabilities() };
  if (action === "afterUnsubscribe") return api.data.subscribe({ collection: description.collection }, event => markerEvents.push(event));
  if (action === "unsubscribe") { await api.data.unsubscribe(subscription.subscriptionId); return { events }; }
  if (action === "back") return api.ui.navigation.back();
  throw new Error("unknown QA action");
}
const qa = { results, events, tasks, markerEvents, async run(key: string, action: string, value = "") { try { results[key] = { ok: true, value: await execute(action, value) }; } catch (error) { results[key] = { ok: false, code: String((error as { code?: string }).code ?? error) }; } document.querySelector("#result")!.textContent = JSON.stringify(results, null, 2); } };
Object.assign(window, { qa });
async function load() {
  const context = await api.context.read();
  description = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection! }) as DataDescription;
  field = description.fields.find(item => item.displayName === "Text")!.fieldId;
  relation = description.fields.find(item => item.displayName === "Self relation")!.fieldId;
  attachment = description.fields.find(item => item.logicalType === "file")!.fieldId;
  subscription = await api.data.subscribe({ collection: description.collection }, event => events.push(event));
  api.tasks.onChanged(items => tasks.push(items));
  document.querySelector("#status")!.textContent = "ready";
}
window.addEventListener("pagehide", () => api.dispose());
load().catch(error => { document.querySelector("#status")!.textContent = String(error); });
