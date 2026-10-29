import { createSurfaceClient, type TransactionPlan, type TransactionDeclaration, type SurfaceDataset, type SurfaceModelStatus, type SurfaceOutput, type DataDescription } from "@vibetable/plugin-sdk";
import manifest from "../manifest.json";
import { t01Workbook } from "./t01-xlsx.js";

const client = createSurfaceClient(window.name);
const result = document.querySelector<HTMLElement>("#result")!;
const status = document.querySelector<HTMLElement>("#status")!;
const request = document.querySelector<HTMLInputElement>("#request")!;
let headId: string | undefined;
let dataset: SurfaceDataset | undefined;
let datasetModel: SurfaceModelStatus | undefined;
let concurrent: { first: { requestId: string; newIds: Record<string, Record<string, string>> }; second: { requestId: string; newIds: Record<string, Record<string, string>> } } | undefined;
let t01: { output: SurfaceOutput; rows: { entity: string; recordId: string; amount: string; relation: string }[]; versions: unknown; datasetId: string } | undefined;
let t01Serial = 0;
const t01TemplateVersion = "T01-synthetic-template-v1";
const subscriptions: string[] = [];

async function releaseDataset(): Promise<void> {
  const previous = dataset; dataset = undefined;
  await previous?.release();
  for (const id of subscriptions.splice(0)) await client.data.unsubscribe(id);
}

async function show(operation: () => Promise<unknown>): Promise<void> {
  status.textContent = "running";
  try {
    const value = await operation();
    result.textContent = JSON.stringify(value, null, 2);
    status.textContent = "succeeded";
  } catch (error) {
    result.textContent = JSON.stringify({ code: error instanceof Error && "code" in error ? error.code : "error", message: String(error) });
    status.textContent = "failed";
  }
}

function plan(existingHead?: string): TransactionPlan {
  return {
    contract: "vibetable.transaction-plan.v1", modelId: "transaction_example",
    operations: [
      ...(!existingHead ? [{ entity: "heads", kind: "create" as const, tempId: "h", values: { code: "SYNTHETIC-01", budget: "10.00" } }] : []),
      { entity: "lines", kind: "create", tempId: "l", values: { amount: "8.00", head: existingHead ?? { entity: "heads", tempId: "h" } } },
      { entity: "allocations", kind: "create", tempId: "a", values: { amount: "8.00", line: { entity: "lines", tempId: "l" } } },
    ],
  };
}

document.querySelector("#model")!.addEventListener("click", () => void show(() => client.model.configure({ mode: "create" })));
document.querySelector("#configure")!.addEventListener("click", () => void show(() => client.transaction.configure({ declaration: manifest.permissions.transactions as TransactionDeclaration })));
for (const id of ["preview", "excess"]) {
  document.querySelector(`#${id}`)!.addEventListener("click", () => void show(async () => {
    if (id === "excess" && !headId) throw new Error("先查询已成功的原请求");
    const value = await client.transaction.preview({ plan: plan(id === "excess" ? headId : undefined) });
    request.value = value.requestId;
    return value;
  }));
}
document.querySelector("#dataset-seed")!.addEventListener("click", () => void show(async () => {
  if (!headId) throw new Error("先查询三表提交的原成功结果");
  const operations: TransactionPlan["operations"][number][] = [];
  for (let i = 0; i < 200; i++) {
    operations.push({ entity: "lines", kind: "create", tempId: `dl${i}`, values: { amount: "0.01", head: headId } },
      { entity: "allocations", kind: "create", tempId: `da${i}`, values: { amount: "0.01", line: { entity: "lines", tempId: `dl${i}` } } });
  }
  const preview = await client.transaction.preview({ plan: { contract: "vibetable.transaction-plan.v1", modelId: "transaction_example", operations } });
  request.value = preview.requestId;
  return preview;
}));
for (const id of ["submit", "recover"]) {
  document.querySelector(`#${id}`)!.addEventListener("click", () => void show(async () => {
    const value = id === "submit" ? await client.transaction.submit({ requestId: request.value }) : await client.transaction.status({ requestId: request.value });
    if (value?.status === "applied") headId = value.newIds.heads?.h;
    return value;
  }));
}
document.querySelector("#dataset")!.addEventListener("click", () => void show(async () => {
  await releaseDataset();
  const model = await client.model.read();
  if (model.status !== "completed") throw new Error("先创建并确认模型");
  const sources = Object.entries(model.bindings).map(([entity, binding]) => {
    const amount = entity === "heads" ? binding.fields.budget : binding.fields.amount;
    const relation = entity === "lines" ? binding.fields.head : entity === "allocations" ? binding.fields.line : undefined;
    return { query: { contract: "vibetable.plugin-query.v2" as const, collection: binding.tableId, fields: ["id", amount, ...(relation ? [relation] : [])] },
      groupBy: relation ? [relation] : [], sums: [{ field: amount, storage: "decimalText" as const, scale: 2, digits: 38 }] };
  });
  dataset = await client.dataset.open({ contract: "vibetable.plugin-dataset.v1", sources });
  datasetModel = model;
  try {
    for (const source of sources) {
      const subscription = await client.data.subscribe({ collection: source.query.collection }, () => {
        // Invalidation never silently replaces the version visible to the user.
        document.querySelector("#dataset-state")!.textContent = "stale";
      });
      subscriptions.push(subscription.subscriptionId);
    }
    const summaries = await Promise.all(sources.map(source => dataset!.summary(source.query.collection)));
    document.querySelector("#dataset-state")!.textContent = "current";
    return { datasetId: dataset.datasetId, versions: dataset.versions, sources: dataset.sources, summaries };
  } catch (error) { await releaseDataset(); throw error; }
}));
document.querySelector("#dataset-pages")!.addEventListener("click", () => void show(async () => {
  if (!dataset || !datasetModel) throw new Error("先打开数据集");
  const collection = datasetModel.bindings.lines.tableId;
  const rows = [];
  let offset: number | null = 0, pages = 0;
  do { const page = await dataset.page(collection, offset); rows.push(...page.items); offset = page.nextOffset; pages++; } while (offset !== null);
  return { datasetId: dataset.datasetId, versions: dataset.versions, rows, pages };
}));
document.querySelector("#dataset-export")!.addEventListener("click", () => void show(async () => {
  if (!dataset || !datasetModel) throw new Error("先打开数据集");
  return dataset.export(datasetModel.bindings.lines.tableId, "synthetic-dataset.csv");
}));
document.querySelector("#dataset-release")!.addEventListener("click", () => void show(async () => { await releaseDataset(); return { released: true }; }));

// 双窗口待决证据：先用空白模型建立仅含余额 10 头的权威余额，再预览两个各自申请 8 的独立请求。
document.querySelector("#concurrent-seed")!.addEventListener("click", () => void show(async () => {
  if (!headId) {
    const value = await client.transaction.preview({ plan: {
      contract: "vibetable.transaction-plan.v1", modelId: "transaction_example",
      operations: [{ entity: "heads", kind: "create", tempId: "h", values: { code: "SYNTHETIC-CONCURRENCY-01", budget: "10.00" } }],
    } });
    if (value.status !== "prepared") throw new Error("余额头预览未处于 prepared");
    request.value = value.requestId;
    return value;
  }
  return { headId, reused: true };
}));
document.querySelector("#concurrent-prepare")!.addEventListener("click", () => void show(async () => {
  if (!headId) throw new Error("先提交并确认余额 10 的合成头");
  const planFor = (): TransactionPlan => ({
    contract: "vibetable.transaction-plan.v1", modelId: "transaction_example",
    operations: [
      { entity: "lines", kind: "create", tempId: "l", values: { amount: "8.00", head: headId! } },
      { entity: "allocations", kind: "create", tempId: "a", values: { amount: "8.00", line: { entity: "lines", tempId: "l" } } },
    ],
  });
  const first = await client.transaction.preview({ plan: planFor() });
  const second = await client.transaction.preview({ plan: planFor() });
  if (first.status !== "prepared" || second.status !== "prepared") throw new Error("两个独立请求未全部 prepared");
  if (first.requestId === second.requestId) throw new Error("两个请求共享同一持久身份");
  concurrent = {
    first: { requestId: first.requestId, newIds: first.newIds as Record<string, Record<string, string>> },
    second: { requestId: second.requestId, newIds: second.newIds as Record<string, Record<string, string>> },
  };
  return { headId, first, second };
}));
document.querySelector("#concurrent-submit")!.addEventListener("click", () => void show(async () => {
  if (!concurrent) throw new Error("先准备两个独立请求");
  const settle = async (entry: { requestId: string }) => {
    try { return await client.transaction.submit({ requestId: entry.requestId }); }
    catch (error) { return { code: error instanceof Error && "code" in error ? error.code : "error", message: String(error) }; }
  };
  // 两个提交在任一确认前均已发出，两个原生确认窗口同时待决。
  const [first, second] = await Promise.all([settle(concurrent.first), settle(concurrent.second)]);
  return { first, second, requests: concurrent };
}));

// T01 产物链：从真实公开数据集行生成模板产物，经原生保存/归档/重开/重印。
document.querySelector("#t01-generate")!.addEventListener("click", () => void show(async () => {
  if (!dataset || !datasetModel) throw new Error("先打开数据集");
  const rows: { entity: string; recordId: string; amount: string; relation: string }[] = [];
  for (const [entity, binding] of Object.entries(datasetModel.bindings)) {
    const amountField = entity === "heads" ? binding.fields.budget : binding.fields.amount;
    const relationField = entity === "lines" ? binding.fields.head : entity === "allocations" ? binding.fields.line : undefined;
    let offset: number | null = 0;
    do {
      const page = await dataset.page(binding.tableId, offset);
      for (const item of page.items) {
        const recordId = String(item.id);
        const amount = item[amountField];
        if (typeof amount !== "string" || !/^-?\d+\.\d{2}$/.test(amount)) throw new Error(`数据集金额不是固定小数文本：${entity}`);
        rows.push({ entity, recordId, amount, relation: relationField ? String(item[relationField] ?? "") : "" });
      }
      offset = page.nextOffset;
    } while (offset !== null);
  }
  const content = t01Workbook({ templateVersion: t01TemplateVersion, datasetId: dataset.datasetId, versions: dataset.versions, rows });
  t01?.output.release();
  const output = await client.output.create(content, { suggestedName: "T01-proof.xlsx", mediaType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    source: { templateVersion: t01TemplateVersion, dataVersion: dataset.datasetId, purpose: "issued-dataset-proof" } });
  t01 = { output, rows, versions: dataset.versions, datasetId: dataset.datasetId };
  return { outputId: output.outputId, source: output.source, bytes: content.length, datasetId: dataset.datasetId, versions: dataset.versions, rows };
}));
document.querySelector("#t01-save")!.addEventListener("click", () => void show(async () => {
  if (!t01) throw new Error("先生成 T01 产物");
  return t01.output.save(`t01-${++t01Serial}`);
}));
document.querySelector("#t01-archive")!.addEventListener("click", () => void show(async () => {
  if (!t01) throw new Error("先生成 T01 产物");
  const context = await client.context.read();
  if (!context.collection) throw new Error("需要活动附件表");
  const table = await client.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: context.collection }) as DataDescription;
  const field = table.fields.find(item => item.logicalType === "file");
  if (!field) throw new Error("需要附件字段");
  const rows = await client.data.query({ contract: "vibetable.plugin-query.v2", collection: table.collection, fields: ["id", field.fieldId], pageSize: 1 });
  const recordId = String(rows.items[0]?.id ?? "");
  const preview = await t01.output.previewArchive({ collection: table.collection, recordId, fieldId: field.fieldId, expectedDigest: rows.rowGuards[recordId] });
  const saved = await client.mutation.submit(preview.planId);
  return { saved, artifactId: t01.output.outputId };
}));
document.querySelector("#t01-reopen")!.addEventListener("click", () => void show(async () => {
  const artifact = document.querySelector<HTMLInputElement>("#artifact")!.value;
  if (!artifact) throw new Error("先输入归档产物 ID");
  t01?.output.release();
  const output = await client.output.reopen(artifact);
  t01 = { output, rows: [], versions: null, datasetId: "" };
  return { outputId: output.outputId, artifactId: artifact, source: output.source };
}));
window.addEventListener("pagehide", () => client.dispose());
status.textContent = "ready";
