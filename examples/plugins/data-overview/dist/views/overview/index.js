const summary = document.querySelector("#summary");
const refresh = document.querySelector("#refresh");
let surfaceToken = "";

function emit(event, payload = {}) {
  if (!surfaceToken) return;
  window.parent.postMessage({
    contract: "vibetable.plugin-surface.v1",
    surfaceToken,
    event,
    payload,
  }, "https://app.vibetable.local");
}

window.addEventListener("message", (event) => {
  if (event.source !== window.parent
    || event.origin !== "https://app.vibetable.local"
    || event.data?.contract !== "vibetable.plugin-surface.v1") return;
  surfaceToken = event.data.surfaceToken;
  if (event.data.event === "themeChanged"
    && event.data.payload?.contract === "vibetable.plugin-theme.v1") {
    document.documentElement.dataset.theme = event.data.payload.mode;
    document.documentElement.dataset.density = event.data.payload.density;
    document.documentElement.lang = event.data.payload.locale;
    for (const [name, value] of Object.entries(event.data.payload.variables ?? {})) {
      document.documentElement.style.setProperty(name, value);
    }
    summary.textContent = "主题已同步，可刷新概览数据。";
    emit("ready");
  }
  if (event.data.event === "taskChanged") {
    const task = event.data.payload;
    summary.textContent = task?.result?.summary
      ?? task?.error
      ?? (task?.state === "running" ? "正在读取概览数据…" : `任务状态：${task?.state ?? "未知"}`);
  }
});

refresh.addEventListener("click", () => emit("action", {}));
