import { describe, expect, it, vi } from "vitest";
import { flushPromises } from "@vue/test-utils";
import { TabulatorFull } from "tabulator-tables";
import {
  applyDataSourceView,
  captureDataSourceView,
  createTabulatorDataSourceViewAdapter,
  type DataSourceViewGrid,
} from "./dataSourceViewState";

describe("dataSourceViewState", () => {
  it("keeps the latest edit schema and added columns when restoring presentation", async () => {
    const element = document.createElement("div");
    document.body.append(element);
    const table = new TabulatorFull(element, {
      columns: [{ field: "name", editable: false }, { field: "removed" }],
      data: [{ name: "initial" }],
      sortMode: "remote", filterMode: "remote",
    });
    try {
      const grid = createTabulatorDataSourceViewAdapter(table);
      await vi.waitFor(() => expect(grid?.initialized).toBe(true));
      table.setColumns([
        { field: "name", title: "Renamed", editable: true, editor: "input" },
        { field: "added", editor: "number", editable: true },
      ]);
      await applyDataSourceView(grid, {
        layout: "table", search: "", filters: [], sorts: [],
        columns: [{ name: "name", width: 240, visible: true, frozen: true }],
      });
      expect(grid!.getColumns().map(column => column.getField())).toEqual(["name", "added"]);
      expect(grid!.getColumns()[0]?.getDefinition?.()).toMatchObject({
        title: "Renamed", editable: true, editor: "input", width: 240, frozen: true,
      });
      expect(grid!.getColumns()[1]?.getDefinition?.()).toMatchObject({ editor: "number", editable: true });
    } finally { table.destroy?.(); element.remove(); }
  });

  it.each([false, true])("keeps authoritative rows while restoring remote query controls, withQuery=%s", async (withQuery) => {
    const element = document.createElement("div");
    document.body.append(element);
    const table = new TabulatorFull(element, {
      columns: [{ field: "name", headerFilter: "input" }],
      data: [{ name: "initial" }],
      sortMode: "remote", filterMode: "remote",
    });
    try {
      const grid = createTabulatorDataSourceViewAdapter(table);
      await vi.waitFor(() => expect(grid?.initialized).toBe(true));
      const authoritativeRows = [{ name: "competitor-write" }, { name: "another-record" }];
      await table.setData(authoritativeRows);
      await applyDataSourceView(grid, {
        layout: "table", search: "",
        columns: [{ name: "name", visible: true }],
        sorts: withQuery ? [{ field: "name", direction: "desc" }] : [],
        filters: withQuery ? [{ field: "name", operator: "eq", value: "competitor-write" }] : [],
      });
      await flushPromises();
      await new Promise(resolve => setTimeout(resolve, 0));
      expect((table as unknown as { getData(): unknown[] }).getData()).toEqual(authoritativeRows);
      expect(captureDataSourceView(grid)).toMatchObject({
        sorts: withQuery ? [{ field: "name", direction: "desc" }] : [],
        filters: withQuery ? [{ field: "name", operator: "eq", value: "competitor-write" }] : [],
      });
      const restoring = applyDataSourceView(grid, {
        layout: "table", search: "", columns: [{ name: "name" }], filters: [], sorts: [],
      });
      const newerRows = [{ name: "newer-authoritative-revision" }];
      await Promise.resolve().then(() => table.setData(newerRows));
      await restoring;
      await flushPromises();
      expect((table as unknown as { getData(): unknown[] }).getData()).toEqual(newerRows);
    } finally { table.destroy?.(); element.remove(); }
  });

  it.each([
    ["presentation", ""], ["presentation", "restored"],
    ["header value", ""], ["header value", "restored"],
  ])("%s ignores retired header input after newer rows, filter=%s", async (operation, restoredValue) => {
    const element = document.createElement("div");
    document.body.append(element);
    const table = new TabulatorFull(element, {
      columns: [{ field: "name", headerFilter: "input" }],
      data: [{ name: "initial" }],
      sortMode: "remote", filterMode: "remote",
    });
    try {
      const grid = createTabulatorDataSourceViewAdapter(table);
      await vi.waitFor(() => expect(grid?.initialized).toBe(true));
      vi.useFakeTimers();
      const retiredInput = element.querySelector<HTMLInputElement>(".tabulator-header-filter input")!;
      retiredInput.value = "retired-query";
      retiredInput.dispatchEvent(new KeyboardEvent("keyup", { key: "y", bubbles: true }));
      if (operation === "presentation") {
        await applyDataSourceView(grid, {
          layout: "table", search: "", columns: [{ name: "name" }], sorts: [],
          filters: restoredValue ? [{ field: "name", operator: "eq", value: restoredValue }] : [],
        });
      } else {
        (table as unknown as { setHeaderFilterValue(field: string, value: string): void })
          .setHeaderFilterValue("name", restoredValue);
      }
      const authoritativeRows = [{ name: "newer-authoritative-revision" }];
      await table.setData(authoritativeRows);
      const currentInput = element.querySelector<HTMLInputElement>(".tabulator-header-filter input")!;
      expect(retiredInput.isConnected).toBe(false);
      expect(currentInput.value).toBe(restoredValue);
      const expectedFilters = restoredValue ? [{ field: "name", operator: "eq", value: restoredValue }] : [];
      expect(captureDataSourceView(grid).filters).toEqual(expectedFilters);
      await vi.advanceTimersByTimeAsync(350);
      expect((table as unknown as { getData(): unknown[] }).getData()).toEqual(authoritativeRows);
      retiredInput.dispatchEvent(new FocusEvent("blur"));
      expect((table as unknown as { getData(): unknown[] }).getData()).toEqual(authoritativeRows);
      expect(captureDataSourceView(grid).filters).toEqual(expectedFilters);
      expect(currentInput.value).toBe(restoredValue);

      // The replacement header still submits user edits after the default delay.
      currentInput.value = "current-query";
      currentInput.dispatchEvent(new KeyboardEvent("keyup", { key: "y", bubbles: true }));
      await vi.advanceTimersByTimeAsync(299);
      expect(captureDataSourceView(grid).filters).toEqual(expectedFilters);
      await vi.advanceTimersByTimeAsync(1);
      expect(captureDataSourceView(grid).filters).toEqual([
        { field: "name", operator: "eq", value: "current-query" },
      ]);
    } finally {
      table.destroy?.(); element.remove(); vi.useRealTimers();
    }
  });

  it("restores sorting through the real Tabulator input contract", async () => {
    const element = document.createElement("div");
    document.body.append(element);
    const warnings = vi.spyOn(console, "warn");
    const table = new TabulatorFull(element, {
      columns: [{ field: "name", title: "Name", width: 160 }],
      data: [{ name: "B" }, { name: "A" }],
      sortMode: "local",
    });
    try {
      const grid = createTabulatorDataSourceViewAdapter(table);
      expect(grid).not.toBeNull();
      await vi.waitFor(() => expect(grid?.initialized).toBe(true));
      await applyDataSourceView(grid, {
        layout: "table", search: "", filters: [],
        columns: [{ name: "name", order: 0, width: 160, visible: true }],
        sorts: [{ field: "name", direction: "desc" }],
      });
      expect(captureDataSourceView(grid).sorts).toEqual([{ field: "name", direction: "desc" }]);
      expect(warnings).not.toHaveBeenCalled();
    } finally {
      table.destroy?.();
      element.remove();
      warnings.mockRestore();
    }
  });
  it("restores explicitly ordered columns before unordered saved and newly added columns", async () => {
    const applyPresentation = vi.fn();
    await applyDataSourceView({
      getColumns: () => ["new", "unordered", "ordered"].map(field => ({ getField: () => field })),
      applyPresentation,
    }, { layout: "table", search: "", filters: [], sorts: [], columns: [
      { name: "unordered" }, { name: "ordered", order: 4 },
    ] });
    expect(applyPresentation.mock.calls[0][0].columns.map((column: { field: string }) => column.field))
      .toEqual(["ordered", "unordered", "new"]);
  });
  it("captures only actual table presentation state, never record data", () => {
    const grid: DataSourceViewGrid = {
      getColumns: () => [
        { getField: () => "name", getWidth: () => 220, isVisible: () => true },
        { getField: () => "cost", getWidth: () => 96, isVisible: () => false },
        { getField: () => "__internal", getWidth: () => 40, isVisible: () => true },
      ],
      getSorters: () => [{ field: "name", dir: "asc" }],
      getHeaderFilters: () => [{ field: "cost", value: 10 }],
    };
    const view = captureDataSourceView(grid, { isDefault: true });
    expect(view.columns).toEqual([
      { name: "name", order: 0, width: 220, visible: true, frozen: false },
      { name: "cost", order: 1, width: 96, visible: false, frozen: false },
    ]);
    expect(view.sorts).toEqual([{ field: "name", direction: "asc" }]);
    expect(view.filters).toEqual([{ field: "cost", operator: "eq", value: 10 }]);
    expect(view.search).toBe("");
    expect(view).not.toHaveProperty("visibleFields");
    expect(JSON.stringify(view)).not.toContain("rows");
  });

  it("does not read Tabulator runtime state before tableBuilt", () => {
    const getSorters = vi.fn();
    const getHeaderFilters = vi.fn();
    const view = captureDataSourceView({
      initialized: false,
      getColumns: () => [],
      getSorters,
      getHeaderFilters,
    });

    expect(getSorters).not.toHaveBeenCalled();
    expect(getHeaderFilters).not.toHaveBeenCalled();
    expect(view.sorts).toEqual([]);
    expect(view.filters).toEqual([]);
  });

  it("applies column layout, sort and supported equality filters", async () => {
    const applyPresentation = vi.fn();
    const grid: DataSourceViewGrid = {
      getColumns: () => [
        { getField: () => "name", getWidth: () => 200, isVisible: () => true },
      ],
      applyPresentation,
    };
    await applyDataSourceView(grid, {
      kind: "table",
      layout: "table",
      search: "",
      columns: [{ name: "name", order: 0, width: 180, visible: true }],
      sorts: [{ field: "name", direction: "desc" }],
      filters: [{ field: "name", operator: "eq", value: "A" }],
    });
    expect(applyPresentation).toHaveBeenCalledWith({
      columns: [{ field: "name", width: 180, visible: true, frozen: false }],
      sorters: [{ column: "name", dir: "desc" }],
      headerFilters: [{ field: "name", value: "A" }],
    });
  });
  it("does not flatten an advanced filter group into incorrect header filters", async () => {
    const applyPresentation = vi.fn();
    await applyDataSourceView({
      getColumns: () => [{ getField: () => "status" }, { getField: () => "priority" }],
      applyPresentation,
    }, {
      layout: "table",
      search: "",
      columns: [
        { name: "status", order: 0, visible: true },
        { name: "priority", order: 1, visible: true },
      ],
      sorts: [],
      filters: [{
        groupLogic: "OR",
        filters: [
          { field: "status", operator: "eq", value: "open" },
          { field: "priority", operator: "eq", value: "urgent" },
        ],
      }],
    });

    expect(applyPresentation.mock.calls[0][0].headerFilters).toEqual([]);
  });

  it("rejects legacy visibleFields-only state without mutating the grid", async () => {
    const applyPresentation = vi.fn();
    const grid: DataSourceViewGrid = {
      getColumns: () => [
        { getField: () => "name", getWidth: () => 180, isVisible: () => true },
        { getField: () => "newField", getWidth: () => 120, isVisible: () => true },
      ],
      applyPresentation,
    };
    const legacyView = {
      layout: "table",
      search: "",
      visibleFields: ["name", "removedField"],
      sorts: [{ field: "removedField", direction: "asc" }],
      filters: [{ field: "removedField", operator: "eq", value: "old" }],
    };

    await applyDataSourceView(grid, legacyView as never);

    expect(applyPresentation).not.toHaveBeenCalled();
  });
});

it("captures fitColumns subpixel widths as whole CSS pixels after dynamic schema changes", async () => {
  const element = document.createElement("div");
  document.body.append(element);
  const geometry = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect")
    .mockReturnValue({ width: 726 + 6 / 7, height: 300 } as DOMRect);
  const table = new TabulatorFull(element, {
    layout: "fitColumns",
    columns: [{ field: "__vt_row_number", width: 42 }, { field: "id", minWidth: 160 }],
    data: [], sortMode: "remote", filterMode: "remote",
  });
  try {
    const grid = createTabulatorDataSourceViewAdapter(table)!;
    await vi.waitFor(() => expect(grid.initialized).toBe(true));
    table.setColumns([
      { field: "__vt_row_number", width: 42 },
      { field: "id", minWidth: 160 },
      { field: "group", minWidth: 160 },
      { field: "amount", minWidth: 120 },
    ]);
    const runtimeWidths = grid.getColumns().map(column => column.getWidth?.());
    expect(runtimeWidths.slice(0, 3)).toEqual([42, 228, 228]);
    expect(runtimeWidths[3]).toBeCloseTo(228 + 6 / 7);
    expect(Number.isInteger(runtimeWidths[3])).toBe(false);
    const captured = captureDataSourceView(grid);
    expect(captured.columns?.map(column => column.width)).toEqual([228, 228, 229]);
    expect(captured.columns?.map(column => column.name)).toEqual(["id", "group", "amount"]);
  } finally {
    table.destroy?.(); element.remove(); geometry.mockRestore();
  }
});
