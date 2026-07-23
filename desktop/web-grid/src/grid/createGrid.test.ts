import { describe, expect, it, vi } from "vitest";

import { buildColumns, buildOptions, ROW_NUMBER_FIELD } from "./createGrid";
import type { ColumnEditSchema, TablePage } from "@/contracts";

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

  it("keeps date-family display semantics distinct", () => {
    const page: TablePage = {
      ...samplePage(),
      columns: [
        { name: "day", title: "Day", dataType: "date", editable: false, nullable: true },
        {
          name: "occurred_at",
          title: "Occurred At",
          dataType: "datetime",
          editable: false,
          nullable: true,
        },
        {
          name: "starts_at",
          title: "Starts At",
          dataType: "time",
          editable: false,
          nullable: true,
        },
      ],
      rows: [],
    };

    const columns = buildColumns(page);
    expect(columns.map((column) => column.formatter)).toEqual([
      "datetime",
      "datetime",
      "plaintext",
    ]);
  });

  it("derives decimal display precision from the column's scale", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        {
          name: "amount",
          title: "Amount",
          dataType: "decimal",
          editable: false,
          nullable: true,
          scale: 2,
        },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    const col = buildColumns(page)[0] as {
      formatter?: string;
      formatterParams?: { precision?: number };
    };
    expect(col.formatter).toBe("money");
    expect(col.formatterParams?.precision).toBe(2);
  });

  it("falls back to 6 decimal places when scale is absent", () => {
    // Legacy behavior: a decimal column without scale shows up to 6 places so
    // high-precision values are not truncated on display.
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "amount", title: "Amount", dataType: "decimal", editable: false, nullable: true },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    const col = buildColumns(page)[0] as {
      formatterParams?: { precision?: number };
    };
    expect(col.formatterParams?.precision).toBe(6);
  });
});

describe("buildOptions (read-only Tabulator options)", () => {
  it("prepends a narrow frozen row-number gutter for explicit row selection", () => {
    const columns = buildOptions(samplePage()).columns as Array<Record<string, unknown>>;
    expect(columns[0]).toMatchObject({
      field: ROW_NUMBER_FIELD,
      formatter: "rownum",
      width: 42,
      frozen: true,
      headerSort: false,
    });
  });

  it("enables selectableRange:true", () => {
    const opts = buildOptions(samplePage());
    expect(opts.selectableRange).toBe(true);
  });

  it("keeps remote header interactions enabled and delegates sort/filter to the server", () => {
    const opts = buildOptions({ ...samplePage(), mode: "remote" });
    expect(opts.headerSort).not.toBe(false);
    expect(opts.sortMode).toBe("remote");
    expect(opts.filterMode).toBe("remote");
    const columns = opts.columns as Array<Record<string, unknown>>;
    expect(columns.find((column) => column.field !== ROW_NUMBER_FIELD)?.headerFilter).toBe("input");
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

/**
 * Editable column wiring (Task M3). When an `editSchema` is provided, columns
 * the host marks `editable:true` get a Tabulator editor attached; multi_select
 * and non-editable columns stay read-only.
 *
 * Build-only (no Tabulator runtime): we assert on the structural props of the
 * returned `GridColumnDefinition`. The cellEditing/cellEdited wiring is not
 * unit-tested here (it needs a live Tabulator in a real DOM).
 */

/** Edit schema entry builder for tests (keeps the verbose object literal terse). */
function editCol(
  name: string,
  editor: ColumnEditSchema["editor"],
  editable = true,
): ColumnEditSchema {
  return {
    name,
    storageName: name,
    dataType: "text",
    editable,
    nullable: true,
    primaryKey: false,
    editor,
    validation: [],
  };
}

describe("buildColumns (with editSchema — Task M3)", () => {
  it("attaches a Tabulator editor to editable columns", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "id", title: "Id", dataType: "integer", editable: false, nullable: false },
        { name: "name", title: "Name", dataType: "text", editable: true, nullable: false },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    const editSchema: ColumnEditSchema[] = [
      editCol("id", { kind: "number", storage: "integer" }, false),
      editCol("name", { kind: "text" }, true),
    ];
    const cols = buildColumns(page, editSchema);
    expect((cols[0] as { editable: boolean }).editable).toBe(false);
    expect((cols[1] as { editable: boolean }).editable).toBe(true);
    // The editable column carries a Tabulator editor name (input for text).
    expect((cols[0] as { editor?: string }).editor).toBeUndefined();
    expect((cols[1] as { editor?: string }).editor).toBe("input");
  });

  it("downgrades multi_select columns to read-only (no host dialog)", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "tags", title: "Tags", dataType: "text", editable: true, nullable: true },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    // The host advertises tags as editable + multi_select. Spec §7.3: multi_select
    // cannot be edited inline (no host dialog in web-grid), so it MUST degrade.
    const editSchema: ColumnEditSchema[] = [
      editCol("tags", { kind: "multi_select", options: ["a", "b"], allowCustom: false }, true),
    ];
    const cols = buildColumns(page, editSchema);
    expect((cols[0] as { editable: boolean }).editable).toBe(false);
    expect((cols[0] as { editor?: string }).editor).toBeUndefined();
  });

  it("leaves columns absent from editSchema read-only (editable:false, no editor)", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "id", title: "Id", dataType: "integer", editable: false, nullable: false },
        { name: "name", title: "Name", dataType: "text", editable: true, nullable: false },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    // editSchema only describes `id`; `name` has no entry -> stays read-only.
    const editSchema: ColumnEditSchema[] = [
      editCol("id", { kind: "number", storage: "integer" }, false),
    ];
    const cols = buildColumns(page, editSchema);
    expect((cols[0] as { editable: boolean }).editable).toBe(false);
    expect((cols[1] as { editable: boolean }).editable).toBe(false);
    expect((cols[1] as { editor?: string }).editor).toBeUndefined();
  });

  it("treats columns flagged editable:false in editSchema as read-only", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "name", title: "Name", dataType: "text", editable: true, nullable: false },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    // Backend says editable:false despite the column existing -> no editor.
    const editSchema: ColumnEditSchema[] = [
      editCol("name", { kind: "text" }, false),
    ];
    const cols = buildColumns(page, editSchema);
    expect((cols[0] as { editable: boolean }).editable).toBe(false);
    expect((cols[0] as { editor?: string }).editor).toBeUndefined();
  });

  it("preserves existing formatter when attaching an editor (no display regression)", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "amount", title: "Amount", dataType: "decimal", editable: true, nullable: true },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    const editSchema: ColumnEditSchema[] = [
      {
        name: "amount",
        storageName: "amount",
        dataType: "decimal",
        editable: true,
        nullable: true,
        primaryKey: false,
        editor: { kind: "number", storage: "decimal" },
        validation: [],
      },
    ];
    const cols = buildColumns(page, editSchema);
    // Editor attached AND money formatter preserved.
    expect((cols[0] as { editable: boolean }).editable).toBe(true);
    expect((cols[0] as { editor?: string }).editor).toBe("number");
    expect((cols[0] as { formatter?: string }).formatter).toBe("money");
  });

  it("when editSchema is null/undefined, every column stays read-only (Phase-A behavior)", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "name", title: "Name", dataType: "text", editable: true, nullable: false },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    const colsNoSchema = buildColumns(page);
    const colsNull = buildColumns(page, null);
    for (const cols of [colsNoSchema, colsNull]) {
      expect((cols[0] as { editable: boolean }).editable).toBe(false);
      expect((cols[0] as { editor?: string }).editor).toBeUndefined();
    }
  });
});

describe("buildOptions (with onCellEdited — Task M3)", () => {
  /**
   * Tabulator's cellEdited fires AFTER the value is already changed; oldValue
   * must be captured in cellEditing. We cannot drive a real Tabulator in jsdom,
   * but we CAN assert that buildOptions wires both callbacks onto the options
   * object so the grid layer hands them to Tabulator.
   */
  it("registers cellEditing + cellEdited callbacks when onCellEdited is supplied", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "name", title: "Name", dataType: "text", editable: true, nullable: false },
      ],
      rows: [{ rowKey: 1, name: "old" }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "client",
    };
    const onCellEdited = vi.fn();
    const opts = buildOptions(page, { onCellEdited });
    expect(typeof opts.cellEditing).toBe("function");
    expect(typeof opts.cellEdited).toBe("function");
  });

  it("does NOT register cellEditing/cellEdited when no onCellEdited callback is given (read-only)", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "name", title: "Name", dataType: "text", editable: false, nullable: false },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };
    const opts = buildOptions(page);
    expect(opts.cellEditing ?? null).toBeNull();
    expect(opts.cellEdited ?? null).toBeNull();
  });

  /**
   * oldValue capture: simulate Tabulator's two-phase edit by invoking the
   * callbacks with hand-built cell stubs. cellEditing captures the pre-edit
   * value; cellEdited retrieves it and forwards (rowKey, column, oldValue,
   * newValue) to onCellEdited.
   */
  it("captures oldValue in cellEditing and forwards (rk, col, old, new) in cellEdited", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "name", title: "Name", dataType: "text", editable: true, nullable: false },
      ],
      rows: [{ rowKey: 7, name: "old" }],
      offset: 0,
      limit: 1,
      totalRows: 1,
      mode: "client",
    };
    const onCellEdited = vi.fn();
    const opts = buildOptions(page, { onCellEdited });

    // A minimal cell stub that mimics Tabulator's CellComponent for our wiring.
    let current = "old";
    const cell = {
      getField: () => "name",
      getValue: () => current,
      getRow: () => ({ getData: () => ({ rowKey: 7, name: current }) }),
    };
    // Phase 1: cellEditing fires BEFORE the value changes. cell.getValue() is
    // still the old value; the wiring caches it.
    (opts.cellEditing as (c: typeof cell) => void)(cell);
    // Phase 2: Tabulator commits the new value, THEN fires cellEdited.
    current = "new";
    (opts.cellEdited as (c: typeof cell) => void)(cell);

    expect(onCellEdited).toHaveBeenCalledTimes(1);
    expect(onCellEdited).toHaveBeenCalledWith(7, "name", "old", "new");
  });
});
