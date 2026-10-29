import { createSurfaceClient, type TransactionPlan, type TransactionDeclaration, type SurfaceDataset, type SurfaceModelStatus } from "@vibetable/plugin-sdk";
import manifest from "../manifest.json";

const client = createSurfaceClient(window.name);
const result = document.querySelector<HTMLElement>("#result")!;
const status = document.querySelector<HTMLElement>("#status")!;
const request = document.querySelector<HTMLInputElement>("#request")!;
let headId: string | undefined;
let dataset: SurfaceDataset | undefined;
let datasetModel: SurfaceModelStatus | undefined;
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
window.addEventListener("pagehide", () => client.dispose());
status.textContent = "ready";
