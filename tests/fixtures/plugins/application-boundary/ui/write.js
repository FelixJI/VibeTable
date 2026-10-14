const channel = window.chrome.webview, token = window.name;
let sequence = 0;
const pending = new Map();
function call(method, params = {}) {
  return new Promise((resolve, reject) => {
    const requestId = `headless-write-${++sequence}`;
    pending.set(requestId, { resolve, reject });
    channel.postMessage({ contract: "vibetable.plugin-api.v1", surfaceToken: token, requestId, method, params });
  });
}
channel.addEventListener("message", event => {
  const reply = event.data, saved = pending.get(reply?.requestId);
  if (!saved) return;
  pending.delete(reply.requestId);
  if (reply.error) saved.reject(Object.assign(new Error(reply.error.message), { code: reply.error.code }));
  else saved.resolve(reply.result);
});
try {
  const context = await call("context.read");
  const description = await call("data.describe", { accepts: ["vibetable.plugin-data.v2"], collection: context.collection });
  const field = description.fields.find(item => !item.readonly && item.logicalType === "text").fieldId;
  const page = await call("data.query", { contract: "vibetable.plugin-query.v2", collection: context.collection, fields: ["id", field], pageSize: 200 });
  const row = page.items[0];
  const plan = await call("mutation.preview", { contract: "vibetable.surface-mutation-plan.v1", collection: description.collection,
    schemaRevision: description.schemaRevision, operations: [{ kind: "update", primaryKey: String(row.id),
      expectedDigest: page.rowGuards[String(row.id)], values: { [field]: "must-not-commit-headless" } }] });
  await call("mutation.submit", { planId: plan.planId });
} catch (error) {
  parent.postMessage({ contract: "vibetable.plugin-surface.v1", surfaceToken: token, event: "close",
    payload: { status: "failed", summary: error.code || error.message } }, "https://app.vibetable.local");
}
