import { describe, expect, it, vi } from "vitest";

import type { ColumnSchema, PresetView } from "@/contracts";
import { createAlternativeViewInteractionController } from "./alternativeViewInteractionController";

const digest = `sha256:${"a".repeat(64)}`;

function kanbanView(groupField = "status"): PresetView {
  return {
    kind: "kanban",
    layout: "kanban",
    filters: [],
    sorts: [],
    search: "",
    visibleFields: ["title", groupField],
    groupField,
    titleField: "title",
  };
}

function statusColumn(overrides: Partial<ColumnSchema> = {}): ColumnSchema {
  return {
    name: "status",
    title: "状态",
    dataType: "text",
    editable: true,
    nullable: true,
    filterInput: "select",
    filterOptions: [
      { value: "opt_todo", label: "待处理" },
      { value: "opt_done", label: "已完成" },
    ],
    ...overrides,
  };
}

function harness() {
  let view: PresetView | null = kanbanView();
  let schema: readonly ColumnSchema[] = [statusColumn()];
  let rows: readonly Readonly<Record<string, unknown>>[] = [
    { rowKey: "row-1", title: "准备合同", status: "opt_todo", __vibetableDigest: digest },
  ];
  const updateCell = vi.fn();
  const controller = createAlternativeViewInteractionController({
    getActiveView: () => view,
    getSchema: () => schema,
    getRows: () => rows,
    updateCell,
  });
  return {
    controller,
    updateCell,
    setView: (next: PresetView | null) => { view = next; },
    setSchema: (next: readonly ColumnSchema[]) => { schema = next; },
    setRows: (next: readonly Readonly<Record<string, unknown>>[]) => { rows = next; },
  };
}

describe("alternative view interaction controller", () => {
  it("describes active option-id lanes while keeping labels presentation-only", () => {
    const { controller } = harness();

    expect(controller.kanbanState()).toEqual({
      enabled: true,
      lanes: [
        { optionId: "opt_todo", label: "待处理" },
        { optionId: "opt_done", label: "已完成" },
      ],
    });
  });

  it("routes a valid card move through updateCell with the authoritative old option id", () => {
    const { controller, updateCell } = harness();

    expect(controller.dispatch({
      type: "kanban.card.move",
      rowKey: "row-1",
      targetOptionId: "opt_done",
      expectedDigest: digest,
    })).toBe(true);
    expect(updateCell).toHaveBeenCalledOnce();
    expect(updateCell).toHaveBeenCalledWith(
      "row-1",
      "status",
      "opt_todo",
      "opt_done",
      digest,
    );
    expect(updateCell).not.toHaveBeenCalledWith(
      expect.anything(),
      expect.anything(),
      expect.anything(),
      "已完成",
      expect.anything(),
    );
  });

  it("fails closed when view, field, target, row, digest, or lane is stale", () => {
    const cases: Array<{
      name: string;
      arrange: (state: ReturnType<typeof harness>) => void;
      targetOptionId?: string;
      expectedDigest?: string;
    }> = [
      { name: "non-kanban", arrange: state => state.setView({ ...kanbanView(), kind: "gallery" }) },
      { name: "stale group field", arrange: state => state.setView(kanbanView("missing")) },
      {
        name: "non-select",
        arrange: state => state.setSchema([statusColumn({ filterInput: "text" })]),
      },
      {
        name: "read-only",
        arrange: state => state.setSchema([statusColumn({ editable: false })]),
      },
      { name: "unknown target", arrange: () => undefined, targetOptionId: "opt_retired" },
      { name: "missing row", arrange: state => state.setRows([]) },
      {
        name: "changed digest",
        arrange: state => state.setRows([{
          rowKey: "row-1",
          status: "opt_todo",
          __vibetableDigest: `sha256:${"b".repeat(64)}`,
        }]),
      },
      { name: "same lane", arrange: () => undefined, targetOptionId: "opt_todo" },
    ];

    for (const testCase of cases) {
      const state = harness();
      testCase.arrange(state);
      expect(state.controller.dispatch({
        type: "kanban.card.move",
        rowKey: "row-1",
        targetOptionId: testCase.targetOptionId ?? "opt_done",
        expectedDigest: testCase.expectedDigest ?? digest,
      }), testCase.name).toBe(false);
      expect(state.updateCell, testCase.name).not.toHaveBeenCalled();
    }
  });

  it("rejects malformed or duplicate option authority instead of guessing", () => {
    const missing = harness();
    missing.setSchema([statusColumn({ filterOptions: undefined })]);
    expect(missing.controller.kanbanState()).toEqual({ enabled: false, lanes: [] });

    const duplicate = harness();
    duplicate.setSchema([statusColumn({
      filterOptions: [
        { value: "opt_same", label: "甲" },
        { value: "opt_same", label: "乙" },
      ],
    })]);
    expect(duplicate.controller.kanbanState()).toEqual({ enabled: false, lanes: [] });
  });
});
