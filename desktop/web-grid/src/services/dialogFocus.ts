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
}

export interface StructuredGridLike {
  getRow?: (rowKey: string | number) => StructuredRowLike | null | undefined;
}

/**
 * Restore focus to the structured cell that opened a dialog.
 *
 * Tabulator can replace a cell DOM node while its range module settles. The
 * original trigger is preferred, but row/field identity lets us resolve the
 * current node when that happens.
 */
export function restoreStructuredDialogFocus(
  grid: StructuredGridLike | null,
  target: StructuredDialogFocusTarget | null,
): boolean {
  if (!target) return false;
  const fallback = grid
    ?.getRow?.(target.rowKey)
    ?.getCell?.(target.field)
    ?.getElement?.();
  const element = target.element?.isConnected ? target.element : fallback;
  if (!element?.isConnected) return false;
  element.focus({ preventScroll: true });
  return document.activeElement === element;
}
