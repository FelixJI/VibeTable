interface ActiveApplication { readonly surfaceToken: string; readonly src: string }
interface PrintScope {
  prepare(surfaceToken: string, requestId: string, height: number): boolean;
  release(requestId: string): void;
  clear(): void;
  dispose(): void;
}
declare global { interface Window { __vibetablePluginPrint?: PrintScope } }

/** Parent-owned DOM only: a plugin supplies neither selectors nor HTML. */
export function installPluginPrintScope(current: () => ActiveApplication | null): PrintScope {
  const previous = window.__vibetablePluginPrint;
  let disposed = false;
  let pending: { surfaceToken: string; requestId: string; height: number } | null = null;
  const clear = (): void => {
    for (const element of document.querySelectorAll("[data-vt-plugin-print-path]"))
      element.removeAttribute("data-vt-plugin-print-path");
    for (const frame of document.querySelectorAll<HTMLElement>("[data-plugin-print-surface]"))
      frame.style.removeProperty("--plugin-print-height");
    // Keep print mode fail-closed until preview closes: navigation must not expose platform DOM.
  };
  const release = (requestId: string): void => {
    if (pending?.requestId !== requestId) { previous?.release(requestId); return; }
    pending = null;
    clear();
    document.documentElement.removeAttribute("data-vt-plugin-print");
    if (disposed) window.removeEventListener("afterprint", afterPrint);
    if (disposed && window.__vibetablePluginPrint === scope) delete window.__vibetablePluginPrint;
  };
  const mark = (surfaceToken: string, height: number): boolean => {
    const app = disposed ? null : current();
    if (!app || app.surfaceToken !== surfaceToken) { clear(); return false; }
    const frames = [...document.querySelectorAll<HTMLIFrameElement>(
      '[data-testid="plugin-app-viewport"] iframe[data-plugin-print-surface]',
    )];
    if (frames.length !== 1) { clear(); return false; }
    const frame = frames[0]!;
    const bounds = frame.getBoundingClientRect();
    if (frame.name !== surfaceToken || frame.dataset.pluginPrintSurface !== surfaceToken
      || frame.getAttribute("src") !== app.src || bounds.width <= 0 || bounds.height <= 0) { clear(); return false; }
    clear();
    frame.style.setProperty("--plugin-print-height", `${height}px`);
    for (let element: HTMLElement | null = frame; element; element = element.parentElement) {
      element.setAttribute("data-vt-plugin-print-path", "");
      if (element === document.body) return true;
    }
    clear();
    return false;
  };
  const beforePrint = (): void => { if (pending) mark(pending.surfaceToken, pending.height); };
  const afterPrint = (): void => {
    if (pending) release(pending.requestId);
    if (disposed) window.removeEventListener("afterprint", afterPrint);
  };
  const scope: PrintScope = {
    prepare(surfaceToken, requestId, height) {
      if (disposed || pending || document.documentElement.hasAttribute("data-vt-plugin-print")
        || !requestId || !Number.isSafeInteger(height) || height < 1 || height > 100_000 || !mark(surfaceToken, height)) return false;
      pending = { surfaceToken, requestId, height };
      document.documentElement.setAttribute("data-vt-plugin-print", "");
      return true;
    },
    clear,
    release,
    dispose() {
      disposed = true;
      clear();
      window.removeEventListener("beforeprint", beforePrint);
      if (!pending) window.removeEventListener("afterprint", afterPrint);
      if (!pending && window.__vibetablePluginPrint === scope) delete window.__vibetablePluginPrint;
    },
  };
  window.addEventListener("beforeprint", beforePrint);
  window.addEventListener("afterprint", afterPrint);
  window.__vibetablePluginPrint = scope;
  return scope;
}
