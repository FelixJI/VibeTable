import {
  PRODUCT_PANEL_TYPES,
  cloneRecord,
  isRecord,
  type Dashboard,
  type DashboardPanel,
  type PanelPosition,
  type ParsedPanelType,
  type ProductPanelType,
  type WirePanel,
} from "./types";

const PRODUCT_TYPES = new Set<string>(PRODUCT_PANEL_TYPES);

export function parseProductPanelType(type: string): ParsedPanelType {
  if (type === "custom") return "custom";
  return PRODUCT_TYPES.has(type) ? type as ProductPanelType : "unknown";
}

export function serializeProductPanelType(type: ProductPanelType): string {
  return type;
}

export function parseWirePanel(value: unknown): DashboardPanel {
  const source = requiredRecord(value, "dashboard panel");
  rejectLegacyField(source, "dashboard_id");
  rejectLegacyField(source, "show_header");
  const rawOptions = cloneRecord(requiredRecord(source.options, "dashboard panel.options"));
  const rawQuery = cloneRecord(requiredRecord(source.query, "dashboard panel.query"));
  const rawType = requiredString(source.type, "dashboard panel.type");
  const productType = parseProductPanelType(rawType);
  return {
    id: requiredString(source.id, "dashboard panel.id"),
    dashboardId: requiredString(source.dashboardId, "dashboard panel.dashboardId"),
    name: requiredString(source.name, "dashboard panel.name"),
    note: optionalNullableString(source.note, "dashboard panel.note"),
    icon: optionalNullableString(source.icon, "dashboard panel.icon"),
    color: optionalNullableString(source.color, "dashboard panel.color"),
    showHeader: optionalBoolean(source.showHeader, "dashboard panel.showHeader", true),
    type: rawType,
    rawType,
    productType,
    editable: productType !== "custom" && productType !== "unknown",
    position: parsePosition(source.position),
    options: cloneRecord(rawOptions),
    query: cloneRecord(rawQuery),
    rawOptions,
    rawQuery,
    rawPresentation: {
      note: hasOwn(source, "note"),
      icon: hasOwn(source, "icon"),
      color: hasOwn(source, "color"),
      showHeader: hasOwn(source, "showHeader"),
    },
  };
}

export function parseWireDashboard(value: unknown): Dashboard {
  const source = requiredRecord(value, "dashboard");
  if (!Array.isArray(source.panels)) invalid("dashboard.panels");
  const panels = source.panels.map(parseWirePanel);
  return {
    id: requiredString(source.id, "dashboard.id"),
    name: requiredString(source.name, "dashboard.name"),
    note: requiredString(source.note, "dashboard.note"),
    panels,
  };
}

/**
 * Project a trusted product panel back to the wire contract. Unknown/custom panels are
 * emitted byte-for-byte at the options/query/type boundary and remain read-only.
 */
export function toWirePanel(panel: DashboardPanel): WirePanel {
  if (panel.productType === "custom" || panel.productType === "unknown") {
    const presentation = panel.rawPresentation;
    return {
      id: panel.id,
      dashboardId: panel.dashboardId,
      name: panel.name,
      ...(presentation?.note ? { note: panel.note } : {}),
      ...(presentation?.icon ? { icon: panel.icon } : {}),
      ...(presentation?.color ? { color: panel.color } : {}),
      ...(presentation?.showHeader ? { showHeader: panel.showHeader } : {}),
      type: panel.rawType,
      position: { ...panel.position },
      options: cloneRecord(panel.rawOptions),
      query: cloneRecord(panel.rawQuery),
    };
  }
  return {
    id: panel.id,
    dashboardId: panel.dashboardId,
    name: panel.name,
    note: panel.note,
    icon: panel.icon,
    color: panel.color,
    showHeader: panel.showHeader,
    type: serializeProductPanelType(panel.productType),
    position: { ...panel.position },
    options: cloneRecord(panel.options),
    query: cloneRecord(panel.query),
  };
}

function hasOwn(source: Readonly<Record<string, unknown>>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(source, key);
}

function parsePosition(value: unknown): PanelPosition {
  const source = requiredRecord(value, "dashboard panel.position");
  return {
    x: nonNegativeInteger(source.x, "dashboard panel.position.x"),
    y: nonNegativeInteger(source.y, "dashboard panel.position.y"),
    width: positiveInteger(source.width, "dashboard panel.position.width"),
    height: positiveInteger(source.height, "dashboard panel.position.height"),
  };
}

function requiredRecord(value: unknown, path: string): Readonly<Record<string, unknown>> {
  if (!isRecord(value)) invalid(path);
  return value;
}

function requiredString(value: unknown, path: string): string {
  if (typeof value !== "string") invalid(path);
  return value;
}

function optionalNullableString(value: unknown, path: string): string | null {
  if (value === undefined || value === null) return null;
  if (typeof value !== "string") invalid(path);
  return value;
}

function optionalBoolean(value: unknown, path: string, fallback: boolean): boolean {
  if (value === undefined) return fallback;
  if (typeof value !== "boolean") invalid(path);
  return value;
}

function nonNegativeInteger(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) invalid(path);
  return value;
}

function positiveInteger(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 1) invalid(path);
  return value;
}

function rejectLegacyField(source: Readonly<Record<string, unknown>>, field: string): void {
  if (hasOwn(source, field)) invalid(`dashboard panel.${field}`);
}

function invalid(path: string): never {
  throw new TypeError(`Invalid ${path}`);
}
