import { describe, it, expect, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { NSelect } from "naive-ui";

import CreateTableModal from "./CreateTableModal.vue";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { setLocale } from "@/i18n";

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
    setLocale("zh-CN");
    useTableAdminStore().setAutoDateProducerEnabled(true);
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

  it("defaults both immutable system time presets on and lets either be disabled", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();

    expect(admin.form.includeCreatedAt).toBe(true);
    expect(admin.form.includeUpdatedAt).toBe(true);
    const options = wrapper.findAllComponents(NSelect)[0]!.props("options") as
      readonly { readonly value: string }[];
    expect(options.some(({ value }) => value === "autoDate")).toBe(false);

    await bodyEl("create-table-include-created-at").click();
    await flushPromises();
    expect(admin.form.includeCreatedAt).toBe(false);
    expect(admin.form.includeUpdatedAt).toBe(true);
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

  it("emits cancel when NModal requests close via update:show=false (X button / mask / ESC)", async () => {
    // Regression guard: the card-preset NModal closes by emitting
    // `update:show=false`. The component must translate that into its own
    // `cancel` event so WorkspaceView can run ui.closeCreate() + admin.close()
    // (otherwise the X button appears dead while the store stays open).
    // We drive the real DOM close button (aria-label="close") that naive-ui
    // renders in the card header — the same path the user clicks.
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();
    const closeBtn = document.body.querySelector<HTMLButtonElement>(
      '.n-card-header__close[aria-label="close"]',
    );
    if (!closeBtn) throw new Error("close button not rendered");
    await closeBtn.click();
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

  it("preserves an entered field name when the field type changes", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();

    const nameInput = bodyEl("create-table-field-name-0").querySelector("input");
    if (!nameInput) throw new Error("field-name input not rendered");
    nameInput.value = "author";
    nameInput.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();

    const originalClientId = admin.form.fields[0]!.clientId;
    admin.updateField(0, {
      defaultText: '"draft"',
      minLength: 3,
      nullable: false,
      unique: true,
    });
    wrapper.findAllComponents(NSelect)[0]!.vm.$emit("update:value", "relation");
    await flushPromises();

    expect(admin.form.fields[0]).toMatchObject({
      clientId: originalClientId,
      name: "author",
      type: "relation",
      defaultText: "",
      minLength: null,
      nullable: true,
      unique: false,
    });
    expect(nameInput.value).toBe("author");
  });

  it("updates field type labels immediately when the locale changes", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();
    const select = wrapper.findAllComponents(NSelect)[0]!;
    expect(select.props("options")).toContainEqual(
      expect.objectContaining({ value: "formula", label: "公式" }),
    );

    setLocale("en-US");
    await flushPromises();
    expect(select.props("options")).toContainEqual(
      expect.objectContaining({ value: "formula", label: "Formula" }),
    );
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
    expect(bodyEl("field-type-hint").textContent).toContain("产品字段");
    expect(bodyEl("field-type-hint").textContent).toContain("服务端校验");
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

  it("renders decimal precision constraints and an exact field-path error", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    admin.updateField(0, { name: "金额", type: "decimal", precision: 8, scale: 10 });
    admin.fail("小数位数不能大于总精度。", "fields[0].constraints.scale");
    ui.openCreate();
    mountModal();
    await flushPromises();

    expect(bodyEl("field-precision-0")).toBeTruthy();
    expect(bodyEl("field-scale-0")).toBeTruthy();
    expect(bodyEl("field-error-0").textContent).toContain("fields[0].constraints.scale");
  });

  it("renders JSON, formula, relation, lookup and attachment configuration by type", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.updateField(0, { name: "metadata", type: "json" });
    admin.addField("formula");
    admin.updateField(1, { name: "total" });
    admin.addField("relation");
    admin.updateField(2, { name: "customer" });
    admin.addField("lookup");
    admin.updateField(3, { name: "customer_name" });
    admin.addField("file");
    admin.updateField(4, { name: "receipt" });
    ui.openCreate();
    mountModal();
    await flushPromises();

    expect(bodyEl("field-json-schema-0")).toBeTruthy();
    expect(bodyEl("field-formula-source-1")).toBeTruthy();
    expect(bodyEl("field-relation-target-2")).toBeTruthy();
    expect(bodyEl("field-lookup-relation-3")).toBeTruthy();
    expect(bodyEl("field-attachment-max-files-4")).toBeTruthy();
    expect(bodyEl("field-attachment-thumbnails-4")).toBeTruthy();
  });

  it("lets the user configure a unique composite index through visible controls", async () => {
    const ui = useUiStore();
    const admin = useTableAdminStore();
    admin.openCreate();
    admin.form.name = "订单";
    admin.updateField(0, { name: "status" });
    admin.addField("dateTime");
    admin.updateField(1, { name: "created_at" });
    ui.openCreate();
    const wrapper = mountModal();
    await flushPromises();

    await bodyEl("create-table-add-index").click();
    await flushPromises();
    expect(bodyFieldRowCount()).toBe(2);
    expect(admin.form.indexes).toHaveLength(1);
    expect(admin.localIndexErrors).toContainEqual(
      expect.objectContaining({ path: "indexes[0].fieldIds" }),
    );
    expect(document.body.querySelectorAll('[data-testid="create-table-index-error-0"]'))
      .toHaveLength(2);

    const nameInput = bodyEl("create-table-index-name-0").querySelector("input");
    if (!nameInput) throw new Error("index-name input not rendered");
    nameInput.value = "uidx_status_created";
    nameInput.dispatchEvent(new Event("input", { bubbles: true }));

    const indexFields = wrapper.findAllComponents(NSelect).find((component) =>
      component.attributes()["data-testid"] === "create-table-index-fields-0");
    const indexType = wrapper.findAllComponents(NSelect).find((component) =>
      component.attributes()["data-testid"] === "create-table-index-type-0");
    if (!indexFields || !indexType) throw new Error("index controls not rendered");
    indexFields.vm.$emit("update:value", admin.form.fields.map((field) => field.clientId));
    indexType.vm.$emit("update:value", "unique");
    await flushPromises();

    expect(admin.form.indexes[0]).toMatchObject({
      name: "uidx_status_created",
      fieldClientIds: [
        admin.form.fields[0]!.clientId,
        admin.form.fields[1]!.clientId,
      ],
      unique: true,
    });
    expect(admin.canSubmit).toBe(true);
  });
});
