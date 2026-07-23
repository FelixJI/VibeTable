import type { PanelPosition, ProductPanelType } from "./types";

export type DashboardTemplateId = "blank" | "operations-overview" | "trend-analysis" | "detail-monitoring";

export interface LocalizedLabel {
  readonly "zh-CN": string;
  readonly "en-US": string;
}

export interface DashboardTemplatePanel {
  readonly key: string;
  readonly type: ProductPanelType;
  readonly title: LocalizedLabel;
  readonly position: PanelPosition;
  /** Templates never assume a business collection or field. */
  readonly requiresConfiguration: true;
}

export interface DashboardTemplate {
  readonly id: DashboardTemplateId;
  readonly name: LocalizedLabel;
  readonly panels: readonly DashboardTemplatePanel[];
}

export const DASHBOARD_TEMPLATES: readonly DashboardTemplate[] = [
  {
    id: "blank",
    name: { "zh-CN": "空白仪表盘", "en-US": "Blank dashboard" },
    panels: [],
  },
  {
    id: "operations-overview",
    name: { "zh-CN": "运营概览", "en-US": "Operations overview" },
    panels: [
      panel("heading", "label", "概览", "Overview", 0, 0, 12, 1),
      panel("metric-a", "metric", "关键指标一", "Primary metric", 0, 1, 3, 2),
      panel("metric-b", "metric", "关键指标二", "Secondary metric", 3, 1, 3, 2),
      panel("breakdown", "donut", "构成", "Breakdown", 6, 1, 6, 4),
      panel("trend", "time-series", "趋势", "Trend", 0, 3, 6, 4),
      panel("records", "list", "最新明细", "Recent records", 0, 7, 12, 4),
    ],
  },
  {
    id: "trend-analysis",
    name: { "zh-CN": "趋势分析", "en-US": "Trend analysis" },
    panels: [
      panel("heading", "label", "趋势分析", "Trend analysis", 0, 0, 12, 1),
      panel("primary-trend", "time-series", "主要趋势", "Primary trend", 0, 1, 12, 5),
      panel("comparison", "line", "趋势对比", "Trend comparison", 0, 6, 8, 4),
      panel("ranking", "bar", "分类排行", "Category ranking", 8, 6, 4, 4),
    ],
  },
  {
    id: "detail-monitoring",
    name: { "zh-CN": "明细监控", "en-US": "Detail monitoring" },
    panels: [
      panel("heading", "label", "明细监控", "Detail monitoring", 0, 0, 12, 1),
      panel("metrics", "metric-list", "状态指标", "Status metrics", 0, 1, 4, 4),
      panel("activity", "time-series", "变化趋势", "Activity trend", 4, 1, 8, 4),
      panel("details", "list", "记录明细", "Record details", 0, 5, 12, 5),
    ],
  },
] as const;

export function getDashboardTemplate(id: DashboardTemplateId): DashboardTemplate {
  const found = DASHBOARD_TEMPLATES.find((template) => template.id === id);
  if (!found) throw new Error(`Unknown dashboard template: ${id}`);
  return {
    ...found,
    name: { ...found.name },
    panels: found.panels.map((item) => ({
      ...item,
      title: { ...item.title },
      position: { ...item.position },
    })),
  };
}

function panel(
  key: string,
  type: ProductPanelType,
  zh: string,
  en: string,
  x: number,
  y: number,
  width: number,
  height: number,
): DashboardTemplatePanel {
  return {
    key,
    type,
    title: { "zh-CN": zh, "en-US": en },
    position: { x, y, width, height },
    requiresConfiguration: true,
  };
}
