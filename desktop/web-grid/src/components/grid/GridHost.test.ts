import { beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { ref } from "vue";
import GridHost from "./GridHost.vue";
import { TABULATOR_INJECTION_KEY } from "./tabulatorInjection";

describe("GridHost history selection", () => {
  beforeEach(() => setActivePinia(createPinia()));

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
});
