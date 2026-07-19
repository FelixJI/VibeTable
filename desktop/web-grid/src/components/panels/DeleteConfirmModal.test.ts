import { describe, it, expect, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import DeleteConfirmModal from "./DeleteConfirmModal.vue";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";

/**
 * DeleteConfirmModal is pure-presentation. It reads `uiStore.deleteModalOpen`
 * and `uiStore.deleteTarget`, and emits confirm/cancel. These tests verify the
 * target name is shown and the buttons emit the right events.
 *
 * NModal teleports to document.body when show=true; we query document.body for
 * both text and click targets.
 */
function bodyText(): string {
  return document.body.textContent ?? "";
}

function bodyEl(testId: string): HTMLElement {
  const el = document.body.querySelector(`[data-testid="${testId}"]`);
  if (!el) throw new Error(`no element for ${testId}`);
  return el as HTMLElement;
}

function mountModal() {
  return mount(DeleteConfirmModal, { attachTo: document.body });
}

describe("DeleteConfirmModal", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setActivePinia(createPinia());
  });

  it("does not render the body when deleteModalOpen is false", async () => {
    const ui = useUiStore();
    ui.closeDelete();
    mountModal();
    await flushPromises();
    expect(bodyText()).not.toContain("删除");
  });

  it("renders the delete-target name in the confirmation message", async () => {
    const ui = useUiStore();
    ui.openDelete("orders");
    mountModal();
    await flushPromises();
    const text = bodyText();
    expect(text).toContain("orders");
    expect(text).toContain("删除");
  });

  it("emits confirm when the confirm button is clicked (no service call)", async () => {
    const ui = useUiStore();
    ui.openDelete("orders");
    const wrapper = mountModal();
    await flushPromises();
    await bodyEl("delete-confirm-ok").click();
    await flushPromises();
    expect(wrapper.emitted("confirm")).toBeTruthy();
    // The modal itself does NOT close the uiStore; the container does.
    expect(ui.deleteModalOpen).toBe(true);
  });

  it("emits cancel when the cancel button is clicked", async () => {
    const ui = useUiStore();
    ui.openDelete("orders");
    const wrapper = mountModal();
    await flushPromises();
    await bodyEl("delete-confirm-cancel").click();
    await flushPromises();
    expect(wrapper.emitted("cancel")).toBeTruthy();
    expect(ui.deleteModalOpen).toBe(true);
  });

  it("shows no target name when deleteTarget is null", async () => {
    const ui = useUiStore();
    // Manually flip the modal open without setting a target (shouldn't normally
    // happen via openDelete, but the modal should not crash).
    ui.deleteModalOpen = true;
    ui.deleteTarget = null;
    mountModal();
    await flushPromises();
    // The title still renders (zh-CN: 确认删除) even without a target name.
    expect(bodyText()).toContain("删除");
  });

  it("renders a delete failure so the user can retry", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    ui.openDelete("orders");
    admin.requestDelete("orders");
    admin.fail("该表受保护，无法删除");
    mountModal();
    await flushPromises();

    expect(bodyEl("delete-error").textContent).toContain("无法删除");
  });
});
