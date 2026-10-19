import { createSurfaceClient, type DataDescription } from "@vibetable/plugin-sdk";

const api = createSurfaceClient(window.name);
const status = document.querySelector<HTMLParagraphElement>("#status")!;
const output = document.querySelector<HTMLPreElement>("#result")!;
const buttons = [...document.querySelectorAll<HTMLButtonElement>("button:not(#cancel)")];
const cancel = document.querySelector<HTMLButtonElement>("#cancel")!;
let table: DataDescription;
let submitting: AbortController | null = null;

async function run(kind: string): Promise<void> {
  for (const button of buttons) button.disabled = true;
  output.textContent = "";
  try {
    if (kind === "files") {
      const source = await api.file.pickRead({ mediaTypes: ["text/plain"] });
      if (!source) throw new Error("read selection cancelled");
      const content = await source.read();
      const target = await api.file.pickWrite({ suggestedName: "52-output.bin", mediaType: "text/plain" });
      if (!target) throw new Error("write selection cancelled");
      await target.write(content);
      await api.storage.set("lastFileBytes", content.length);
      output.textContent = JSON.stringify({ kind, status: "succeeded", bytes: content.length });
      return;
    }
    const preview = await api.mutation.preview({
      contract: "vibetable.surface-mutation-plan.v1", collection: table.collection,
      schemaRevision: table.schemaRevision,
      operations: [{ kind: "create", values: kind === "denied" ? { forbidden: "must not write" } : {} }],
    });
    submitting = new AbortController(); cancel.disabled = false;
    const receipt = await api.mutation.submit(preview.planId, submitting.signal);
    output.textContent = JSON.stringify({ kind, ...receipt });
  } catch (error) {
    output.textContent = JSON.stringify({ kind, status: "failed", code: String((error as { code?: string }).code ?? "plugin_cancel_requested"), message: error instanceof Error ? error.message : String(error) });
  } finally {
    submitting = null; cancel.disabled = true;
    for (const button of buttons) button.disabled = false;
  }
}
for (const button of buttons) button.addEventListener("click", () => { void run(button.id); });
cancel.addEventListener("click", () => submitting?.abort());
window.addEventListener("pagehide", () => api.dispose());
const context = await api.context.read();
if (!context.collection) throw new Error("active table required");
table = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection }) as DataDescription;
status.textContent = "ready";
for (const button of buttons) button.disabled = false;
