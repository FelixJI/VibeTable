export const PRODUCT_PANEL_TYPES = [
  "label",
  "metric",
  "metric-list",
  "list",
  "time-series",
  "bar",
  "line",
  "donut",
  "pie",
] as const;

export type ProductPanelType = (typeof PRODUCT_PANEL_TYPES)[number];
export type ParsedPanelType = ProductPanelType | "custom" | "unknown";

export interface PanelPosition {
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
}

export interface WirePanel {
  readonly id: string;
  readonly dashboardId: string;
  readonly name: string;
  readonly note?: string | null;
  readonly icon?: string | null;
  readonly color?: string | null;
  readonly showHeader?: boolean;
  readonly type: string;
  readonly position: PanelPosition;
  readonly options: Readonly<Record<string, unknown>>;
  readonly query?: Readonly<Record<string, unknown>> | null;
}

export interface DashboardPanel extends WirePanel {
  readonly productType: ParsedPanelType;
  readonly editable: boolean;
  /** Exact inbound type, including an unknown/custom Directus extension id. */
  readonly rawType: string;
  readonly query: Readonly<Record<string, unknown>>;
  /** Exact inbound options retained for lossless safe fallback. */
  readonly rawOptions: Readonly<Record<string, unknown>>;
  /** Exact inbound query retained even when VibeTable cannot edit it. */
  readonly rawQuery: Readonly<Record<string, unknown>>;
  /** Presence bits retain omitted optional presentation fields on unknown panels. */
  readonly rawPresentation?: Readonly<{ note: boolean; icon: boolean; color: boolean; showHeader: boolean }>;
}

export interface Dashboard {
  readonly id: string;
  readonly name: string;
  readonly note: string;
  readonly panels: readonly DashboardPanel[];
}

export interface DomainDiagnostic {
  readonly code: string;
  readonly message: string;
  readonly path?: string;
  readonly severity: "warning" | "error";
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Clone JSON-shaped wire values so callers cannot mutate the inbound object. */
export function cloneRecord(value: unknown): Record<string, unknown> {
  if (!isRecord(value)) return {};
  return cloneValue(value) as Record<string, unknown>;
}

function cloneValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(cloneValue);
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, cloneValue(item)]),
    );
  }
  return value;
}
