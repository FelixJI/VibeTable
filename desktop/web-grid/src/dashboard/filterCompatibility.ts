import type { DashboardFilterVariablePayload, FilterCondition } from "@/contracts";

type DashboardFilterType = DashboardFilterVariablePayload["type"];
type DashboardFilterOperator = FilterCondition["operator"];

/** Operator produced by the interactive control for a configured filter type. */
export function interactiveDashboardFilterOperator(
  type: DashboardFilterType,
): DashboardFilterOperator {
  if (type === "date-range" || type === "number-range") return "between";
  return type === "enum" ? "in" : "eq";
}

/** Preserve persisted/default scalar compatibility while matching interactive array values. */
export function runtimeDashboardFilterOperator(
  type: DashboardFilterType,
  value: unknown,
): DashboardFilterOperator {
  if (type === "date-range" || type === "number-range") return "between";
  return Array.isArray(value) ? "in" : "eq";
}

/** Resolve the field exactly as the dashboard runtime does for one panel. */
export function resolveDashboardFilterField(
  filter: DashboardFilterVariablePayload,
  panelId: string,
): string | undefined {
  if (filter.targetPanels.length > 0 && !filter.targetPanels.includes(panelId)) return undefined;
  return filter.fieldBindings?.[panelId]
    ?? (filter.allowedFields.length === 1 ? filter.allowedFields[0] : undefined);
}
