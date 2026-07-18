import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import ShortcutsView from "./ShortcutsView.vue";
import { useUiStore } from "@/stores/uiStore";
import {
  SHORTCUTS,
  UNDO_LIMITATIONS_ZH,
  UNDO_LIMITATIONS_EN,
} from "@/keyboard/shortcuts";
import { setLocale, getLocale } from "@/i18n";

/**
 * ShortcutsView renders inside an NModal. By default NModal teleports its
 * content to `document.body` and uses `display-directive="if"` (content is not
 * mounted on first render when `show` is false). So assertions are made against
 * `document.body` (where the teleported modal lives), not the wrapper's root.
 *
 * Note: NModal keeps `displayed` true until its leave *transition* completes.
 * jsdom does not fire CSS transition-end events, so once a modal has been shown
 * its DOM lingers after `show` flips to false. We therefore test the closed
 * state from a fresh mount (deterministic) rather than after a close toggle.
 */
function bodyText(): string {
  return document.body.textContent ?? "";
}

function bodyShortcutRowCount(): number {
  return document.body.querySelectorAll(".shortcut-row").length;
}

function mountView() {
  return mount(ShortcutsView, { attachTo: document.body });
}

describe("ShortcutsView", () => {
  const prevLocale = getLocale();

  beforeEach(() => {
    document.body.innerHTML = "";
    setActivePinia(createPinia());
    // Default to zh-CN so the existing assertions (which expect Chinese) hold.
    setLocale("zh-CN");
  });

  // The i18n module holds locale in module state; restore so other test files
  // are not affected by a locale flip here.
  afterEach(() => setLocale(prevLocale));

  it("does not render the modal body when shortcutsOpen is false", async () => {
    const ui = useUiStore();
    ui.closeShortcuts();
    mountView();
    await flushPromises();
    // First-mount with show=false: NModal mounts nothing (displayDirective "if").
    expect(bodyShortcutRowCount()).toBe(0);
    expect(bodyText()).not.toContain(SHORTCUTS[0].descriptionZh);
    // The title is gated behind the modal's show flag.
    expect(bodyText()).not.toContain("快捷键");
  });

  it("renders the modal title and a sample shortcut description when open", async () => {
    const ui = useUiStore();
    ui.openShortcuts();
    mountView();
    await flushPromises();

    const text = bodyText();
    // Title (zh-CN: 快捷键)
    expect(text).toContain("快捷键");
    // First shortcut's Chinese description + key chip render.
    expect(text).toContain(SHORTCUTS[0].descriptionZh);
    expect(text).toContain(SHORTCUTS[0].keys);
  });

  it("renders one shortcut-row per SHORTCUTS entry, grouped under category headings", async () => {
    const ui = useUiStore();
    ui.openShortcuts();
    mountView();
    await flushPromises();

    // Exactly one .shortcut-row per entry in the single-source-of-truth array.
    expect(bodyShortcutRowCount()).toBe(SHORTCUTS.length);
    // Both grouping categories (general + navigation) are labeled via i18n.
    expect(bodyText()).toContain("通用");
    expect(bodyText()).toContain("网格导航");
  });

  it("renders the undo-limitation notes section", async () => {
    const ui = useUiStore();
    ui.openShortcuts();
    mountView();
    await flushPromises();

    const text = bodyText();
    // The notes-category heading (zh-CN: 说明) renders.
    expect(text).toContain("说明");
    // Each undo-limitation note is rendered.
    for (const note of UNDO_LIMITATIONS_ZH) {
      expect(text).toContain(note);
    }
  });

  it("reflects every SHORTCUTS entry (single source of truth)", async () => {
    const ui = useUiStore();
    ui.openShortcuts();
    mountView();
    await flushPromises();

    const text = bodyText();
    // Every registered shortcut (description + key) appears on the help page.
    expect(SHORTCUTS.length).toBeGreaterThan(0);
    for (const sc of SHORTCUTS) {
      expect(text).toContain(sc.descriptionZh);
      expect(text).toContain(sc.keys);
    }
  });

  it("closes the modal by emitting the update:show(false) path (uiStore flips)", async () => {
    // The component binds @update:show="(v) => !v && ui.closeShortcuts()".
    // Simulating the modal's own close (Esc / mask click) drives update:show(false).
    const ui = useUiStore();
    ui.openShortcuts();
    const wrapper = mountView();
    await flushPromises();
    expect(ui.shortcutsOpen).toBe(true);

    // Drive the same handler the NModal child invokes on close.
    // NModal's internal component name is "Modal".
    const modal = wrapper.findComponent({ name: "Modal" });
    expect(modal.exists()).toBe(true);
    modal.vm.$emit("update:show", false);
    await flushPromises();
    expect(ui.shortcutsOpen).toBe(false);
  });

  it("renders English descriptions + EN undo-limitations when locale is en-US", async () => {
    // Issue I4(b): ShortcutsView must be locale-aware, selecting descriptionEn
    // / UNDO_LIMITATIONS_EN (previously dead code) when the locale is en-US.
    setLocale("en-US");
    const ui = useUiStore();
    ui.openShortcuts();
    mountView();
    await flushPromises();

    const text = bodyText();
    // Title + first shortcut's English description render.
    expect(text).toContain("Keyboard shortcuts");
    expect(text).toContain(SHORTCUTS[0].descriptionEn);
    // The Chinese description must NOT be shown in en-US.
    expect(text).not.toContain(SHORTCUTS[0].descriptionZh);
    // Each English undo-limitation note renders.
    for (const note of UNDO_LIMITATIONS_EN) {
      expect(text).toContain(note);
    }
    // And the Chinese notes are absent.
    expect(text).not.toContain(UNDO_LIMITATIONS_ZH[0]);
  });
});
