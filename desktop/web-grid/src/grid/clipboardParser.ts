/**
 * B2 Task 1: clipboard TSV parsing + client-side limits.
 *
 * The parser is a pure function: it reads a raw clipboard string and returns a
 * normalized {@link ParsedClipboard} rectangle of cells, plus the row/column/
 * cell/byte counts the Web layer uses for the 10k cell cap and the C1 overflow
 * redirect.
 *
 * The parser is a single state machine (#465) so quoted fields behave exactly
 * like the encoder on the copy side:
 *
 * * Empty cells stay as `""` (they are NOT dropped) — a paste of
 *   `"a\t\tb"` is three cells, not two.
 * * Quoted fields are unescaped (RFC 4180-style: `""` -> `"`); embedded tabs,
 *   CR, LF and CRLF inside quotes are part of the cell value and survive the
 *   round trip verbatim (no newline pre-splitting).
 * * Both CRLF (`\r\n`) and LF (`\n`) row terminators are supported outside
 *   quotes; a lone CR is also a terminator.
 * * A trailing empty row produced by a final terminator is dropped, but a
 *   trailing empty column (final tab) is preserved so column counts are
 *   stable.
 * * A quote only opens a quoted field at the START of a field; quotes inside
 *   an unquoted value (notably JSON object keys) are ordinary data.
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
  const rows = parseRows(raw);
  if (rows.length === 0) {
    throw new Error("clipboard is empty");
  }
  const grid: ParsedCell[][] = [];
  let columnCount = 0;
  for (let rowIndex = 0; rowIndex < rows.length; rowIndex += 1) {
    const cells = rows[rowIndex].map((value, columnIndex) =>
      makeCell(rowIndex, columnIndex, value),
    );
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
 * Encode a cell rectangle as TSV for the system clipboard, the exact inverse
 * of {@link parseClipboard}: a cell containing a quote, tab, CR or LF is
 * wrapped in quotes with `"` doubled; everything else is emitted verbatim
 * (so `0`, `false`, leading zeros and Unicode text keep their raw shape).
 * Rows are joined with `\n` and cells with `\t` — no trailing terminator, and
 * a trailing empty cell still yields a trailing tab so the rectangle width
 * round-trips.
 */
export function encodeClipboardTsv(rows: readonly (readonly string[])[]): string {
  return rows.map(row => row.map(encodeCell).join("\t")).join("\n");
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

function encodeCell(value: string): string {
  return /["\t\r\n]/u.test(value) ? `"${value.replaceAll('"', '""')}"` : value;
}

/** States of the TSV field state machine. */
const enum FieldState {
  /** At the start of a field; a quote here opens a quoted field. */
  Start,
  /** Inside an unquoted field. */
  Unquoted,
  /** Inside a quoted field; every char is literal data. */
  Quoted,
  /** Just closed a quoted field; a doubled quote re-opens it. */
  QuotedQuote,
}

/**
 * Split `raw` into rows of unescaped cell values using a per-character state
 * machine. Row terminators (LF / CRLF / lone CR) only count OUTSIDE quotes, so
 * embedded newlines stay inside their cell. A final terminator does not
 * produce a trailing empty row.
 */
function parseRows(raw: string): string[][] {
  const rows: string[][] = [];
  let cells: string[] = [];
  let current = "";
  let state: FieldState = FieldState.Start;

  const endField = (): void => {
    cells.push(current);
    current = "";
    state = FieldState.Start;
  };
  const endRow = (): void => {
    endField();
    rows.push(cells);
    cells = [];
  };

  for (let i = 0; i < raw.length; i += 1) {
    const char = raw[i];
    const next = raw[i + 1];
    switch (state) {
      case FieldState.Start:
        if (char === '"') {
          state = FieldState.Quoted;
        } else if (char === "\t") {
          endField();
        } else if (char === "\n" || (char === "\r" && next === "\n")) {
          endRow();
          if (char === "\r") i += 1; // consume the LF of a CRLF pair
        } else if (char === "\r") {
          endRow();
        } else {
          current += char;
          state = FieldState.Unquoted;
        }
        break;
      case FieldState.Unquoted:
        if (char === "\t") {
          endField();
        } else if (char === "\n" || (char === "\r" && next === "\n")) {
          endRow();
          if (char === "\r") i += 1;
        } else if (char === "\r") {
          endRow();
        } else {
          // A quote mid-field (notably inside JSON values) is ordinary data.
          current += char;
        }
        break;
      case FieldState.Quoted:
        if (char === '"') {
          state = FieldState.QuotedQuote;
        } else {
          // Embedded tab/newline/CR inside quotes is literal cell content.
          current += char;
        }
        break;
      case FieldState.QuotedQuote:
        if (char === '"') {
          current += '"';
          state = FieldState.Quoted;
        } else if (char === "\t") {
          endField();
        } else if (char === "\n" || (char === "\r" && next === "\n")) {
          endRow();
          if (char === "\r") i += 1;
        } else if (char === "\r") {
          endRow();
        } else {
          // Lenient: trailing garbage right after a closing quote joins the
          // field (matches the historical `"a"b` -> `ab` behaviour).
          current += char;
          state = FieldState.Unquoted;
        }
        break;
    }
  }

  // End of input: keep the pending row only when it carries any cell content;
  // a row left pending right after a terminator (e.g. "a\tb\n") is dropped.
  if (current !== "" || cells.length > 0) {
    endRow();
  }
  return rows;
}

function makeCell(rowIndex: number, columnIndex: number, value: string): ParsedCell {
  return { rowIndex, columnIndex, rawValue: value };
}
