export interface StructuredDialogFocusTarget {
  element: HTMLElement | null;
  rowKey: string | number;
  field: string;
}

interface StructuredCellLike {
  getElement?: () => HTMLElement;
}

interface StructuredRowLike {
  getCell?: (field: string) => StructuredCellLike | null | undefined;
  getIndex?: () => string | number;
}

export interface StructuredGridLike {
  getRows?: () => readonly StructuredRowLike[] | null | undefined;
}

/**
 * Restore focus to the structured cell that opened a dialog.
 *
 * Tabulator can replace a cell DOM node while its range module settles. The
 * original trigger is preferred, but row/field identity lets us resolve the
 * current node when that happens. The row is resolved from one enumerated
 * `getRows` snapshot matched by `getIndex` instead of Tabulator's `getRow`,
 * whose miss path emits the "Find Error - No matching row found" console
 * warning that product E2E treats as a renderer contract violation.
 */
export function restoreStructuredDialogFocus(
  grid: StructuredGridLike | null,
  target: StructuredDialogFocusTarget | null,
): boolean {
  if (!target) return false;
  const rows = grid?.getRows?.() ?? [];
  const fallback = rows
    .find((row) => String(row.getIndex?.()) === String(target.rowKey))
    ?.getCell?.(target.field)
    ?.getElement?.();
  const element = target.element?.isConnected ? target.element : fallback;
  if (!element?.isConnected) return false;
  element.focus({ preventScroll: true });
  return document.activeElement === element;
}
