import { createSurfaceClient, type DataDescription, type SurfaceOutput } from "@vibetable/plugin-sdk";
import { syntheticWorkbook } from "./synthetic-xlsx.js";

const api = createSurfaceClient(window.name);
const status = document.querySelector<HTMLElement>("#status")!;
const result = document.querySelector<HTMLElement>("#result")!;
let current: SurfaceOutput | undefined;
let artifactId = "";
let serial = 0;
let templateVersion = "template-v1";
const mediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";

async function generate(large: boolean): Promise<void> {
  current?.release();
  const content = syntheticWorkbook(large, templateVersion);
  current = await api.output.create(content, { suggestedName: "synthetic.xlsx", mediaType,
    source: { templateVersion, dataVersion: "synthetic-business-v7", purpose: "issued-copy" } });
  result.textContent = JSON.stringify({ outputId: current.outputId, bytes: content.length, source: current.source });
}
async function archive(): Promise<void> {
  if (!current) throw new Error("先生成产物");
  const context = await api.context.read();
  if (!context.collection) throw new Error("需要活动表");
  const table = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection }) as DataDescription;
  const field = table.fields.find(item => item.logicalType === "file");
  if (!field) throw new Error("需要附件字段");
  const rows = await api.data.query({ contract: "vibetable.plugin-query.v2", collection: table.collection, fields: ["id", field.fieldId], pageSize: 1 });
  const recordId = String(rows.items[0]?.id ?? "");
  const preview = await current.previewArchive({ collection: table.collection, recordId, fieldId: field.fieldId, expectedDigest: rows.rowGuards[recordId] });
  const saved = await api.mutation.submit(preview.planId);
  if (saved.status === "succeeded") {
    artifactId = current.outputId;
    document.querySelector<HTMLInputElement>("#artifact")!.value = artifactId;
  }
  result.textContent = JSON.stringify({ ...saved, artifactId });
}
function action(id: string, run: () => Promise<void>): void {
  document.querySelector<HTMLButtonElement>(`#${id}`)!.addEventListener("click", () => {
    status.textContent = "running";
    run().then(() => { status.textContent = "succeeded"; }, error => {
      status.textContent = "failed"; result.textContent = JSON.stringify({ code: error.code, message: error.message });
    });
  });
}
action("small", () => generate(false));
action("large", () => generate(true));
action("next", async () => { templateVersion = "template-v2"; await generate(false); });
action("save", async () => {
  if (!current) throw new Error("先生成产物");
  result.textContent = JSON.stringify(await current.save(`save-${++serial}`));
});
action("archive", archive);
action("reopen", async () => {
  current?.release();
  artifactId = document.querySelector<HTMLInputElement>("#artifact")!.value;
  current = await api.output.reopen(artifactId);
  result.textContent = JSON.stringify({ outputId: current.outputId, artifactId, source: current.source });
});
action("oversize", async () => { await api.output.create(new Uint8Array(33554433), { suggestedName: "oversize.xlsx", mediaType, source: { templateVersion, dataVersion: "d", purpose: "budget-test" } }); });
window.addEventListener("pagehide", () => api.dispose());
