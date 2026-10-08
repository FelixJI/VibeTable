import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import type { ColumnSchema, TablePage } from "@/contracts";
import { buildColumns } from "./createGrid";
import { displayValue } from "@/components/grid/recordViewUtils";
import RecordFieldValue from "@/components/grid/RecordFieldValue.vue";
import type { DisplaySpec } from "@/contracts/generated/schemaV2";
import { enumDisplayParts, formatFieldDisplay, progressDisplay } from "./commonFieldDisplay";

const display: DisplaySpec = { kind: "number", preset: "number", displayScale: 2, scaleMode: "max",
  trimTrailingZeros: true, useGrouping: true, currency: "CNY", percentStorage: "ratio", unit: null,
  precision: "minute", timezone: "UTC", mode: "text", trueLabel: "完成<script>", falseLabel: "未完成" };
const format = (value: unknown, dataType: "decimal" | "date" | "datetime" | "time" | "boolean" | "text", patch: Partial<DisplaySpec> = {}) => formatFieldDisplay(value, { dataType, display: { ...display, ...patch } }, "zh-CN");

describe("common field display stays separate from stored values", () => {
  it("keeps progress text as raw ratio, clamps only the bar and distinguishes zero/null", () => {
    expect([0, 0.125, 1, 1.2, -0.1, 1.5].map(value => format(value, "decimal", { preset: "progress" })))
      .toEqual(["0%", "12.5%", "100%", "120%", "-10%", "150%"]);
    const custom = { ...display, preset: "progress", progressStart: 1, progressTarget: 2 };
    expect(formatFieldDisplay(1.5, { dataType: "decimal", display: custom }, "zh-CN")).toBe("150%");
    expect(progressDisplay(1.5, custom)).toEqual({ width: 50, outside: false });
    expect(progressDisplay(-0.1, { ...display, preset: "progress" })).toEqual({ width: 0, outside: true });
    expect(progressDisplay(1.2, { ...display, preset: "progress" })).toEqual({ width: 100, outside: true });
    expect(format(null, "decimal", { preset: "rating" })).toBe("—");
    expect(format(0, "decimal", { preset: "rating" })).toBe("☆☆☆☆☆ 0/5");
    expect(format(5, "decimal", { preset: "rating" })).toBe("★★★★★ 5/5");
    expect(format(1.5, "decimal", { preset: "rating" })).toBe("1.5");
    expect(format(1.5, "decimal", { preset: "future" })).toBe("1.5");
    expect(format(1.5, "decimal", { preset: "progress", progressStart: -1e308, progressTarget: 1e308 })).toBe("1.5");
  });
  it("keeps calendar dates stable and handles UTC day boundary and DST without changing instants", () => {
    expect(format("2026-03-08 00:00:00.000Z", "date", { timezone: "America/Los_Angeles" })).toBe("2026-03-08");
    expect(format("2026-01-01T00:30:00Z", "datetime", { timezone: "America/Los_Angeles", precision: "day" })).toBe("2025-12-31");
    expect(format("2026-03-08T06:59:59Z", "datetime", { timezone: "America/New_York", precision: "second" })).toBe("2026-03-08 01:59:59");
    expect(format("2026-03-08T07:00:00Z", "datetime", { timezone: "America/New_York", precision: "second" })).toBe("2026-03-08 03:00:00");
    expect(format("2026-11-01T05:30:00Z", "datetime", { timezone: "America/New_York" })).toBe("2026-11-01 01:30");
    expect(format("2026-11-01T06:30:00Z", "datetime", { timezone: "America/New_York" })).toBe("2026-11-01 01:30");
    expect(format("09:30:15.125", "time", { precision: "minute" })).toBe("09:30");
    expect(format("09:30:15.125", "time", { precision: "millisecond" })).toBe("09:30:15.125");
  });
  it("uses canonical option identity/order, marks retired values, and keeps all text safe", () => {
    const options = [{ optionId: "a", label: "中文", color: "#ffaa00", order: 0, state: "active" as const },
      { optionId: "b", label: "旧标签", color: "red;position:fixed", order: 1, state: "retired" as const }];
    expect(enumDisplayParts(["b", "a", "unknown"], options)).toEqual([
      { text: "旧标签（已停用）", color: null }, { text: "中文", color: "#ffaa00" }, { text: "unknown", color: null }]);
    expect(format(null, "boolean")).toBe("—"); expect(format(false, "boolean")).toBe("未完成");
    expect(format(true, "boolean")).toBe("完成<script>");
    expect(format("+86 010-0012 ext.03", "text", { preset: "phone" })).toBe("+86 010-0012 ext.03");
    expect(format("<p>你好</p><script>alert(1)</script><b>世界</b>", "text", { kind: "editor" })).toBe("你好 世界");
    expect(format("x".repeat(300), "text")).toHaveLength(240);
  });
  it("grid and cards share text, colors and safe values; only http/https become links", () => {
    const render = (value: unknown, column: ColumnSchema): HTMLElement => {
      const page: TablePage = { table: "t", columns: [column], rows: [], offset: 0, limit: 1, totalRows: 0, mode: "remote" };
      const formatter = buildColumns(page)[0].formatter as (cell: { getValue(): unknown }) => HTMLElement;
      return formatter({ getValue: () => value });
    };
    const base: ColumnSchema = { name: "value", title: "值", dataType: "decimal", editable: true, nullable: true,
      display: { ...display, preset: "progress" } };
    for (const value of [null, 0, 0.125, 1.5]) expect(render(value, base).textContent).toBe(displayValue(value, base));
    expect(render(1.5, base).getAttribute("aria-label")).toContain("超出显示范围");
    const enumColumn: ColumnSchema = { ...base, dataType: "text", display: { ...display, kind: "select", preset: "" },
      enumOptions: [{ optionId: "a", label: "中文", color: "#ffaa00", order: 0, state: "active" },
        { optionId: "b", label: "旧值", color: "blue", order: 1, state: "retired" }] };
    const raw = ["b", "unknown", "a"];
    expect(render(raw, enumColumn).textContent).toBe(displayValue(raw, enumColumn));
    expect(render([], enumColumn).textContent).toBe("—");
    expect(render([], enumColumn).classList.contains("vt-cell-empty")).toBe(true);
    const jsonColumn: ColumnSchema = { ...base, dataType: "json", display: { ...display, kind: "json", preset: "" } };
    const jsonArray = render([], jsonColumn);
    expect(jsonArray.textContent).toBe("[…] · 0 项");
    expect(jsonArray.classList.contains("vt-cell-empty")).toBe(false);
    const card = mount(RecordFieldValue, { props: { value: raw, column: enumColumn } });
    expect(card.text()).toBe(displayValue(raw, enumColumn)); expect(card.html()).toContain("border-bottom: 3px solid blue"); card.unmount();
    const urlColumn = { ...base, dataType: "text" as const, display: { ...display, kind: "url" as const, preset: "" } };
    expect(render("https://example.com/path", urlColumn).querySelector("a")?.href).toBe("https://example.com/path");
    expect(render("https://example.com/path", urlColumn).querySelector("a")?.target).toBe("_blank");
    expect(render("https://example.com/path", urlColumn).querySelector("a")?.rel).toBe("noreferrer");
    expect(render("javascript:alert(1)", urlColumn).querySelector("a")).toBeNull();
    expect(render("mailto:user@example.com", urlColumn).querySelector("a")).toBeNull();
    const richColumn = { ...urlColumn, display: { ...display, kind: "editor" as const, preset: "" } };
    expect(render("<b>你好</b><script>alert(1)</script>", richColumn).querySelector("script")).toBeNull();
    expect(render("<b>你好</b><script>alert(1)</script>", richColumn).textContent).toBe("你好");
  });

});
