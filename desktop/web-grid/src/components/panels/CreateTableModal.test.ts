import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import CreateTableModal from "./CreateTableModal.vue";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";

describe("CreateTableModal", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("collects only the table display name and delegates fields to Schema v2", async () => {
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    const wrapper = mount(CreateTableModal, { attachTo: document.body });

    expect(wrapper.find('[data-testid="create-table-field-row"]').exists()).toBe(false);
    expect(document.body.textContent).toContain("Schema v2");
    expect(admin.canSubmit).toBe(false);

    admin.form.name = "订单";
    await wrapper.vm.$nextTick();
    expect(admin.canSubmit).toBe(true);
    document.querySelector<HTMLElement>('[data-testid="create-table-submit"]')
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("submit")).toHaveLength(1);
  });

  it("keeps host errors visible without exposing physical identifiers", () => {
    const admin = useTableAdminStore();
    const ui = useUiStore();
    admin.openCreate();
    ui.openCreate();
    admin.fail("创建数据表失败。");
    const wrapper = mount(CreateTableModal, { attachTo: document.body });

    expect(document.body.querySelector('[data-testid="create-table-error"]')?.textContent)
      .toContain("创建数据表失败");
    expect(wrapper.find('[data-testid="physical-name-hint"]').exists()).toBe(false);
  });
});
