import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import ViewGroupPanel from "./ViewGroupPanel.vue";

describe("ViewGroupPanel", () => {
  it("renders full-result counts and requests the next independent group page", async () => {
    const wrapper = mount(ViewGroupPanel, { props: {
      rows: [{ key: ["east"], count: 7000, summaries: [12345] }],
      groups: [{ field: "region" }],
      summaries: [{ field: "amount", function: "sum" }],
      columns: [
        { name: "region", title: "区域", dataType: "text", editable: true, nullable: true },
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
      ],
      hasMore: true,
    } });

    expect(wrapper.text()).toContain("区域: east");
    expect(wrapper.text()).toContain("7000");
    expect(wrapper.text()).toContain("金额 合计: 12345");
    await wrapper.get('[data-testid="view-group-more"]').trigger("click");
    expect(wrapper.emitted("more")).toHaveLength(1);
  });

  it("renders a collapsible two-level tree and emits a stable persisted key", async () => {
    const wrapper = mount(ViewGroupPanel, { props: {
      rows: [
        { key: ["east", "open"], count: 3, summaries: [30], parentCount: 9, parentSummaries: [90] },
        { key: ["east", "closed"], count: 2, summaries: [20], parentCount: 9, parentSummaries: [90] },
      ],
      groups: [{ field: "region" }, { field: "status" }],
      summaries: [{ field: "amount", function: "sum" }],
      columns: [
        { name: "region", title: "区域", dataType: "text", editable: true, nullable: true },
        { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true },
      ],
      hasMore: false,
      collapsedKeys: [],
    } });

    expect(wrapper.text()).toContain("区域: east");
    expect(wrapper.text()).toContain("状态: open");
    expect(wrapper.get(".group-toggle").text()).toContain("9");
    expect(wrapper.get(".group-toggle").text()).toContain("金额 合计: 90");
    await wrapper.get(".group-toggle").trigger("click");
    expect(wrapper.emitted("toggle")).toEqual([['["east"]']]);
  });
  it("formats parent and child numeric summaries without changing aggregates or counts", () => {
    setLocale("zh-CN");
    const raw = [1234.56789, 12, null, -0];
    const wrapper = mount(ViewGroupPanel, { props: {
      rows: [{ key: ["east", "open"], count: 7, summaries: raw,
        parentCount: 9, parentSummaries: raw }],
      groups: [{ field: "region" }, { field: "status" }],
      summaries: (["sum", "avg", "min", "max"] as const)
        .map(fn => ({ field: "amount", function: fn })),
      columns: [{ name: "amount", title: "金额", dataType: "decimal",
        editable: true, nullable: true, display: {
          kind: "number", preset: "currency", currency: "CNY", displayScale: 2,
          scaleMode: "fixed", trimTrailingZeros: false, useGrouping: true,
          percentStorage: "ratio", unit: null, precision: "exact", timezone: "system",
          mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
        } }],
      hasMore: false,
    } });
    for (const summary of wrapper.findAll(".group-summary")) {
      expect(summary.text()).toBe("金额 合计: ¥1,234.57 · 金额 平均: ¥12.00 · 金额 最小: — · 金额 最大: ¥0.00");
    }
    expect(wrapper.findAll(".group-summary")).toHaveLength(2);
    expect(wrapper.findAll("b").map(node => node.text())).toEqual(["9", "7"]);
    expect(raw).toEqual([1234.56789, 12, null, -0]);
  });

  it("formats numeric group keys through the column display spec, matching grid cells", () => {
    setLocale("zh-CN");
    const wrapper = mount(ViewGroupPanel, { props: {
      rows: [
        { key: [0.125, 1234.56789], count: 2, summaries: [] },
        { key: [null, 0], count: 1, summaries: [] },
      ],
      groups: [{ field: "ratio" }, { field: "amount" }],
      summaries: [],
      columns: [
        { name: "ratio", title: "比例", dataType: "decimal", editable: true, nullable: true, display: {
          kind: "number", preset: "percent", currency: "", displayScale: 1,
          scaleMode: "fixed", trimTrailingZeros: false, useGrouping: true,
          percentStorage: "ratio", unit: null, precision: "exact", timezone: "system",
          mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
        } },
        { name: "amount", title: "金额", dataType: "decimal", editable: true, nullable: true, display: {
          kind: "number", preset: "currency", currency: "CNY", displayScale: 2,
          scaleMode: "fixed", trimTrailingZeros: false, useGrouping: true,
          percentStorage: "ratio", unit: null, precision: "exact", timezone: "system",
          mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
        } },
      ],
      hasMore: false,
    } });
    expect(wrapper.text()).toContain("比例: 12.5%");
    expect(wrapper.text()).toContain("金额: ¥1,234.57");
    expect(wrapper.text()).toContain("比例: 空值");
    expect(wrapper.text()).toContain("金额: ¥0.00");
    expect(wrapper.text()).not.toContain("0.125");
  });

});
