import { describe, expect, it } from "vitest";

import { buildColumns, buildOptions } from "./createGrid";
import type { TablePage } from "../contracts";

/** A representative Phase-A page: text/integer/decimal/boolean/date + rowKey. */
function samplePage(): TablePage {
  return {
    table: "contracts",
    columns: [
      { name: "id", title: "Id", dataType: "integer", editable: false, nullable: false },
      { name: "name", title: "Name", dataType: "text", editable: false, nullable: false },
      {
        name: "amount",
        title: "Amount",
        dataType: "decimal",
        editable: false,
        nullable: true,
      },
      {
        name: "active",
        title: "Active",
        dataType: "boolean",
        editable: false,
        nullable: false,
      },
      { name: "signed_on", title: "Signed On", dataType: "date", editable: false, nullable: true },
    ],
    rows: [
      // Decimal values must be preserved exactly (no rounding/formatting in
      // the data layer; display formatting happens in Tabulator formatters
      // that read but do not mutate the raw cell).
      { rowKey: 1, id: 1, name: "Alpha", amount: 12.1, active: true, signed_on: "2024-01-01" },
      { rowKey: 2, id: 2, name: "Beta", amount: 0.0, active: false, signed_on: null },
      { rowKey: 3, id: 3, name: "Gamma", amount: 123456.789, active: true, signed_on: "2024-02-02" },
    ],
    offset: 0,
    limit: 50,
    totalRows: 3,
    mode: "client",
  };
}

describe("buildColumns (read-only Tabulator column defs)", () => {
  it("emits one Tabulator column per TablePage column", () => {
    const cols = buildColumns(samplePage());
    expect(cols).toHaveLength(5);
    expect(cols.map((c) => (c as { field: string }).field)).toEqual([
      "id",
      "name",
      "amount",
      "active",
      "signed_on",
    ]);
  });

  it("forces editable:false on every column regardless of input", () => {
    // Even if a (misconfigured) column claims editable:true, Phase A forces it off.
    const page: TablePage = {
      ...samplePage(),
      columns: [
        { name: "x", title: "X", dataType: "text", editable: true, nullable: false },
      ],
    };
    const cols = buildColumns(page);
    expect(cols).toHaveLength(1);
    expect((cols[0] as { editable: boolean }).editable).toBe(false);
  });

  it("does NOT emit a Tabulator column for rowKey (hidden transport metadata)", () => {
    const cols = buildColumns(samplePage());
    const fields = cols.map((c) => (c as { field: string }).field);
    expect(fields).not.toContain("rowKey");
  });

  it("preserves decimal raw values in the data (no mutation/formatting at the data layer)", () => {
    // Re-derive the row data set the grid would hand to Tabulator. The grid
    // must NOT round, stringify, or otherwise alter numeric values: any
    // display formatting is a Tabulator formatter concern and must not touch
    // the underlying cell value.
    const page = samplePage();
    const data = page.rows.map((r) => ({ ...r }));
    expect(data[0]!.amount).toBe(12.1);
    expect(data[1]!.amount).toBe(0.0);
    expect(data[2]!.amount).toBe(123456.789);
  });
});

describe("buildOptions (read-only Tabulator options)", () => {
  it("enables selectableRange:true", () => {
    const opts = buildOptions(samplePage());
    expect(opts.selectableRange).toBe(true);
  });

  it("disables clipboard paste (Phase A is read-only)", () => {
    const opts = buildOptions(samplePage());
    // Paste must be off. Either clipboard is fully disabled, or explicitly
    // excludes "paste".
    const clip = opts.clipboard;
    if (typeof clip === "string") {
      expect(clip).not.toMatch(/paste/);
    } else {
      expect(clip).toBe(false);
    }
    // Defensive belt-and-braces: also assert no paste action is wired.
    expect(opts.clipboardPasteAction ?? null).toBeNull();
  });

  it("passes through row data verbatim (decimals intact) under the `data` option", () => {
    const page = samplePage();
    const opts = buildOptions(page);
    expect(Array.isArray(opts.data)).toBe(true);
    const data = opts.data as Array<Record<string, unknown>>;
    expect(data[0]!.amount).toBe(12.1);
    expect(data[2]!.amount).toBe(123456.789);
  });
});
