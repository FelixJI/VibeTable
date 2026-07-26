import { describe, expect, it, vi } from "vitest";
import {
  applyDataSourceView,
  captureDataSourceView,
  type DataSourceViewGrid,
} from "./dataSourceViewState";

describe("dataSourceViewState", () => {
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
    expect(JSON.stringify(view)).not.toContain("rows");
  });

  it("applies column layout, sort and supported equality filters", async () => {
    const setColumnLayout = vi.fn();
    const setSort = vi.fn();
    const clearHeaderFilter = vi.fn();
    const setHeaderFilterValue = vi.fn();
    const grid: DataSourceViewGrid = {
      getColumns: () => [
        { getField: () => "name", getWidth: () => 200, isVisible: () => true },
      ],
      setColumnLayout,
      setSort,
      clearHeaderFilter,
      setHeaderFilterValue,
    };
    await applyDataSourceView(grid, {
      kind: "table",
      layout: "table",
      search: "",
      visibleFields: ["name"],
      columns: [{ name: "name", order: 0, width: 180, visible: true }],
      sorts: [{ field: "name", direction: "desc" }],
      filters: [{ field: "name", operator: "eq", value: "A" }],
    });
    expect(setColumnLayout).toHaveBeenCalledWith([
      { field: "name", width: 180, visible: true, frozen: false },
    ]);
    expect(setSort).toHaveBeenCalledWith([{ field: "name", dir: "desc" }]);
    expect(clearHeaderFilter).toHaveBeenCalled();
    expect(setHeaderFilterValue).toHaveBeenCalledWith("name", "A");
  });

  it("adapts legacy visibleFields and ignores removed fields", async () => {
    const setColumnLayout = vi.fn();
    const setSort = vi.fn();
    const setHeaderFilterValue = vi.fn();
    const grid: DataSourceViewGrid = {
      getColumns: () => [
        { getField: () => "name", getWidth: () => 180, isVisible: () => true },
        { getField: () => "newField", getWidth: () => 120, isVisible: () => true },
      ],
      setColumnLayout,
      setSort,
      setHeaderFilterValue,
    };
    await applyDataSourceView(grid, {
      layout: "table",
      search: "",
      visibleFields: ["name", "removedField"],
      sorts: [{ field: "removedField", direction: "asc" }],
      filters: [{ field: "removedField", operator: "eq", value: "old" }],
    });
    expect(setColumnLayout).toHaveBeenCalledWith([
      { field: "name", width: 180, visible: true, frozen: false },
      { field: "newField", width: 120, visible: false, frozen: false },
    ]);
    expect(setSort).toHaveBeenCalledWith([]);
    expect(setHeaderFilterValue).not.toHaveBeenCalled();
  });

  it("treats empty legacy visibleFields as unspecified visibility", async () => {
    const setColumnLayout = vi.fn();
    const grid: DataSourceViewGrid = {
      getColumns: () => [
        { getField: () => "name", isVisible: () => true },
        { getField: () => "private", isVisible: () => false },
      ],
      setColumnLayout,
    };
    await applyDataSourceView(grid, {
      layout: "table",
      search: "",
      visibleFields: [],
      sorts: [],
      filters: [],
    });
    expect(setColumnLayout).toHaveBeenCalledWith([
      { field: "name", visible: true, frozen: false },
      { field: "private", visible: false, frozen: false },
    ]);
  });
});
