// Pure helpers for grid keyboard navigation. The composable wires these to
// Tabulator's range API; tests cover the math, not the DOM. Task 15.

export interface CellPos {
  row: number;
  col: number;
}

export interface GridBounds {
  rowCount: number;
  colCount: number;
}

export function moveUp(pos: CellPos, _b: GridBounds): CellPos {
  return { ...pos, row: Math.max(0, pos.row - 1) };
}

export function moveDown(pos: CellPos, b: GridBounds): CellPos {
  return { ...pos, row: Math.min(b.rowCount - 1, pos.row + 1) };
}

export function moveLeft(pos: CellPos, _b: GridBounds): CellPos {
  return { ...pos, col: Math.max(0, pos.col - 1) };
}

export function moveRight(pos: CellPos, b: GridBounds): CellPos {
  return { ...pos, col: Math.min(b.colCount - 1, pos.col + 1) };
}

/** Tab at the right edge wraps to next row's first cell (Feishu-style). */
export function tabForward(pos: CellPos, b: GridBounds): CellPos {
  if (pos.col < b.colCount - 1) return moveRight(pos, b);
  if (pos.row < b.rowCount - 1) return { row: pos.row + 1, col: 0 };
  return pos;
}

/** Shift+Tab at the left edge wraps to previous row's last cell. */
export function tabBackward(pos: CellPos, b: GridBounds): CellPos {
  if (pos.col > 0) return moveLeft(pos, b);
  if (pos.row > 0) return { row: pos.row - 1, col: b.colCount - 1 };
  return pos;
}
