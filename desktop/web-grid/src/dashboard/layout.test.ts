import { describe, expect, it } from "vitest";
import {
  panelCountDiagnostic,
  adjustPositionWithKeyboard,
  positionsCollide,
  projectLayout,
  validateLayout,
  type LayoutItem,
} from "./layout";

const item = (id: string, x: number, y: number, width: number, height: number): LayoutItem => ({
  id,
  position: { x, y, width, height },
});

describe("dashboard layout", () => {
  it("uses half-open rectangles for collision detection", () => {
    expect(positionsCollide(item("a", 0, 0, 4, 3).position, item("b", 4, 0, 4, 3).position)).toBe(false);
    expect(positionsCollide(item("a", 0, 0, 4, 3).position, item("b", 3, 2, 4, 3).position)).toBe(true);
  });

  it("validates bounds, minimum sizes, duplicates and collisions", () => {
    const diagnostics = validateLayout([
      { ...item("same", 10, 0, 3, 2), minWidth: 4 },
      item("same", 10, 1, 2, 2),
    ]);
    expect(diagnostics.map((entry) => entry.code)).toEqual(expect.arrayContaining([
      "layout_duplicate_id",
      "layout_out_of_bounds",
      "layout_below_minimum",
      "layout_collision",
    ]));
  });

  it("warns after 30 panels and rejects after 100", () => {
    expect(panelCountDiagnostic(30)).toBeNull();
    expect(panelCountDiagnostic(31)).toMatchObject({ code: "panel_count_warning", severity: "warning" });
    expect(panelCountDiagnostic(101)).toMatchObject({ code: "panel_limit_exceeded", severity: "error" });
  });

  it("projects to narrow grids without mutating or writing responsive positions back", () => {
    const canonical = [item("later", 6, 4, 6, 3), item("first", 0, 0, 6, 2)];
    const snapshot = JSON.stringify(canonical);
    const projected = projectLayout(canonical, 1);
    expect(projected.map((entry) => entry.id)).toEqual(["first", "later"]);
    expect(projected.map((entry) => entry.position)).toEqual([
      { x: 0, y: 0, width: 1, height: 2 },
      { x: 0, y: 2, width: 1, height: 3 },
    ]);
    expect(projected[0]?.canonicalPosition).toEqual(canonical[1]?.position);
    expect(JSON.stringify(canonical)).toBe(snapshot);
  });

  it("rejects invalid responsive column counts", () => {
    expect(() => projectLayout([], 0)).toThrow(RangeError);
    expect(() => projectLayout([], 13)).toThrow(RangeError);
  });

  it("supports bounded keyboard movement and resize on the canonical grid", () => {
    const source = { x: 0, y: 0, width: 4, height: 3 };
    expect(adjustPositionWithKeyboard(source, "ArrowLeft", false)).toEqual(source);
    expect(adjustPositionWithKeyboard(source, "ArrowRight", false)).toEqual({ ...source, x: 1 });
    expect(adjustPositionWithKeyboard(source, "ArrowDown", false)).toEqual({ ...source, y: 1 });
    expect(adjustPositionWithKeyboard(source, "ArrowRight", true)).toEqual({ ...source, width: 5 });
    expect(adjustPositionWithKeyboard(source, "ArrowUp", true)).toEqual({ ...source, height: 2 });
    expect(adjustPositionWithKeyboard({ x: 11, y: 0, width: 1, height: 1 }, "ArrowRight", true).width).toBe(1);
  });
});
