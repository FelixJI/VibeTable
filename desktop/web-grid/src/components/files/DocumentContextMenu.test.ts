import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DOMWrapper, flushPromises, mount } from "@vue/test-utils";
import DocumentContextMenu from "./DocumentContextMenu.vue";
import { setLocale } from "@/i18n";
import type { DocumentEntry } from "@/stores/documentWorkspaceStore";

/**
 * DocumentContextMenu renders NDropdown, which teleports the menu to
 * document.body. All queries go through document.body with real teleport
 * behavior (no stubs) so the shell integration is exercised as shipped.
 */
function entry(overrides: Partial<DocumentEntry> = {}): DocumentEntry {
  return {
    documentId: "11111111-1111-4111-8111-111111111111",
    entryHandle: "doc-1",
    displayName: "方案.docx",
    relativePath: "方案.docx",
    extension: "docx",
    authority: "workspace",
    availability: "available",
    mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    sizeBytes: 1,
    effectiveRevisionCreatedAt: "2026-08-12T00:00:00Z",
    formalVersion: 1,
    status: "active",
    capabilities: ["open", "preview", "reveal", "history", "unlink"],
    ...overrides,
  };
}

function mountMenu(target: DocumentEntry) {
  return mount(DocumentContextMenu, {
    props: { entry: target, x: 12, y: 34 },
    attachTo: document.body,
  });
}

function menu(): DOMWrapper<Element> {
  const el = document.body.querySelector('[data-testid="document-context-menu"]');
  if (!el) throw new Error("context menu not rendered");
  return new DOMWrapper(el);
}

function item(text: string): DOMWrapper<Element> {
  const found = menu().findAll('[role="menuitem"]')
    .find((candidate) => candidate.text().includes(text));
  if (!found) throw new Error(`missing context menu item: ${text}`);
  return found;
}

async function pressOutside(): Promise<void> {
  document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  document.body.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
  await flushPromises();
}

/**
 * Real key events originate at the focused element and bubble to window,
 * where evtd's unified listener (vooks keyboard / focus trap) receives them.
 * jsdom needs explicit key + code + bubbles to reproduce that path.
 */
async function pressEscape(): Promise<void> {
  const target = document.activeElement instanceof HTMLElement ? document.activeElement : document.body;
  target.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", code: "Escape", bubbles: true }));
  await flushPromises();
}

describe("DocumentContextMenu", () => {
  beforeEach(() => setLocale("zh-CN"));
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("renders only capability-gated actions, separators, and the danger unlink entry", async () => {
    const wrapper = mountMenu(entry());
    await flushPromises();

    expect(menu().attributes("role")).toBe("menu");
    expect(item("打开").exists()).toBe(true);
    expect(item("预览").exists()).toBe(true);
    expect(item("在资源管理器中显示").exists()).toBe(true);
    expect(item("版本历史").exists()).toBe(true);
    expect(menu().text()).not.toContain("重新定位");
    expect(document.body.querySelector(".n-dropdown-divider")).toBeTruthy();

    const unlink = item("移入工作区回收站");
    expect(unlink.attributes("data-testid")).toBe("document-unlink");
    expect(unlink.classes()).toContain("document-menu-danger");
    wrapper.unmount();
  });

  it("offers relink instead of reveal for a missing workspace document", async () => {
    const wrapper = mountMenu(entry({
      availability: "missing",
      capabilities: ["relink", "history"],
    }));
    await flushPromises();

    expect(menu().text()).toContain("重新定位");
    expect(menu().text()).not.toContain("在资源管理器中显示");
    await item("重新定位").trigger("click");

    expect(wrapper.emitted("action")).toHaveLength(1);
    expect(wrapper.emitted("action")![0]![0]).toBe("relink");
    wrapper.unmount();
  });

  it("emits action and close when an item is chosen", async () => {
    const wrapper = mountMenu(entry());
    await flushPromises();

    await item("打开").trigger("click");
    expect(wrapper.emitted("action")![0]![0]).toBe("open");
    expect(wrapper.emitted("close")).toHaveLength(1);
    wrapper.unmount();
  });

  it("closes on Escape through the dropdown keyboard handler", async () => {
    const wrapper = mountMenu(entry());
    await flushPromises();

    await pressEscape();
    expect(wrapper.emitted("close")).toHaveLength(1);
    expect(wrapper.emitted("action")).toBeUndefined();
    wrapper.unmount();
  });

  it("closes when a pointer press lands outside the menu", async () => {
    const wrapper = mountMenu(entry());
    await flushPromises();

    await pressOutside();
    expect(wrapper.emitted("close")).toHaveLength(1);
    expect(wrapper.emitted("action")).toBeUndefined();
    wrapper.unmount();
  });

  it("closes when the window loses focus", async () => {
    const wrapper = mountMenu(entry());
    await flushPromises();

    window.dispatchEvent(new Event("blur"));
    await flushPromises();
    expect(wrapper.emitted("close")).toHaveLength(1);
    wrapper.unmount();
  });
});
