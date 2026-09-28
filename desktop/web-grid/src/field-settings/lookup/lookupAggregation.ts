import type { LookupAggregationV2 } from "@/contracts";

/** 计数区分命中记录数与有效值数。 */
export const LOOKUP_AGGREGATION_LABELS: Readonly<Record<LookupAggregationV2, string>> = {
  values: "原值",
  distinct: "去重",
  countRecords: "命中记录数",
  countNonEmpty: "非空值数",
  countDistinct: "去重非空值数",
  sum: "SUM 求和",
  average: "AVERAGE 平均值",
  min: "MIN 最小值",
  max: "MAX 最大值",
};

/** 简洁空值规则提示；完整语义由 Go 契约测试固定，UI 不复述长文。 */
export const LOOKUP_AGGREGATION_HINTS: Readonly<Record<LookupAggregationV2, string>> = {
  values: "保留原有取值方式与空值规则。",
  distinct: "相同值只保留一次（含空值），按首次出现顺序。",
  countRecords: "统计命中的记录条数，空集合为 0。",
  countNonEmpty: "空值与空串不计入，0 与 false 是有效值。",
  countDistinct: "排除空值与空串后按值去重计数。",
  sum: "只汇总数字：忽略空值，空串/布尔/非法文本报错，空集合为 0。",
  average: "数字平均值：忽略空值，空集合为空，结果是数字。",
  min: "数字最小值：忽略空值，空集合为空，结果是数字。",
  max: "数字最大值：忽略空值，空集合为空，结果是数字。",
};

/** 数值聚合仅适用于声明为 number 的来源（路径模式含数值公式列）。 */
const NUMERIC_AGGREGATIONS: ReadonlySet<LookupAggregationV2> = new Set([
  "sum",
  "average",
  "min",
  "max",
]);

export function isNumericAggregation(aggregation: LookupAggregationV2): boolean {
  return NUMERIC_AGGREGATIONS.has(aggregation);
}

/**
 * 目标类型未知（null）或未选择（""）时数值聚合一律不可选：
 * 不能验证就不放行，改目标类型也不静默接受错误聚合。
 */
export function isAggregationApplicable(
  aggregation: LookupAggregationV2,
  logicalType: string | null,
): boolean {
  if (!isNumericAggregation(aggregation)) return true;
  return logicalType === "number";
}
