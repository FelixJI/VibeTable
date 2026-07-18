import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, ref, type Ref } from "vue";
import { mount, flushPromises } from "@vue/test-utils";

import { useTabulator } from "./useTabulator";
import { useTableStore } from "@/stores/tableStore";
import type {
  ColumnSchema,
  DatasetReadyPayload,
  TablePage,
} from "@/contracts";

/**
 * useTabulator drives a real Tabulator via `createGrid`. Tabulator needs a
 * real DOM with layout, which jsdom does not provide reliably. We mock
 * `createGrid` so tests exercise the *lifecycle* of useTabulator (init on
 * first page, setData on data change, destroy on unmount) without depending
 * on Tabulator internals. The mock instance records every call for assertions.
 *
 * The mock is factory-installed at the top level (vi.mock is hoisted) and
 * exposes `__lastMock` so each test can read the most-recently-created mock
 * instance. `createGrid` is also reset between tests so call counts stay clean.
 */

interface MockTabulator {
  setData: ReturnType<typeof vi.fn>;
  setColumns: ReturnType<typeof vi.fn>;
  destroy: ReturnType<typeof vi.fn>;
}

let lastMock: MockTabulator | null = null;

vi.mock("@/grid/createGrid", () => ({
  // Return an object that quacks like TabulatorFull for our lifecycle.
  createGrid: vi.fn((_el: HTMLElement, _page: TablePage) => {
    const mock: MockTabulator = {
      setData: vi.fn().mockResolvedValue(undefined),
      setColumns: vi.fn(),
      destroy: vi.fn(),
    };
    lastMock = mock;
    return mock;
  }),
  buildColumns: vi.fn((page: TablePage) =>
    page.columns.map((c) => ({ field: c.name, title: c.title })),
  ),
}));

function makeColumn(name: string): ColumnSchema {
  return {
    name,
    title: name,
    dataType: "integer",
    editable: false,
    nullable: true,
  };
}

function makePage(
  rows: Record<string, unknown>[],
  columns: readonly ColumnSchema[] = [],
  opts: Partial<TablePage> = {},
): TablePage {
  return {
    table: "users",
    columns,
    rows,
    offset: 0,
    limit: rows.length,
    totalRows: rows.length,
    mode: "client",
    ...opts,
  };
}

function makeDatasetReady(
  rows: Record<string, unknown>[],
  columns: readonly ColumnSchema[] = [],
): DatasetReadyPayload {
  return {
    table: "users",
    columns,
    rows,
    offset: 0,
    limit: rows.length,
    totalRows: rows.length,
    mode: "client",
    loadedRows: rows.length,
  };
}

/**
 * Mount a host component that exposes a `gridEl` ref to useTabulator. The
 * template renders the div immediately so the ref is populated on mount.
 */
function mountHost(gridEl: Ref<HTMLElement | null>) {
  const Host = defineComponent({
    setup() {
      useTabulator(gridEl);
      return () => h("div", { class: "wrapper" });
    },
  });
  return mount(Host);
}

describe("useTabulator", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    lastMock = null;
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("does NOT call createGrid before the first page arrives", async () => {
    const { createGrid } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();

    const wrapper = mountHost(gridEl);
    // Wire the ref to a real element AFTER mount (simulating the template ref).
    gridEl.value = document.createElement("div");
    await flushPromises();

    expect(createGrid).not.toHaveBeenCalled();
    // Sanity: store has no pages.
    expect(table.pages).toHaveLength(0);
    wrapper.unmount();
  });

  it("calls createGrid once when the first page arrives (and only once)", async () => {
    const { createGrid } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();

    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    await flushPromises();
    expect(createGrid).not.toHaveBeenCalled();

    // First page arrives -> createGrid fires with (element, page[0]).
    const page1 = makePage(
      [{ id: 1 }, { id: 2 }],
      [makeColumn("id")],
    );
    table.beginLoad();
    table.appendPage(page1);
    await flushPromises();

    expect(createGrid).toHaveBeenCalledTimes(1);
    expect(createGrid).toHaveBeenCalledWith(gridEl.value, page1);
    wrapper.unmount();
  });

  it("uses incremental setData on subsequent data changes (NOT createGrid again)", async () => {
    const { createGrid } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();

    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    await flushPromises();

    // First page -> createGrid.
    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));
    await flushPromises();
    expect(createGrid).toHaveBeenCalledTimes(1);
    expect(lastMock).not.toBeNull();
    expect(lastMock!.setData).not.toHaveBeenCalled();

    // datasetReady replaces pages with the authoritative full dataset.
    // This is a data-only change (same schema) -> must go through setData.
    table.setDatasetReady(
      makeDatasetReady([{ id: 1 }, { id: 2 }, { id: 3 }], [makeColumn("id")]),
    );
    await flushPromises();

    // createGrid must NOT have been called again (no destroy+rebuild).
    expect(createGrid).toHaveBeenCalledTimes(1);
    // setData must have been called with the flattened row set.
    expect(lastMock!.setData).toHaveBeenCalledTimes(1);
    expect(lastMock!.setData).toHaveBeenCalledWith([
      { id: 1 },
      { id: 2 },
      { id: 3 },
    ]);
    wrapper.unmount();
  });

  it("destroys the tabulator instance on unmount", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();

    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    await flushPromises();

    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));
    await flushPromises();
    expect(lastMock).not.toBeNull();
    expect(lastMock!.destroy).not.toHaveBeenCalled();

    wrapper.unmount();
    expect(lastMock!.destroy).toHaveBeenCalledTimes(1);
  });

  it("waits for the grid element to mount before initializing", async () => {
    const { createGrid } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();

    // Push a page BEFORE the element exists.
    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));

    const wrapper = mountHost(gridEl);
    await flushPromises();
    // No element yet -> no init.
    expect(createGrid).not.toHaveBeenCalled();

    // Now the element appears (template ref populates).
    gridEl.value = document.createElement("div");
    await flushPromises();
    expect(createGrid).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
