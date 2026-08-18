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
  on?: (event: string, handler: () => void) => void;
  off?: (event: string, handler: () => void) => void;
}

export interface StructuredGridFocusScope {
  readonly workspaceId: string | null;
  readonly sessionEpoch: number;
  readonly tableId: string | null;
}

export interface StructuredDialogFocusLease {
  restore(): void;
  cancel(): void;
}

export interface StructuredDialogFocus {
  capture(target: StructuredDialogFocusTarget): StructuredDialogFocusLease;
  dispose(): void;
}

export interface StructuredDialogFocusDependencies {
  readonly getGrid: () => StructuredGridLike | null;
  readonly getScope: () => StructuredGridFocusScope;
  readonly subscribeScope: (listener: () => void) => () => void;
}

function sameScope(left: StructuredGridFocusScope, right: StructuredGridFocusScope): boolean {
  return left.workspaceId === right.workspaceId
    && left.sessionEpoch === right.sessionEpoch
    && left.tableId === right.tableId;
}

export function createStructuredDialogFocus(
  dependencies: StructuredDialogFocusDependencies,
): StructuredDialogFocus {
  let current: StructuredDialogFocusLease | null = null;
  let disposed = false;
  const stopScope = dependencies.subscribeScope(() => current?.cancel());

  function capture(target: StructuredDialogFocusTarget): StructuredDialogFocusLease {
    current?.cancel();
    const scope = dependencies.getScope();
    let cancelled = false;
    let restoreRequested = false;
    let boundGrid: StructuredGridLike | null = null;
    let observingFocusOwnership = false;
    let restoringFocus = false;

    const attemptRestore = (grid: StructuredGridLike | null): void => {
      restoringFocus = true;
      try {
        restoreStructuredDialogFocus(grid, target);
      } finally {
        restoringFocus = false;
      }
    };

    const onDocumentFocusIn = () => {
      if (!restoringFocus) lease.cancel();
    };

    const onRenderComplete = () => {
      if (cancelled || disposed || current !== lease) return;
      if (!sameScope(scope, dependencies.getScope())) {
        lease.cancel();
        return;
      }
      attemptRestore(dependencies.getGrid());
    };

    const onWindowBlur = () => lease.cancel();

    const lease: StructuredDialogFocusLease = {
      restore(): void {
        if (cancelled || disposed || restoreRequested || current !== lease) return;
        if (!sameScope(scope, dependencies.getScope())) {
          lease.cancel();
          return;
        }
        restoreRequested = true;
        boundGrid = dependencies.getGrid();
        boundGrid?.on?.("renderComplete", onRenderComplete);
        attemptRestore(boundGrid);
        document.addEventListener("focusin", onDocumentFocusIn);
        window.addEventListener("blur", onWindowBlur);
        observingFocusOwnership = true;
      },
      cancel(): void {
        if (cancelled) return;
        cancelled = true;
        boundGrid?.off?.("renderComplete", onRenderComplete);
        if (observingFocusOwnership) {
          document.removeEventListener("focusin", onDocumentFocusIn);
          window.removeEventListener("blur", onWindowBlur);
        }
        boundGrid = null;
        if (current === lease) current = null;
      },
    };
    current = lease;
    return lease;
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    current?.cancel();
    stopScope();
  }

  return { capture, dispose };
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
  const row = rows.find((candidate) => String(candidate.getIndex?.()) === String(target.rowKey));
  if (!row) return false;
  const fallback = row.getCell?.(target.field)?.getElement?.();
  const candidates = [target.element, fallback];
  const attempted = new Set<HTMLElement>();
  for (const element of candidates) {
    if (!element?.isConnected || attempted.has(element)) continue;
    attempted.add(element);
    element.focus({ preventScroll: true });
    if (document.activeElement === element) return true;
  }
  return false;
}
