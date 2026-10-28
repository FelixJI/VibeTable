import { createSurfaceClient } from "@vibetable/plugin-sdk";

const client = createSurfaceClient(window.name);
const result = document.querySelector<HTMLElement>("#result")!;
async function show(operation: () => Promise<unknown>): Promise<void> {
  result.textContent = "正在处理";
  try { result.textContent = JSON.stringify(await operation(), null, 2); }
  catch (error) { result.textContent = error instanceof Error ? error.message : String(error); }
}
document.querySelector("#create")!.addEventListener("click", () => void show(() => client.model.configure({ mode: "create" })));
document.querySelector("#map")!.addEventListener("click", () => void show(() => client.model.configure({ mode: "map" })));
document.querySelector("#read")!.addEventListener("click", () => void show(() => client.model.read()));
client.model.read().then(state => {
  result.textContent = JSON.stringify(state, null, 2);
  document.querySelector("#status")!.textContent = "ready";
}).catch(error => { document.querySelector("#status")!.textContent = String(error); });
