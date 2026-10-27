import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises } from "@vue/test-utils";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { ref } from "vue";
import GridHost from "./GridHost.vue";
import * as tabulatorRuntime from "@/composables/useTabulator";
import { TABULATOR_INJECTION_KEY } from "./tabulatorInjection";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useViewQueryStore } from "@/stores/viewQueryStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { useUiStore } from "@/stores/uiStore";
import type { NormalizedRelationDescriptor } from "@/contracts";
import { buildColumns } from "@/grid/createGrid";
import { setHostBridgeForTesting } from "@/services/bridgeContext";
import type { HostBridge } from "@/bridge/hostBridge";

interface FormatterTestCell {
  getField(): string;
  getValue(): unknown;
  setValue(value: unknown): void;
  getRow(): { getData(): Record<string, unknown> };
  getElement(): HTMLElement;
}

function runFormatterOnRendered(
  column: ReturnType<typeof buildColumns>[number] | undefined,
  cell: FormatterTestCell,
): HTMLElement {
  expect(column).toBeDefined();
  expect(typeof column?.formatter).toBe("function");
  const formatter = column?.formatter as (
    formattedCell: FormatterTestCell,
    params: Record<string, unknown>,
    onRendered: (callback: () => void) => void,
  ) => HTMLElement;
  return formatter(cell, {}, (callback) => callback());
}

describe("GridHost history selection", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts the empty overlay below the measured header and follows header resizing", async () => {
    let restore: (()=>Promise<void>) | undefined;
    let resize!: ()=>void;
    const disconnect=vi.fn();
    vi.stubGlobal("ResizeObserver",class implements ResizeObserver {
      constructor(callback:ResizeObserverCallback){resize=()=>callback([],this);}
      observe=vi.fn();unobserve=vi.fn();disconnect=disconnect;
    });
    const runtime=vi.spyOn(tabulatorRuntime,"useTabulator").mockImplementation((_element,options)=>{
      restore=options?.onPresentationRestore;
      return {tabulator:ref(null),dataApplying:ref(false),clearHeaderFilterForMenu:vi.fn()};
    });
    useTableStore().setDatasetReady({table:"empty",columns:[],rows:[],offset:0,limit:100,totalRows:0,mode:"remote"});
    const wrapper=mount(GridHost);
    try {
      const host=wrapper.get(".grid-host").element;
      const header=document.createElement("div");header.className="tabulator-header";
      wrapper.get(".tabulator-mount").element.append(header);
      vi.spyOn(host,"getBoundingClientRect").mockReturnValue(new DOMRect(20,100,400,500));
      const geometry=vi.spyOn(header,"getBoundingClientRect").mockReturnValue(new DOMRect(20,100,400,72));
      await restore?.();await wrapper.vm.$nextTick();
      const overlay=wrapper.get('[data-testid="grid-empty-state"]').element as HTMLElement;
      expect(overlay.style.top).toBe("72px");
      geometry.mockReturnValue(new DOMRect(20,100,400,105));resize();await wrapper.vm.$nextTick();
      expect(overlay.style.top).toBe("105px");
      wrapper.unmount();expect(disconnect).toHaveBeenCalled();
    } finally {wrapper.unmount();runtime.mockRestore();vi.unstubAllGlobals();}
  });

  it("mounts Tabulator below the themed grid host instead of on the host itself", () => {
    const wrapper = mount(GridHost);
    expect(wrapper.find(".grid-host > .tabulator-mount").exists()).toBe(true);
  });

  it("applies compact density to the Tabulator geometry scope", async () => {
    const ui = useUiStore();
    ui.setDensity("comfortable");
    const wrapper = mount(GridHost);
    expect(wrapper.get(".grid-wrapper").classes()).toContain("density-comfortable");

    ui.setDensity("compact");
    await wrapper.vm.$nextTick();
    expect(wrapper.get(".grid-wrapper").classes()).toContain("density-compact");
  });

  it("renders a keyboard-focusable first-row CTA and emits insert intent", async () => {
    const store = useTableStore();
    store.setDatasetReady({
      table: "items",
      columns: [],
      rows: [],
      offset: 0,
      limit: 100,
      totalRows: 0,
      mode: "remote",
      revision: {
        databaseSessionId: "session-1",
        schemaRevision: "schema-1",
        dataRevision: 1,
      },
    });
    const wrapper = mount(GridHost, { attachTo: document.body });
    const button = wrapper.get('[data-testid="grid-add-first-row"]');

    expect(button.element.tagName).toBe("BUTTON");
    (button.element as HTMLButtonElement).focus();
    expect(document.activeElement).toBe(button.element);
    await button.trigger("click");
    expect(wrapper.emitted("insertFirstRow")).toHaveLength(1);
    wrapper.unmount();
  });

  it("keeps the Tabulator mount stable while the zero-row CTA appears and disappears", async () => {
    const store = useTableStore();
    store.setDatasetReady({
      table: "items",
      columns: [],
      rows: [],
      offset: 0,
      limit: 100,
      totalRows: 0,
      mode: "remote",
      revision: {
        databaseSessionId: "session-1",
        schemaRevision: "schema-1",
        dataRevision: 1,
      },
    });
    const wrapper = mount(GridHost);
    const mountElement = wrapper.get(".tabulator-mount").element;
    expect(wrapper.find('[data-testid="grid-empty-state"]').exists()).toBe(true);

    store.setDatasetReady({
      table: "items",
      columns: [],
      rows: [{ rowKey: "row-1" }],
      offset: 0,
      limit: 100,
      totalRows: 1,
      mode: "remote",
      revision: {
        databaseSessionId: "session-1",
        schemaRevision: "schema-1",
        dataRevision: 2,
      },
    });
    await wrapper.vm.$nextTick();

    expect(wrapper.get(".tabulator-mount").element).toBe(mountElement);
    expect(wrapper.find('[data-testid="grid-empty-state"]').exists()).toBe(false);
  });

  it("distinguishes the row-number gutter from a single data cell", async () => {
    const rowElement = document.createElement("div");
    const rowNumber = document.createElement("div");
    const statusCell = document.createElement("div");
    rowElement.append(rowNumber, statusCell);
    const fakeGrid = {
      getRows: () => [{
        getData: () => ({ rowKey: 42, status: "done" }),
        getElement: () => rowElement,
        getCells: () => [{
          getField: () => "__vt_row_number",
          getElement: () => rowNumber,
        }, {
          getField: () => "status",
          getElement: () => statusCell,
        }],
      }],
      destroy: () => undefined,
    };
    const wrapper = mount(GridHost, {
      global: {
        provide: {
          [TABULATOR_INJECTION_KEY as symbol]: ref(fakeGrid),
        },
      },
    });
    wrapper.get(".grid-host").element.append(rowElement);

    rowNumber.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("selectionChange")?.[0]).toEqual([{ scope: "row", rowKey: 42 }]);
    expect(rowElement.classList.contains("vt-row-selected")).toBe(true);

    statusCell.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("selectionChange")?.[1]).toEqual([{ scope: "cell", rowKey: 42, field: "status" }]);
    expect(statusCell.classList.contains("vt-cell-selected")).toBe(true);
  });

  it("includes the clicked field in the context-menu intent", async () => {
    const rowElement = document.createElement("div");
    const statusCell = document.createElement("div");
    rowElement.append(statusCell);
    const fakeGrid = {
      getRows: () => [{
        getData: () => ({ rowKey: "row-1" }),
        getElement: () => rowElement,
        getCells: () => [{ getField: () => "status", getElement: () => statusCell }],
      }],
      destroy: () => undefined,
    };
    const wrapper = mount(GridHost, {
      global: { provide: { [TABULATOR_INJECTION_KEY as symbol]: ref(fakeGrid) } },
    });
    wrapper.get(".grid-host").element.append(rowElement);
    statusCell.dispatchEvent(new MouseEvent("contextmenu", {
      bubbles: true,
      clientX: 12,
      clientY: 24,
    }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("rowContext")?.[0]).toEqual([{
      rowKey: "row-1",
      field: "status",
      x: 12,
      y: 24,
    }]);

  });

  it("routes a column-header context menu to field settings without selecting a row", async () => {
    const wrapper = mount(GridHost);
    const header = document.createElement("div");
    header.className = "tabulator-col";
    header.setAttribute("tabulator-field", "status");
    const title = document.createElement("span");
    title.textContent = "Status";
    header.append(title);
    wrapper.get(".tabulator-mount").element.append(header);

    title.dispatchEvent(new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: 32,
      clientY: 48,
    }));
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("columnContext")?.[0]).toEqual([{
      field: "status",
      x: 32,
      y: 48,
    }]);
    expect(wrapper.emitted("rowContext")).toBeUndefined();
  });

  it("does not collapse an active multi-cell range back to a single click", async () => {
    const rowElements = [document.createElement("div"), document.createElement("div")];
    const cells = rowElements.map((rowElement) => {
      const status = document.createElement("div");
      const owner = document.createElement("div");
      const notes = document.createElement("div");
      rowElement.append(status, owner, notes);
      return { status, owner, notes };
    });
    const columns = [
      { getField: () => "status" },
      { getField: () => "owner" },
    ];
    const rows = rowElements.map((rowElement, index) => ({
      getData: () => ({ rowKey: `row-${index + 1}` }),
      getElement: () => rowElement,
      getCells: () => [
        { getField: () => "status", getElement: () => cells[index]!.status },
        { getField: () => "owner", getElement: () => cells[index]!.owner },
        { getField: () => "notes", getElement: () => cells[index]!.notes },
      ],
    }));
    const fakeGrid = {
      getRows: () => rows,
      getRanges: () => [{
        getRows: () => rows,
        getColumns: () => columns,
      }],
      destroy: () => undefined,
    };
    const wrapper = mount(GridHost, {
      global: { provide: { [TABULATOR_INJECTION_KEY as symbol]: ref(fakeGrid) } },
    });
    wrapper.get(".grid-host").element.append(...rowElements);
    cells[0]!.status.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("selectionChange")).toBeUndefined();
    expect(cells.flatMap(({ status, owner }) => [status, owner])
      .every((cell) => cell.getAttribute("aria-selected") === "true")).toBe(true);
    expect(cells.every(({ notes }) => notes.getAttribute("aria-selected") === null)).toBe(true);
  });

  it("opens the structured JSON editor for an editable JSON cell", async () => {
    const store = useTableStore();
    store.appendPage({
      table: "items",
      columns: [{
        name: "metadata",
        title: "Metadata",
        dataType: "json",
        editable: true,
        nullable: true,
      }],
      rows: [{ rowKey: "row-1", metadata: { nested: [1, true] } }],
      offset: 0,
      limit: 100,
      totalRows: 1,
      mode: "remote",
    });
    store.setEditSchema([{
      name: "metadata",
      storageName: "metadata",
      dataType: "json",
      editable: true,
      nullable: true,
      primaryKey: false,
      editor: { kind: "json" },
      validation: [],
    }], {
      databaseSessionId: "session-1",
      schemaRevision: "schema_0001",
      dataRevision: 1,
    });

    const rowElement = document.createElement("div");
    const jsonCell = document.createElement("div");
    rowElement.append(jsonCell);
    const fakeGrid = {
      getRows: () => [{
        getData: () => ({ rowKey: "row-1", metadata: { nested: [1, true] } }),
        getElement: () => rowElement,
        getCells: () => [{
          getField: () => "metadata",
          getElement: () => jsonCell,
        }],
      }],
      destroy: () => undefined,
    };
    const wrapper = mount(GridHost, {
      attachTo: document.body,
      global: { provide: { [TABULATOR_INJECTION_KEY as symbol]: ref(fakeGrid) } },
    });
    wrapper.get(".grid-host").element.append(rowElement);
    runFormatterOnRendered(
      buildColumns(store.pages[0]!, store.editSchema)[0],
      {
        getField: () => "metadata",
        getValue: () => ({ nested: [1, true] }),
        setValue: () => undefined,
        getRow: () => ({ getData: () => ({ rowKey: "row-1" }) }),
        getElement: () => jsonCell,
      },
    );
    expect(jsonCell.tabIndex).toBe(0);
    jsonCell.focus();
    expect(document.activeElement).toBe(jsonCell);
    jsonCell.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("jsonEdit")?.[0]).toEqual([{
      rowKey: "row-1",
      column: store.schema?.[0],
      value: { nested: [1, true] },
      expectedDigest: null,
      trigger: jsonCell,
    }]);

    const tabulatorKeydown = vi.fn();
    jsonCell.addEventListener("keydown", tabulatorKeydown);
    const enterEvent = new KeyboardEvent("keydown", {
      bubbles: true,
      cancelable: true,
      key: "Enter",
    });
    jsonCell.dispatchEvent(enterEvent);
    await wrapper.vm.$nextTick();
    expect(enterEvent.defaultPrevented).toBe(true);
    expect(tabulatorKeydown).not.toHaveBeenCalled();
    expect(wrapper.emitted("jsonEdit")?.[1]).toEqual([{
      rowKey: "row-1",
      column: store.schema?.[0],
      value: { nested: [1, true] },
      expectedDigest: null,
      trigger: jsonCell,
    }]);
    wrapper.unmount();
  });

  it("opens managed attachments and valid relations with the keyboard", async () => {
    const table = useTableStore();
    const relations = useRelationLookupStore();
    const relation: NormalizedRelationDescriptor = {
      relationId: "orders.customer",
      fieldRef: "customer",
      sourceCollection: "orders",
      kind: "m2o",
      relatedCollection: "customers",
      unique: true,
      nullable: true,
      onDelete: "nullify",
      preset: "standard",
      selfRelation: false,
      managed: true,
      state: "valid",
      displayTemplate: "{{name}}",
      diagnostics: [],
    };
    table.appendPage({
      table: "orders",
      columns: [{
        name: "files",
        title: "附件",
        fieldId: "fld_files",
        kind: "attachment",
        dataType: "text",
        editable: true,
        nullable: true,
        attachmentPolicy: {
          maxFiles: 2,
          maxBytesPerFile: 1024,
          allowedMimeTypes: ["text/plain"],
          thumbnailVariants: [],
          protected: false,
        },
      }, {
        name: "customer",
        title: "客户",
        kind: "relation",
        relationId: relation.relationId,
        dataType: "text",
        editable: true,
        nullable: true,
      }],
      rows: [{ rowKey: "row-1", files: [], customer: "customer-1" }],
      offset: 0,
      limit: 100,
      totalRows: 1,
      mode: "remote",
    });
    const generation = relations.beginContext("orders");
    relations.acceptContext(generation, {
      collection: "orders",
      primaryKey: "id",
      columns: [],
      normalizedRelations: [relation],
      schemaRevision: "schema-1",
      permissionRevision: "permission-1",
      capabilityHash: "capability-1",
      lookupRevision: "lookup-1",
    }, [], {
      contract: "vibetable.relation-capabilities.v1",
      relationReadV1: true,
      relationEditV1: true,
      lookupQueryV1: true,
    });

    const rowElement = document.createElement("div");
    const attachmentCell = document.createElement("div");
    const relationCell = document.createElement("div");
    rowElement.append(attachmentCell, relationCell);
    const fakeGrid = {
      getRows: () => [{
        getData: () => ({
          rowKey: "row-1",
          files: [],
          customer: "customer-1",
        }),
        getElement: () => rowElement,
        getCells: () => [{
          getField: () => "files",
          getElement: () => attachmentCell,
        }, {
          getField: () => "customer",
          getElement: () => relationCell,
        }],
      }],
      destroy: () => undefined,
    };
    const wrapper = mount(GridHost, {
      attachTo: document.body,
      global: { provide: { [TABULATOR_INJECTION_KEY as symbol]: ref(fakeGrid) } },
    });
    wrapper.get(".grid-host").element.append(rowElement);
    const columns = buildColumns(
      table.pages[0]!,
      null,
      {
        relations: new Map([[relation.relationId, relation]]),
        lookups: new Map(),
        relationEditAvailable: true,
        lookupQueryAvailable: true,
        onRelationEditRequested: () => undefined,
        onAttachmentOpenRequested: () => undefined,
      },
    );
    const accessibilityCell = (
      field: string,
      value: unknown,
      element: HTMLElement,
    ): FormatterTestCell => ({
      getField: () => field,
      getValue: () => value,
      setValue: () => undefined,
      getRow: () => ({ getData: () => ({ rowKey: "row-1" }) }),
      getElement: () => element,
    });
    runFormatterOnRendered(
      columns[0],
      accessibilityCell("files", [], attachmentCell),
    );
    runFormatterOnRendered(
      columns[1],
      accessibilityCell("customer", "customer-1", relationCell),
    );

    attachmentCell.focus();
    expect(document.activeElement).toBe(attachmentCell);
    attachmentCell.dispatchEvent(new KeyboardEvent("keydown", {
      bubbles: true,
      key: "Enter",
    }));
    relationCell.dispatchEvent(new KeyboardEvent("keydown", {
      bubbles: true,
      key: " ",
    }));
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("attachmentOpen")?.[0]).toEqual([{
      rowKey: "row-1",
      column: table.schema?.[0],
      trigger: attachmentCell,
    }]);
    expect(wrapper.emitted("relationEdit")?.[0]).toEqual([{
      rowKey: "row-1",
      field: "customer",
      descriptor: relation,
      value: "customer-1",
    }]);
    attachmentCell.focus();
    attachmentCell.dispatchEvent(new KeyboardEvent("keydown", {
      bubbles: true,
      key: "F10",
      shiftKey: true,
    }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted("rowContext")?.[0]).toEqual([{
      rowKey: "row-1",
      field: "files",
      x: 0,
      y: 0,
    }]);
    wrapper.unmount();
  });

  it("表头值面板：动态权威取值、空白原始值、应用走 runtime 管道", async () => {
    const store = useTableStore();
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }], { orders: "Orders" });
    workspace.selectTable("orders");
    store.setDatasetReady({
      table: "orders",
      columns: [
        { name: "note", title: "备注", dataType: "text", editable: true, nullable: true },
      ],
      rows: [{ rowKey: "1", note: "a" }],
      offset: 0, limit: 100, totalRows: 1, mode: "remote",
      revision: { databaseSessionId: "s1", schemaRevision: "schema-1", dataRevision: 1 },
    });
    let resolveValues!: (value: unknown) => void;
    const request = vi.fn(async () => new Promise(resolve => { resolveValues = resolve; }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);
    const wrapper = mount(GridHost, { attachTo: document.body });

    wrapper.get(".grid-wrapper").element.dispatchEvent(new CustomEvent("vt-header-menu", {
      bubbles: true,
      detail: { field: "note" },
    }));
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-testid="vt-header-menu-loading"]').exists()).toBe(true);
    expect(request).toHaveBeenCalledWith("query.view", {
      tableId: "orders",
      view: {
        query: { limit: 1, offset: 0 },
        groups: [{ field: "note", direction: "asc", bucket: "value" }],
        groupOffset: 0,
        groupLimit: 100,
      },
    });

    resolveValues({
      page: { rows: [], snapshot: { table: "orders" } },
      groupRows: [{ key: ["a"], count: 2, summaries: [] }, { key: [null], count: 1, summaries: [] }],
      groupOffset: 0, hasMoreGroups: false,
    });
    await flushPromises();
    expect(wrapper.find('[data-testid="vt-header-menu-loading"]').exists()).toBe(false);
    expect(wrapper.findAll(".vt-header-menu__option").map(label => label.text())).toEqual(["a2", "（空白）1"]);

    // 全不选：应用禁用；勾选“（空白）+ a”后应用。
    document.querySelector<HTMLElement>('[data-testid="vt-header-menu-none"]')!.click();
    await flushPromises();
    expect(
      (document.querySelector('[data-testid="vt-header-menu-apply"]') as HTMLButtonElement).disabled,
    ).toBe(true);
    document.querySelector<HTMLElement>('[data-testid="vt-header-menu-option-null"]')!.click();
    await flushPromises();
    document.querySelector<HTMLElement>('[data-testid="vt-header-menu-apply"]')!.click();
    await flushPromises();

    const query = wrapper.emitted("viewQueryChange")?.at(-1)?.[0] as {
      headerFilters: readonly import("@/contracts").FilterExpression[];
    };
    expect(query.headerFilters).toEqual([]);
    const viewQuery = useViewQueryStore();
    expect(viewQuery.filters).toEqual([
      { field: "note", operator: "is_null" },
    ]);
    // 真实父链：presetViewController 收到同一 payload 会调 updateRuntime，
    // 同步后的镜像不得把旧条件加回。
    viewQuery.updateRuntime({
      headerFilters: query.headerFilters.map(filter => ({ ...filter })),
      sorts: [], groups: [],
    });
    expect(viewQuery.filters).toEqual([
      { field: "note", operator: "is_null" },
    ]);
    expect(wrapper.find('[data-testid="vt-header-menu"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("动态分页面板：未加载的既有勾选值随应用原样保留，不静默丢失", async () => {
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }], { orders: "Orders" });
    workspace.selectTable("orders");
    useTableStore().setDatasetReady({
      table: "orders",
      columns: [{ name: "note", title: "备注", dataType: "text", editable: true, nullable: true }],
      rows: [], offset: 0, limit: 100, totalRows: 0, mode: "remote",
    });
    // 首页只加载 a 与空白，后续分页未取：既有筛选勾选了 a、b、z。
    setHostBridgeForTesting({
      request: vi.fn(async () => ({
        page: { rows: [], snapshot: { table: "orders" } },
        groupRows: [{ key: ["a"], count: 2, summaries: [] }, { key: [null], count: 1, summaries: [] }],
        groupOffset: 0, hasMoreGroups: true,
      })),
    } as unknown as HostBridge);
    const view = useViewQueryStore();
    view.replace("orders", {
      kind: "table", layout: "table",
      filters: [{ field: "note", operator: "in", value: ["a", "b", "z"] }],
      sorts: [], search: "", visibleFields: ["note"],
    }, ["note"]);
    const wrapper = mount(GridHost, {
      attachTo: document.body,
      props: { onViewQueryChange: query => view.updateRuntime(query) },
    });

    wrapper.get(".grid-wrapper").element.dispatchEvent(new CustomEvent("vt-header-menu", {
      bubbles: true, detail: { field: "note" },
    }));
    await flushPromises();
    // 未改动勾选直接应用：b/z 尚未加载，也必须随提交保留。
    document.querySelector<HTMLElement>('[data-testid="vt-header-menu-apply"]')!.click();
    await flushPromises();

    expect(view.filters).toEqual([
      { field: "note", operator: "in", value: ["a", "b", "z"] },
    ]);
    wrapper.unmount();
  });

  it("replaces and clears a checked header value through the real parent runtime callback", async () => {
    const workspace=useWorkspaceStore();
    workspace.setOpened([{collection:"orders",metadata:{}}],{orders:"Orders"});
    workspace.selectTable("orders");
    useTableStore().setDatasetReady({table:"orders",columns:[{
      name:"status",title:"状态",dataType:"text",editable:true,nullable:true,
      filterInput:"select",filterOptions:[{value:"a",label:"A"},{value:"b",label:"B"}],
    }],rows:[],offset:0,limit:100,totalRows:0,mode:"remote"});
    const view=useViewQueryStore();
    view.replace("orders",{kind:"table",layout:"table",filters:[{field:"status",operator:"eq",value:"a"}],sorts:[],search:"",visibleFields:["status"]},["status"]);
    const wrapper=mount(GridHost,{attachTo:document.body,props:{onViewQueryChange:query=>view.updateRuntime(query)}});
    const open=async()=>{
      wrapper.get(".grid-wrapper").element.dispatchEvent(new CustomEvent("vt-header-menu",{bubbles:true,detail:{field:"status"}}));
      await flushPromises();
    };
    await open();
    wrapper.getComponent({name:"HeaderFilterMenu"}).vm.$emit("apply",["s:b"]);
    await flushPromises();
    expect(view.filters).toEqual([{field:"status",operator:"eq",value:"b"}]);
    await open();
    wrapper.getComponent({name:"HeaderFilterMenu"}).vm.$emit("clear");
    await flushPromises();
    expect(view.filters).toEqual([]);
    view.filters=[{field:"status",operator:"eq",value:"a"},{field:"other",operator:"eq",value:"b",logic:"OR"}];
    await open();
    expect(wrapper.text()).toContain("请先在高级筛选中清除或调整条件");
    expect(wrapper.find('[data-testid="vt-header-menu-apply"]').exists()).toBe(false);
    expect(view.filters).toHaveLength(2);
    wrapper.unmount();
  });

  it("表头值面板：换表后过期取值响应不回写", async () => {
    const store = useTableStore();
    const workspace = useWorkspaceStore();
    workspace.setOpened([{ collection: "orders", metadata: {} }, { collection: "other", metadata: {} }], {});
    workspace.selectTable("orders");
    store.setDatasetReady({
      table: "orders",
      columns: [{ name: "note", title: "备注", dataType: "text", editable: true, nullable: true }],
      rows: [], offset: 0, limit: 100, totalRows: 0, mode: "remote",
      revision: { databaseSessionId: "s1", schemaRevision: "schema-1", dataRevision: 1 },
    });
    let resolveValues!: (value: unknown) => void;
    setHostBridgeForTesting({
      request: vi.fn(async () => new Promise(resolve => { resolveValues = resolve; })),
    } as unknown as HostBridge);
    const wrapper = mount(GridHost, { attachTo: document.body });

    wrapper.get(".grid-wrapper").element.dispatchEvent(new CustomEvent("vt-header-menu", {
      bubbles: true, detail: { field: "note" },
    }));
    await wrapper.vm.$nextTick();
    workspace.selectTable("other");
    await flushPromises();
    resolveValues({
      page: { rows: [], snapshot: { table: "orders" } },
      groupRows: [{ key: ["late"], count: 1, summaries: [] }],
      groupOffset: 0, hasMoreGroups: false,
    });
    await flushPromises();
    expect(wrapper.find('[data-testid="vt-header-menu"]').exists()).toBe(false);
    wrapper.unmount();
  });
});
