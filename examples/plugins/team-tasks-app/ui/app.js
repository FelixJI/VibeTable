// 内联最小 surface 客户端：与 @vibetable/plugin-sdk createSurfaceClient 同协议，
    // 用于本示例无 npm 依赖地演示应用形态。
    const SURFACE_CONTRACT = "vibetable.plugin-api.v1";
    const channel = window.chrome && window.chrome.webview;
    const token = window.name;
    const pending = new Map();
    let seq = 0;
    function call(method, params) {
      if (!channel || !token) return Promise.reject(new Error("缺少宿主 surface 通道"));
      const requestId = "app-" + (++seq);
      return new Promise((resolve, reject) => {
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
        if (reply.error) entry.reject(new Error(reply.error.message || reply.error.code || "请求失败"));
        else entry.resolve(reply.result);
      });
    }
    const statusEl = document.getElementById("status");
    function setStatus(text) { statusEl.textContent = text; }

    let table = null; // { collection, fields, rows }
    async function loadActiveTable() {
      if (table) return table;
      const context = await call("context.read");
      if (!context.collection) throw new Error("宿主没有活动数据表：请先在平台打开一个数据表，再回到本应用。");
      const described = await call("data.describe", { accepts: ["vibetable.plugin-data.v2"], collection: context.collection });
      const columns = described.fields.slice(0, 4).map((field) => field.fieldId);
      const rows = [];
      let cursor = null;
      do {
        const page = await call("data.query", {
          contract: "vibetable.plugin-query.v2", collection: context.collection,
          fields: columns, sorts: [], pageSize: 50, ...(cursor ? { cursor } : {}),
        });
        if (!page.complete) throw new Error("计算数据尚未就绪；未把不完整结果当成全部记录。");
        rows.push(...page.items);
        cursor = page.nextCursor;
      } while (cursor && rows.length < 200);
      table = { collection: context.collection, columns, rows };
      return table;
    }
    function showError(where, error) {
      document.getElementById(where).innerHTML = "";
      const p = document.createElement("p");
      p.className = "error";
      p.setAttribute("role", "alert");
      p.textContent = error instanceof Error ? error.message : String(error);
      document.getElementById(where).appendChild(p);
    }
    async function renderTasks() {
      try {
        const data = await loadActiveTable();
        const host = document.getElementById("tasks-body");
        host.innerHTML = "";
        if (!data.rows.length) { host.innerHTML = '<p class="muted">表中暂无记录。</p>'; return; }
        const tableEl = document.createElement("table");
        const head = document.createElement("tr");
        for (const column of data.columns) {
          const th = document.createElement("th"); th.textContent = column; head.appendChild(th);
        }
        tableEl.appendChild(head);
        for (const row of data.rows.slice(0, 20)) {
          const tr = document.createElement("tr");
          for (const column of data.columns) {
            const td = document.createElement("td"); td.textContent = String(row[column] ?? ""); tr.appendChild(td);
          }
          tableEl.appendChild(tr);
        }
        host.appendChild(tableEl);
        setStatus("已读取 " + data.rows.length + " 条记录");
      } catch (error) { showError("tasks-body", error); }
    }
    async function renderDetail() {
      try {
        const data = await loadActiveTable();
        const host = document.getElementById("detail-body");
        host.innerHTML = "";
        if (!data.rows.length) { host.innerHTML = '<p class="muted">没有可显示的记录。</p>'; return; }
        const list = document.createElement("dl");
        const row = data.rows[0];
        for (const column of data.columns) {
          const dt = document.createElement("dt"); dt.textContent = column;
          const dd = document.createElement("dd"); dd.textContent = String(row[column] ?? "");
          list.appendChild(dt); list.appendChild(dd);
        }
        host.appendChild(list);
      } catch (error) { showError("detail-body", error); }
    }
    async function renderStats() {
      try {
        const data = await loadActiveTable();
        const remembered = await call("storage.get", { key: "lastRememberedTable" });
        const host = document.getElementById("stats-body");
        host.innerHTML = "";
        const ul = document.createElement("ul");
        const items = [
          "活动数据表：" + data.collection,
          "读取记录数：" + data.rows.length,
          "上次记住的表：" + (remembered && typeof remembered === "object" ? remembered.collection : "（未记录）"),
        ];
        for (const item of items) {
          const li = document.createElement("li"); li.textContent = item; ul.appendChild(li);
        }
        host.appendChild(ul);
      } catch (error) { showError("stats-body", error); }
    }
    const pages = ["tasks", "detail", "stats"];
    function route() {
      const hash = (location.hash || "#tasks").slice(1);
      const page = hash === "detail" || hash === "stats" ? hash : "tasks";
      for (const name of pages) {
        document.getElementById("page-" + name).hidden = name !== page;
      }
      for (const link of document.querySelectorAll("nav a")) {
        if (link.dataset.page === page) link.setAttribute("aria-current", "page");
        else link.removeAttribute("aria-current");
      }
      if (page === "detail") renderDetail();
      else if (page === "stats") renderStats();
      else renderTasks();
    }
    document.getElementById("reload").addEventListener("click", () => { table = null; route(); });
    window.addEventListener("hashchange", route);
    route();
