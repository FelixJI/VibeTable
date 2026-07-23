export const DASHBOARD_FILTER_TYPES = [
  "date-range",
  "enum",
  "user",
  "relation",
  "number-range",
] as const;

export type DashboardFilterType = (typeof DASHBOARD_FILTER_TYPES)[number];
export type FilterOperator =
  | "eq"
  | "ne"
  | "in"
  | "contains"
  | "starts_with"
  | "ends_with"
  | "gt"
  | "gte"
  | "lt"
  | "lte"
  | "between"
  | "is_null"
  | "is_not_null";

export interface FilterPredicate {
  readonly field: string;
  readonly operator: FilterOperator;
  readonly value?: unknown;
}

export interface FilterAnd {
  readonly and: readonly FilterExpression[];
}

export type FilterExpression = FilterPredicate | FilterAnd;

export interface DashboardFilterVariable {
  readonly key: string;
  readonly label: string;
  readonly type: DashboardFilterType;
  readonly defaultValue?: unknown;
  readonly allowedFields: readonly string[];
  /** Empty means every panel. */
  readonly targetPanels: readonly string[];
}

export interface PanelSelectionLink {
  readonly sourcePanelId: string;
  readonly value: unknown;
  readonly targetPanels: readonly string[];
  readonly targetField: string;
  readonly operator?: FilterOperator;
}

export function resolveSelectionTargets(
  link: PanelSelectionLink,
  existingPanelIds: readonly string[],
): string[] {
  const existing = new Set(existingPanelIds);
  return [...new Set(link.targetPanels)].filter(
    (target) => target !== link.sourcePanelId && existing.has(target),
  );
}

export function mergePanelFilters(
  panelId: string,
  ownFilter: FilterExpression | null,
  variables: readonly DashboardFilterVariable[],
  values: Readonly<Record<string, unknown>>,
  selectionLinks: readonly PanelSelectionLink[] = [],
): FilterExpression | null {
  const clauses: FilterExpression[] = ownFilter ? [ownFilter] : [];
  for (const variable of variables) {
    const value = Object.prototype.hasOwnProperty.call(values, variable.key)
      ? values[variable.key]
      : variable.defaultValue;
    if (value === null || value === undefined || value === "") continue;
    if (variable.targetPanels.length > 0 && !variable.targetPanels.includes(panelId)) continue;
    for (const field of variable.allowedFields) {
      clauses.push({ field, operator: operatorForVariable(variable.type, value), value });
    }
  }
  for (const link of selectionLinks) {
    if (!link.targetPanels.includes(panelId) || panelId === link.sourcePanelId) continue;
    if (link.value === null || link.value === undefined) continue;
    clauses.push({
      field: link.targetField,
      operator: link.operator ?? (Array.isArray(link.value) ? "in" : "eq"),
      value: link.value,
    });
  }
  if (clauses.length === 0) return null;
  if (clauses.length === 1) return clauses[0] ?? null;
  return { and: clauses };
}

export function toDirectusFilter(expression: FilterExpression | null): Record<string, unknown> {
  if (!expression) return {};
  if ("and" in expression) {
    return { _and: expression.and.map((item) => toDirectusFilter(item)) };
  }
  if (expression.operator === "is_null") {
    return { [expression.field]: { _null: true } };
  }
  if (expression.operator === "is_not_null") {
    return { [expression.field]: { _nnull: true } };
  }
  const directusOperator = `_${expression.operator}`;
  return {
    [expression.field]: {
      [directusOperator]: expression.value,
    },
  };
}

function operatorForVariable(type: DashboardFilterType, value: unknown): FilterOperator {
  if (type === "date-range" || type === "number-range") return "between";
  return Array.isArray(value) ? "in" : "eq";
}
