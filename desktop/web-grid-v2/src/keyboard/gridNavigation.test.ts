import { describe, it, expect } from "vitest";
import {
  moveUp,
  moveDown,
  moveLeft,
  moveRight,
  tabForward,
  tabBackward,
  type CellPos,
  type GridBounds,
} from "./gridNavigation";

const bounds: GridBounds = { rowCount: 5, colCount: 3 };
const pos = (row: number, col: number): CellPos => ({ row, col });

describe("gridNavigation.moveUp", () => {
  it("decrements row by 1", () => {
    expect(moveUp(pos(2, 1), bounds)).toEqual(pos(1, 1));
  });

  it("clamps at row 0", () => {
    expect(moveUp(pos(0, 1), bounds)).toEqual(pos(0, 1));
  });
});

describe("gridNavigation.moveDown", () => {
  it("increments row by 1", () => {
    expect(moveDown(pos(1, 1), bounds)).toEqual(pos(2, 1));
  });

  it("clamps at rowCount-1", () => {
    expect(moveDown(pos(4, 1), bounds)).toEqual(pos(4, 1));
  });
});

describe("gridNavigation.moveLeft / moveRight", () => {
  it("moveLeft decrements and clamps at col 0", () => {
    expect(moveLeft(pos(1, 2), bounds)).toEqual(pos(1, 1));
    expect(moveLeft(pos(1, 0), bounds)).toEqual(pos(1, 0));
  });

  it("moveRight increments and clamps at colCount-1", () => {
    expect(moveRight(pos(1, 1), bounds)).toEqual(pos(1, 2));
    expect(moveRight(pos(1, 2), bounds)).toEqual(pos(1, 2));
  });
});

describe("gridNavigation.tabForward", () => {
  it("moves right when not at right edge", () => {
    expect(tabForward(pos(1, 0), bounds)).toEqual(pos(1, 1));
  });

  it("wraps to next row's first cell at right edge", () => {
    expect(tabForward(pos(0, 2), bounds)).toEqual(pos(1, 0));
  });

  it("stays at last cell of last row (no further wrap)", () => {
    expect(tabForward(pos(4, 2), bounds)).toEqual(pos(4, 2));
  });
});

describe("gridNavigation.tabBackward", () => {
  it("moves left when not at left edge", () => {
    expect(tabBackward(pos(1, 2), bounds)).toEqual(pos(1, 1));
  });

  it("wraps to previous row's last cell at left edge", () => {
    expect(tabBackward(pos(1, 0), bounds)).toEqual(pos(0, 2));
  });

  it("stays at first cell of first row (no further wrap)", () => {
    expect(tabBackward(pos(0, 0), bounds)).toEqual(pos(0, 0));
  });
});
