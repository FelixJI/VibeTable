import { describe, expect, it } from "vitest";
import type { PresetView } from "@/contracts";
import { projectPresetRows } from "./projectPresetRows";

function view(input: Partial<PresetView>): PresetView {
  return {
    filters: [],
    sorts: [],
    search: "",
    visibleFields: [],
    layout: "kanban",
    kind: "kanban",
    ...input,
  };
}

describe("projectPresetRows", () => {
  it("applies a non-table preset's saved filters, search, and sort order", () => {
    const rows = [
      { rowKey: "1", title: "Alpha", status: "open" },
      { rowKey: "2", title: "Gamma", status: "open" },
      { rowKey: "3", title: "Beta", status: "closed" },
    ];
    const result = projectPresetRows(rows, view({
      filters: [{ field: "status", operator: "eq", value: "open" }],
      sorts: [{ field: "title", direction: "desc" }],
      search: "a",
      visibleFields: ["title"],
    }));
    expect(result.map((row) => row.rowKey)).toEqual(["2", "1"]);
  });

  it("ignores fields removed after a preset was saved", () => {
    const rows = [{ rowKey: "1", title: "Alpha" }];
    expect(projectPresetRows(rows, view({
      filters: [{ field: "removed", operator: "eq", value: "old" }],
      sorts: [{ field: "removed", direction: "desc" }],
    }))).toEqual(rows);
  });

  it("projects two presets independently when switching saved filters", () => {
    const rows = [
      { rowKey: "1", status: "open" },
      { rowKey: "2", status: "closed" },
    ];
    const open = view({ filters: [{ field: "status", operator: "eq", value: "open" }] });
    const closed = view({ filters: [{ field: "status", operator: "eq", value: "closed" }] });
    expect(projectPresetRows(rows, open).map((row) => row.rowKey)).toEqual(["1"]);
    expect(projectPresetRows(rows, closed).map((row) => row.rowKey)).toEqual(["2"]);
  });
});
