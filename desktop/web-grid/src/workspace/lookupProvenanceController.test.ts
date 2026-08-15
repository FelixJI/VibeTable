import { nextTick, ref } from "vue";
import { describe, expect, it, vi } from "vitest";

import type { LookupSourcePageIntent, LookupValueProvenance } from "@/contracts";
import { createLookupProvenanceController } from "./lookupProvenanceController";

const source = (itemId: string): LookupValueProvenance => ({
  collection: "customers",
  collectionLabel: "Customers",
  itemId,
  recordLabel: `Customer ${itemId}`,
  fieldId: "name-id",
  fieldLabel: "Name",
  value: `Customer ${itemId}`,
});

const pageIntent = (hasMore = true): LookupSourcePageIntent => ({
  fieldRef: "customer-name",
  sourceRecordId: "order-1",
  cell: {
    state: "ok",
    value: "Customer one",
    diagnostic: null,
    provenance: [source("one")],
    provenanceTotal: 2,
    provenanceTotalKnown: true,
    provenanceOffset: 0,
    provenanceLimit: 1,
    provenanceHasMore: hasMore,
  },
});

describe("lookup provenance controller", () => {
  it("ignores a page response after the source dialog closes", async () => {
    let resolve!: (value: {
      provenance: LookupValueProvenance[];
      provenanceTotal: number;
      provenanceTotalKnown: boolean;
      provenanceHasMore: boolean;
    }) => void;
    const readPage = vi.fn(() => new Promise<{
      provenance: LookupValueProvenance[];
      provenanceTotal: number;
      provenanceTotalKnown: boolean;
      provenanceHasMore: boolean;
    }>(done => { resolve = done; }));
    const controller = createLookupProvenanceController({
      readPage,
      selectTable: vi.fn(),
      navigateTables: vi.fn(),
      queryRecord: vi.fn(),
      getCurrentTable: () => null,
      getSchemaContext: () => ({ collection: null, primaryKey: null }),
      getRows: () => [],
      getGrid: () => null,
      getColumns: () => [],
      openContent: vi.fn(),
      openAttachment: vi.fn(),
      reportLocated: vi.fn(),
      reportFiltered: vi.fn(),
      errorMessage: error => String(error),
    });
    await controller.dispatch({ type: "sources.open", page: pageIntent() });
    const pending = controller.dispatch({ type: "sources.loadMore" });
    await controller.dispatch({ type: "sources.close" });
    resolve({
      provenance: [source("two")],
      provenanceTotal: 2,
      provenanceTotalKnown: true,
      provenanceHasMore: false,
    });
    await pending;
    expect(controller.state.items.map(item => item.itemId)).toEqual(["one"]);
    expect(controller.state.show).toBe(false);
  });

  it("owns cross-table query, grid location, and content opening as one transition", async () => {
    const currentTable = ref<string | null>(null);
    const schema = ref({ collection: null as string | null, primaryKey: null as string | null });
    const rows = ref<readonly Readonly<Record<string, unknown>>[]>([]);
    const grid = ref<unknown>(null);
    const selectTable = vi.fn((collection: string) => { currentTable.value = collection; });
    const queryRecord = vi.fn();
    const openContent = vi.fn();
    const reportLocated = vi.fn();
    const row = {
      getIndex: () => "one",
      scrollTo: vi.fn(async () => undefined),
      select: vi.fn(),
      getElement: () => document.createElement("div"),
    };
    const controller = createLookupProvenanceController({
      readPage: vi.fn(),
      selectTable,
      navigateTables: vi.fn(),
      queryRecord,
      getCurrentTable: () => currentTable.value,
      getSchemaContext: () => schema.value,
      getRows: () => rows.value,
      getGrid: () => grid.value,
      getColumns: () => [],
      openContent,
      openAttachment: vi.fn(),
      reportLocated,
      reportFiltered: vi.fn(),
      errorMessage: error => String(error),
    });

    await controller.dispatch({
      type: "source.locate",
      target: { source: source("one"), open: "content", fieldId: null },
    });
    schema.value = { collection: "customers", primaryKey: "id" };
    await nextTick();
    expect(queryRecord).toHaveBeenCalledWith("customers", "id", "one");

    grid.value = { getRows: vi.fn(() => [row]) };
    rows.value = [{ rowKey: "one" }];
    await nextTick();
    await vi.waitFor(() => expect(openContent).toHaveBeenCalledWith("one"));
    expect(row.scrollTo).toHaveBeenCalledWith("center", true);
    expect(reportLocated).toHaveBeenCalledWith(source("one"));
  });
});
