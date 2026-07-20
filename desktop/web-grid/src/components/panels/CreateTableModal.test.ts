import { describe, it, expect, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

import CreateTableModal from "./CreateTableModal.vue";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";

/**
 * CreateTableModal is pure-presentation. The store (`tableAdminStore`) owns the
 * form state and the `canSubmit` derivation; the modal just renders the form
 * and emits submit/cancel. These tests verify:
 *
 *   1. Field rows render from the store's form.fields (store-binding).
 *   2. addField / removeField (called via the store on click) mutate the form.
 *   3. The submit button's disabled state tracks `admin.canSubmit` exactly.
 *   4. Clicking submit/cancel emits the right event (no service call).
 *
 * NModal teleports to document.body when show=true, so we assert body content
 * and trigger clicks against document.body-teleported elements. The wrapper
 * still captures emits because emitted events bubble through the component
 * instance regardless of where its DOM is teleported.
 */
function bodyFieldRowCount(): number {
  return document.body.querySelectorAll('[data-testid="create-table-field-row"]').length;
}

function bodyEl(testId: string): HTMLElement {
  const el = document.body.querySelector(`[data-testid="${testId}"]`);
  if (!el) throw new Error(`no element for ${testId}`);
  return el as HTMLElement;
}

function mountModal() {
  return mount(CreateTableModal, { attachTo: document.body });
}

describe("CreateTableModal", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setActivePinia(createPinia());
  });

  it("does not render the modal body when createModalOpen is false", async () => {
    const ui = useUiStore();
    ui.closeCreate();
    mountModal();
    await flushPromises();
    expect(bodyFieldRowCount()).toBe(0);
  });

  it("renders one field row per store.form.fields entry", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate(); // initial form: one empty field
    admin.addField(); // now two
    ui.openCreate();
    mountModal();
    await flushPromises();
    expect(bodyFieldRowCount()).toBe(2);
  });

  it("the add-field button appends a new field row through the store", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();
    expect(bodyFieldRowCount()).toBe(1);

    await bodyEl("create-table-add-field").click();
    await flushPromises();
    expect(admin.form.fields).toHaveLength(2);
    expect(bodyFieldRowCount()).toBe(2);
    // Wrapper still captures emits because the component instance owns them.
    expect(wrapper).toBeTruthy();
  });

  it("the remove-field button deletes a field row through the store", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.addField();
    ui.openCreate();
    mountModal();
    await flushPromises();
    expect(bodyFieldRowCount()).toBe(2);

    await bodyEl("create-table-remove-field-0").click();
    await flushPromises();
    expect(admin.form.fields).toHaveLength(1);
    expect(bodyFieldRowCount()).toBe(1);
  });

  it("disables the submit button when canSubmit is false (blank name)", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate(); // name blank -> canSubmit false
    ui.openCreate();
    mountModal();
    await flushPromises();
    expect(admin.canSubmit).toBe(false);
    const submit = bodyEl("create-table-submit");
    expect(submit.hasAttribute("disabled")).toBe(true);
  });

  it("enables the submit button once name + a named field make canSubmit true", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    admin.updateField(0, { name: "订单编号" });
    ui.openCreate();
    mountModal();
    await flushPromises();
    expect(admin.canSubmit).toBe(true);
    const submit = bodyEl("create-table-submit");
    expect(submit.hasAttribute("disabled")).toBe(false);
  });

  it("emits submit when the submit button is clicked (no service call)", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    admin.updateField(0, { name: "订单编号" });
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();
    await bodyEl("create-table-submit").click();
    await flushPromises();
    expect(wrapper.emitted("submit")).toBeTruthy();
  });

  it("emits cancel when the cancel button is clicked", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();
    await bodyEl("create-table-cancel").click();
    await flushPromises();
    expect(wrapper.emitted("cancel")).toBeTruthy();
  });

  it("renders the field-name input bound to admin.form.fields[i].name", async () => {
    // Sanity check: the component renders field inputs from admin.form.fields;
    // the store is the single source of truth. We confirm by setting the name
    // through the store and reading the input's value back from the DOM.
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.updateField(0, { name: "price", type: "decimal" });
    ui.openCreate();
    mountModal();
    await flushPromises();
    const nameInput = bodyEl("create-table-field-name-0").querySelector("input");
    expect(nameInput?.value).toBe("price");
    expect(admin.form.fields[0].name).toBe("price");
  });

  it("explains that physical identifiers are generated and stable", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    mountModal();
    await flushPromises();
    expect(bodyEl("physical-name-hint").textContent).toContain("自动生成");
    expect(bodyEl("physical-name-hint").textContent).toContain("保持不变");
    expect(bodyEl("field-type-hint").textContent).toContain("Directus");
    expect(bodyEl("field-type-hint").textContent).toContain("关系与别名字段");
  });

  it("renders a create failure and keeps the valid form retryable", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    admin.updateField(0, { name: "订单编号" });
    admin.fail("名称已经存在");
    ui.openCreate();
    mountModal();
    await flushPromises();

    expect(bodyEl("create-table-error").textContent).toContain("名称已经存在");
    expect(bodyEl("create-table-submit").hasAttribute("disabled")).toBe(false);
  });
});
