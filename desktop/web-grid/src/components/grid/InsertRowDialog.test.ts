import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { NInputNumber } from "naive-ui";
import InsertRowDialog from "./InsertRowDialog.vue";
import type { ColumnEditSchema, ColumnSchema } from "@/contracts";

const displayColumns: readonly ColumnSchema[] = [
  { name: "title", title: "标题", dataType: "text", editable: true, nullable: false },
  { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: false },
  { name: "due", title: "到期日", dataType: "date", editable: true, nullable: false },
  { name: "kind", title: "类型", dataType: "text", editable: true, nullable: false },
];

function column(
  name: string,
  editor: ColumnEditSchema["editor"],
  validation: ColumnEditSchema["validation"] = [],
): ColumnEditSchema {
  return {
    name, storageName: name, dataType: "text", editable: true,
    nullable: false, primaryKey: false, editor, validation,
  };
}

// nullable=false 即必填；不依赖宿主是否附带 required 规则。
const columns: readonly ColumnEditSchema[] = [
  column("title", { kind: "text", multiline: false }),
  column("amount", { kind: "number", storage: "decimal", scale: 2 }),
  column("due", { kind: "date", dateType: "date" }),
  column("when", { kind: "date", dateType: "datetime" }),
  column("kind", { kind: "single_select", options: ["标准", "加急"] }),
];

const field = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const input = (id: string) => document.querySelector<HTMLInputElement>(`[data-testid="${id}"] input`);

describe("InsertRowDialog", () => {
  const mounted: VueWrapper[] = [];
  // 与真实父组件一致：卸载组件（回收 teleport）并清空 body，隔离每个用例。
  afterEach(() => {
    for (const wrapper of mounted.splice(0)) wrapper.unmount();
    document.body.innerHTML = "";
  });

  function mountDialog(overrides: Record<string, unknown> = {}) {
    const wrapper = mount(InsertRowDialog, {
      attachTo: document.body,
      props: {
        show: true,
        columns,
        displayColumns,
        busy: false,
        disabled: false,
        error: null,
        ...overrides,
      } as never,
    });
    mounted.push(wrapper);
    return wrapper;
  }

  it("renders nothing without required columns", () => {
    mountDialog({ columns: [] });
    expect(field("insert-row-dialog")).toBeNull();
  });

  it("blocks submit until every required value is valid and parses values by editor", async () => {
    const wrapper = mountDialog({ columns: columns.slice(0, 4) });
    await new Promise(resolve => setTimeout(resolve));

    expect(field("insert-row-submit")!.getAttribute("disabled")).toBeDefined();
    expect(field("insert-row-issue-title")!.textContent).toBe("必填");

    const title = input("insert-row-input-title")!;
    title.value = "首笔订单";
    title.dispatchEvent(new Event("input", { bubbles: true }));
    wrapper.findAllComponents(NInputNumber)
      .filter(component => component.attributes("data-testid") === "insert-row-input-amount")
      .forEach(component => component.vm.$emit("update:value", 12.5));
    const due = field("insert-row-input-due") as HTMLInputElement;
    due.value = "2026-07-01";
    due.dispatchEvent(new Event("input", { bubbles: true }));
    const when = field("insert-row-input-when") as HTMLInputElement;
    when.value = "2026-07-01T09:30";
    when.dispatchEvent(new Event("input", { bubbles: true }));
    await new Promise(resolve => setTimeout(resolve));

    expect(field("insert-row-issue-title")).toBeNull();
    const remaining = [...document.querySelectorAll('[data-testid^="insert-row-issue-"]')]
      .map(node => node.getAttribute("data-testid") + "=" + node.textContent);
    expect(remaining).toEqual([]);
    expect(field("insert-row-submit")!.getAttribute("disabled")).toBeNull();

    field("insert-row-submit")!.click();
    await new Promise(resolve => setTimeout(resolve));
    const values = wrapper.emitted("submit")?.at(-1)?.[0] as Record<string, unknown>;
    expect(values.title).toBe("首笔订单");
    expect(values.amount).toBe(12.5);
    expect(values.due).toBe("2026-07-01");
    // datetime 以权威 UTC RFC3339 提交，保留用户选择的非午夜时间，不要求手输时区。
    expect(typeof values.when).toBe("string");
    expect(values.when).toBe(new Date("2026-07-01T09:30").toISOString());
    expect(String(values.when)).not.toMatch(/T00:00:00/u);
  });

  it("keeps inputs and shows the error; busy blocks submit; close emits", async () => {
    const wrapper = mountDialog({
      columns: columns.slice(0, 1),
      busy: true,
      error: "写入被拒绝：并发修改",
    });
    await new Promise(resolve => setTimeout(resolve));

    const title = input("insert-row-input-title")!;
    title.value = "保留输入";
    title.dispatchEvent(new Event("input", { bubbles: true }));
    await new Promise(resolve => setTimeout(resolve));
    expect(field("insert-row-error")!.textContent).toContain("并发修改");
    expect(title.value).toContain("保留输入");
    expect(field("insert-row-submit")!.getAttribute("disabled")).toBeDefined();
    field("insert-row-submit")!.click();
    expect(wrapper.emitted("submit")).toBeUndefined();

    // busy 解除后取消可关闭。
    await wrapper.setProps({ busy: false });
    await new Promise(resolve => setTimeout(resolve));
    field("insert-row-cancel")!.click();
    expect(wrapper.emitted("close")).toHaveLength(1);
  });
});
