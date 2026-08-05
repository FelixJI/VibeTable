import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useViewQueryStore } from "./viewQueryStore";

describe("viewQueryStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps one normalized query for every layout and limits groups/summaries", () => {
    const store = useViewQueryStore();
    store.replace("orders", {
      kind: "kanban",
      layout: "kanban",
      filters: [{ groupLogic: "OR", filters: [
        { field: "status", operator: "eq", value: "open" },
        { field: "priority", operator: "eq", value: "urgent" },
      ] }],
      sorts: [{ field: "amount", direction: "desc" }],
      groups: [
        { field: "status" }, { field: "priority" }, { field: "owner" },
      ],
      summaries: [
        { field: "amount", function: "sum" },
        { field: "amount", function: "avg" },
        { field: "amount", function: "min" },
        { field: "amount", function: "max" },
      ],
      search: "north",
      visibleFields: ["status", "missing"],
      collapsedGroupKeys: ['["open"]'],
    }, ["status", "priority", "amount"]);

    expect(store.visibleFields).toEqual(["status"]);
    expect(store.groups).toHaveLength(2);
    expect(store.summaries).toHaveLength(3);
    expect(store.collapsedGroupKeys).toEqual(['["open"]']);
    store.toggleGroup('["open"]');
    expect(store.collapsedGroupKeys).toEqual([]);
    expect(store.toQuery()).toMatchObject({
      keyword: "north",
      offset: 0,
      limit: 500,
      groupLimit: 100,
    });
  });

  it("defaults visibility to every current field when a view has no saved list", () => {
    const store = useViewQueryStore();
    store.replace("orders", {
      kind: "table",
      layout: "table",
      filters: [],
      sorts: [],
      search: "",
      visibleFields: [],
    }, ["id", "status"]);

    expect(store.visibleFields).toEqual(["id", "status"]);
  });
});
