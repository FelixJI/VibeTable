import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import PastePanel from "./PastePanel.vue";
import { usePasteStore } from "@/stores/pasteStore";
import { useUiStore } from "@/stores/uiStore";
import type { PastePlan } from "@/contracts";

/**
 * PastePanel is pure-presentation. It reads `pasteStore` (phase, plan,
 * summaryText, acked) and `uiStore.pastePanelOpen`, and emits confirm/cancel.
 *
 * The panel is wrapped in a plain NCard (not NModal), so its DOM stays inline
 * (no teleport). We can assert against the wrapper directly.
 */
function mountPanel() {
  return mount(PastePanel);
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

  it("does not render when ui.pastePanelOpen is false", () => {
    const ui = useUiStore();
    ui.closePastePanel();
    const wrapper = mountPanel();
    expect(wrapper.find('[data-testid="paste-panel"]').exists()).toBe(false);
  });

  it("renders the preview title when phase is previewing", () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = mountPanel();
    expect(wrapper.text()).toContain("粘贴预览");
  });

  it("renders the result title when phase is applied", () => {
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
    const wrapper = mountPanel();
    expect(wrapper.text()).toContain("粘贴结果");
  });

  it("renders the overflow hint when phase is overflow", () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan({ overflow: true }));
    ui.openPastePanel();
    const wrapper = mountPanel();
    expect(wrapper.find('[data-testid="paste-overflow"]').exists()).toBe(true);
  });

  it("disables the confirm button when acked is false in previewing phase", () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = mountPanel();
    const confirm = wrapper.find('[data-testid="paste-confirm"]');
    expect(confirm.attributes("disabled")).toBeDefined();
  });

  it("emits confirm when the confirm button is clicked and acked is true", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    paste.toggleAck();
    ui.openPastePanel();
    const wrapper = mountPanel();
    await wrapper.find('[data-testid="paste-confirm"]').trigger("click");
    expect(wrapper.emitted("confirm")).toBeTruthy();
  });

  it("emits cancel when the close button is clicked", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = mountPanel();
    await wrapper.find('[data-testid="paste-close"]').trigger("click");
    expect(wrapper.emitted("cancel")).toBeTruthy();
  });

  it("emits cancel when the cancel button is clicked", async () => {
    const ui = useUiStore();
    const paste = usePasteStore();
    paste.setPlan(makePlan());
    ui.openPastePanel();
    const wrapper = mountPanel();
    await wrapper.find('[data-testid="paste-cancel"]').trigger("click");
    expect(wrapper.emitted("cancel")).toBeTruthy();
  });
});
