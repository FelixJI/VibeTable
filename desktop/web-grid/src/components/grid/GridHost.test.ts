import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { ref } from "vue";
import GridHost from "./GridHost.vue";
import { TABULATOR_INJECTION_KEY } from "./tabulatorInjection";
import { useTableStore } from "@/stores/tableStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import { useUiStore } from "@/stores/uiStore";
import type { NormalizedRelationDescriptor } from "@/contracts";
import { buildColumns } from "@/grid/createGrid";

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
      loadedRows: 0,
      mode: "client",
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
      loadedRows: 0,
      mode: "client",
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
      loadedRows: 1,
      mode: "client",
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
      mode: "client",
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
      allowedCollections: [],
      junction: null,
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
      mode: "client",
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
});
