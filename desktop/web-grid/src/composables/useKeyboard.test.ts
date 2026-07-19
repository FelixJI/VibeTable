import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import { mount } from "@vue/test-utils";
import { useKeyboardStore } from "@/stores/keyboardStore";
import { useKeyboard, matchesShortcut } from "./useKeyboard";
import type { UseKeyboardOptions } from "./useKeyboard";

/**
 * The composable registers its keydown listener in onMounted/onBeforeUnmount.
 * Those lifecycle hooks only run inside a component instance, so we mount a
 * thin wrapper component that invokes useKeyboard during its setup().
 */
function mountKeyboard(opts: UseKeyboardOptions): ReturnType<typeof mount> {
  const Host = defineComponent({
    setup() {
      useKeyboard(opts);
      return () => h("div");
    },
  });
  return mount(Host);
}

const noopTabulator = { tabulator: { value: null } } as unknown as UseKeyboardOptions;

function fireKey(key: string, opts: KeyboardEventInit = {}): void {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...opts,
  });
  document.dispatchEvent(event);
}

describe("useKeyboard", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("fires copy action on Ctrl+C", () => {
    const kb = useKeyboardStore();
    const wrapper = mountKeyboard(noopTabulator);
    fireKey("c", { ctrlKey: true });
    expect(kb.lastFired).toBe("copy");
    wrapper.unmount();
  });

  it("fires help action on '?'", () => {
    const kb = useKeyboardStore();
    const wrapper = mountKeyboard(noopTabulator);
    fireKey("?");
    expect(kb.lastFired).toBe("help");
    wrapper.unmount();
  });

  it("does not fire when focus is in an input", () => {
    const kb = useKeyboardStore();
    const wrapper = mountKeyboard(noopTabulator);
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    fireKey("c", { ctrlKey: true });
    expect(kb.lastFired).toBeNull(); // copy is grid-scoped; suppressed in input
    document.body.removeChild(input);
    wrapper.unmount();
  });

  it("lets Escape bubble through even when focus is in an input", () => {
    const kb = useKeyboardStore();
    const wrapper = mountKeyboard(noopTabulator);
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    fireKey("Escape");
    expect(kb.lastFired).toBe("cancel");
    document.body.removeChild(input);
    wrapper.unmount();
  });

  it("removes the keydown listener on unmount", () => {
    const kb = useKeyboardStore();
    const wrapper = mountKeyboard(noopTabulator);
    wrapper.unmount();
    fireKey("c", { ctrlKey: true });
    expect(kb.lastFired).toBeNull();
  });

  it("invokes optional onCopy / onHelp callbacks", () => {
    const onCopy = vi.fn();
    const onHelp = vi.fn();
    const wrapper = mountKeyboard({ ...noopTabulator, onCopy, onHelp });
    fireKey("c", { ctrlKey: true });
    expect(onCopy).toHaveBeenCalledTimes(1);
    fireKey("?");
    expect(onHelp).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it("fires undo on Ctrl+Z and redo on Ctrl+Shift+Z", () => {
    const kb = useKeyboardStore();
    const wrapper = mountKeyboard(noopTabulator);
    fireKey("z", { ctrlKey: true });
    expect(kb.lastFired).toBe("undo");
    fireKey("z", { ctrlKey: true, shiftKey: true });
    expect(kb.lastFired).toBe("redo");
    wrapper.unmount();
  });

  it("invokes onUndo on Ctrl+Z and onRedo on Ctrl+Shift+Z / Ctrl+Y", () => {
    const onUndo = vi.fn();
    const onRedo = vi.fn();
    const wrapper = mountKeyboard({ ...noopTabulator, onUndo, onRedo });
    fireKey("z", { ctrlKey: true });
    expect(onUndo).toHaveBeenCalledTimes(1);
    expect(onRedo).not.toHaveBeenCalled();
    fireKey("z", { ctrlKey: true, shiftKey: true });
    expect(onRedo).toHaveBeenCalledTimes(1);
    // Ctrl+Y also routes to onRedo.
    fireKey("y", { ctrlKey: true });
    expect(onRedo).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("suppresses table shortcuts outside the visible table view", () => {
    const kb = useKeyboardStore();
    const onDelete = vi.fn();
    const onRefresh = vi.fn();
    const onHelp = vi.fn();
    const wrapper = mountKeyboard({
      isTableContext: () => false,
      onDelete,
      onRefresh,
      onHelp,
    });

    fireKey("Delete");
    fireKey("r", { ctrlKey: true });
    expect(onDelete).not.toHaveBeenCalled();
    expect(onRefresh).not.toHaveBeenCalled();
    expect(kb.lastFired).toBeNull();

    fireKey("?");
    expect(onHelp).toHaveBeenCalledTimes(1);
    expect(kb.lastFired).toBe("help");
    wrapper.unmount();
  });
});

describe("matchesShortcut", () => {
  const mk = (key: string, init: KeyboardEventInit = {}): KeyboardEvent =>
    new KeyboardEvent("keydown", { key, ...init });

  it("matches Ctrl+C when ctrl held and key is c", () => {
    expect(matchesShortcut(mk("c", { ctrlKey: true }), "Ctrl+C")).toBe(true);
  });

  it("does NOT match Ctrl+C when ctrl is not held (plain c)", () => {
    expect(matchesShortcut(mk("c"), "Ctrl+C")).toBe(false);
  });

  it("treats meta (cmd) as satisfying the Ctrl modifier", () => {
    expect(matchesShortcut(mk("c", { metaKey: true }), "Ctrl+C")).toBe(true);
  });

  it("matches Ctrl+Shift+Z when ctrl+shift held", () => {
    expect(
      matchesShortcut(mk("Z", { ctrlKey: true, shiftKey: true }), "Ctrl+Shift+Z"),
    ).toBe(true);
  });

  it("Ctrl+Z (no shift) does NOT match the Ctrl+Shift+Z pattern", () => {
    expect(matchesShortcut(mk("z", { ctrlKey: true }), "Ctrl+Shift+Z")).toBe(
      false,
    );
  });

  it("matches single-char '?' regardless of modifiers", () => {
    expect(matchesShortcut(mk("?"), "?")).toBe(true);
    expect(matchesShortcut(mk("?", { shiftKey: true }), "?")).toBe(true);
  });

  it("is case-insensitive on both the key and the pattern", () => {
    expect(matchesShortcut(mk("C", { ctrlKey: true }), "ctrl+c")).toBe(true);
    expect(matchesShortcut(mk("c", { ctrlKey: true }), "Ctrl+C")).toBe(true);
  });
});
