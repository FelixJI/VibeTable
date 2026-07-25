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
  const source = isRecord(value) ? value : {};
  const rawOptions = cloneRecord(source.options);
  const rawQuery = cloneRecord(source.query);
  const rawType = stringValue(source.type, "unknown");
  const productType = parseProductPanelType(rawType);
  return {
    id: stringValue(source.id),
    dashboardId: stringValue(source.dashboardId ?? source.dashboard_id),
    name: stringValue(source.name),
    note: nullableString(source.note),
    icon: nullableString(source.icon),
    color: nullableString(source.color),
    showHeader: source.showHeader === undefined && source.show_header === undefined
      ? true
      : (source.showHeader ?? source.show_header) === true,
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
      showHeader: hasOwn(source, "showHeader") || hasOwn(source, "show_header"),
    },
  };
}

export function parseWireDashboard(value: unknown): Dashboard {
  const source = isRecord(value) ? value : {};
  const panels = Array.isArray(source.panels) ? source.panels.map(parseWirePanel) : [];
  return {
    id: stringValue(source.id),
    name: stringValue(source.name),
    note: stringValue(source.note),
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
  const source = isRecord(value) ? value : {};
  return {
    x: nonNegativeInteger(source.x, 0),
    y: nonNegativeInteger(source.y, 0),
    width: positiveInteger(source.width ?? source.w, 4),
    height: positiveInteger(source.height ?? source.h, 4),
  };
}

function stringValue(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function nullableString(value: unknown): string | null {
  return typeof value === "string" ? value : null;
}

function nonNegativeInteger(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0
    ? value
    : fallback;
}

function positiveInteger(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isInteger(value) && value >= 1
    ? value
    : fallback;
}
