import { describe, expect, it } from "vitest";

import type { NormalizedRelationDescriptor, RelationDisplayFieldInfo } from "@/contracts";
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

const labelDisplayBase = {
  displayScale: 2, scaleMode: "fixed" as const, trimTrailingZeros: false, useGrouping: true,
  currency: "", percentStorage: "ratio" as const, unit: null, indent: 0 as const,
  mode: "default", trueLabel: "是", falseLabel: "否",
};

// 与 grid/commonFieldDisplay 的 #459 契约一致：日期/时间/自定义 bool/select
// 标签用来源字段自己的 DisplaySpec/enumOptions 渲染，而不是第二套规则。
describe("formatRelationLabelValue × commonFieldDisplay", () => {
  it("日期与时间标签按来源字段 precision/timezone 一致显示", () => {
    expect(formatRelationLabelValue("2026-03-08T07:00:00Z", {
      fieldId: "fld_at", dataType: "datetime",
      display: { ...labelDisplayBase, kind: "dateTime", preset: "", precision: "millisecond", timezone: "UTC" },
    })).toBe("2026-03-08 07:00:00.000");
    expect(formatRelationLabelValue("2026-03-08T06:59:59Z", {
      fieldId: "fld_at", dataType: "datetime",
      display: { ...labelDisplayBase, kind: "dateTime", preset: "", precision: "second", timezone: "America/New_York" },
    })).toBe("2026-03-08 01:59:59");
    expect(formatRelationLabelValue("09:30:15", {
      fieldId: "fld_time", dataType: "time",
      display: { ...labelDisplayBase, kind: "time", preset: "", precision: "minute", timezone: "UTC" },
    })).toBe("09:30");
  });

  it("布尔标签只在来源字段本身是布尔时使用自定义标签", () => {
    expect(formatRelationLabelValue(true, {
      fieldId: "fld_signed", dataType: "boolean",
      display: { ...labelDisplayBase, kind: "bool", preset: "", precision: "exact", timezone: "system", mode: "text", trueLabel: "已签约", falseLabel: "未签约" },
    })).toBe("已签约");
    expect(formatRelationLabelValue(false, {
      fieldId: "fld_signed", dataType: "boolean",
      display: { ...labelDisplayBase, kind: "bool", preset: "", precision: "exact", timezone: "system", mode: "text", trueLabel: "已签约", falseLabel: "未签约" },
    })).toBe("未签约");
    // 声明类型不是布尔时保持旧契约 ✓/✕，不因运行时形状套用自定义标签。
    expect(formatRelationLabelValue(false, numericDescriptor().displayFieldInfo)).toBe("✕");
  });

  it("select 标签按 canonical enumOptions 渲染并标注停用，缺选项回退原始值", () => {
    const info: RelationDisplayFieldInfo = {
      fieldId: "fld_status", dataType: "text",
      display: { ...labelDisplayBase, kind: "select", preset: "", precision: "exact", timezone: "system" },
      enumOptions: [
        { optionId: "opt_a", label: "进行中", color: "#ffaa00", order: 0, state: "active" },
        { optionId: "opt_b", label: "旧状态", color: "", order: 1, state: "retired" },
      ],
    };
    expect(formatRelationLabelValue("opt_a", info)).toBe("进行中");
    expect(formatRelationLabelValue("opt_b", info)).toBe("旧状态（已停用）");
    expect(formatRelationLabelValue("opt_missing", info)).toBe("opt_missing");
    expect(formatRelationLabelValue("opt_a", { ...info, enumOptions: null })).toBe("opt_a");
  });

  it("数组/对象标签资格不因通用 formatter 扩大，空白与缺失仍回退", () => {
    const info: RelationDisplayFieldInfo = {
      fieldId: "fld_status", dataType: "text",
      display: { ...labelDisplayBase, kind: "select", preset: "", precision: "exact", timezone: "system" },
      enumOptions: [{ optionId: "opt_a", label: "进行中", color: "", order: 0, state: "active" }],
    };
    expect(formatRelationLabelValue(["opt_a"], info)).toBeNull();
    expect(formatRelationLabelValue({ value: "opt_a", source: "display" }, info)).toBeNull();
    expect(formatRelationLabelValue("   ", info)).toBeNull();
    expect(formatRelationLabelValue(null, info)).toBeNull();
  });

  it("NaN 与 ±Infinity 保持缺失（回退记录 ID），不得渲染成文本", () => {
    const numeric = numericDescriptor().displayFieldInfo;
    // 共享 formatter 会把非有限数变成 "NaN"/"Infinity" 文本；关系标签沿
    // 用旧契约（formatNumberDisplay null），非有限数直接回退。
    for (const value of [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY]) {
      expect(formatRelationLabelValue(value, numeric)).toBeNull();
      expect(formatRelationLabelValue(value, { fieldId: "f", dataType: "text" })).toBeNull();
    }
    expect(relationTargetLabel({
      collection: "c", itemId: "rec_bad", label: "rec_bad", displayValue: Number.NaN,
    }, numericDescriptor())).toBe("rec_bad");
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
    const equalRaw = {
      collection: "c", itemId: "i", label: "1", displayValue: 1, secondaryValue: 1,
    };
    expect(relationTargetLabel(equalRaw, descriptor)).toBe("100.0%");
    expect(relationTargetSecondaryLabel(equalRaw, descriptor)).toBe("¥1.00");
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
