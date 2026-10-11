import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import {
  classifyClipboard,
  encodeClipboardTsv,
  mapCellsToColumns,
  parseClipboard,
  PASTE_CELL_LIMIT,
} from "./clipboardParser";

interface FieldValueCorpus {
  readonly cases: readonly {
    readonly id: string;
    readonly rawValue: string;
    readonly productValue: unknown;
  }[];
}

const fieldValueCorpus = JSON.parse(
  readFileSync(
    resolve(
      process.cwd(),
      "..",
      "..",
      "contracts",
      "schema-v2",
      "fixtures",
      "field-value-entry-corpus.json",
    ),
    "utf8",
  ),
) as FieldValueCorpus;

describe("parseClipboard", () => {
  it("preserves every shared corpus raw value for authoritative normalization", () => {
    for (const test of fieldValueCorpus.cases) {
      const parsed = parseClipboard(test.rawValue);
      expect(parsed.cells[0]?.[0]?.rawValue, test.id).toEqual(test.rawValue);
    }
  });

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

  it("preserves a single quoted empty cell", () => {
    const parsed = parseClipboard('""');
    expect(parsed.cellCount).toBe(1);
    expect(parsed.cells[0][0].rawValue).toBe("");
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

  it("keeps CRLF/LF/CR inside a quoted cell as one multi-line cell (#465 regression)", () => {
    const parsed = parseClipboard('"第一行\r\n第二行"\t"lf\nonly"\t"cr\ronly"\t尾部');
    expect(parsed.rowCount).toBe(1);
    expect(parsed.columnCount).toBe(4);
    expect(parsed.cells[0].map((c) => c.rawValue)).toEqual([
      "第一行\r\n第二行",
      "lf\nonly",
      "cr\ronly",
      "尾部",
    ]);
  });

  it("keeps embedded newlines inside quotes while real terminators still split rows", () => {
    const parsed = parseClipboard('头\t"a\nb"\r\n尾行');
    expect(parsed.rowCount).toBe(2);
    expect(parsed.cells[0][1]?.rawValue).toBe("a\nb");
    expect(parsed.cells[1][0]?.rawValue).toBe("尾行");
  });

  it("treats a doubled quote after a closed quoted field as a literal quote", () => {
    const parsed = parseClipboard('"a""b"\tc');
    expect(parsed.cells[0][0]?.rawValue).toBe('a"b');
  });

  it("parses an unterminated quoted field leniently to end of input", () => {
    const parsed = parseClipboard('"未闭合\t内容');
    expect(parsed.rowCount).toBe(1);
    expect(parsed.cells[0][0]?.rawValue).toBe("未闭合\t内容");
  });

  it("preserves quotes inside an unquoted JSON value", () => {
    const json = '{"nested":{"value":8},"items":[4,5],"enabled":false}';
    const parsed = parseClipboard(json);
    expect(parsed.cells[0][0]?.rawValue).toBe(json);
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

describe("encodeClipboardTsv", () => {
  it("emits plain cells verbatim and joins rows without a trailing terminator", () => {
    expect(encodeClipboardTsv([["a", "b"], ["c", "d"]])).toBe("a\tb\nc\td");
  });

  it("quotes cells containing quotes, tabs, CR or LF and doubles quotes", () => {
    expect(encodeClipboardTsv([['含"引号"', "制\t表", "多\n行", "回\r车", "0", "false"]]))
      .toBe('"含""引号"""\t"制\t表"\t"多\n行"\t"回\r车"\t0\tfalse');
  });

  it("preserves a single quoted empty cell", () => {
    const parsed = parseClipboard('""');
    expect(parsed.cellCount).toBe(1);
    expect(parsed.cells[0][0].rawValue).toBe("");
  });

  it("preserves a trailing empty column as a trailing tab", () => {
    expect(encodeClipboardTsv([["a", ""]])).toBe("a\t");
  });

  it("round-trips multi-line/quoted/tabbed rectangles through parseClipboard", () => {
    const rectangle = [
      ["表头一", "表头二", ""],
      ["多\n行\r\n值", '含"引号"与\t制表符', "007"],
      ["0", "false", null as unknown as string],
    ];
    const encoded = encodeClipboardTsv(rectangle);
    const parsed = parseClipboard(encoded);
    expect(parsed.rowCount).toBe(3);
    expect(parsed.columnCount).toBe(3);
    expect(parsed.cells.map(row => row.map(cell => cell.rawValue))).toEqual([
      ["表头一", "表头二", ""],
      ["多\n行\r\n值", '含"引号"与\t制表符', "007"],
      ["0", "false", ""],
    ]);
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
