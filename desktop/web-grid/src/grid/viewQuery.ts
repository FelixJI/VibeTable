import type { FilterCondition, FilterExpression } from "@/contracts";

export function isFilterCondition(
  expression: FilterExpression,
): expression is FilterCondition {
  return "field" in expression && "operator" in expression;
}

/**
 * Return leaf conditions only when doing so preserves the original boolean
 * expression. Consumers that cannot render a grouped tree must not flatten it.
 */
export function ungroupedFilterConditions(
  expressions: readonly FilterExpression[],
): readonly FilterCondition[] {
  return expressions.every(isFilterCondition) ? expressions : [];
}

/**
 * Return the subset that Tabulator header filters can represent without
 * changing the saved tree's meaning. A grouped expression must stay in the
 * advanced editor and authoritative query adapter.
 */
export function headerFilterConditions(
  expressions: readonly FilterExpression[],
): readonly FilterCondition[] {
  const leaves = ungroupedFilterConditions(expressions);
  if (leaves.some(expression => expression.logic === "OR")) return [];
  return leaves.filter(expression => expression.operator === "eq"
    && expression.value !== "" && expression.value !== null && expression.value !== undefined
    && typeof expression.value !== "object"
    && leaves.filter(other => other.field === expression.field && other.operator === "eq").length === 1);
}
