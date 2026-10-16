import fs from "node:fs/promises";
const printUrl = url => /^(?:chrome|edge):\/\/print\/(?:[?#].*)?$/.test(url);

export function selectOwnedPreviewTarget(targets, contextId) {
  if (typeof contextId !== "string" || !contextId) throw new Error("owned native preview context required");
  const matches = targets.filter(target => target.type === "page" && printUrl(target.url) && target.browserContextId === contextId);
  if (matches.length > 1) throw new Error("ambiguous native print preview targets");
  return matches[0] ?? null;
}

/** A public CDP attachment to the actual WebUI target omitted by WebView2 Page enumeration. */
export async function attachNativePreview(session, target) {
  if (!target?.targetId || !target.browserContextId || !printUrl(target.url)) throw new Error("owned native preview target required");
  let sequence = 0, closed = false, disposed = false, sessionId;
  const pending = new Map(), closedWaiters = new Set();
  function finish(error) {
    for (const item of pending.values()) { clearTimeout(item.timer); item.reject(error); }
    pending.clear();
  }
  function received(event) {
    if (event.sessionId !== sessionId) return;
    const message = JSON.parse(event.message), item = pending.get(message.id);
    if (!item) return;
    pending.delete(message.id); clearTimeout(item.timer);
    if (message.error) item.reject(new Error(message.error.message)); else item.resolve(message.result);
  }
  function destroyed(event) {
    if (event.targetId !== target.targetId) return;
    closed = true; finish(new Error("native preview target closed"));
    for (const done of closedWaiters) done(); closedWaiters.clear();
  }
  session.on("Target.receivedMessageFromTarget", received);
  session.on("Target.targetDestroyed", destroyed);
  async function dispose() {
    if (disposed) return; disposed = true;
    session.off("Target.receivedMessageFromTarget", received); session.off("Target.targetDestroyed", destroyed);
    finish(new Error("native preview observation detached"));
    for (const done of closedWaiters) done(new Error("native preview observation detached")); closedWaiters.clear();
    try { if (sessionId && !closed) await session.send("Target.detachFromTarget", { sessionId }); }
    finally { await session.detach(); }
  }
  async function command(method, params = {}, timeout = 25000) {
    if (disposed || closed) throw new Error("native preview target closed");
    const id = ++sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`native preview CDP deadline: ${method}`)); }, timeout);
      pending.set(id, { resolve, reject, timer });
      session.send("Target.sendMessageToTarget", { sessionId, message: JSON.stringify({ id, method, params }) }).catch(error => {
        const item = pending.get(id); if (!item) return; clearTimeout(item.timer); pending.delete(id); reject(error);
      });
    });
  }
  async function current(timeout) {
    let timer;
    const read = session.send("Target.getTargetInfo", { targetId: target.targetId });
    let targetInfo;
    try {
      ({ targetInfo } = await (timeout === undefined ? read : Promise.race([read, new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("native preview identity deadline")), timeout);
      })])));
    } finally { clearTimeout(timer); }
    if (targetInfo.targetId !== target.targetId || targetInfo.browserContextId !== target.browserContextId || !printUrl(targetInfo.url)) throw new Error("native preview target identity changed");
  }
  async function evaluate(fn) {
    await current();
    const response = await command("Runtime.evaluate", { expression: `(() => { if (!(${printUrl.toString()})(location.href)) throw new Error("native preview origin changed"); return (${fn.toString()})(); })()`, returnByValue: true, awaitPromise: true });
    if (response.exceptionDetails) throw new Error(`native preview evaluation failed: ${response.exceptionDetails.exception?.description ?? response.exceptionDetails.text}`);
    return response.result?.value;
  }
  try {
    await session.send("Target.setDiscoverTargets", { discover: true });
    ({ sessionId } = await session.send("Target.attachToTarget", { targetId: target.targetId, flatten: false }));
    if (!sessionId) throw new Error("native preview attachment returned no session");
    await current();
    await command("Runtime.enable");
    return {
      url: () => target.url, isClosed: () => closed, evaluate, dispose,
      targetObservation: { targetId: target.targetId, browserContextId: target.browserContextId, url: target.url, transport: "owned-cdp-target-session" },
      async screenshot({ path, timeout = 25000 }) {
        const deadline = Date.now() + timeout;
        await current(timeout);
        const { data } = await command("Page.captureScreenshot", { format: "png" }, Math.max(1, deadline - Date.now()));
        await fs.writeFile(path, Buffer.from(data, "base64"));
      },
      locator(selector) {
        if (selector !== "print-preview-button-strip .cancel-button, app-shell print-preview side-bar footer-container fluent-button.cancel") throw new Error("native preview control outside closed set");
        const buttons = () => {
          const app = document.querySelector("print-preview-app");
          const strip = app?.shadowRoot?.querySelector("print-preview-sidebar")?.shadowRoot?.querySelector("print-preview-button-strip")
            ?? app?.shadowRoot?.querySelector("print-preview-button-strip");
          const edge = document.querySelector("app-shell")?.shadowRoot?.querySelector("print-preview");
          const footer = edge?.shadowRoot?.querySelector("side-bar")?.shadowRoot?.querySelector("footer-container");
          return [...(strip?.shadowRoot?.querySelectorAll(".cancel-button") ?? []),
            ...(footer?.shadowRoot?.querySelectorAll("fluent-button.cancel") ?? [])];
        };
        return {
          count: () => evaluate(new Function(`return (${buttons.toString()})().length;`)),
          async click() {
            try { await evaluate(new Function(`const controls = (${buttons.toString()})(); if (controls.length !== 1 || controls[0].disabled) throw new Error("one enabled native Cancel required"); controls[0].click();`)); }
            catch (error) { if (!closed) throw error; }
          },
        };
      },
      waitForEvent(name, { timeout = 15000 } = {}) {
        if (name !== "close") throw new Error("native preview event outside closed set");
        if (closed) return Promise.resolve();
        return new Promise((resolve, reject) => {
          const done = error => { clearTimeout(timer); closedWaiters.delete(done); if (error) reject(error); else resolve(); };
          const timer = setTimeout(() => { closedWaiters.delete(done); reject(new Error("native preview close deadline")); }, timeout);
          closedWaiters.add(done);
        });
      },
    };
  } catch (error) { await dispose(); throw error; }
}
