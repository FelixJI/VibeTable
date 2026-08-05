import { describe, expect, it, vi } from "vitest";

import {
  buildColumns,
  buildEditEventHandlers,
  buildOptions,
  buildTabulatorColumns,
  jsonHeaderFilter,
  jsonValueFormatter,
  ROW_KEY_FIELD,
  ROW_NUMBER_FIELD,
} from "./createGrid";
import type { GridColumnDefinition } from "./createGrid";
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
  it("renders formula recalculation state and diagnostic instead of a stale value", () => {
    const page: TablePage = {
      ...samplePage(),
      columns: [{
        name: "total", title: "Total", kind: "formula", dataType: "decimal",
        editable: false, nullable: true,
      }],
    };
    const column = buildColumns(page)[0];
    const formatter = column?.formatter as (cell: { getValue(): unknown }) => HTMLElement;
    const rendered = formatter({ getValue: () => ({
      state: "error", value: null,
      diagnostic: { code: "calculation.failed", message: "boom" },
    }) });

    expect(rendered.textContent).toBe("计算失败");
    expect(rendered.title).toBe("boom");
  });

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

  it("sets readable field-family minimum widths so dense grids scroll horizontally", () => {
    const page: TablePage = {
      ...samplePage(),
      columns: [
        { name: "enabled", title: "Enabled", dataType: "boolean", editable: false, nullable: false },
        { name: "count", title: "Count", dataType: "integer", editable: false, nullable: false },
        { name: "due", title: "Due", dataType: "date", editable: false, nullable: false },
        { name: "created", title: "Created", dataType: "datetime", editable: false, nullable: false },
        { name: "name", title: "Name", dataType: "text", editable: false, nullable: false },
        { name: "payload", title: "Payload", dataType: "json", editable: false, nullable: false },
        {
          name: "files",
          title: "Files",
          dataType: "json",
          kind: "attachment",
          editable: false,
          nullable: false,
        },
      ],
      rows: [],
    };

    expect(buildColumns(page).map(({ minWidth }) => minWidth))
      .toEqual([100, 120, 132, 180, 160, 190, 190]);
    expect(buildOptions(page).layout).toBe("fitColumns");
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
    expect(typeof columns[0]?.formatter).toBe("function");
    expect(typeof columns[1]?.formatter).toBe("function");
    expect(columns[2]?.formatter).toBe("plaintext");
    const dateValue = "2026-07-25";
    const dateTimeValue = "2026-07-25T08:30:45.123Z";
    const dateFormatter = columns[0]?.formatter as (
      cell: { getValue(): unknown },
    ) => HTMLElement;
    const dateTimeFormatter = columns[1]?.formatter as (
      cell: { getValue(): unknown },
      params?: { readonly timeZone?: string },
    ) => HTMLElement;
    expect(dateFormatter({ getValue: () => dateValue }).textContent).toBe(dateValue);
    const utc = dateTimeFormatter(
      { getValue: () => dateTimeValue },
      { timeZone: "UTC" },
    );
    expect(utc.textContent).toBe("2026-07-25 08:30:45.123");
    expect(utc.title).toBe(dateTimeValue);
    expect(dateTimeFormatter(
      { getValue: () => dateTimeValue },
      { timeZone: "Asia/Shanghai" },
    ).textContent).toBe("2026-07-25 16:30:45.123");
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

  it("renders JSON as a compact safe summary without changing the raw value", () => {
    const value = {
      status: "<img src=x onerror=alert(1)>",
      nested: { html: "<script>alert(1)</script>" },
    };
    const rendered = jsonValueFormatter({ getValue: () => value });
    expect(rendered.textContent).toBe("{…} · 2 个键");
    expect(rendered.title).toBe(JSON.stringify(value));
    expect(rendered.querySelector("script")).toBeNull();
    expect(rendered.innerHTML).not.toContain("<img");
    expect(rendered.innerHTML).not.toContain("<script");
    expect(value).toEqual({
      status: "<img src=x onerror=alert(1)>",
      nested: { html: "<script>alert(1)</script>" },
    });
  });

  it("summarizes JSON arrays and renders null as an em dash", () => {
    expect(jsonValueFormatter({ getValue: () => [1, 2, 3] }).textContent)
      .toBe("[…] · 3 项");
    expect(jsonValueFormatter({ getValue: () => null }).textContent).toBe("—");
  });

  it("filters JSON by nested serialized values without mutating or throwing", () => {
    const matching = { nested: { value: 8 }, items: [4, 5], label: "Alpha" };
    const other = { nested: { value: 7 }, items: [1, 2, 3] };
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;

    expect(jsonHeaderFilter("8", matching)).toBe(true);
    expect(jsonHeaderFilter("8", other)).toBe(false);
    expect(jsonHeaderFilter("alpha", matching)).toBe(true);
    expect(jsonHeaderFilter("", matching)).toBe(true);
    expect(jsonHeaderFilter("self", cyclic)).toBe(false);
    expect(matching).toEqual({
      nested: { value: 8 },
      items: [4, 5],
      label: "Alpha",
    });
  });

  it("wires the structured JSON filter into the Tabulator column", () => {
    const page: TablePage = {
      table: "t",
      columns: [
        { name: "payload", title: "Payload", dataType: "json", editable: true, nullable: true },
      ],
      rows: [],
      offset: 0,
      limit: 0,
      totalRows: 0,
      mode: "client",
    };

    expect(buildColumns(page)[0]?.headerFilterFunc).toBe(jsonHeaderFilter);
    expect(buildTabulatorColumns(page)[1]?.headerFilterFunc).toBe(jsonHeaderFilter);

    const interactive = buildColumns(
      page,
      [editCol("payload", { kind: "json" }, true)],
    )[0]!;
    const element = document.createElement("div");
    const cell = {
      getField: () => "payload",
      getValue: () => ({}),
      setValue: vi.fn(),
      getRow: () => ({ getData: () => ({ rowKey: "row-1" }) }),
      getElement: () => element,
    };
    const formatter = interactive.formatter as (
      formattedCell: typeof cell,
      params: Record<string, unknown>,
      onRendered: (callback: () => void) => void,
    ) => HTMLElement;
    formatter(cell, {}, (callback) => callback());
    expect(element.tabIndex).toBe(0);
    expect(element.getAttribute("aria-keyshortcuts")).toContain("Enter");
    expect(interactive).not.toHaveProperty("cellRendered");
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

  it("renders a localized empty-table placeholder", () => {
    expect(buildOptions(samplePage()).placeholder)
      .toBe("暂无记录，使用“+”添加第一行");
  });

  it("does not leak product-only metadata into Tabulator column options", () => {
    const columns = buildOptions(samplePage()).columns as Array<Record<string, unknown>>;
    expect(columns.every((column) => !("dataType" in column))).toBe(true);
    expect(columns.every((column) => !("nullable" in column))).toBe(true);
  });

  it("strips product-only metadata for every incremental setColumns refresh", () => {
    const columns = buildTabulatorColumns(samplePage());
    expect(columns.every((column) => !("dataType" in column))).toBe(true);
    expect(columns.every((column) => !("nullable" in column))).toBe(true);
  });

  it("enables selectableRange:true", () => {
    const opts = buildOptions(samplePage());
    expect(opts.selectableRange).toBe(true);
    expect(opts.selectableRangeAutoFocus).toBe(false);
    expect(opts.selectableRangeRows).toBe(true);
    expect(opts.editTriggerEvent).toBe("dblclick");
    expect(opts.popupContainer).toBe(true);
  });

  it("uses the transport row key as the stable Tabulator index", () => {
    expect(buildOptions(samplePage()).index).toBe(ROW_KEY_FIELD);
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
  it("keeps system timestamps read-only even when edit schema advertises an editor", () => {
    const page: TablePage = {
      table: "orders",
      columns: [{
        name: "updated_at",
        title: "最后更新时间",
        dataType: "datetime",
        kind: "system",
        editable: true,
        nullable: false,
      }],
      rows: [],
      offset: 0,
      limit: 100,
      totalRows: 0,
      mode: "client",
    };

    const column = buildColumns(
      page,
      [editCol("updated_at", { kind: "text" }, true)],
    )[0] as GridColumnDefinition;
    expect(column.editable).toBe(false);
    expect(column.editor).toBeUndefined();
  });

  it("keeps managed attachments on the native panel when edit schema advertises text editing", () => {
    const onAttachmentOpenRequested = vi.fn();
    const page: TablePage = {
      table: "orders",
      columns: [
        {
          name: "invoice",
          title: "附件",
          fieldId: "fld_invoice",
          dataType: "text",
          kind: "attachment",
          editable: true,
          nullable: true,
          attachmentPolicy: {
            maxFiles: 2,
            maxBytesPerFile: 1024,
            allowedMimeTypes: ["application/pdf"],
            thumbnailVariants: [],
            protected: false,
          },
        },
      ],
      rows: [],
      offset: 0,
      limit: 100,
      totalRows: 0,
      mode: "client",
    };

    const column = buildColumns(
      page,
      [editCol("invoice", { kind: "text" }, true)],
      {
        relations: new Map(),
        lookups: new Map(),
        relationEditAvailable: false,
        lookupQueryAvailable: false,
        onAttachmentOpenRequested,
      },
    )[0] as GridColumnDefinition;
    expect(column.editable).toBe(false);
    expect(column.editor).toBeUndefined();
    expect(column.cssClass).toBe("vt-attachment-cell vt-structured-cell");
    expect(column.cellDblClick).toEqual(expect.any(Function));
    const formatter = column.formatter as (
      cell: {
        getField(): string;
        getValue(): unknown;
        setValue(value: unknown): void;
        getRow(): { getData(): Record<string, unknown> };
        getElement(): HTMLElement;
      },
      params?: Record<string, unknown>,
      onRendered?: (callback: () => void) => void,
    ) => HTMLElement;
    const element = document.createElement("div");
    const cell = {
      getField: () => "invoice",
      getValue: () => [
        { storedName: "hash_invoice.pdf", originalName: "发票.pdf" },
      ],
      setValue: vi.fn(),
      getRow: () => ({ getData: () => ({ rowKey: "record-7" }) }),
      getElement: () => element,
    };
    const rendered = formatter(cell, {}, (callback) => callback());
    expect(rendered.textContent).toBe("1 个附件 · 发票.pdf");
    expect(element.tabIndex).toBe(0);
    expect(element.getAttribute("aria-haspopup")).toBe("dialog");
    expect(element.getAttribute("aria-label")).toContain("附件");
    expect(column).not.toHaveProperty("cellRendered");
    column.cellDblClick?.(new MouseEvent("dblclick"), cell);
    expect(onAttachmentOpenRequested).toHaveBeenCalledWith(
      "record-7",
      page.columns[0],
    );
  });

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

  it("attaches the real multi_select list editor", () => {
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
    const editSchema: ColumnEditSchema[] = [
      editCol("tags", { kind: "multi_select", options: ["a", "b"], allowCustom: false }, true),
    ];
    const cols = buildColumns(page, editSchema);
    expect((cols[0] as { editable: boolean }).editable).toBe(true);
    expect((cols[0] as { editor?: string }).editor).toBe("list");
    expect((cols[0] as { editorParams?: Record<string, unknown> }).editorParams)
      .toMatchObject({ values: ["a", "b"], multiselect: true });
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

describe("buildEditEventHandlers (Task M3)", () => {
  /**
   * Tabulator's cellEdited fires AFTER the value is already changed; oldValue
   * must be captured in cellEditing. We cannot drive a real Tabulator in jsdom,
   * but we CAN assert the handlers registered through Tabulator 6's table.on
   * event API.
   */
  it("registers cellEditing + cellEdited callbacks when onCellEdited is supplied", () => {
    const onCellEdited = vi.fn();
    const handlers = buildEditEventHandlers(undefined, onCellEdited);
    expect(typeof handlers?.cellEditing).toBe("function");
    expect(typeof handlers?.cellEdited).toBe("function");
  });

  it("does NOT register cellEditing/cellEdited when no onCellEdited callback is given (read-only)", () => {
    expect(buildEditEventHandlers(undefined)).toBeNull();
  });

  /**
   * oldValue capture: simulate Tabulator's two-phase edit by invoking the
   * callbacks with hand-built cell stubs. cellEditing captures the pre-edit
   * value; cellEdited retrieves it and forwards (rowKey, column, oldValue,
   * newValue) to onCellEdited.
   */
  it("captures oldValue in cellEditing and forwards (rk, col, old, new) in cellEdited", () => {
    const onCellEdited = vi.fn();
    const handlers = buildEditEventHandlers(undefined, onCellEdited);
    const digest = `sha256:${"a".repeat(64)}`;

    // A minimal cell stub that mimics Tabulator's CellComponent for our wiring.
    let current = "old";
    const cell = {
      getField: () => "name",
      getValue: () => current,
      setValue: (value: unknown) => {
        current = String(value);
      },
      getRow: () => ({
        getData: () => ({
          rowKey: 7,
          name: current,
          __vibetableDigest: digest,
        }),
      }),
    };
    // Phase 1: cellEditing fires BEFORE the value changes. cell.getValue() is
    // still the old value; the wiring caches it.
    handlers!.cellEditing(cell);
    // Phase 2: Tabulator commits the new value, THEN fires cellEdited.
    current = "new";
    handlers!.cellEdited(cell);

    expect(onCellEdited).toHaveBeenCalledTimes(1);
    expect(onCellEdited).toHaveBeenCalledWith(
      7,
      "name",
      "old",
      "new",
      digest,
    );
  });
});
