import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { DOMWrapper, flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import PastePanel from "./PastePanel.vue";
import { usePasteStore } from "@/stores/pasteStore";
import { useUiStore } from "@/stores/uiStore";
import type { PastePlan } from "@/contracts";

/**
 * PastePanel is pure-presentation. It reads `pasteStore` (phase, plan,
 * summaryText, acked) and `uiStore.pastePanelOpen`, and emits confirm/cancel.
 *
 * The card is wrapped in a standard NModal shell (mask + focus trap) that
 * teleports to document.body, so we mount with attachTo and assert against
 * document.body. Escape/overlay dismissal routes through requestCancel and
 * stays locked while an apply awaits the authoritative host receipt.
 */
function mountPanel() {
  return mount(PastePanel, { attachTo: document.body });
}

function bodyEl(testId: string): DOMWrapper<Element> {
  const el = document.body.querySelector(`[data-testid="${testId}"]`);
  if (!el) throw new Error(`missing ${testId}`);
  return new DOMWrapper(el);
}

function bodyFind(testId: string): DOMWrapper<Element> | null {
  const el = document.body.querySelector(`[data-testid="${testId}"]`);
  return el ? new DOMWrapper(el) : null;
}

function bodyText(): string {
  return document.body.textContent ?? "";
}

/**
 * VTU's transition stub keeps a closed modal card as display:none instead of
 * unmounting it, so "closed" means absent or visually hidden.
 */
function panelHidden(): boolean {
  const el = document.body.querySelector<HTMLElement>('[data-testid="paste-panel"]');
  return el === null || el.style.display === "none";
}

/** Real key events bubble from the focused element to window, where evtd's
 * unified listener and the modal focus trap receive them. */
async function pressEscape(): Promise<void> {
  const target = document.activeElement instanceof HTMLElement ? document.activeElement : document.body;
  target.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", code: "Escape", bubbles: true }));
  await flushPromises();
}

async function openPanel() {
  const wrapper = mountPanel();
  await flushPromises();
  return wrapper;
}

/** Build a minimal plan to seed pasteStore.setPlan for tests. */
function makePlan(overrides: Partial<PastePlan> = {}): PastePlan {
  return {
    summary: {
      updateRows: 0,
      insertRows: 0,
      skipRows: 0,
      errorCount: 0,
      warningCount: 0,
    },
    rows: [],
    diagnostics: [],
    token: { token: "tok-1" },
    overflow: false,
    ...overrides,
  } as unknown as PastePlan;
}

describe("PastePanel", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("does not render when ui.pastePanelOpen is false", async () => {
    const ui = useUiStore();
    ui.closePastePanel();
    await openPanel();
    expect(bodyFind("paste-panel")).toBeNull();
  });

  it("renders the preview title when phase is previewing", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    await openPanel();
    expect(bodyText()).toContain("粘贴预览");
  });

  it("renders the result title when phase is applied", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    paste.beginApply();
    paste.setResult({
      createdRowKeys: ["r1"],
      updatedRowKeys: [],
      skippedRowKeys: [],
    } as never);
    ui.openPastePanel();
    await openPanel();
    expect(bodyText()).toContain("粘贴结果");
    expect(bodyEl("paste-panel").attributes("data-phase")).toBe("applied");
  });

  it.each([
    "粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。",
    "HostBridge timeout: 粘贴结果尚未确认，请刷新并核实权威数据；不要直接重试。",
  ])("makes the unknown paste result visible and accessible: %s", async (message) => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setError(message);
    ui.openPastePanel();
    const wrapper = await openPanel();
    const error = bodyEl("paste-error");
    expect(error.isVisible()).toBe(true);
    expect(error.text()).toBe(message);
    expect(bodyText()).toContain("粘贴出错");
    expect(bodyText()).not.toContain("未保存");
    expect(bodyEl("paste-confirm").attributes("disabled")).toBeDefined();
    expect(wrapper.emitted("confirm")).toBeUndefined();
  });

  it("renders the overflow hint when phase is overflow", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan({ overflow: true }));
    ui.openPastePanel();
    await openPanel();
    expect(bodyFind("paste-overflow")).not.toBeNull();
  });

  it("allows a clean preview to be confirmed without an acknowledgement", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    await openPanel();
    expect(bodyEl("paste-confirm").attributes("disabled")).toBeUndefined();
  });

  it("requires acknowledgement when the preview contains a warning", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan({
      summary: { updateRows: 1, insertRows: 0, skipRows: 0, errorCount: 0, warningCount: 1 },
      diagnostics: [{ rowIndex: 0, columnIndex: 0, severity: "warning", code: "check", message: "check" }],
    }));
    ui.openPastePanel();
    const wrapper = await openPanel();
    expect(bodyEl("paste-confirm").attributes("disabled")).toBeDefined();
    await bodyEl("paste-ack").trigger("click");
    await flushPromises();
    expect(bodyEl("paste-confirm").attributes("disabled")).toBeUndefined();
    await bodyEl("paste-confirm").trigger("click");
    expect(wrapper.emitted("confirm")).toBeTruthy();
  });

  it("never allows a preview containing an error to be confirmed", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan({
      summary: { updateRows: 0, insertRows: 0, skipRows: 1, errorCount: 1, warningCount: 1 },
      diagnostics: [
        { rowIndex: 0, columnIndex: 0, severity: "warning", code: "check", message: "check" },
        { rowIndex: 0, columnIndex: 1, severity: "error", code: "invalid", message: "invalid" },
      ],
    }));
    paste.toggleAck();
    ui.openPastePanel();
    await openPanel();
    expect(bodyEl("paste-confirm").attributes("disabled")).toBeDefined();
  });

  it("emits cancel when the close button is clicked", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = await openPanel();
    expect(bodyEl("paste-close").attributes("aria-label")).toBe("取消");
    expect(bodyEl("paste-close").find("svg").exists()).toBe(true);
    await bodyEl("paste-close").trigger("click");
    await flushPromises();
    expect(wrapper.emitted("cancel")).toBeTruthy();
    expect(panelHidden()).toBe(true);
  });

  it("emits cancel when the cancel button is clicked", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = await openPanel();
    await bodyEl("paste-cancel").trigger("click");
    await flushPromises();
    expect(wrapper.emitted("cancel")).toBeTruthy();
    expect(panelHidden()).toBe(true);
  });

  it("does not dismiss an apply that is awaiting the authoritative host receipt", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    paste.beginApply();
    ui.openPastePanel();
    const wrapper = await openPanel();

    expect(bodyEl("paste-panel").attributes("data-phase")).toBe("applying");
    expect(bodyEl("paste-close").attributes("disabled")).toBeDefined();
    expect(bodyEl("paste-cancel").attributes("disabled")).toBeDefined();

    await bodyEl("paste-close").trigger("click");
    await bodyEl("paste-cancel").trigger("click");

    expect(wrapper.emitted("cancel")).toBeUndefined();
    expect(ui.pastePanelOpen).toBe(true);
  });

  it("renders through the standard modal shell with mask and named dialog", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    await openPanel();

    const panel = bodyEl("paste-panel");
    expect(panel.attributes("aria-label")).toBe("粘贴预览");
    expect(panel.classes()).toContain("paste-panel");
    // The card keeps its size constraint while the modal provides the overlay.
    expect(document.body.querySelector(".n-modal-mask")).toBeTruthy();
  });

  it("dismisses through Escape when the phase allows it", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = await openPanel();

    await pressEscape();

    expect(wrapper.emitted("cancel")).toBeTruthy();
    expect(ui.pastePanelOpen).toBe(false);
    expect(panelHidden()).toBe(true);
  });

  it("never dismisses through the modal shell while awaiting the host receipt", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    paste.beginApply();
    ui.openPastePanel();
    const wrapper = await openPanel();

    // Escape and mask presses must not close the shell in the applying phase.
    await pressEscape();
    const mask = document.body.querySelector(".n-modal-mask");
    mask?.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    mask?.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    await flushPromises();

    expect(wrapper.emitted("cancel")).toBeUndefined();
    expect(ui.pastePanelOpen).toBe(true);
    expect(panelHidden()).toBe(false);
  });
});
