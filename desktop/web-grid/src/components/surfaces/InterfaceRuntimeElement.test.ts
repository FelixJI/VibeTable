import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";

import type { InterfaceElement } from "@/contracts/generated/workbench";
import InterfaceRuntimeElement from "./InterfaceRuntimeElement.vue";

const element = (
  kind: InterfaceElement["kind"],
  patch: Partial<InterfaceElement> = {},
): InterfaceElement => ({
  elementId: `${kind}-1`,
  kind,
  bindingId: null,
  actionId: null,
  text: null,
  width: "full",
  children: [],
  ...patch,
});

const data = {
  orders: {
    state: "ready" as const,
    rows: [
      { rowKey: "r1", name: "North", amount: 72, nested: { ok: true }, empty: null },
      { rowKey: "r2", name: "South", amount: -14, nested: {}, empty: "" },
    ],
    offset: 0,
    filteredRows: 2,
    error: null,
  },
};

function mountElement(target: InterfaceElement, patch: Record<string, unknown> = {}) {
  return mount(InterfaceRuntimeElement, {
    props: {
      element: target,
      data,
      selected: {},
      forms: {},
      bindingFields: { orders: ["name", "amount"] },
      actionKinds: { create: "record.create", update: "record.update" },
      ...patch,
    },
  });
}

describe("InterfaceRuntimeElement", () => {
  it("renders and forwards recursive structural elements", async () => {
    const child = element("button", { actionId: "run", text: "执行" });
    const wrapper = mountElement(element("section", {
      text: "分区",
      children: [element("columns", { children: [element("tabs", { children: [child] })] })],
    }));
    expect(wrapper.text()).toContain("分区");
    await wrapper.get("button").trigger("click");
    expect(wrapper.emitted("action")?.[0]).toEqual(["run", null, "button-1"]);
  });

  it("renders text, metric and bounded chart values", () => {
    expect(mountElement(element("text", { text: "说明文字" })).text()).toContain("说明文字");
    expect(mountElement(element("metric", { bindingId: "orders", text: "收入" })).text()).toContain("North");
    const chart = mountElement(element("chart", { bindingId: "orders", text: "区域" }));
    expect(chart.findAll(".chart-row")).toHaveLength(2);
    expect(chart.findAll(".chart-row i")[0]!.attributes("style")).toContain("72%");
    expect(chart.findAll(".chart-row i")[1]!.attributes("style")).toContain("14%");
    expect(mountElement(element("chart", { bindingId: "missing" })).text()).toContain("暂无可视化数据");
  });

  it("shows loading and stable binding failure states", () => {
    const loading = mountElement(element("metric", { bindingId: "orders" }), {
      data: {
        orders: { state: "loading", rows: [], offset: 0, filteredRows: 0, error: null },
      },
    });
    expect(loading.text()).toContain("正在加载数据");
    const failed = mountElement(element("metric", { bindingId: "orders" }), {
      data: {
        orders: {
          state: "failed",
          rows: [],
          offset: 0,
          filteredRows: 0,
          error: "query.offline",
        },
      },
    });
    expect(failed.get('[role="alert"]').text()).toContain("query.offline");
  });

  it("renders list/detail, selects rows, and formats objects and empty values", async () => {
    const list = mountElement(element("record-list", { bindingId: "orders", text: "订单" }));
    expect(list.findAll('[role="row"]')).toHaveLength(3);
    expect(list.text()).not.toContain('{"ok":true}');
    expect(list.text()).not.toContain("empty");
    await list.findAll("button.record-row")[1]!.trigger("click");
    expect(list.emitted("select")?.[0]?.[0]).toBe("orders");
    expect(list.emitted("select")?.[0]?.[1]).toMatchObject({ rowKey: "r2" });

    const detail = mountElement(element("record-detail", { bindingId: "orders" }), {
      selected: { orders: data.orders.rows[1] },
    });
    expect(detail.text()).toContain("South");
    expect(mountElement(element("record-detail", { bindingId: "missing" })).text())
      .toContain("选择一条记录查看详情");
    expect(mountElement(element("record-list", { bindingId: "missing" })).text())
      .toContain("暂无记录");
  });

  it("edits and submits forms and emits button/navigation actions", async () => {
    const form = mountElement(element("form", {
      bindingId: "orders",
      actionId: "update",
      text: "编辑订单",
    }));
    expect(form.findAll("input")).toHaveLength(2);
    expect((form.findAll("input")[0]!.element as HTMLInputElement).value).toBe("North");
    await form.findAll("input")[0]!.setValue("Changed");
    expect(form.emitted("form")?.[0]).toEqual(["form-1", "name", "Changed"]);
    await form.get("form").trigger("submit");
    expect(form.emitted("action")?.[0]).toEqual(["update", "orders", "form-1"]);

    const create = mountElement(element("form", {
      bindingId: "orders",
      actionId: "create",
      text: "新建订单",
    }));
    expect((create.findAll("input")[0]!.element as HTMLInputElement).value).toBe("");

    for (const kind of ["button", "navigation"] as const) {
      const action = mountElement(element(kind, { actionId: `action-${kind}` }));
      await action.get("button").trigger("click");
      expect(action.emitted("action")?.[0]?.[0]).toBe(`action-${kind}`);
    }
    expect(mountElement(element("button")).get("button").attributes("disabled")).toBeDefined();
  });
});
