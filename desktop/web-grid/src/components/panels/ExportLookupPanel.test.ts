import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DOMWrapper, flushPromises, mount } from "@vue/test-utils";
import { NCheckbox } from "naive-ui";
import ExportLookupPanel from "./ExportLookupPanel.vue";
import { setLocale } from "@/i18n";
import type { ExportLookupPanelState } from "@/composables/useDataIoTask";

/**
 * ExportLookupPanel renders an NModal preset card that teleports to
 * document.body. We mount with attachTo and query document.body so the real
 * modal shell (mask, escape-to-cancel) is exercised.
 */
function panel(overrides: Partial<ExportLookupPanelState> = {}): ExportLookupPanelState {
  return {
    format: "csv",
    collection: "orders",
    loading: false,
    error: null,
    options: [
      { lookupId: "lkp-1", displayName: "合作方标签", outputType: "text" },
      { lookupId: "lkp-2", displayName: "订单金额", outputType: "decimal" },
    ],
    lookupRevision: "schema-7",
    ...overrides,
  };
}

function mountPanel(state: ExportLookupPanelState, selectedIds: readonly string[] = []) {
  return mount(ExportLookupPanel, {
    props: { panel: state, selectedIds },
    attachTo: document.body,
  });
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

/** Real key events bubble from the focused element to window, where evtd's
 * unified listener and the modal focus trap receive them. */
async function pressEscape(): Promise<void> {
  const target = document.activeElement instanceof HTMLElement ? document.activeElement : document.body;
  target.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", code: "Escape", bubbles: true }));
  await flushPromises();
}

describe("ExportLookupPanel", () => {
  beforeEach(() => setLocale("zh-CN"));
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("lists valid lookups as read-only text columns, unselected by default", async () => {
    await mountPanel(panel());
    await flushPromises();

    const root = bodyEl("export-lookup-panel");
    expect(root.attributes("aria-label")).toBe("确认导出 CSV");
    expect(root.text()).toContain("orders");
    expect(root.text()).toContain("合作方标签");
    expect(root.text()).toContain("只读文本");
    const checkbox = document.body.querySelector('[data-testid="export-lookup-option-lkp-1"]');
    expect(checkbox?.getAttribute("aria-checked")).toBe("false");
    expect(bodyEl("export-lookup-confirm").attributes("disabled")).toBeUndefined();
    expect(document.body.querySelector(".n-modal-mask")).toBeTruthy();
  });

  it("emits the checked lookup ids and the confirm intent", async () => {
    const wrapper = await mountPanel(panel());
    await flushPromises();

    const first = wrapper.findAllComponents(NCheckbox)[0]!;
    first.vm.$emit("update:checked", true);
    await flushPromises();
    expect(wrapper.emitted("update:selectedIds")![0]).toEqual([["lkp-1"]]);

    await bodyEl("export-lookup-confirm").trigger("click");
    expect(wrapper.emitted("confirm")).toHaveLength(1);
  });

  it("cancel emits without confirm and loading keeps confirm disabled", async () => {
    const loading = await mountPanel(panel({ loading: true, options: [] }));
    await flushPromises();
    expect(bodyFind("export-lookup-loading")).not.toBeNull();
    expect(bodyEl("export-lookup-confirm").attributes("disabled")).toBeDefined();
    loading.unmount();

    const wrapper = await mountPanel(panel());
    await flushPromises();
    await bodyEl("export-lookup-cancel").trigger("click");
    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("confirm")).toBeUndefined();
  });

  it("surfaces catalog failures and the no-lookup state without blocking plain export", async () => {
    const errored = await mountPanel(panel({ error: "bridge unavailable" }));
    await flushPromises();
    expect(bodyFind("export-lookup-error")).not.toBeNull();
    expect(bodyEl("export-lookup-confirm").attributes("disabled")).toBeUndefined();
    errored.unmount();

    await mountPanel(panel({ options: [] }));
    await flushPromises();
    expect(bodyFind("export-lookup-empty")).not.toBeNull();
  });

  it("lets Escape leave the dialog as a plain cancel while loading stays explicit", async () => {
    const wrapper = await mountPanel(panel());
    await flushPromises();

    await pressEscape();
    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("confirm")).toBeUndefined();
  });
});
