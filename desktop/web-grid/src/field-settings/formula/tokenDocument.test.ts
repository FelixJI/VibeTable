import { describe, expect, it } from "vitest";
import type { FormulaAuthorDocument, FormulaAuthorToken } from "@/contracts/generated/workbench";
import {
  applySourceEdit,
  emptyFormulaAuthorDocument,
  formatTextRange,
  insertPlainText,
  insertReference,
  isFormulaAuthorDocument,
  isFormulaTextRange,
  offsetToPosition,
  positionToOffset,
  textRangeToSelection,
  tokenSpans,
} from "./tokenDocument";

function fieldToken(fieldId: string, range: FormulaAuthorToken["range"]): FormulaAuthorToken {
  return { range, kind: "field", fieldId, relationFieldId: null, targetFieldId: null };
}

function relationTargetToken(
  relationFieldId: string,
  targetFieldId: string,
  range: FormulaAuthorToken["range"],
): FormulaAuthorToken {
  return {
    range,
    kind: "relationTarget",
    fieldId: targetFieldId,
    relationFieldId,
    targetFieldId,
  };
}

function tokenAt(source: string, fieldId: string, start: number, end: number): FormulaAuthorToken {
  return fieldToken(fieldId, {
    start: offsetToPosition(source, start),
    end: offsetToPosition(source, end),
  });
}

describe("tokenDocument UTF-16 coordinates", () => {
  it("round-trips offsets across CRLF and surrogate pairs", () => {
    const source = "IF(true,\r\n\"🙂({单价})\",\n1)";
    for (const offset of [0, 1, 12, 13, 14, 15, 16, 23]) {
      expect(positionToOffset(source, offsetToPosition(source, offset))).toBe(offset);
    }
    expect(offsetToPosition(source, 23)).toEqual({ line: 2, character: 1 });
    // A surrogate pair advances the UTF-16 character by two.
    expect(offsetToPosition(source, 12)).toEqual({ line: 1, character: 2 });
    expect(offsetToPosition(source, 13)).toEqual({ line: 1, character: 3 });
  });

  it("treats lone CR as a line terminator like the Go author coordinates", () => {
    const source = "a\rb\nc";
    expect(offsetToPosition(source, 2)).toEqual({ line: 1, character: 0 });
    expect(positionToOffset(source, { line: 2, character: 0 })).toBe(4);
  });

  it("validates wire documents and ranges without trusting their shape", () => {
    expect(isFormulaAuthorDocument(emptyFormulaAuthorDocument())).toBe(true);
    expect(isFormulaAuthorDocument({ displaySource: "1", tokens: [], documentRevision: 0 }))
      .toBe(false);
    expect(isFormulaTextRange({
      start: { line: 0, character: 3 },
      end: { line: 0, character: 9 },
    })).toBe(true);
    expect(isFormulaTextRange({ start: { line: 0 } })).toBe(false);
  });

  it("formats one-based human positions and maps them back to selections", () => {
    const range = {
      start: { line: 1, character: 4 },
      end: { line: 1, character: 10 },
    };
    expect(formatTextRange(range)).toBe("第2行 第5列");
    expect(textRangeToSelection("ab\ncdefghijkl", range)).toEqual({ start: 7, end: 13 });
  });
});

describe("tokenDocument edits", () => {
  it("shifts untouched tokens and drops only tokens touched by the edit", () => {
    const source = "{单价} + {运费} + {备注}";
    const document: FormulaAuthorDocument = {
      displaySource: source,
      documentRevision: 4,
      tokens: [
        tokenAt(source, "fld_price", 0, 4),
        tokenAt(source, "fld_shipping", 7, 11),
        tokenAt(source, "fld_note", 14, 18),
      ],
    };
    // Edit inside "{运费}" drops just that token.
    const edited = applySourceEdit(document, source, "{单价} + {运 费} + {备注}");
    expect(edited.documentRevision).toBe(5);
    expect(edited.tokens.map(token => token.fieldId)).toEqual(["fld_price", "fld_note"]);
    expect(edited.tokens[1]?.range).toEqual({
      start: { line: 0, character: 15 },
      end: { line: 0, character: 19 },
    });
    // Insertion before all tokens shifts every token by the length delta.
    const shifted = applySourceEdit(document, source, "1 + " + source);
    expect(shifted.tokens.map(token => token.fieldId))
      .toEqual(["fld_price", "fld_shipping", "fld_note"]);
    expect(tokenSpans(shifted)[0]).toMatchObject({ start: 4, end: 8 });
  });

  it("keeps tokens byte-stable when the edit only appends text", () => {
    const source = 'CONCATENATE({名}, "-")';
    const document: FormulaAuthorDocument = {
      displaySource: source,
      documentRevision: 2,
      tokens: [tokenAt(source, "fld_name", 12, 15)],
    };
    const appended = applySourceEdit(document, source, source + " + 1");
    expect(appended.tokens).toHaveLength(1);
    expect(tokenSpans(appended)[0]).toMatchObject({ start: 12, end: 15 });
  });

  it("never rewrites string literal content because it performs no name parsing", () => {
    const source = '"SUM({金额})"';
    const document: FormulaAuthorDocument = {
      displaySource: source,
      documentRevision: 1,
      tokens: [],
    };
    const edited = applySourceEdit(document, source, '"SUM({金额})" + {数量}');
    expect(edited.displaySource.endsWith('+ {数量}')).toBe(true);
    expect(edited.displaySource.startsWith('"SUM({金额})"')).toBe(true);
    // The helper cannot know the literal is special: it stays verbatim and no
    // token is fabricated for text inside it.
    expect(edited.tokens).toEqual([]);
  });

  it("degrades safely when the document and source disagree", () => {
    const document: FormulaAuthorDocument = {
      displaySource: "{旧}",
      documentRevision: 3,
      tokens: [tokenAt("{旧}", "fld_old", 0, 4)],
    };
    const edited = applySourceEdit(document, "{旧}", "{新}");
    expect(edited.tokens).toEqual([]);
    expect(edited.documentRevision).toBe(4);
    expect(edited.displaySource).toBe("{新}");
  });

  it("replaces the selection and binds a stable field token at the cursor", () => {
    const source = "{单价} * 2";
    const document: FormulaAuthorDocument = {
      displaySource: source,
      documentRevision: 7,
      tokens: [tokenAt(source, "fld_price", 0, 4)],
    };
    const insertion = insertReference(document, source, 8, 9, {
      text: " + {备注}",
      token: fieldToken("fld_note", {
        start: { line: 0, character: 0 },
        end: { line: 0, character: 0 },
      }),
      labelStart: 3,
      labelLength: 4,
    });
    expect(insertion.source).toBe("{单价} * 2 + {备注}");
    expect(insertion.caret).toBe(15);
    expect(insertion.document.documentRevision).toBe(8);
    const spans = tokenSpans(insertion.document);
    expect(spans.map(span => [span.token.fieldId, span.start, span.end])).toEqual([
      ["fld_price", 0, 4],
      ["fld_note", 11, 15],
    ]);
  });

  it("binds one relationTarget token over the whole aggregate path", () => {
    const insertion = insertReference(emptyFormulaAuthorDocument(), "", 0, 0, {
      text: "SUM({明细}.{金额})",
      token: relationTargetToken("fld_lines", "fld_amount", {
        start: { line: 0, character: 0 },
        end: { line: 0, character: 0 },
      }),
      labelStart: 4,
      labelLength: 9,
    });
    expect(insertion.source).toBe("SUM({明细}.{金额})");
    const spans = tokenSpans(insertion.document);
    expect(spans).toHaveLength(1);
    expect(spans[0]).toMatchObject({ start: 4, end: 13 });
    expect(spans[0]?.token.kind).toBe("relationTarget");
  });

  it("inserts plain text for examples without fabricating tokens", () => {
    const source = "{单价} * 2";
    const document: FormulaAuthorDocument = {
      displaySource: source,
      documentRevision: 1,
      tokens: [tokenAt(source, "fld_price", 0, 4)],
    };
    const insertion = insertPlainText(document, source, 9, 9, " + IFERROR({单价}/0, 0)");
    expect(insertion.source).toBe("{单价} * 2 + IFERROR({单价}/0, 0)");
    expect(insertion.caret).toBe(insertion.source.length);
    expect(insertion.document.tokens).toHaveLength(1);
    expect(insertion.document.tokens[0]?.fieldId).toBe("fld_price");
  });

  it("keeps a surrogate-pair label whole while shifting over an emoji prefix", () => {
    const source = "{🙂}+{单价}";
    const document: FormulaAuthorDocument = {
      displaySource: source,
      documentRevision: 1,
      tokens: [
        tokenAt(source, "fld_emoji", 0, 4),
        tokenAt(source, "fld_price", 5, 9),
      ],
    };
    const edited = applySourceEdit(document, source, "{🙂!}+{单价}");
    expect(edited.tokens.map(token => token.fieldId)).toEqual(["fld_price"]);
    expect(tokenSpans(edited)[0]).toMatchObject({ start: 6, end: 10 });
  });
});

describe("tokenDocument token identity validation", () => {
  const span = (start: number, end: number) => ({
    start: { line: 0, character: start },
    end: { line: 0, character: end },
  });

  it("accepts table and sourceField tokens with closed identities", () => {
    expect(isFormulaAuthorDocument({
      displaySource: "TABLE({出货}) + CurrentValue.{金额}",
      documentRevision: 2,
      tokens: [
        {
          range: span(6, 10),
          kind: "table",
          fieldId: null,
          tableId: "tbl_ship",
          relationFieldId: null,
          targetFieldId: null,
        },
        {
          range: span(27, 31),
          kind: "sourceField",
          fieldId: "fld_amount",
          tableId: "tbl_ship",
          relationFieldId: null,
          targetFieldId: null,
        },
      ],
    })).toBe(true);
    // Old relation identities keep passing unchanged.
    expect(isFormulaAuthorDocument({
      displaySource: "SUM({明细}.{金额})",
      documentRevision: 1,
      tokens: [{
        range: span(4, 13),
        kind: "relationTarget",
        fieldId: "fld_amount",
        relationFieldId: "fld_lines",
        targetFieldId: "fld_amount",
      }],
    })).toBe(true);
  });

  it("rejects mixed kind/fieldId/tableId combinations", () => {
    const cases: unknown[] = [
      // table tokens must not carry a fieldId and require tableId.
      { range: span(6, 10), kind: "table", fieldId: "fld_x", tableId: "tbl_ship", relationFieldId: null, targetFieldId: null },
      { range: span(6, 10), kind: "table", fieldId: null, relationFieldId: null, targetFieldId: null },
      // sourceField tokens need both identities.
      { range: span(6, 10), kind: "sourceField", fieldId: "fld_x", tableId: null, relationFieldId: null, targetFieldId: null },
      { range: span(6, 10), kind: "sourceField", fieldId: "fld_x", relationFieldId: null, targetFieldId: null },
      // Old kinds must not mix in a tableId or drop their field identity.
      { range: span(6, 10), kind: "field", fieldId: "fld_x", tableId: "tbl_ship", relationFieldId: null, targetFieldId: null },
      { range: span(6, 10), kind: "field", fieldId: null, relationFieldId: null, targetFieldId: null },
      { range: span(6, 10), kind: "relationTarget", fieldId: "fld_x", relationFieldId: "fld_lines", targetFieldId: null },
    ];
    for (const token of cases) {
      expect(isFormulaAuthorDocument({
        displaySource: "TABLE({出货})",
        documentRevision: 1,
        tokens: [token],
      })).toBe(false);
    }
  });

  it("rejects unknown token fields, bad kinds, and malformed ranges", () => {
    const cases: unknown[] = [
      { range: span(0, 4), kind: "field", fieldId: "fld_price", relationFieldId: null, targetFieldId: null, physicalName: "f_price" },
      { range: span(0, 4), kind: "cursor", fieldId: "fld_price", relationFieldId: null, targetFieldId: null },
      { range: span(0, 99), kind: "field", fieldId: "fld_price", relationFieldId: null, targetFieldId: null },
      { range: span(4, 0), kind: "field", fieldId: "fld_price", relationFieldId: null, targetFieldId: null },
      { range: span(0, 0), kind: "field", fieldId: "fld_price", relationFieldId: null, targetFieldId: null },
      { range: { start: { line: 3, character: 0 }, end: { line: 3, character: 1 } }, kind: "field", fieldId: "fld_price", relationFieldId: null, targetFieldId: null },
      { range: { start: { line: 0, character: 0 } }, kind: "field", fieldId: "fld_price", relationFieldId: null, targetFieldId: null },
    ];
    for (const token of cases) {
      expect(isFormulaAuthorDocument({
        displaySource: "{单价} + 1",
        documentRevision: 1,
        tokens: [token],
      })).toBe(false);
    }
  });
});
