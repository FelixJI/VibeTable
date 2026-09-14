import { afterEach, describe, expect, it, vi } from "vitest";
import { TabulatorFull } from "tabulator-tables";

// Exercise the real locked Tabulator handlers; jsdom only supplies missing
// sticky-column geometry. No selection or scrolling methods are mocked.
interface Cell {
  getElement(): HTMLElement;
  column: { modules: { frozen?: { position: string } } };
}
interface Range {
  end: { row: number; col: number };
  element: HTMLElement;
  setBounds(start: Cell, end?: Cell): void;
  getData(): Record<string, unknown>[];
  layout(): void;
}
interface RangeManager {
  activeRange: Range;
  getCell(row: number, col: number): Cell;
  resetRanges(): Range;
  navigate(jump: boolean, expand: boolean, direction: string): boolean;
  keyNavigate(direction: string, event: KeyboardEvent): void;
  handleCellMouseDown(event: MouseEvent, cell: Cell): void;
  handleCellMouseMove(event: MouseEvent, cell: Cell): void;
}

interface InvestigatedTable {
  initialized: boolean;
  modules: { selectRange: RangeManager };
}
const cleanups: (() => void)[] = [];
afterEach(() => { cleanups.splice(0).forEach(cleanup => cleanup()); });

async function fixture(side: "left" | "right" = "left") {
  const element = document.createElement("div");
  Object.defineProperties(element, {
    offsetWidth: { get: () => 400 }, offsetHeight: { get: () => 200 },
    clientWidth: { get: () => 400 }, clientHeight: { get: () => 200 },
  });
  document.body.append(element);
  const columns = side === "left" ? [
      { field: "rownum", width: 42, frozen: true, headerSort: false },
      { field: "title", width: 160, frozen: true },
      { field: "amount", width: 120 },
      { field: "other", width: 300 },
    ] : [
      { field: "rownum", width: 42, frozen: true, headerSort: false },
      { field: "amount", width: 120 },
      { field: "other", width: 120 },
      { field: "spacer", width: 300 },
      { field: "title", width: 160, frozen: true },
    ];
  const warnings = vi.spyOn(console, "warn");
  const table = new TabulatorFull(element, {
    columns,
    data: [{ rownum: 1, title: "A", amount: 5, other: "B" }],
    selectableRange: true, selectableRangeRows: true,
    selectableRangeAutoFocus: false, renderVertical: "basic",
  });
  cleanups.push(() => {
    table.destroy?.(); element.remove();
    try { expect(warnings).not.toHaveBeenCalled(); } finally { warnings.mockRestore(); }
  });
  const real = table as unknown as InvestigatedTable;
  await vi.waitFor(() => expect(real.initialized).toBe(true));
  const holder = element.querySelector<HTMLElement>(".tabulator-tableholder")!;
  const widths = columns.map(column => column.width);
  const starts = columns.map((_, index) => widths.slice(0, index).reduce((a, b) => a + b, 0));
  const rect = (left: number, top: number, width: number, height: number) =>
    new DOMRect(left, top, width, height);
  for (const selector of [".tabulator-tableholder", ".tabulator-header"]) {
    const target = element.querySelector<HTMLElement>(selector)!;
    target.getBoundingClientRect = () => rect(0, 0, 400, 200);
    Object.defineProperties(target, {
      clientWidth: { configurable: true, get: () => 400 },
      clientHeight: { configurable: true, get: () => 200 },
      offsetWidth: { configurable: true, get: () => 400 },
      offsetHeight: { configurable: true, get: () => 200 },
    });
  }
  await table.setData([
    { rownum: 1, title: "A", amount: 5, other: "B" },
    { rownum: 2, title: "C", amount: 6, other: "D" },
  ]);
  for (const [index, row] of element.querySelectorAll<HTMLElement>(".tabulator-row").entries()) {
    row.getBoundingClientRect = () => rect(0, index * 250 - holder.scrollTop, 622, 30);
    Object.defineProperties(row, {
      offsetHeight: { configurable: true, get: () => 30 },
      offsetTop: { configurable: true, get: () => index * 250 },
    });
  }
  for (const [index, column] of columns.entries()) {
    const position = real.modules.selectRange.getCell(0, index).column.modules.frozen?.position;
    const visibleLeft = () => position === "left"
      ? Math.max(starts[index]! - holder.scrollLeft, starts[index]!)
      : position === "right"
        ? Math.min(starts[index]! - holder.scrollLeft, 400 - widths[index]!)
        : starts[index]! - holder.scrollLeft;
    const field = column.field;
    for (const target of element.querySelectorAll<HTMLElement>(`[tabulator-field="${field}"]`)) {
      // Sticky cells occupy their fixed viewport position, but offsetLeft is
      // measured in the scrolling content's coordinate system.
      Object.defineProperties(target, {
        offsetLeft: { configurable: true, get: () => visibleLeft() + holder.scrollLeft },
        offsetWidth: { configurable: true, get: () => widths[index]! },
      });
      target.getBoundingClientRect = () => rect(
        visibleLeft(), 0, widths[index]!, 30,
      );
    }
  }
  const manager = real.modules.selectRange;
  const range = manager.resetRanges();
  range.setBounds(manager.getCell(0, 1));
  return { table, holder, manager, range, columns };
}

describe("locked Tabulator frozen/range compatibility", () => {
  for (const side of ["left", "right"] as const) {
    it(`scrolls vertically without horizontal movement for a ${side} frozen destination`, async () => {
      const { holder, manager, range } = await fixture(side);
      range.setBounds(manager.getCell(0, side === "left" ? 1 : 4));
      holder.scrollLeft = 100;
      manager.keyNavigate("down", new KeyboardEvent("keydown", { key: "ArrowDown", cancelable: true }));
      expect(holder.scrollTop).toBe(80);
      expect(holder.scrollLeft).toBe(100);
      expect(manager.activeRange.end.row).toBe(1);
    });
    it(`keyboard navigation exposes both navigation directions with ${side} frozen columns`, async () => {
      const f = await fixture(side);
      const { holder, manager } = f;
      holder.scrollLeft = side === "left" ? 100 : 0;
      manager.keyNavigate("right", new KeyboardEvent("keydown", { key: "ArrowRight", cancelable: true }));
      const target = manager.getCell(0, 2).getElement().getBoundingClientRect();
      if (side === "left") expect(target.left).toBe(202);
      else expect(target.right).toBe(240);
      manager.keyNavigate("left", new KeyboardEvent("keydown", { key: "ArrowLeft", cancelable: true }));
      expect(holder.scrollLeft).toBe(0);
      expect(manager.activeRange.end.col).toBe(1);
    });

    it(`keyboard navigation keeps horizontal position on a ${side} frozen destination`, async () => {
      const f = await fixture(side);
      const { holder, manager, range } = f;
      const source = side === "left" ? 2 : 3;
      range.setBounds(manager.getCell(0, source));
      holder.scrollLeft = 100;
      manager.keyNavigate(side === "left" ? "left" : "right", new KeyboardEvent("keydown", { cancelable: true }));
      expect(holder.scrollLeft).toBe(100);
      expect(manager.getCell(0, manager.activeRange.end.col).column.modules.frozen?.position).toBe(side);
    });

    it(`preserves real drag selection endpoints across ${side} frozen columns`, async () => {
      const { holder, manager } = await fixture(side);
      holder.scrollLeft = 100;
      const frozenIndex = side === "left" ? 1 : 4;
      const normalIndex = 2;
      manager.handleCellMouseDown(new MouseEvent("mousedown", { buttons: 1 }), manager.getCell(0, frozenIndex));
      manager.handleCellMouseMove(new MouseEvent("mousemove", { buttons: 1 }), manager.getCell(0, normalIndex));
      expect(manager.activeRange.end.col).toBe(normalIndex);
      expect(manager.activeRange.getData()[0]).toMatchObject(
        side === "left" ? { title: "A", amount: 5 } : { title: "A", other: "B" },
      );
    });
  }
  it("assigns a business column after an unfrozen column to the right frozen group", async () => {
    const { manager } = await fixture("right");
    expect(manager.getCell(0, 0).column.modules.frozen?.position).toBe("left");
    expect(manager.getCell(0, 4).column.modules.frozen?.position).toBe("right");
  });

  it("reveals a destination obscured by a right-frozen business column", async () => {
    const { manager } = await fixture("right");
    manager.keyNavigate("right", new KeyboardEvent("keydown", { key: "ArrowRight", cancelable: true }));
    const destination = manager.getCell(0, 2).getElement().getBoundingClientRect();
    const frozenTitle = manager.getCell(0, 4).getElement().getBoundingClientRect();
    expect(destination.right).toBeLessThanOrEqual(frozenTitle.left);
  });

  it("keeps range data across the frozen boundary before and after horizontal scrolling", async () => {
    const { holder, manager, range } = await fixture();
    range.setBounds(manager.getCell(0, 1), manager.getCell(0, 2));
    expect(range.getData()).toEqual([{ title: "A", amount: 5 }]);
    holder.scrollLeft = 100;
    range.layout();
    expect(range.getData()).toEqual([{ title: "A", amount: 5 }]);
    expect(range.element.style.left).toBe("142px");
    expect(range.element.style.width).toBe("180px");
  });

  it("reveals the unfrozen destination when navigating right from frozen Title", async () => {
    const { holder, manager } = await fixture();
    holder.scrollLeft = 100;
    manager.keyNavigate("right", new KeyboardEvent("keydown", { key: "ArrowRight", cancelable: true }));
    const destination = manager.getCell(0, 2).getElement().getBoundingClientRect();
    const frozenTitle = manager.getCell(0, 1).getElement().getBoundingClientRect();
    expect(destination.left).toBeGreaterThanOrEqual(frozenTitle.right);
  });

  it("records an unfrozen-only overlay overlapping frozen cells after scrolling", async () => {
    const { holder, manager, range } = await fixture();
    holder.scrollLeft = 100;
    range.setBounds(manager.getCell(0, 2));
    range.layout();
    const viewportLeft = Number.parseFloat(range.element.style.left) - holder.scrollLeft;
    const frozenRight = manager.getCell(0, 1).getElement().getBoundingClientRect().right;
    // This alone is not a rendering bug: upstream frozen cells have z-index 11,
    // above the range overlay's z-index 10. A browser must verify the occlusion.
    expect(viewportLeft).toBe(102);
    expect(frozenRight).toBe(202);
  });

});

describe("unsupported frozen/range diagnostics", () => {
  const warning = "Using frozen columns that are not the range header in combination with the selectRange option may result in unpredictable behavior";
  const leaf = { field: "title", frozen: true, width: 160 };
  const cases = [
    { name: "parent-frozen group", columns: [{ title: "Group", frozen: true, columns: [{ field: "title" }] }] },
    { name: "nested parent-frozen group", columns: [{ title: "Group", frozen: true, columns: [{ title: "Nested", columns: [{ field: "title" }] }] }] },
    { name: "RTL", columns: [leaf], options: { textDirection: "rtl" } },
    { name: "virtual horizontal rendering", columns: [leaf], options: { renderHorizontal: "virtual" } },
  ];
  for (const scenario of cases) {
    it(`retains the diagnostic for ${scenario.name}`, async () => {
      const element = document.createElement("div");
      document.body.append(element);
      const warnings = vi.spyOn(console, "warn").mockImplementation(() => {});
      const table = new TabulatorFull(element, {
        columns: [...scenario.columns, { field: "amount" }],
        data: [{ title: "A", amount: 5 }],
        selectableRange: true, renderVertical: "basic",
        ...scenario.options,
      });
      cleanups.push(() => { table.destroy?.(); element.remove(); warnings.mockRestore(); });
      await vi.waitFor(() => expect((table as unknown as InvestigatedTable).initialized).toBe(true));
      expect(warnings).toHaveBeenCalledWith(warning);
    });
  }
});