import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, ref, type Ref } from "vue";
import { mount, flushPromises } from "@vue/test-utils";

import { useTabulator, type UseTabulatorOptions } from "./useTabulator";
import { useTableStore } from "@/stores/tableStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type {
  ColumnSchema,
  DatasetReadyPayload,
  NormalizedRelationDescriptor,
  RelationLookupCapabilities,
  SchemaSnapshot,
  TablePage,
} from "@/contracts";
import { setLocale } from "@/i18n";

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
  initialized?: boolean;
  setData: ReturnType<typeof vi.fn>;
  setColumns: ReturnType<typeof vi.fn>;
  destroy: ReturnType<typeof vi.fn>;
  on: ReturnType<typeof vi.fn>;
  off: ReturnType<typeof vi.fn>;
  getSorters: ReturnType<typeof vi.fn>;
  getHeaderFilters: ReturnType<typeof vi.fn>;
  getRanges: ReturnType<typeof vi.fn>;
  getColumns: ReturnType<typeof vi.fn>;
  addRange: ReturnType<typeof vi.fn>;
  getRows: ReturnType<typeof vi.fn>;
  getSelectedRows: ReturnType<typeof vi.fn>;
  getRow: ReturnType<typeof vi.fn>;
  selectRow: ReturnType<typeof vi.fn>;
}

let lastMock: MockTabulator | null = null;
let mockStartsInitialized = true;

vi.mock("@/grid/createGrid", () => ({
  // Return an object that quacks like TabulatorFull for our lifecycle.
  createGrid: vi.fn((_el: HTMLElement, _page: TablePage) => {
    const mock: MockTabulator = {
      initialized: mockStartsInitialized,
      setData: vi.fn().mockResolvedValue(undefined),
      setColumns: vi.fn(),
      destroy: vi.fn(),
      on: vi.fn(),
      off: vi.fn(),
      getSorters: vi.fn().mockReturnValue([]),
      getHeaderFilters: vi.fn().mockReturnValue([]),
      getRanges: vi.fn().mockReturnValue([]),
      getColumns: vi.fn().mockReturnValue([]),
      addRange: vi.fn(),
      getRows: vi.fn().mockReturnValue([]),
      getSelectedRows: vi.fn().mockReturnValue([]),
      getRow: vi.fn().mockReturnValue(false),
      selectRow: vi.fn(),
    };
    lastMock = mock;
    return mock;
  }),
  buildTabulatorColumns: vi.fn((page: TablePage) =>
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
    mode: "remote",
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
    mode: "remote",
  };
}

const relationCapabilities: RelationLookupCapabilities = {
  contract: "vibetable.relation-capabilities.v1",
  relationReadV1: true,
  relationEditV1: true,
  lookupQueryV1: true,
};

function relationDescriptor(
  collection: string,
  field: string,
): NormalizedRelationDescriptor {
  return {
    relationId: `${collection}.${field}`,
    fieldRef: field,
    sourceCollection: collection,
    kind: "m2o",
    relatedCollection: "targets",
    unique: true,
    nullable: true,
    onDelete: "nullify",
    preset: "standard",
    selfRelation: false,
    managed: true,
    state: "valid",
    displayTemplate: null,
    diagnostics: [],
  };
}

function relationSchema(
  collection: string,
  relation: NormalizedRelationDescriptor,
): SchemaSnapshot {
  return {
    collection,
    primaryKey: "id",
    columns: [],
    normalizedRelations: [relation],
    // Deliberately identical across collections: the regression is that a
    // revision-only signature previously skipped the second column rebuild.
    schemaRevision: "schema_0001",
    permissionRevision: "schema_0001",
    capabilityHash: "same-capability",
    lookupRevision: "same-lookup",
  };
}

/**
 * Mount a host component that exposes a `gridEl` ref to useTabulator. The
 * template renders the div immediately so the ref is populated on mount.
 */
function mountHost(gridEl: Ref<HTMLElement | null>, options?: UseTabulatorOptions) {
  const Host = defineComponent({
    setup() {
      useTabulator(gridEl, options);
      return () => h("div", { class: "wrapper" });
    },
  });
  return mount(Host);
}

/** Model the destructive range reset shared by Tabulator's data/column rebuilds. */
function installRangeRuntime(mock: MockTabulator) {
  type Cell = { rowKey: string; field: string };
  let rows = ["a", "b"];
  let fields = ["id", "payload", "status"];
  const row = (rowKey: string) => ({
    getData: () => ({ rowKey }),
    getCell: (field: string) => ({ rowKey, field }),
  });
  const column = (field: string) => ({ getField: () => field });
  const emit = (event: string, value: unknown) => {
    for (const call of mock.on.mock.calls) if (call[0] === event) call[1](value);
  };
  const range = (start: Cell, end = start) => ({
    getRows: () => rows.slice(rows.indexOf(start.rowKey), rows.indexOf(end.rowKey) + 1).map(row),
    getColumns: () => fields.slice(fields.indexOf(start.field), fields.indexOf(end.field) + 1).map(column),
    setBounds: (nextStart: Cell, nextEnd = nextStart) => {
      start = nextStart;
      end = nextEnd;
      emit("rangeChanged", current.at(-1));
    },
    remove: () => { current = []; },
  });
  let current: ReturnType<typeof range>[] = [];
  let retiredRange: ReturnType<typeof range> | undefined;
  const select = (field = "payload", lastRow = "a") => {
    current = [range({ rowKey: "a", field }, { rowKey: lastRow, field })];
    emit("rangeChanged", current[0]);
  };
  const addSelection = (rowKey: string, field: string) => {
    current.push(range({ rowKey, field }));
    emit("rangeChanged", current.at(-1));
  };
  const reset = () => {
    retiredRange = current[0];
    current = rows.length && fields.length ? [range({ rowKey: rows[0]!, field: fields[0]! })] : [];
    if (current[0]) emit("rangeAdded", current[0]);
  };
  mock.getRows.mockImplementation(() => rows.map(row));
  mock.getColumns.mockImplementation(() => fields.map(column));
  mock.getRanges.mockImplementation(() => current);
  mock.addRange.mockImplementation(() => {
    const added = range({ rowKey: rows[0]!, field: fields[0]! });
    current.push(added);
    // The installed version returns its internal Range here, not a component.
    // Consumers must obtain the public component through getRanges().
    return {};
  });
  mock.setData.mockImplementation(async (data: Array<{ rowKey: string }>) => {
    rows = data.map(item => item.rowKey);
    reset();
  });
  mock.setColumns.mockImplementation((columns: Array<{ field: string }>) => {
    fields = columns.map(item => item.field);
    reset();
  });
  return { select, addSelection, emitRetiredRange: () => emit("rangeChanged", retiredRange), selection: () => current.map(item => ({
    rowKeys: item.getRows().map(item => item.getData().rowKey),
    fields: item.getColumns().map(item => item.getField()),
  })) };
}

describe("useTabulator", () => {
  beforeEach(() => {
    setLocale("zh-CN");
    setActivePinia(createPinia());
    lastMock = null;
    mockStartsInitialized = true;
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("retains cell ranges through an empty loading window and consecutive column rebuilds", async () => {
    const table = useTableStore();
    useWorkspaceStore().selectTable("users");
    const gridEl = ref<HTMLElement | null>(document.createElement("div"));
    const columns = ["id", "payload", "status"].map(makeColumn);
    table.appendPage(makePage([{ rowKey: "a" }, { rowKey: "b" }], columns));
    const onRangeSelectionChanged = vi.fn();
    const wrapper = mountHost(gridEl, { onRangeSelectionChanged });
    await flushPromises();
    const runtime = installRangeRuntime(lastMock!);
    runtime.select("payload", "b");

    table.beginLoad();
    await flushPromises();
    expect(lastMock!.setData).toHaveBeenCalledWith([]);
    table.setDatasetReady(makeDatasetReady([{ rowKey: "a" }, { rowKey: "b" }], columns));
    await flushPromises();
    expect(runtime.selection()).toEqual([{ rowKeys: ["a", "b"], fields: ["payload"] }]);
    setLocale("en-US");
    await flushPromises();

    expect(lastMock!.setColumns).toHaveBeenCalledTimes(2);
    const selectionEvents = onRangeSelectionChanged.mock.calls.length;
    runtime.emitRetiredRange();
    expect(onRangeSelectionChanged).toHaveBeenCalledTimes(selectionEvents);
    expect(runtime.selection()).toEqual([{ rowKeys: ["a", "b"], fields: ["payload"] }]);
    expect(onRangeSelectionChanged).toHaveBeenLastCalledWith({ rowKeys: ["a", "b"], fields: ["payload"] });
    wrapper.unmount();
  });

  it.each(["row", "column", "table", "workspace"])("does not resurrect a range after its %s disappears", async (boundary) => {
    const table = useTableStore();
    const workspace = useWorkspaceStore();
    workspace.selectTable("users");
    const gridEl = ref<HTMLElement | null>(document.createElement("div"));
    const columns = ["id", "payload", "status"].map(makeColumn);
    table.appendPage(makePage([{ rowKey: "a" }, { rowKey: "b" }], columns));
    const wrapper = mountHost(gridEl);
    await flushPromises();
    const runtime = installRangeRuntime(lastMock!);
    runtime.select("payload", "b");
    table.beginLoad();
    await flushPromises();
    if (boundary === "table") workspace.selectTable("orders");
    if (boundary === "workspace") useWorkspaceSessionStore().sessionEpoch += 1;
    table.setDatasetReady(makeDatasetReady(
      boundary === "row" ? [{ rowKey: "b" }] : [{ rowKey: "a" }, { rowKey: "b" }],
      boundary === "column" ? [makeColumn("id"), makeColumn("status")] : columns,
    ));
    await flushPromises();
    // A later refresh must not resurrect a removed selection when names return.
    table.setDatasetReady(makeDatasetReady([{ rowKey: "a" }, { rowKey: "b" }], columns));
    await flushPromises();
    expect(runtime.selection()).not.toContainEqual({ rowKeys: ["a", "b"], fields: ["payload"] });
    wrapper.unmount();
  });

  it.each(["row", "column"])("retains independent ranges when another range loses a %s", async (boundary) => {
    const table = useTableStore();
    const gridEl = ref<HTMLElement | null>(document.createElement("div"));
    const columns = ["id", "payload", "status"].map(makeColumn);
    table.appendPage(makePage([{ rowKey: "a" }, { rowKey: "b" }], columns));
    const onRangeSelectionChanged = vi.fn();
    const wrapper = mountHost(gridEl, { onRangeSelectionChanged });
    await flushPromises();
    const runtime = installRangeRuntime(lastMock!);
    runtime.select("payload");
    runtime.addSelection("b", "status");

    table.setDatasetReady(makeDatasetReady(
      boundary === "row" ? [{ rowKey: "a" }] : [{ rowKey: "a" }, { rowKey: "b" }],
      boundary === "column" ? [makeColumn("id"), makeColumn("payload")] : columns,
    ));
    await flushPromises();

    const retained = { rowKeys: ["a"], fields: ["payload"] };
    expect(runtime.selection()).toEqual([retained]);
    expect(onRangeSelectionChanged).toHaveBeenLastCalledWith(retained);
    wrapper.unmount();
  });

  it.each(["data", "columns"])("keeps a later user range when an earlier %s application completes", async (operation) => {
    const table = useTableStore();
    useWorkspaceStore().selectTable("users");
    const gridEl = ref<HTMLElement | null>(document.createElement("div"));
    const columns = ["id", "payload", "status"].map(makeColumn);
    table.appendPage(makePage([{ rowKey: "a" }, { rowKey: "b" }], columns));
    const wrapper = mountHost(gridEl);
    await flushPromises();
    const runtime = installRangeRuntime(lastMock!);
    runtime.select();
    const method = operation === "data" ? lastMock!.setData : lastMock!.setColumns;
    const apply = method.getMockImplementation() as (data: unknown) => unknown;
    let finish!: () => void;
    method.mockImplementation((data) => {
      apply(data);
      return new Promise<void>(resolve => { finish = resolve; });
    });
    if (operation === "data") {
      table.setDatasetReady(makeDatasetReady([{ rowKey: "a" }, { rowKey: "b" }], columns));
    } else {
      setLocale("en-US");
    }
    await flushPromises();
    gridEl.value!.dispatchEvent(new Event("pointerdown"));
    runtime.select("status");
    finish();
    await flushPromises();
    expect(runtime.selection()).toEqual([{ rowKeys: ["a"], fields: ["status"] }]);
    wrapper.unmount();
  });

  it("restores a data-only refresh without taking focus from an open dialog", async () => {
    const table = useTableStore();
    const gridEl = ref<HTMLElement | null>(document.createElement("div"));
    const columns = ["id", "payload", "status"].map(makeColumn);
    table.appendPage(makePage([{ rowKey: "a" }, { rowKey: "b" }], columns));
    const wrapper = mountHost(gridEl);
    await flushPromises();
    const runtime = installRangeRuntime(lastMock!);
    runtime.select();
    const input = document.createElement("input");
    document.body.append(input);
    input.focus();
    table.setDatasetReady(makeDatasetReady([{ rowKey: "a" }, { rowKey: "b" }], columns));
    await flushPromises();
    expect(runtime.selection()).toEqual([{ rowKeys: ["a"], fields: ["payload"] }]);
    expect(document.activeElement).toBe(input);
    setLocale("en-US");
    await flushPromises();
    expect(runtime.selection()).toEqual([{ rowKeys: ["a"], fields: ["payload"] }]);
    expect(document.activeElement).toBe(input);
    input.remove();
    wrapper.unmount();
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
    // createGrid now takes a 3rd options arg { editSchema, onCellEdited,
    // onValidationError } so editors attach on first paint when the schema has
    // already arrived. Here no editSchema has been set, so editSchema is null
    // and the callbacks are forwarding wrappers (useTabulator wraps them to
    // avoid stale capture).
    expect(createGrid).toHaveBeenCalledWith(gridEl.value, page1, {
      editSchema: null,
      onCellEdited: expect.any(Function),
      onValidationError: expect.any(Function),
      relationLookup: {
        relations: new Map(),
        lookups: new Map(),
        relationEditAvailable: false,
        lookupQueryAvailable: false,
        lookupUnavailableReason: null,
        onRelationEditRequested: undefined,
      },
    });
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

  it("queues data until tableBuilt when Tabulator is not initialized", async () => {
    mockStartsInitialized = false;
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");

    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));
    await flushPromises();
    table.setDatasetReady(
      makeDatasetReady([{ id: 1 }, { id: 2 }], [makeColumn("id")]),
    );
    await flushPromises();
    expect(lastMock!.setData).not.toHaveBeenCalled();

    const tableBuilt = lastMock!.on.mock.calls
      .find((call) => call[0] === "tableBuilt")?.[1] as () => void;
    tableBuilt();
    await flushPromises();
    expect(lastMock!.setData).toHaveBeenCalledWith([{ id: 1 }, { id: 2 }]);
    wrapper.unmount();
  });

  it("defers dataset replacement until an active cell editor finishes", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");

    table.beginLoad();
    table.appendPage(makePage([{ id: 1, name: "draft" }], [makeColumn("id")]));
    await flushPromises();

    const editing = lastMock!.on.mock.calls.find((call) => call[0] === "cellEditing")?.[1] as
      | (() => void)
      | undefined;
    const edited = lastMock!.on.mock.calls.find((call) => call[0] === "cellEdited")?.[1] as
      | (() => void)
      | undefined;
    expect(editing).toBeTypeOf("function");
    expect(edited).toBeTypeOf("function");
    lastMock!.setColumns.mockClear();

    editing!();
    table.beginLoad();
    table.setDatasetReady(
      makeDatasetReady([{ id: 1, name: "server refresh" }], [makeColumn("id")]),
    );
    await flushPromises();
    expect(lastMock!.setData).not.toHaveBeenCalled();
    expect(lastMock!.setColumns).not.toHaveBeenCalled();

    edited!();
    await flushPromises();
    expect(lastMock!.setData).toHaveBeenCalledOnce();
    expect(lastMock!.setColumns).toHaveBeenCalledOnce();
    expect(lastMock!.setData).toHaveBeenCalledWith([{ id: 1, name: "server refresh" }]);
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

  it("waits for an in-flight data application before destroying Tabulator", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));
    await flushPromises();

    let finishSetData!: () => void;
    lastMock!.setData.mockImplementation(() => new Promise<void>((resolve) => {
      finishSetData = resolve;
    }));
    table.setDatasetReady(
      makeDatasetReady([{ id: 1 }, { id: 2 }], [makeColumn("id")]),
    );
    await flushPromises();
    expect(lastMock!.setData).toHaveBeenCalledOnce();

    wrapper.unmount();
    expect(lastMock!.destroy).not.toHaveBeenCalled();
    finishSetData();
    await flushPromises();
    expect(lastMock!.destroy).toHaveBeenCalledOnce();
  });

  /**
   * Task M3: when tableStore.editSchema arrives AFTER the grid is already
   * initialized, useTabulator rebuilds the columns in place via setColumns so
   * per-column editors attach without a full grid rebuild. The editSchema
   * typically arrives via a separate `table.editSchemaLoaded` host event.
   */
  it("rebuilds columns via setColumns when editSchema arrives after init", async () => {
    const { buildTabulatorColumns } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();

    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    await flushPromises();

    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));
    await flushPromises();
    expect(lastMock).not.toBeNull();
    expect(lastMock!.setColumns).not.toHaveBeenCalled();

    // editSchema arrives -> setColumns called with columns built from the
    // current schema + the new editSchema (editors attach in place).
    table.setEditSchema(
      [
        {
          name: "id",
          storageName: "id",
          dataType: "integer",
          editable: false,
          nullable: false,
          primaryKey: true,
          editor: { kind: "number", storage: "integer" },
          validation: [],
        },
      ],
      { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 1 },
    );
    await flushPromises();

    expect(lastMock!.setColumns).toHaveBeenCalledTimes(1);
    expect(buildTabulatorColumns).toHaveBeenCalled();
    wrapper.unmount();
  });

  it("rebinds identical columns and edit schema after a table refresh generation", async () => {
    const { buildTabulatorColumns } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    const columns = [makeColumn("id")];
    const editableSchema = [{
      name: "id",
      storageName: "id",
      dataType: "integer" as const,
      editable: true,
      nullable: false,
      primaryKey: false,
      editor: { kind: "number" as const, storage: "integer" as const },
      validation: [],
    }];

    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], columns));
    table.setEditSchema(
      editableSchema,
      { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 1 },
    );
    await flushPromises();
    lastMock!.setColumns.mockClear();
    vi.mocked(buildTabulatorColumns).mockClear();

    // A formula/realtime refresh can return byte-for-byte identical schema.
    // The new generation must still rebind editors after the store reset.
    table.reset();
    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], columns));
    table.setEditSchema(
      editableSchema,
      { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 2 },
    );
    await flushPromises();

    expect(lastMock!.setColumns).toHaveBeenCalled();
    expect(vi.mocked(buildTabulatorColumns).mock.calls.some(
      (call) => Array.isArray(call[1]) && call[1][0]?.editable === true,
    )).toBe(true);
    wrapper.unmount();
  });

  it("serializes a dataset column rebuild with the following edit-schema rebuild", async () => {
    const { buildTabulatorColumns } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    const columns = [makeColumn("id")];
    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], columns));
    await flushPromises();

    let finishFirst!: () => void;
    const firstRefresh = new Promise<void>((resolve) => {
      finishFirst = resolve;
    });
    lastMock!.setColumns
      .mockImplementationOnce(() => firstRefresh)
      .mockResolvedValue(undefined);
    lastMock!.setColumns.mockClear();
    vi.mocked(buildTabulatorColumns).mockClear();

    table.beginLoad();
    table.setDatasetReady(makeDatasetReady([{ id: 1 }], columns));
    await flushPromises();
    expect(lastMock!.setColumns).toHaveBeenCalledTimes(1);

    const editableSchema = [{
      name: "id",
      storageName: "id",
      dataType: "integer" as const,
      editable: true,
      nullable: false,
      primaryKey: false,
      editor: { kind: "number" as const, storage: "integer" as const },
      validation: [],
    }];
    table.setEditSchema(
      editableSchema,
      { databaseSessionId: "s", schemaRevision: "sr", dataRevision: 1 },
    );
    await flushPromises();
    expect(lastMock!.setColumns).toHaveBeenCalledTimes(1);

    finishFirst();
    await flushPromises();
    expect(lastMock!.setColumns).toHaveBeenCalledTimes(2);
    expect(vi.mocked(buildTabulatorColumns).mock.calls.at(-1)?.[1]).toEqual(editableSchema);
    wrapper.unmount();
  });

  it("rebuilds localized column chrome when the locale changes", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    const placeholder = document.createElement("div");
    placeholder.className = "tabulator-placeholder-contents";
    placeholder.textContent = "暂无记录，使用“+”添加第一行";
    gridEl.value.append(placeholder);
    const focusedCell = document.createElement("button");
    focusedCell.textContent = "selected";
    gridEl.value.append(focusedCell);
    document.body.append(gridEl.value);
    focusedCell.focus();
    const range = document.createRange();
    range.selectNodeContents(focusedCell);
    window.getSelection()?.addRange(range);
    table.beginLoad();
    table.appendPage(makePage([], [makeColumn("id")]));
    await flushPromises();
    lastMock!.setColumns.mockClear();
    const removeRange = vi.fn();
    lastMock!.getRanges.mockReturnValue([{ remove: removeRange }]);
    lastMock!.setColumns.mockImplementation(() => {
      expect(removeRange).toHaveBeenCalledOnce();
      expect(document.activeElement).not.toBe(focusedCell);
      expect(window.getSelection()?.rangeCount).toBe(0);
    });

    setLocale("en-US");
    await flushPromises();

    expect(lastMock!.setColumns).toHaveBeenCalledTimes(1);
    expect(placeholder.textContent).toBe("No records yet — use + to add the first row");
    wrapper.unmount();
    gridEl.value.remove();
  });

  it("rebuilds relation columns when collection and relation IDs change at equal revisions", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const relations = useRelationLookupStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    table.beginLoad();
    table.appendPage(makePage(
      [{ rowKey: "1" }],
      [{
        ...makeColumn("relation"),
        kind: "relation",
        relationId: "orders.customer",
      }],
    ));
    await flushPromises();

    const ordersRelation = relationDescriptor("orders", "customer");
    const ordersGeneration = relations.beginContext("orders");
    relations.acceptContext(
      ordersGeneration,
      relationSchema("orders", ordersRelation),
      [],
      relationCapabilities,
    );
    await flushPromises();
    expect(lastMock!.setColumns).toHaveBeenCalledTimes(1);
    lastMock!.setColumns.mockClear();

    const articlesRelation = relationDescriptor("articles", "author");
    const articlesGeneration = relations.beginContext("articles");
    relations.acceptContext(
      articlesGeneration,
      relationSchema("articles", articlesRelation),
      [],
      relationCapabilities,
    );
    await flushPromises();

    expect(lastMock!.setColumns).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  /**
   * Task M3: when the caller supplies an onCellEdited callback, it must be
   * forwarded to createGrid so committed edits reach mutationService. We
   * verify the wiring by reading the options createGrid was called with and
   * invoking the captured onCellEdited wrapper.
   */
  it("forwards onCellEdited to createGrid so edits reach the caller", async () => {
    const { createGrid } = await import("@/grid/createGrid");
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const onCellEdited = vi.fn();

    const Host = defineComponent({
      setup() {
        useTabulator(gridEl, { onCellEdited });
        return () => h("div");
      },
    });
    const wrapper = mount(Host);
    gridEl.value = document.createElement("div");
    await flushPromises();

    table.beginLoad();
    table.appendPage(makePage([{ id: 1 }], [makeColumn("id")]));
    await flushPromises();

    expect(createGrid).toHaveBeenCalledTimes(1);
    const thirdArg = (createGrid as unknown as {
      mock: { calls: unknown[][] };
    }).mock.calls[0]![2] as {
      onCellEdited?: (
        a: number,
        b: string,
        c: unknown,
        d: unknown,
        digest: string | null,
      ) => void;
    };
    expect(typeof thirdArg.onCellEdited).toBe("function");
    // Invoke the captured wrapper — it should forward to our onCellEdited.
    const digest = `sha256:${"a".repeat(64)}`;
    thirdArg.onCellEdited!(7, "name", "old", "new", digest);
    expect(onCellEdited).toHaveBeenCalledWith(
      7,
      "name",
      "old",
      "new",
      digest,
    );
    wrapper.unmount();
  });

  it("reports Tabulator multi-cell ranges so revision history can be disabled", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const onRangeSelectionChanged = vi.fn();
    const Host = defineComponent({
      setup() {
        useTabulator(gridEl, { onRangeSelectionChanged });
        return () => h("div");
      },
    });
    const wrapper = mount(Host);
    gridEl.value = document.createElement("div");
    table.beginLoad();
    table.appendPage(makePage([{ rowKey: 1 }, { rowKey: 2 }], [makeColumn("status")]));
    await flushPromises();

    const rangeHandler = lastMock!.on.mock.calls.find((call) => call[0] === "rangeChanged")?.[1] as
      | ((range: unknown) => void)
      | undefined;
    expect(rangeHandler).toBeTypeOf("function");
    const range = {
      getRows: () => [
        { getData: () => ({ rowKey: 1 }) },
        { getData: () => ({ rowKey: 2 }) },
      ],
      getColumns: () => [{ getField: () => "status" }],
    };
    lastMock!.getRanges.mockReturnValue([range]);
    rangeHandler!(range);
    expect(onRangeSelectionChanged).toHaveBeenCalledWith({ rowKeys: [1, 2], fields: ["status"] });
    wrapper.unmount();
  });

  it("forwards user sort/filter/group events as a full-dataset query AST", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const onViewQueryChanged = vi.fn();
    const Host = defineComponent({
      setup() {
        useTabulator(gridEl, { onViewQueryChanged });
        return () => h("div");
      },
    });
    const wrapper = mount(Host);
    gridEl.value = document.createElement("div");
    table.beginLoad();
    table.appendPage(makePage([{ rowKey: 1 }], [makeColumn("price")], { mode: "remote", totalRows: 50_000 }));
    await flushPromises();

    lastMock!.getSorters.mockReturnValue([{ field: "price", dir: "desc" }]);
    lastMock!.getHeaderFilters.mockReturnValue([{ field: "status", value: "signed" }]);
    const sorted = lastMock!.on.mock.calls.find((call) => call[0] === "dataSorted")?.[1] as () => void;
    sorted();
    expect(onViewQueryChanged).not.toHaveBeenCalled();
    const tableBuilt = lastMock!.on.mock.calls.find((call) => call[0] === "tableBuilt")?.[1] as () => void;
    tableBuilt();
    sorted();
    expect(onViewQueryChanged).toHaveBeenLastCalledWith({
      headerFilters: [{ field: "status", operator: "eq", value: "signed", logic: "AND" }],
      sorts: [{ field: "price", direction: "desc", nullsLast: true }],
      groups: [],
    });

    const grouped = lastMock!.on.mock.calls.find((call) => call[0] === "dataGrouped")?.[1] as (groups: unknown[]) => void;
    grouped([{ getField: () => "customer", getSubGroups: () => [] }]);
    expect(onViewQueryChanged).toHaveBeenLastCalledWith(expect.objectContaining({
      groups: [{ field: "customer", direction: "asc", bucket: "value" }],
    }));
    wrapper.unmount();
  });

  it("requests a cursor window near the bottom and restores the viewport after replacement", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const onWindowBoundary = vi.fn();
    const Host = defineComponent({
      setup() {
        useTabulator(gridEl, { onWindowBoundary });
        return () => h("div");
      },
    });
    const wrapper = mount(Host);
    gridEl.value = document.createElement("div");
    table.appendPage(makePage(
      Array.from({ length: 40 }, (_, index) => ({ rowKey: index + 1 })),
      [makeColumn("id")],
    ));
    await flushPromises();

    const scrollTo = vi.fn();
    const row = (rowKey: number) => ({
      getData: () => ({ rowKey }),
      scrollTo,
    });
    lastMock!.getRows.mockImplementation((range?: string) =>
      range === "visible"
        ? [row(35), row(36)]
        : Array.from({ length: 40 }, (_, index) => row(index + 1)),
    );
    const scrollHandler = lastMock!.on.mock.calls.find(
      call => call[0] === "scrollVertical",
    )?.[1] as (() => void) | undefined;
    expect(scrollHandler).toBeTypeOf("function");
    scrollHandler!();
    expect(onWindowBoundary).toHaveBeenCalledOnce();

    lastMock!.getSelectedRows.mockReturnValue([row(36)]);
    table.setDatasetReady(makeDatasetReady(
      Array.from({ length: 41 }, (_, index) => ({ rowKey: index + 1 })),
      [makeColumn("id")],
    ));
    await flushPromises();

    expect(lastMock!.getRow).not.toHaveBeenCalled();
    expect(scrollTo).toHaveBeenCalledWith("top", false);
    expect(lastMock!.selectRow).toHaveBeenCalledWith([36]);
    wrapper.unmount();
    expect(lastMock!.off).toHaveBeenCalledWith("scrollVertical", scrollHandler);
  });

  it("does not ask Tabulator to restore rows removed by a dataset replacement", async () => {
    const gridEl = ref<HTMLElement | null>(null);
    const table = useTableStore();
    const wrapper = mountHost(gridEl);
    gridEl.value = document.createElement("div");
    table.appendPage(makePage(
      [{ rowKey: "removed" }, { rowKey: "kept" }],
      [makeColumn("id")],
    ));
    await flushPromises();

    const row = (rowKey: string) => ({ getData: () => ({ rowKey }) });
    lastMock!.getRows.mockImplementation((range?: string) =>
      range === "visible" ? [row("removed")] : [row("removed"), row("kept")],
    );
    lastMock!.getSelectedRows.mockReturnValue([row("removed"), row("kept")]);
    lastMock!.setData.mockImplementation(async () => {
      lastMock!.getRows.mockImplementation((range?: string) =>
        range === "visible" ? [row("kept")] : [row("kept")],
      );
    });

    table.setDatasetReady(makeDatasetReady([{ rowKey: "kept" }], [makeColumn("id")]));
    await flushPromises();

    expect(lastMock!.getRow).not.toHaveBeenCalled();
    expect(lastMock!.selectRow).toHaveBeenCalledWith(["kept"]);
    wrapper.unmount();
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
