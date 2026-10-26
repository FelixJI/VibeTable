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

function isNonEmptyScalar(value: unknown): boolean {
  return value !== "" && value !== null && value !== undefined
    && typeof value !== "object";
}

function isNonEmptyToken(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

/** 顶层 OR joiner：与相邻条件 OR 耦合的条目不可单独镜像。 */
function boundarySafe(
  expressions: readonly FilterExpression[],
  index: number,
): boolean {
  return (index === 0 || expressions[index]!.logic !== "OR")
    && expressions[index + 1]?.logic !== "OR";
}

/**
 * Return the subset that Tabulator header filters can represent without
 * changing the saved tree's meaning.
 *
 * Header-expressible entries:
 *  - the field's single `eq` leaf (classic exact-match input);
 *  - the field's complete `contains` token set — a single flat leaf, or the
 *    column-level `FilterGroup(groupLogic: "OR")` produced by the query
 *    adapter (the input renders tokens joined by spaces; the adapter
 *    re-splits them).
 *
 * Foreign entries (nested/multi-field groups, `in` checkbox filters,
 * cross-entry OR chains, mixed operator pairs on one field) are skipped:
 * the header input must never pretend to express a condition it cannot
 * reproduce.
 */
export function headerFilterConditions(
  expressions: readonly FilterExpression[],
): readonly FilterCondition[] {
  if (expressions.length === 0) return [];
  const flatLeaves = expressions.filter(isFilterCondition);
  const leafCountByFieldOperator = new Map<string, number>();
  const leafCountByField = new Map<string, number>();
  for (const leaf of flatLeaves) {
    const key = `${leaf.field}\u0000${leaf.operator}`;
    leafCountByFieldOperator.set(key, (leafCountByFieldOperator.get(key) ?? 0) + 1);
    leafCountByField.set(leaf.field, (leafCountByField.get(leaf.field) ?? 0) + 1);
  }
  const flatCount = (field: string, operator: string): number =>
    leafCountByFieldOperator.get(`${field}\u0000${operator}`) ?? 0;
  /**
   * 列级 OR token 组：全部叶子同字段且为非空 contains 文本；该字段在
   * 平铺层不得再有叶子（字段条件全部归属此组），同字段组唯一。
   */
  const tokenGroups = new Map<FilterExpression, readonly FilterCondition[]>();
  const tokenGroupFields = new Set<string>();
  for (const expression of expressions) {
    if (isFilterCondition(expression)) continue;
    if (expression.groupLogic !== "OR" || !Array.isArray(expression.filters)) continue;
    const leaves = expression.filters;
    if (leaves.length === 0 || !leaves.every(isFilterCondition)) continue;
    const field = leaves[0]!.field;
    if (!leaves.every(leaf => leaf.field === field)) continue;
    if (!leaves.every(leaf => leaf.operator === "contains" && isNonEmptyToken(leaf.value))) {
      continue;
    }
    if (tokenGroupFields.has(field)) continue;
    // 字段的全部条件必须都在这个组里：平铺层不得再有该字段的任何叶子。
    if ((leafCountByField.get(field) ?? 0) > 0) continue;
    tokenGroups.set(expression, leaves);
    tokenGroupFields.add(field);
  }
  const mirrored: FilterCondition[] = [];
  for (const [index, expression] of expressions.entries()) {
    if (!boundarySafe(expressions, index)) continue;
    if (isFilterCondition(expression)) {
      const field = expression.field;
      // 字段条件已由列级组表达（或冲突）时，平铺叶子不可再镜像。
      if (tokenGroupFields.has(field)) continue;
      // 仅镜像字段唯一的 eq 叶子（既有语义）：高级编辑器创建的同字段
      // contains/in 等条件不属于表头输入，不得被 runtime 更新误清除。
      if (expression.operator === "eq" && flatCount(field, "eq") === 1
        && isNonEmptyScalar(expression.value)) {
        mirrored.push(expression);
      }
      continue;
    }
    const leaves = tokenGroups.get(expression);
    if (leaves) mirrored.push(...leaves);
  }
  return mirrored;
}
