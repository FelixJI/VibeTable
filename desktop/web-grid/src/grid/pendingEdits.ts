/**
 * B1 Task 6: in-memory optimistic-edit state for the grid.
 *
 * When the user edits a cell, the grid optimistically shows the new value
 * immediately and records a "pending edit" keyed by `(rowKey, column)`. When
 * the host commits, the pending edit is replaced by the authoritative
 * `storedValue`. If the host rejects (conflict or validation), the pending
 * edit is rolled back to the pre-edit value and the cell is flagged with a
 * `pending-cell` CSS class so the user sees the rejection.
 *
 * The pending store is a plain map (no Tabulator dependency) so it is fully
 * unit-testable without a DOM.
 */

/** A single optimistic edit awaiting commit confirmation. */
export interface PendingEdit {
  /** Pre-edit authoritative value (for rollback on rejection). */
  readonly oldValue: unknown;
  /** Optimistic value shown to the user. */
  readonly newValue: unknown;
  /** Monotonic sequence number; older edits for the same cell are superseded. */
  readonly seq: number;
}

/** Per-cell key. */
export interface CellKey {
  readonly rowKey: number | string;
  readonly column: string;
}

function keyString(k: CellKey): string {
  return `${k.rowKey}::${k.column}`;
}

/**
 * Pending-edit store. One instance per grid. Operations are pure-ish: they
 * mutate the internal map but return plain results (no DOM).
 */
export class PendingEdits {
  private readonly pending = new Map<string, PendingEdit>();
  private nextSeq = 1;

  /** True when a cell edit is awaiting commit (used to suppress duplicate sends). */
  has(key: CellKey): boolean {
    return this.pending.has(keyString(key));
  }

  /** Record an optimistic edit. Supersedes any prior pending edit for the cell. */
  set(key: CellKey, oldValue: unknown, newValue: unknown): PendingEdit {
    const edit: PendingEdit = { oldValue, newValue, seq: this.nextSeq++ };
    this.pending.set(keyString(key), edit);
    return edit;
  }

  /** Return the pending edit for a cell, or undefined. */
  get(key: CellKey): PendingEdit | undefined {
    return this.pending.get(keyString(key));
  }

  /**
   * Confirm a commit: replace the pending edit with the authoritative stored
   * value. Returns the canonical value so the grid can render it.
   */
  confirm(key: CellKey, storedValue: unknown): unknown {
    this.pending.delete(keyString(key));
    return storedValue;
  }

  /**
   * Roll back a rejected edit to its pre-edit value. Returns the value to
   * restore in the grid, or `undefined` if there was no pending edit (e.g.
   * the edit was already confirmed by a concurrent commit).
   */
  rollback(key: CellKey): unknown | undefined {
    const edit = this.pending.get(keyString(key));
    if (edit === undefined) {
      return undefined;
    }
    this.pending.delete(keyString(key));
    return edit.oldValue;
  }

  /** Drop every pending edit (used on table switch). */
  clear(): void {
    this.pending.clear();
  }

  /** Iterate pending edits (for status display). */
  entries(): readonly PendingEdit[] {
    return Array.from(this.pending.values());
  }

  /** Number of pending edits. */
  get size(): number {
    return this.pending.size;
  }
}
