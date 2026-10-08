import { describe, expect, it } from "vitest";

import type { NormalizedRelationDescriptor } from "@/contracts";
import {
  coerceLegacyLabelEntry,
  formatRelationLabelEntry,
  formatRelationLabelValue,
  relationRowLabels,
  relationTargetLabel,
  relationTargetSecondaryLabel,
} from "./relationDisplay";

function numericDescriptor(): NormalizedRelationDescriptor {
  return {
    relationId: "lines.contract",
    fieldRef: "contract",
    sourceCollection: "lines",
    kind: "m2o",
    relatedCollection: "contracts",
    unique: true,
    nullable: true,
    onDelete: "nullify",
    preset: "standard",
    selfRelation: false,
    managed: true,
    state: "valid",
    diagnostics: [],
    displayFieldId: "fld_rate",
    displayFieldInfo: {
      fieldId: "fld_rate",
      dataType: "decimal",
      display: {
        kind: "number", preset: "percent", displayScale: 1, scaleMode: "fixed",
        trimTrailingZeros: false, useGrouping: true, currency: "",
        percentStorage: "ratio", unit: null, precision: "exact",
        timezone: "local", mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
      },
    },
    fallbackDisplayFieldInfo: {
      fieldId: "fld_amount",
      dataType: "decimal",
      display: {
        kind: "number", preset: "currency", displayScale: 2, scaleMode: "fixed",
        trimTrailingZeros: false, useGrouping: true, currency: "CNY",
        percentStorage: "ratio", unit: null, precision: "exact",
        timezone: "local", mode: "default", indent: 0, trueLabel: "是", falseLabel: "否",
      },
    },
  };
}

describe("formatRelationLabelValue", () => {
  it("数字标签按来源字段自己的 DisplaySpec 类型化显示", () => {
    const descriptor = numericDescriptor();
    // 百分比显示字段（display 来源）：12.5% 而不是货币。
    expect(formatRelationLabelEntry(
      { value: 0.125, source: "display" }, descriptor,
    )).toBe("12.5%");
    // 显示字段为空回退到主字段（primary 来源）：金额格式，不是百分比。
    expect(formatRelationLabelEntry(
      { value: 1982, source: "primary" }, descriptor,
    )).toBe("¥1,982.00");
  });

  it("0 与 false 是有效值，空白文本缺失", () => {
    expect(formatRelationLabelValue(0, numericDescriptor().displayFieldInfo)).toBe("0.0%");
    expect(formatRelationLabelValue(false, { fieldId: "f", dataType: "boolean" })).toBe("✕");
    expect(formatRelationLabelValue("   ", { fieldId: "f", dataType: "text" })).toBeNull();
    expect(formatRelationLabelValue({ state: "updating" }, { fieldId: "f", dataType: "json" })).toBeNull();
  });

  it("旧主机的纯字符串标签按文本渲染", () => {
    expect(coerceLegacyLabelEntry(" 城轨一期 ")).toEqual({ value: " 城轨一期 ", source: "display" });
    expect(formatRelationLabelEntry(coerceLegacyLabelEntry(" 城轨一期 "), null)).toBe("城轨一期");
    expect(coerceLegacyLabelEntry("  ")).toBeNull();
    expect(formatRelationLabelEntry(coerceLegacyLabelEntry("Beta"), null)).toBe("Beta");
  });
});

describe("relationTargetLabel", () => {
  it("选择结果按 显示值→主显示值→标签→ID 链条渲染", () => {
    const descriptor = numericDescriptor();
    expect(relationTargetLabel({
      collection: "c", itemId: "i", label: "0.125", displayValue: 0.125,
    }, descriptor)).toBe("12.5%");
    // 显示字段无效：标签来自全局主字段，用主字段自己的格式。
    expect(relationTargetLabel({
      collection: "c", itemId: "i", label: "1982", secondaryValue: 1982,
    }, descriptor)).toBe("¥1,982.00");
    expect(relationTargetLabel({
      collection: "c", itemId: "i", label: "CT-001",
    }, descriptor)).toBe("CT-001");
    expect(relationTargetLabel({
      collection: "c", itemId: "rec123", label: "rec123",
    }, descriptor)).toBe("rec123");
  });

  it("辅助标签仅在主标签来自显示字段时显示", () => {
    const descriptor = numericDescriptor();
    expect(relationTargetSecondaryLabel({
      collection: "c", itemId: "i", label: "0.125", displayValue: 0.125, secondaryValue: 1982,
    }, descriptor)).toBe("¥1,982.00");
    expect(relationTargetSecondaryLabel({
      collection: "c", itemId: "i", label: "1982", secondaryValue: 1982,
    }, descriptor)).toBe("");
  });
});

describe("relationRowLabels", () => {
  it("读取行内批量标签投影并忽略异形数据", () => {
    const row = {
      __vibetableRelationLabels: {
        contract: { t1: { value: "城轨一期", source: "display" }, t2: { value: 0, source: "display" } },
      },
    };
    expect(relationRowLabels(row, "contract")).toEqual({
      t1: { value: "城轨一期", source: "display" },
      t2: { value: 0, source: "display" },
    });
    expect(relationRowLabels(row, "missing")).toEqual({});
    expect(relationRowLabels(null, "contract")).toEqual({});
    expect(relationRowLabels({ __vibetableRelationLabels: "bad" }, "contract")).toEqual({});
  });
});
