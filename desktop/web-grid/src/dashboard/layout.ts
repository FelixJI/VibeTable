import type { DomainDiagnostic, PanelPosition } from "./types";

export const DASHBOARD_COLUMNS = 12;
export const PANEL_SOFT_LIMIT = 30;
export const PANEL_HARD_LIMIT = 100;

export interface LayoutItem {
  readonly id: string;
  readonly position: PanelPosition;
  readonly minWidth?: number;
  readonly minHeight?: number;
}

export interface ProjectedLayoutItem extends LayoutItem {
  readonly canonicalPosition: PanelPosition;
}

export type LayoutArrow = "ArrowLeft" | "ArrowRight" | "ArrowUp" | "ArrowDown";

/** Move or resize a canonical panel by one grid unit for keyboard editing. */
export function adjustPositionWithKeyboard(position: PanelPosition, key: LayoutArrow, resize: boolean): PanelPosition {
  const next = { ...position };
  if (resize) {
    if (key === "ArrowLeft") next.width = Math.max(1, next.width - 1);
    if (key === "ArrowRight") next.width = Math.min(DASHBOARD_COLUMNS - next.x, next.width + 1);
    if (key === "ArrowUp") next.height = Math.max(1, next.height - 1);
    if (key === "ArrowDown") next.height += 1;
  } else {
    if (key === "ArrowLeft") next.x = Math.max(0, next.x - 1);
    if (key === "ArrowRight") next.x = Math.min(DASHBOARD_COLUMNS - next.width, next.x + 1);
    if (key === "ArrowUp") next.y = Math.max(0, next.y - 1);
    if (key === "ArrowDown") next.y += 1;
  }
  return next;
}

export function positionsCollide(a: PanelPosition, b: PanelPosition): boolean {
  return a.x < b.x + b.width &&
    a.x + a.width > b.x &&
    a.y < b.y + b.height &&
    a.y + a.height > b.y;
}

export function panelCountDiagnostic(count: number): DomainDiagnostic | null {
  if (count > PANEL_HARD_LIMIT) {
    return {
      code: "panel_limit_exceeded",
      message: `A dashboard can contain at most ${PANEL_HARD_LIMIT} panels.`,
      severity: "error",
    };
  }
  if (count > PANEL_SOFT_LIMIT) {
    return {
      code: "panel_count_warning",
      message: `Dashboards with more than ${PANEL_SOFT_LIMIT} panels may load slowly.`,
      severity: "warning",
    };
  }
  return null;
}

export function validateLayout(items: readonly LayoutItem[]): DomainDiagnostic[] {
  const diagnostics: DomainDiagnostic[] = [];
  const countDiagnostic = panelCountDiagnostic(items.length);
  if (countDiagnostic) diagnostics.push(countDiagnostic);
  const ids = new Set<string>();
  for (const item of items) {
    if (ids.has(item.id)) {
      diagnostics.push(error("layout_duplicate_id", `Duplicate panel id: ${item.id}`, item.id));
    }
    ids.add(item.id);
    const p = item.position;
    if (!integers(p) || p.x < 0 || p.y < 0 || p.width < 1 || p.height < 1 ||
      p.x + p.width > DASHBOARD_COLUMNS) {
      diagnostics.push(error(
        "layout_out_of_bounds",
        `Panel ${item.id} must fit within the ${DASHBOARD_COLUMNS}-column grid.`,
        item.id,
      ));
    }
    if (p.width < (item.minWidth ?? 1) || p.height < (item.minHeight ?? 1)) {
      diagnostics.push(error("layout_below_minimum", `Panel ${item.id} is below its minimum size.`, item.id));
    }
  }
  for (let i = 0; i < items.length; i += 1) {
    for (let j = i + 1; j < items.length; j += 1) {
      const left = items[i];
      const right = items[j];
      if (left && right && positionsCollide(left.position, right.position)) {
        diagnostics.push(error(
          "layout_collision",
          `Panels ${left.id} and ${right.id} overlap.`,
          `${left.id},${right.id}`,
        ));
      }
    }
  }
  return diagnostics;
}

/**
 * Create a display-only responsive projection. The canonical 12-column
 * positions are copied onto every result and are never changed or written back.
 */
export function projectLayout(
  items: readonly LayoutItem[],
  columns: number,
): ProjectedLayoutItem[] {
  if (!Number.isInteger(columns) || columns < 1 || columns > DASHBOARD_COLUMNS) {
    throw new RangeError(`columns must be between 1 and ${DASHBOARD_COLUMNS}`);
  }
  const ordered = items
    .map((item, index) => ({ item, index }))
    .sort((a, b) =>
      a.item.position.y - b.item.position.y ||
      a.item.position.x - b.item.position.x ||
      a.index - b.index,
    );
  const projected: ProjectedLayoutItem[] = [];
  for (const { item } of ordered) {
    const scaledWidth = columns === 1
      ? 1
      : Math.max(1, Math.min(columns, Math.ceil(item.position.width * columns / DASHBOARD_COLUMNS)));
    const preferredX = columns === 1
      ? 0
      : Math.min(columns - scaledWidth, Math.floor(item.position.x * columns / DASHBOARD_COLUMNS));
    const candidate = {
      x: preferredX,
      y: columns === 1 ? 0 : Math.floor(item.position.y),
      width: scaledWidth,
      height: item.position.height,
    };
    while (projected.some((placed) => positionsCollide(candidate, placed.position))) {
      candidate.y += 1;
    }
    projected.push({
      ...item,
      position: candidate,
      canonicalPosition: { ...item.position },
    });
  }
  return projected;
}

function integers(position: PanelPosition): boolean {
  return Number.isInteger(position.x) && Number.isInteger(position.y) &&
    Number.isInteger(position.width) && Number.isInteger(position.height);
}

function error(code: string, message: string, path: string): DomainDiagnostic {
  return { code, message, path, severity: "error" };
}
