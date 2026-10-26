import type { ColumnSchema, FilterCondition, FilterExpression, FilterGroup } from "@/contracts";
import { isFilterCondition } from "./viewQuery";
import { filterEditorOptions } from "./filterFieldEditor";
import { headerValueKey, headerValueLabel } from "@/services/headerFilterValues";

/** 勾选值面板的统一选项：rawValue 保留原始类型（空白值为 null）。 */
export interface HeaderMenuOption {
  /** 稳定勾选键（类型前缀编码，0/"0"/false/null 互不冲突）。 */
  readonly key: string;
  readonly label: string;
  readonly rawValue: unknown;
  readonly count?: number;
}

/** 面板可用性：封闭枚举或权威取值接口，二选一。 */
export type HeaderMenuSource =
  | { readonly kind: "enum"; readonly options: readonly HeaderMenuOption[] }
  | { readonly kind: "dynamic" };

/**
 * 解析字段的勾选值来源：
 *  - 封闭枚举 schema（显式 `filterOptions` 或 select 显示契约的
 *    canonical `enumOptions`）→ 静态选项；列可空时补“（空白）”选项，
 *    保证可勾选空白值（即使当前 0 行空白也不损语义）；
 *  - 其余字段 → 权威 `query.view` 分组取值（动态、分页）。
 * 已加载分页行绝不参与：它们不是全量枚举。
 */
export function headerMenuSource(
  column: ColumnSchema | undefined,
): HeaderMenuSource | null {
  const options = filterEditorOptions(column);
  if (options.length > 0 || column?.filterInput === "multiSelect" || column?.filterInput === "select") {
    const withBlank = column?.nullable !== false
      ? [...options.map(option => ({
          key: headerValueKey(option.value),
          label: option.label,
          rawValue: option.value,
        })), { key: headerValueKey(null), label: "（空白）", rawValue: null }]
      : options.map(option => ({
          key: headerValueKey(option.value),
          label: option.label,
          rawValue: option.value,
        }));
    return { kind: "enum", options: withBlank };
  }
  if (!column) return null;
  return { kind: "dynamic" };
}

/** 权威动态取值页 → 面板选项。 */
export function headerMenuOptionsFromPage(
  options: readonly { readonly key: string; readonly label: string; readonly rawValue: unknown; readonly count: number }[],
): readonly HeaderMenuOption[] {
  return options.map(option => ({ ...option }));
}

/** 搜索只过滤选项列表（空白分词 OR 匹配标签）：输入永远不会变成筛选值。 */
export function searchMenuOptions(
  options: readonly HeaderMenuOption[],
  search: string,
): readonly HeaderMenuOption[] {
  const tokens = search.trim().toLocaleLowerCase().split(/\s+/u).filter(Boolean);
  if (tokens.length === 0) return options;
  return options.filter(option => {
    const label = option.label.toLocaleLowerCase();
    const key = option.key.slice(2).toLocaleLowerCase();
    return tokens.some(token => label.includes(token) || key.includes(token));
  });
}

/**
 * 字段级勾选条件。原始标量值按类型参与筛选：
 *  - 空白（null）→ `is_null`；单标量 → `eq`；多标量 → `in`；
 *  - 空白与标量并存 → 按列封装 `FilterGroup(groupLogic:"OR")`，子叶省略
 *    logic（权威编译器要求 index>=1 子叶 logic 与 groupLogic 一致，省略
 *    由权威归一化）；平铺 per-leaf OR 会泄漏到其他列的 AND 约束；
 *  - 数组/对象值（如 multiple select 成员语义）绝不能当标量进 eq/in，
 *    一律剔除。
 * 空集合表示清除该字段筛选。
 */
export function headerValueFilterLeaves(
  field: string,
  values: readonly unknown[],
  column?: ColumnSchema,
): readonly FilterExpression[] {
  if (values.length === 0) return [];
  const isScalar = (value: unknown): value is string | number | boolean =>
    typeof value === "string" || typeof value === "number" || typeof value === "boolean";
  const typed = values.filter(isScalar);
  const hasBlank = values.some(value => value === null || value === undefined)
    && typed.length !== values.length;
  if (column?.filterInput === "multiSelect"
    || (column?.filterOperators?.includes("contains") && !column.filterOperators.includes("eq"))) {
    const leaves: FilterCondition[] = typed.map(value => ({ field, operator: "contains", value }));
    if (hasBlank) leaves.unshift({ field, operator: "is_null" });
    return leaves.length > 1 ? [{ groupLogic: "OR", filters: leaves }]
      : leaves;
  }
  const typedLeaf: FilterCondition | null = typed.length === 0 ? null
    : typed.length === 1
      ? { field, operator: "eq", value: typed[0] }
      : { field, operator: "in", value: [...typed] };
  if (!hasBlank) return typedLeaf ? [typedLeaf] : [];
  const blank: FilterCondition = { field, operator: "is_null" };
  if (!typedLeaf) return [blank];
  return [{ groupLogic: "OR", filters: [blank, typedLeaf] }];
}

/** 面板/表头可归属的单列条件组：组内叶子全部属于同一字段。 */
export function isFieldValueGroup(
  expression: FilterExpression,
): expression is FilterGroup & { readonly filters: readonly FilterCondition[] } {
  if (isFilterCondition(expression) || !Array.isArray(expression.filters)) return false;
  const leaves = expression.filters;
  return leaves.length > 0 && leaves.every(isFilterCondition)
    && new Set(leaves.map(filter => filter.field)).size === 1;
}

function referencesField(expression: FilterExpression, field: string): boolean {
  return isFilterCondition(expression) ? expression.field === field
    : expression.filters.some(child => referencesField(child, field));
}

/** A column cannot be detached from a coupled advanced boolean expression. */
export function canEditHeaderValueFilter(filters: readonly FilterExpression[], field: string): boolean {
  if (filters.some(filter => filter.logic === "OR")) return false;
  return filters.every(filter => !referencesField(filter, field) || isFilterCondition(filter)
    || (isFieldValueGroup(filter) && filter.groupLogic === "OR"
      && filter.filters.every((leaf, index) => index === 0 || leaf.logic !== "AND")));
}

/** QueryPort accepts at most 200 IN values and 50 leaves across the full query. */
export function headerValueFilterError(
  filters: readonly FilterExpression[], field: string, values: readonly unknown[], column?: ColumnSchema,
): string | null {
  const replacement = headerValueFilterLeaves(field, values, column);
  const count = (items: readonly FilterExpression[]): number => items.reduce((sum, item) =>
    sum + (isFilterCondition(item) ? 1 : count(item.filters)), 0);
  const oversized = replacement.some(item => isFilterCondition(item)
    ? item.operator === "in" && Array.isArray(item.value) && item.value.length > 200
    : item.filters.some(leaf => isFilterCondition(leaf) && leaf.operator === "in"
      && Array.isArray(leaf.value) && leaf.value.length > 200));
  if (oversized) return "一次最多勾选 200 个非空值；请减少勾选，或加载完整选项后使用全选。";
  const other = filters.filter(item => isFilterCondition(item) ? item.field !== field
    : !isFieldValueGroup(item) || item.filters[0]!.field !== field);
  if (count(other) + count(replacement) > 50) return "筛选条件总数最多 50 条；请减少勾选或移除其他字段的条件。";
  return null;
}

/** 从当前顶层条件推导面板初始勾选键；其余情形视为未筛选（全选态）。 */
export function headerMenuSelection(
  filters: readonly FilterExpression[],
  field: string,
  column?: ColumnSchema,
): readonly string[] | null {
  if (!canEditHeaderValueFilter(filters, field)) return null;
  const membership = column?.filterInput === "multiSelect"
    || (column?.filterOperators?.includes("contains") && !column.filterOperators.includes("eq"));
  const own = filters.filter(filter => isFilterCondition(filter)
    ? filter.field === field
    : isFieldValueGroup(filter) && filter.filters[0]!.field === field);
  if (own.length !== 1) return null;
  const entry = own[0]!;
  if (isFilterCondition(entry)) {
    if (entry.operator === "eq") return [headerValueKey(entry.value)];
    if (entry.operator === "contains" && membership) return [headerValueKey(entry.value)];
    if (entry.operator === "is_null") return [headerValueKey(null)];
    if (entry.operator === "in" && Array.isArray(entry.value)) {
      return entry.value.map(headerValueKey);
    }
    return null;
  }
  // 仅反推明确的列级 OR 组（面板自己的产出形态）：高级 AND 组不能伪装
  // 等价勾选；顶层 joiner 也不能是 OR（否则与其他条件耦合）。子叶 logic
  // 必须省略或与组语义一致（首叶任意，后续叶不得标 AND）。
  if (!isFieldValueGroup(entry) || entry.groupLogic !== "OR" || entry.logic === "OR") return null;
  if (!entry.filters.every((leaf, index) =>
    index === 0 || leaf.logic === undefined || leaf.logic === "OR")) {
    return null;
  }
  const keys: string[] = [];
  for (const leaf of entry.filters) {
    if (leaf.operator === "is_null") keys.push(headerValueKey(null));
    else if (leaf.operator === "eq") keys.push(headerValueKey(leaf.value));
    else if (leaf.operator === "contains" && membership) keys.push(headerValueKey(leaf.value));
    else if (leaf.operator === "in" && Array.isArray(leaf.value)) {
      keys.push(...leaf.value.map(headerValueKey));
    } else return null;
  }
  return keys.length > 0 ? keys : null;
}

/**
 * 面板应用的勾选键 → 原始值集合。仅保留已知选项内勾选。
 *
 * `complete` 表示选项列表是权威完整枚举：此时勾满全部等同未筛选，
 * 返回空数组由调用方清除。分页未取完（hasMore）时勾满已加载项只是
 * 明确的部分选择，必须原样应用，绝不能当作全表全选而误清除。
 */
export function resolveMenuSelectionValues(
  options: readonly HeaderMenuOption[],
  selectedKeys: readonly string[],
  complete: boolean,
): readonly unknown[] {
  const byKey = new Map(options.map(option => [option.key, option]));
  const values: unknown[] = [];
  for (const key of selectedKeys) {
    const option = byKey.get(key);
    if (option) values.push(option.rawValue);
  }
  if (values.length === 0) return [];
  if (complete && values.length >= options.length) return [];
  return values;
}

/** 面板展示兜底标签（与取值服务一致，供测试与未来复用）。 */
export { headerValueLabel };
