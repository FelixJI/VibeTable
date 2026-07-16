import { describe, expect, it } from "vitest";

import {
  classifyClipboard,
  mapCellsToColumns,
  parseClipboard,
  PASTE_CELL_LIMIT,
} from "./clipboardParser";

describe("parseClipboard", () => {
  it("parses a simple tab-separated single row", () => {
    const parsed = parseClipboard("a\tb\tc");
    expect(parsed.rowCount).toBe(1);
    expect(parsed.columnCount).toBe(3);
    expect(parsed.cellCount).toBe(3);
    expect(parsed.cells[0].map((c) => c.rawValue)).toEqual(["a", "b", "c"]);
  });

  it("parses multiple LF-separated rows", () => {
    const parsed = parseClipboard("a\tb\nc\td");
    expect(parsed.rowCount).toBe(2);
    expect(parsed.cells[1].map((c) => c.rawValue)).toEqual(["c", "d"]);
  });

  it("normalizes CRLF and lone CR terminators", () => {
    const crlf = parseClipboard("a\tb\r\nc\td");
    const cr = parseClipboard("a\tb\rc\td");
    expect(crlf.rowCount).toBe(2);
    expect(cr.rowCount).toBe(2);
  });

  it("preserves empty cells (does not collapse them)", () => {
    const parsed = parseClipboard("a\t\tb");
    expect(parsed.cells[0].map((c) => c.rawValue)).toEqual(["a", "", "b"]);
  });

  it("preserves a trailing empty column from a trailing tab", () => {
    const parsed = parseClipboard("a\tb\t");
    expect(parsed.columnCount).toBe(3);
    expect(parsed.cells[0].map((c) => c.rawValue)).toEqual(["a", "b", ""]);
  });

  it("drops a trailing empty row produced by a final newline", () => {
    const parsed = parseClipboard("a\tb\n");
    expect(parsed.rowCount).toBe(1);
  });

  it("unescapes quoted fields and embedded tabs/quotes", () => {
    const parsed = parseClipboard('"a\tb"\t"c""d"');
    expect(parsed.cells[0].map((c) => c.rawValue)).toEqual(["a\tb", 'c"d']);
  });

  it("reports UTF-8 byte length", () => {
    const parsed = parseClipboard("é\t日");
    // é = U+00E9 = 2 UTF-8 bytes, \t = 1 byte, 日 = U+65E5 = 3 bytes.
    expect(parsed.byteCount).toBe(6);
  });

  it("throws on an empty clipboard", () => {
    expect(() => parseClipboard("")).toThrow("empty");
  });
});

describe("classifyClipboard", () => {
  it("returns the parsed clipboard when under the cap", () => {
    const parsed = parseClipboard("a\tb");
    const classified = classifyClipboard(parsed);
    expect("overflow" in classified).toBe(false);
  });

  it("returns an overflow marker when over the cap", () => {
    // Build a parsed clipboard over the limit without constructing a giant
    // string: parse a small rectangle then assert the cap constant is honoured
    // by constructing a synthetic ParsedClipboard-shaped input.
    const small = parseClipboard("a");
    const over = {
      ...small,
      cells: small.cells,
      cellCount: PASTE_CELL_LIMIT + 1,
    };
    const classified = classifyClipboard(over);
    expect("overflow" in classified && classified.overflow).toBe(true);
  });
});

describe("mapCellsToColumns", () => {
  it("maps cells onto editable columns from an anchor index", () => {
    const parsed = parseClipboard("x\ty");
    const mapped = mapCellsToColumns(parsed, ["number", "title", "amount"], 0);
    expect(mapped[0][0]).toMatchObject({ rawValue: "x", column: "number" });
    expect(mapped[0][1]).toMatchObject({ rawValue: "y", column: "title" });
  });

  it("marks out-of-range columns with a null column", () => {
    const parsed = parseClipboard("x\ty\tz");
    const mapped = mapCellsToColumns(parsed, ["number"], 0);
    expect(mapped[0][1]).toMatchObject({ rawValue: "y", column: null });
    expect(mapped[0][2]).toMatchObject({ rawValue: "z", column: null });
  });

  it("resolves relative to the anchor, not from zero", () => {
    const parsed = parseClipboard("x");
    const mapped = mapCellsToColumns(parsed, ["number", "title", "amount"], 2);
    expect(mapped[0][0]).toMatchObject({ rawValue: "x", column: "amount" });
  });
});
