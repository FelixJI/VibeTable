import { afterEach, describe, expect, it, vi } from "vitest";
import { installPluginPrintScope } from "./pluginPrintScope";

const src = "https://" + "b".repeat(32) + "." + "c".repeat(32) + ".plugins.vibetable.local/ui/index.html";
describe("trusted application print scope", () => {
  it("late cleanup after root replacement releases only the old preparation", () => {
    document.body.innerHTML = `<section data-testid="plugin-app-viewport"><iframe data-plugin-print-surface="active" name="active" src="${src}"></iframe></section>`;
    vi.spyOn(document.querySelector("iframe")!, "getBoundingClientRect").mockReturnValue({ width: 800, height: 600 } as DOMRect);
    const old = installPluginPrintScope(() => ({ surfaceToken: "active", src }));
    expect(old.prepare("active", "old-preparation", 2400)).toBe(true);
    old.dispose();
    const current = installPluginPrintScope(() => ({ surfaceToken: "active", src }));
    expect(current.prepare("active", "new-preparation", 2400)).toBe(false);
    current.release("old-preparation");
    expect(current.prepare("active", "new-preparation", 2400)).toBe(true);
    current.release("old-preparation");
    expect(document.documentElement.hasAttribute("data-vt-plugin-print")).toBe(true);
    current.release("new-preparation");
    current.dispose();
    window.dispatchEvent(new Event("afterprint"));
  });

  afterEach(() => { document.body.replaceChildren(); document.documentElement.removeAttribute("data-vt-plugin-print"); });
  it("keeps only the current iframe ancestor path, excluding every body portal and host sibling", () => {
    document.body.innerHTML = `<div id="app"><div class="workspace"><nav>private nav</nav><main><header>private bar</header><section data-testid="plugin-app-viewport"><div><section><iframe data-plugin-print-surface="active" name="active" src="${src}"></iframe><footer>private task and confirmation</footer></section></div></section><aside>private table</aside></main></div></div><div class="n-modal-container">private lookup attachment field history record</div><div class="n-message-container">private toast</div>`;
    const frame = document.querySelector("iframe")!;
    vi.spyOn(frame, "getBoundingClientRect").mockReturnValue({ width: 800, height: 600 } as DOMRect);
    const scope = installPluginPrintScope(() => ({ surfaceToken: "active", src }));
    expect(scope.prepare("forged", "request", 2400)).toBe(false);
    expect(scope.prepare("active", "request", 100001)).toBe(false);
    expect(scope.prepare("active", "request", 2400)).toBe(true);
    const allowed = [...document.querySelectorAll("[data-vt-plugin-print-path]")];
    expect(allowed.every(element => element === frame || element.contains(frame))).toBe(true);
    expect(allowed).toContain(document.body);
    for (const excluded of document.querySelectorAll("nav, header, footer, aside, .n-modal-container, .n-message-container"))
      expect(excluded.hasAttribute("data-vt-plugin-print-path")).toBe(false);
    scope.release("other-request");
    expect(document.documentElement.hasAttribute("data-vt-plugin-print")).toBe(true);
    scope.dispose();
    expect(document.querySelector("[data-vt-plugin-print-path]")).toBeNull();
    expect(document.documentElement.hasAttribute("data-vt-plugin-print")).toBe(true);
    window.dispatchEvent(new Event("afterprint"));
    expect(document.documentElement.hasAttribute("data-vt-plugin-print")).toBe(false);
  });
  it("rejects hidden, retired, duplicate, background or replaced frame without reviving a cleared range", () => {
    document.body.innerHTML = `<section data-testid="plugin-app-viewport"><iframe data-plugin-print-surface="active" name="active" src="${src}"></iframe></section>`;
    const frame = document.querySelector("iframe")!;
    vi.spyOn(frame, "getBoundingClientRect").mockReturnValue({ width: 800, height: 600 } as DOMRect);
    let active: { surfaceToken: string; src: string } | null = { surfaceToken: "active", src };
    const scope = installPluginPrintScope(() => active);
    expect(scope.prepare("active", "request", 2400)).toBe(true);
    active = null; scope.clear();
    expect(document.documentElement.hasAttribute("data-vt-plugin-print")).toBe(true);
    window.dispatchEvent(new Event("beforeprint"));
    expect(document.querySelector("[data-vt-plugin-print-path]")).toBeNull();
    window.dispatchEvent(new Event("afterprint"));
    expect(scope.prepare("active", "request", 2400)).toBe(false);
    active = { surfaceToken: "replacement", src };
    expect(scope.prepare("active", "request", 2400)).toBe(false);
    active = { surfaceToken: "active", src };
    frame.setAttribute("src", src + "?other");
    expect(scope.prepare("active", "request", 2400)).toBe(false);
    frame.setAttribute("src", src);
    frame.after(frame.cloneNode());
    expect(scope.prepare("active", "request", 2400)).toBe(false);
    frame.nextSibling!.parentNode!.removeChild(frame.nextSibling!);
    vi.mocked(frame.getBoundingClientRect).mockReturnValue({ width: 0, height: 0 } as DOMRect);
    expect(scope.prepare("active", "request", 2400)).toBe(false);
    scope.dispose();
  });
});
