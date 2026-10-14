// 无界面（headless）动作示例：在宿主内置受控文档中作为模块加载。
// 读取当前命令上下文，把活动表记入插件私有存储，然后向可信宿主页
// 报告终态；宿主显示状态并释放 surface token/frame。
(function () {
  "use strict";
  const SURFACE_CONTRACT = "vibetable.plugin-api.v1";
  const channel = globalThis.chrome && globalThis.chrome.webview;
  const token = window.name;
  const pending = new Map();

  function finish(status, summary) {
    try {
      window.parent.postMessage({
        contract: "vibetable.plugin-surface.v1",
        surfaceToken: token,
        event: "close",
        payload: { status, summary },
      }, "https://app.vibetable.local");
    } catch {
      // 宿主取消路径仍会回收 surface；这里不抛出。
    }
  }

  function call(method, params) {
    return new Promise((resolve, reject) => {
      if (!channel || !token) { reject(new Error("缺少宿主 surface 通道")); return; }
      const requestId = "headless-" + Math.random().toString(36).slice(2, 10);
      const timer = setTimeout(() => { pending.delete(requestId); reject(new Error(method + " 超时")); }, 20000);
      pending.set(requestId, { resolve, reject, timer });
      channel.postMessage({ contract: SURFACE_CONTRACT, surfaceToken: token, requestId, method, params: params || {} });
    });
  }

  if (channel) {
    channel.addEventListener("message", (event) => {
      const reply = event.data;
      if (!reply || reply.contract !== SURFACE_CONTRACT || !pending.has(reply.requestId)) return;
      const entry = pending.get(reply.requestId);
      pending.delete(reply.requestId);
      clearTimeout(entry.timer);
      if (reply.error) entry.reject(new Error(reply.error.message || "请求失败"));
      else entry.resolve(reply.result);
    });
  }

  async function run() {
    try {
      const context = await call("context.read", {});
      if (!context.collection) {
        finish("failed", "宿主没有活动数据表，未记录任何内容。");
        return;
      }
      await call("storage.set", { key: "lastRememberedTable", value: { collection: context.collection, rememberedAt: new Date().toISOString() } });
      finish("succeeded", "已记住当前活动表：" + context.collection);
    } catch (error) {
      finish("failed", "无界面动作失败：" + (error instanceof Error ? error.message : String(error)));
    }
  }

  run();
})();
