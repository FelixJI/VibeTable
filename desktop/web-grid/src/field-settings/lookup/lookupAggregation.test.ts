import { describe, expect, it } from "vitest";
import { LOOKUP_AGGREGATIONS } from "@/contracts";
import {
  LOOKUP_AGGREGATION_HINTS,
  LOOKUP_AGGREGATION_LABELS,
  isAggregationApplicable,
  isNumericAggregation,
} from "./lookupAggregation";

describe("lookupAggregation", () => {
  it("标签与空值规则提示覆盖 #393 冻结闭集", () => {
    expect([...LOOKUP_AGGREGATIONS]).toEqual([
      "values", "distinct", "countRecords", "countNonEmpty", "countDistinct",
      "sum", "average", "min", "max",
    ]);
    for (const aggregation of LOOKUP_AGGREGATIONS) {
      expect(LOOKUP_AGGREGATION_LABELS[aggregation].length).toBeGreaterThan(0);
      expect(LOOKUP_AGGREGATION_HINTS[aggregation].length).toBeGreaterThan(0);
    }
    // 名称明确区分“命中记录数”与“有效值数”，不共用同一标签。
    expect(LOOKUP_AGGREGATION_LABELS.countRecords).not.toBe(LOOKUP_AGGREGATION_LABELS.countNonEmpty);
    expect(LOOKUP_AGGREGATION_LABELS.countRecords).toContain("记录");
    expect(LOOKUP_AGGREGATION_LABELS.countNonEmpty).not.toContain("记录");
  });

  it("计数与原值/去重适用于全部类型；数值聚合仅限声明 number 的来源", () => {
    expect(LOOKUP_AGGREGATIONS.filter(isNumericAggregation))
      .toEqual(["sum", "average", "min", "max"]);
    for (const aggregation of ["sum", "average", "min", "max"] as const) {
      expect(isAggregationApplicable(aggregation, "number")).toBe(true);
      expect(isAggregationApplicable(aggregation, "text")).toBe(false);
      expect(isAggregationApplicable(aggregation, "bool")).toBe(false);
      // 类型未知（null）或未选目标（""）时不放行，等类型可验证后再启用。
      expect(isAggregationApplicable(aggregation, null)).toBe(false);
      expect(isAggregationApplicable(aggregation, "")).toBe(false);
    }
    for (const aggregation of [
      "values", "distinct", "countRecords", "countNonEmpty", "countDistinct",
    ] as const) {
      expect(isAggregationApplicable(aggregation, "text")).toBe(true);
      expect(isAggregationApplicable(aggregation, "number")).toBe(true);
      expect(isAggregationApplicable(aggregation, "")).toBe(true);
      expect(isAggregationApplicable(aggregation, null)).toBe(true);
    }
  });
});
