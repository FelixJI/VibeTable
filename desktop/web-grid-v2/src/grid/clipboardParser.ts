/**
 * B2 Task 1: clipboard TSV parsing + client-side limits.
 *
 * The parser is a pure function: it reads a raw clipboard string and returns a
 * normalized {@link ParsedClipboard} rectangle of cells, plus the row/column/
 * cell/byte counts the Web layer uses for the 10k cell cap and the C1 overflow
 * redirect.
 *
 * The parser deliberately preserves the semantics the host's preview needs:
 *
 * * Empty cells stay as `""` (they are NOT dropped) — a paste of
 *   `"a\t\tb"` is three cells, not two.
 * * Quoted fields are unescaped (RFC 4180-style: `""` -> `"`); an embedded
 *   tab/newline inside quotes is part of the cell value.
 * * Both CRLF (`\r\n`) and LF (`\n`) row terminators are supported; a lone CR
 *   is normalized to LF.
 * * A trailing empty row produced by a final terminator is dropped, but a
 *   trailing empty column (final tab) is preserved so column counts are
 *   stable.
 *
 * The caller enforces the 10k cell cap via {@link PASTE_CELL_LIMIT}; oversize
 * clipboards are NOT truncated — the caller surfaces a clear "use file import"
 * path instead.
 */

/** Hard cap on parsed clipboard cells. Mirrors the backend constant. */
export const PASTE_CELL_LIMIT = 10_000;

/** One parsed clipboard cell, before it is mapped to a collection field. */
export interface ParsedCell {
  /** 0-based row offset into the parsed rectangle. */
  readonly rowIndex: number;
  /** 0-based column offset into the parsed rectangle. */
  readonly columnIndex: number;
  /** The raw, unescaped cell text (empty string for an empty cell). */
  readonly rawValue: string;
}

/** The normalized result of parsing a clipboard string. */
export interface ParsedClipboard {
  /** Cells in row-major order. */
  readonly cells: readonly (readonly ParsedCell[])[];
  readonly rowCount: number;
  readonly columnCount: number;
  readonly cellCount: number;
  readonly byteCount: number;
}

/** A clipboard that exceeds the {@link PASTE_CELL_LIMIT}. */
export interface OverflowClipboard {
  readonly overflow: true;
  readonly cellCount: number;
  readonly byteCount: number;
}

/**
 * Parse a raw clipboard string into a normalized cell rectangle.
 *
 * Throws when the clipboard is empty (no rows). Oversize clipboards are
 * returned as an {@link OverflowClipboard} (the caller decides the redirect);
 * the cells are NOT truncated so the caller never silently writes a subset.
 */
export function parseClipboard(raw: string): ParsedClipboard {
  const byteCount = byteLength(raw);
  const rows = splitRows(raw);
  if (rows.length === 0) {
    throw new Error("clipboard is empty");
  }
  const grid: ParsedCell[][] = [];
  let columnCount = 0;
  for (let rowIndex = 0; rowIndex < rows.length; rowIndex += 1) {
    const cells = parseRow(rows[rowIndex], rowIndex);
    grid.push(cells);
    if (cells.length > columnCount) {
      columnCount = cells.length;
    }
  }
  const cellCount = grid.reduce((sum, row) => sum + row.length, 0);
  return {
    cells: grid,
    rowCount: grid.length,
    columnCount,
    cellCount,
    byteCount,
  };
}

/**
 * Classify a parsed clipboard against the 10k cell cap. Returns an overflow
 * marker (do not write) when the cap is exceeded, so the caller can redirect
 * the user to the C1 file-import path without truncating the data.
 */
export function classifyClipboard(
  parsed: ParsedClipboard,
): ParsedClipboard | OverflowClipboard {
  if (parsed.cellCount > PASTE_CELL_LIMIT) {
    return {
      overflow: true,
      cellCount: parsed.cellCount,
      byteCount: parsed.byteCount,
    };
  }
  return parsed;
}

/** A parsed cell mapped onto a (possibly null) collection field name. */
export interface MappedCell extends ParsedCell {
  /** Resolved editable column name, or null when the column is out of range. */
  readonly column: string | null;
}

/**
 * Map a parsed rectangle onto the editable columns starting from an anchor
 * column index. Produces the cell payload the host's `table.previewPaste`
 * expects. Out-of-range columns are kept (with `column: null`) so the preview
 * can attach a localized "out of range" error rather than silently dropping
 * the cell.
 */
export function mapCellsToColumns(
  parsed: ParsedClipboard,
  editableColumns: readonly string[],
  anchorColumnIndex: number,
): MappedCell[][] {
  return parsed.cells.map((row) =>
    row.map((cell) => {
      const resolvedIndex = anchorColumnIndex + cell.columnIndex;
      const column =
        resolvedIndex >= 0 && resolvedIndex < editableColumns.length
          ? editableColumns[resolvedIndex]
          : null;
      return {
        rowIndex: cell.rowIndex,
        columnIndex: cell.columnIndex,
        rawValue: cell.rawValue,
        column,
      };
    }),
  );
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

function byteLength(value: string): number {
  // TextEncoder measures UTF-8 bytes, matching the host's 4 MiB message cap.
  if (typeof TextEncoder !== "undefined") {
    return new TextEncoder().encode(value).length;
  }
  return value.length;
}

function splitRows(raw: string): string[] {
  // Normalize CRLF/CR to LF, then split. A trailing terminator yields a final
  // empty string we drop (a paste ending with a newline is not an extra row).
  const normalized = raw.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  const rows = normalized.split("\n");
  if (rows.length > 0 && rows[rows.length - 1] === "") {
    rows.pop();
  }
  return rows;
}

/**
 * Parse one TSV row into cells, honouring quoted fields. A quoted field may
 * contain embedded tabs/newlines (already split across rows by the caller, so
 * in practice only tabs survive here) and escapes `""` as a literal `"`.
 */
function parseRow(line: string, rowIndex: number): ParsedCell[] {
  const cells: ParsedCell[] = [];
  let columnIndex = 0;
  let current = "";
  let inQuotes = false;

  for (let i = 0; i < line.length; i += 1) {
    const char = line[i];
    if (inQuotes) {
      if (char === '"') {
        if (line[i + 1] === '"') {
          current += '"';
          i += 1;
        } else {
          inQuotes = false;
        }
      } else {
        current += char;
      }
    } else if (char === '"') {
      inQuotes = true;
    } else if (char === "\t") {
      cells.push(makeCell(rowIndex, columnIndex, current));
      columnIndex += 1;
      current = "";
    } else {
      current += char;
    }
  }
  cells.push(makeCell(rowIndex, columnIndex, current));
  return cells;
}

function makeCell(rowIndex: number, columnIndex: number, value: string): ParsedCell {
  return { rowIndex, columnIndex, rawValue: value };
}
