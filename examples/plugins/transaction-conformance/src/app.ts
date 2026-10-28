import { createSurfaceClient, type TransactionPlan, type TransactionDeclaration } from "@vibetable/plugin-sdk";
import manifest from "../manifest.json";

const client = createSurfaceClient(window.name);
const result = document.querySelector<HTMLElement>("#result")!;
const status = document.querySelector<HTMLElement>("#status")!;
const request = document.querySelector<HTMLInputElement>("#request")!;
let headId: string | undefined;

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
for (const id of ["submit", "recover"]) {
  document.querySelector(`#${id}`)!.addEventListener("click", () => void show(async () => {
    const value = id === "submit" ? await client.transaction.submit({ requestId: request.value }) : await client.transaction.status({ requestId: request.value });
    if (value?.status === "applied") headId = value.newIds.heads?.h;
    return value;
  }));
}
status.textContent = "ready";
