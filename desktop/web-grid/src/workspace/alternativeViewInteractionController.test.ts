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

function calendarView(dateField = "startsAt"): PresetView {
  return {
    kind: "calendar",
    layout: "calendar",
    filters: [],
    sorts: [],
    search: "",
    visibleFields: ["title", dateField],
    dateField,
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

  it("does not authorize timezone-sensitive datetime moves", () => {
    const state = harness();
    state.setView(calendarView());
    state.setSchema([{
      name: "startsAt",
      title: "开始时间",
      dataType: "datetime",
      editable: true,
      nullable: true,
    }]);
    state.setRows([{
      rowKey: "row-1",
      title: "评审",
      startsAt: "2026-08-12T14:30:45+08:00",
      __vibetableDigest: digest,
    }]);

    expect(state.controller.calendarState()).toEqual({ enabled: false, movableRecords: [] });
    expect(state.controller.dispatch({
      type: "calendar.record.move",
      rowKey: "row-1",
      targetDate: "2026-08-20",
      expectedDigest: digest,
    })).toBe(false);
    expect(state.updateCell).not.toHaveBeenCalled();
  });

  it("describes only controller-authorized calendar records as movable", () => {
    const state = harness();
    state.setView(calendarView());
    state.setSchema([{
      name: "startsAt",
      title: "开始日期",
      dataType: "date",
      editable: true,
      nullable: true,
    }]);
    state.setRows([
      { rowKey: "row-1", startsAt: "2026-08-12", __vibetableDigest: digest },
      { rowKey: "row-2", startsAt: "invalid", __vibetableDigest: digest },
      { rowKey: "row-3", startsAt: "2026-08-13", __vibetableDigest: "stale" },
    ]);

    expect(state.controller.calendarState()).toEqual({
      enabled: true,
      movableRecords: [{ rowKey: "row-1", expectedDigest: digest }],
    });
  });

  it("moves a date value without introducing a time", () => {
    const state = harness();
    state.setView(calendarView("dueDate"));
    state.setSchema([{
      name: "dueDate",
      title: "截止日期",
      dataType: "date",
      editable: true,
      nullable: true,
    }]);
    state.setRows([{
      rowKey: 7,
      dueDate: "2026-08-12",
      __vibetableDigest: digest,
    }]);

    expect(state.controller.dispatch({
      type: "calendar.record.move",
      rowKey: 7,
      targetDate: "2026-08-20",
      expectedDigest: digest,
    })).toBe(true);
    expect(state.updateCell).toHaveBeenCalledWith(
      7,
      "dueDate",
      "2026-08-12",
      "2026-08-20",
      digest,
    );
  });

  it("fails closed for invalid or stale calendar move authority", () => {
    const arrangeCalendar = (state: ReturnType<typeof harness>): void => {
      state.setView(calendarView());
      state.setSchema([{
        name: "startsAt",
        title: "开始时间",
        dataType: "date",
        editable: true,
        nullable: true,
      }]);
      state.setRows([{
        rowKey: "row-1",
        startsAt: "2026-08-12",
        __vibetableDigest: digest,
      }]);
    };
    const cases: Array<{
      name: string;
      arrange: (state: ReturnType<typeof harness>) => void;
      targetDate?: string;
      expectedDigest?: string;
    }> = [
      { name: "non-calendar", arrange: state => state.setView(kanbanView()) },
      { name: "missing date field", arrange: state => state.setView(calendarView("missing")) },
      {
        name: "read-only",
        arrange: state => state.setSchema([{
          name: "startsAt", title: "开始时间", dataType: "date", editable: false, nullable: true,
        }]),
      },
      {
        name: "unsupported type",
        arrange: state => state.setSchema([{
          name: "startsAt", title: "开始时间", dataType: "datetime", editable: true, nullable: true,
        }]),
      },
      { name: "invalid target date", arrange: () => undefined, targetDate: "2026-02-30" },
      { name: "missing row", arrange: state => state.setRows([]) },
      {
        name: "stale digest",
        arrange: state => state.setRows([{
          rowKey: "row-1",
          startsAt: "2026-08-12",
          __vibetableDigest: `sha256:${"b".repeat(64)}`,
        }]),
      },
      { name: "malformed digest", arrange: () => undefined, expectedDigest: "stale" },
      {
        name: "invalid old value",
        arrange: state => state.setRows([{
          rowKey: "row-1", startsAt: "not-a-date", __vibetableDigest: digest,
        }]),
      },
      { name: "same value", arrange: () => undefined, targetDate: "2026-08-12" },
    ];

    for (const testCase of cases) {
      const state = harness();
      arrangeCalendar(state);
      testCase.arrange(state);
      expect(state.controller.dispatch({
        type: "calendar.record.move",
        rowKey: "row-1",
        targetDate: testCase.targetDate ?? "2026-08-20",
        expectedDigest: testCase.expectedDigest ?? digest,
      }), testCase.name).toBe(false);
      expect(state.updateCell, testCase.name).not.toHaveBeenCalled();
    }
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
