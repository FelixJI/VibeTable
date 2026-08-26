import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import type { ColumnSchema, PresetView } from "@/contracts";
import RecordGalleryView from "./RecordGalleryView.vue";
import RecordKanbanView from "./RecordKanbanView.vue";
import RecordTimelineView from "./RecordTimelineView.vue";

const schema = [
  { name: "title", title: "标题", dataType: "text", editable: true, nullable: false },
  { name: "status", title: "状态", dataType: "text", editable: true, nullable: true },
  { name: "start", title: "开始", dataType: "date", editable: true, nullable: true },
  { name: "end", title: "结束", dataType: "date", editable: true, nullable: true },
  { name: "cover", title: "封面", dataType: "text", editable: true, nullable: true },
] as ColumnSchema[];

function view(input: Partial<PresetView>): PresetView {
  return {
    filters: [],
    sorts: [],
    search: "",
    visibleFields: [],
    layout: input.kind ?? "table",
    ...input,
  };
}

describe("alternative record views", () => {
  it("groups records into kanban lanes and keeps blank values visible", () => {
    const wrapper = mount(RecordKanbanView, {
      props: {
        rows: [
          { rowKey: "1", title: "准备合同", status: "进行中" },
          { rowKey: "2", title: "等待确认", status: null },
          { rowKey: "3", title: "发送归档", status: "已完成" },
        ],
        schema,
        view: view({ kind: "kanban", groupField: "status", titleField: "title" }),
      },
    });
    expect(wrapper.get('[data-testid="record-kanban-view"]').text()).toContain("进行中");
    expect(wrapper.text()).toContain("已完成");
    expect(wrapper.text()).toContain("未分组");
    expect(wrapper.findAll('[data-testid="kanban-card"]')).toHaveLength(3);
    expect(wrapper.get('[data-testid="kanban-card"]').attributes("draggable")).toBe("false");
  });

  it("renders active select labels and emits an option-id move without mutating rows", async () => {
    const rowDigest = `sha256:${"a".repeat(64)}`;
    const rows = [
      { rowKey: "1", title: "准备合同", status: "opt_todo", __vibetableDigest: rowDigest },
      { rowKey: "2", title: "等待确认", status: null, __vibetableDigest: `sha256:${"b".repeat(64)}` },
    ];
    const wrapper = mount(RecordKanbanView, {
      props: {
        rows,
        schema,
        view: view({ kind: "kanban", groupField: "status", titleField: "title" }),
        interactionEnabled: true,
        laneOptions: [
          { optionId: "opt_todo", label: "待处理" },
          { optionId: "opt_doing", label: "进行中" },
          { optionId: "opt_done", label: "已完成" },
        ],
      },
    });

    expect(wrapper.findAll('[data-testid="kanban-lane"]')).toHaveLength(4);
    expect(wrapper.text()).toContain("待处理");
    expect(wrapper.text()).toContain("进行中");
    expect(wrapper.text()).toContain("已完成");
    expect(wrapper.text()).toContain("未分组");
    expect(wrapper.text()).not.toContain("opt_todo");
    expect(wrapper.get('[data-option-id="opt_done"]').findAll('[data-testid="kanban-card"]'))
      .toHaveLength(0);

    await wrapper.get('[data-row-key="1"]').trigger("dragstart");
    await wrapper.get('[data-option-id="opt_done"]').trigger("drop");

    expect(wrapper.emitted("cardMove")).toEqual([[
      { rowKey: "1", targetOptionId: "opt_done", expectedDigest: rowDigest },
    ]]);
    expect(rows[0]?.status).toBe("opt_todo");
  });

  it("renders local gallery covers and falls back for missing or failed covers", async () => {
    const wrapper = mount(RecordGalleryView, {
      props: {
        rows: [
          { rowKey: "1", title: "蓝图", cover: "/covers/cover.png", status: "草稿" },
          { rowKey: "2", title: "无封面", cover: null, status: "完成" },
        ],
        schema,
        view: view({ kind: "gallery", coverField: "cover", titleField: "title" }),
      },
    });
    expect(wrapper.get('img[src="/covers/cover.png"]')).toBeTruthy();
    expect(wrapper.findAll('[data-testid="gallery-card"]')).toHaveLength(2);
    expect(wrapper.get('[data-testid="gallery-cover-placeholder"]')).toBeTruthy();
    await wrapper.get('img[src="/covers/cover.png"]').trigger("error");
    expect(wrapper.findAll('[data-testid="gallery-cover-placeholder"]')).toHaveLength(2);
  });

  it("renders timeline records as proportional horizontal ranges", () => {
    const wrapper = mount(RecordTimelineView, {
      global: { plugins: [createPinia()] },
      props: {
        rows: [
          { rowKey: "1", title: "设计", start: "2026-08-01", end: "2026-08-04" },
          { rowKey: "2", title: "开发", start: "2026-08-05", end: "2026-08-10" },
        ],
        schema,
        view: view({ kind: "timeline", dateField: "start", endDateField: "end", titleField: "title" }),
      },
    });
    expect(wrapper.findAll('[data-testid="timeline-record"]')).toHaveLength(2);
    expect(wrapper.get('[data-testid="timeline-scale"]').text()).toContain("8月");
  });

  it("renders authorized point-date labels in UTC logical-day space", () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const value = "2026-08-12 00:00:00.000Z";
    const instant = new Date("2026-08-12T00:00:00.000Z");
    const locale = "en-US";
    const formatOptions = { month: "short", day: "numeric" } as const;
    const logicalDateLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "UTC",
    }).format(instant);
    const negativeOffsetLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "America/Los_Angeles",
    }).format(instant);
    const viewportStart = new Date("2026-08-05T00:00:00.000Z");
    const logicalViewportLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "UTC",
    }).format(viewportStart);
    const negativeOffsetViewportLabel = new Intl.DateTimeFormat(locale, {
      ...formatOptions,
      timeZone: "America/Los_Angeles",
    }).format(viewportStart);
    expect(negativeOffsetLabel).not.toBe(logicalDateLabel);
    const NativeDateTimeFormat = Intl.DateTimeFormat;
    function DateTimeFormatMock(
      locales?: Intl.LocalesArgument,
      options?: Intl.DateTimeFormatOptions,
    ): Intl.DateTimeFormat {
      return new NativeDateTimeFormat(locales, options);
    }
    const formatter = vi.spyOn(Intl, "DateTimeFormat").mockImplementation(
      DateTimeFormatMock as typeof Intl.DateTimeFormat,
    );
    const pinia = createPinia();
    pinia.state.value.ui = { locale };

    const wrapper = mount(RecordTimelineView, {
      global: { plugins: [pinia] },
      props: {
        rows: [{ rowKey: "row-1", title: "逻辑日期", start: value, __vibetableDigest: digest }],
        schema,
        view: view({
          kind: "timeline",
          dateField: "start",
          endDateField: null,
          titleField: "title",
        }),
        interactionEnabled: true,
        movableRecords: [{ rowKey: "row-1", expectedDigest: digest }],
      },
    });

    expect(formatter).toHaveBeenCalledWith(locale, {
      ...formatOptions,
      timeZone: "UTC",
    });
    expect(wrapper.get(".timeline-title small").text()).toBe(logicalDateLabel);
    expect(wrapper.get('[data-testid="timeline-scale"]').text()).toContain(logicalViewportLabel);
    expect(wrapper.get('[data-testid="timeline-scale"]').text())
      .not.toContain(negativeOffsetViewportLabel);
    expect(wrapper.get(".timeline-title small").text()).not.toBe(negativeOffsetLabel);
    expect(wrapper.get('[data-testid="timeline-track"]').attributes("data-start-date"))
      .toBe("2026-08-05");
    expect(wrapper.get('[data-testid="timeline-track"]').attributes("data-end-date"))
      .toBe("2026-08-19");
    formatter.mockRestore();
  });

  it("emits an opaque point-date move only for a controller-authorized Timeline record", async () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const rows = [
      { rowKey: "row-1", title: "可移动", start: "2026-08-12", __vibetableDigest: digest },
      { rowKey: "row-2", title: "范围锚点", start: "2026-08-20", __vibetableDigest: digest },
    ];
    const wrapper = mount(RecordTimelineView, {
      global: { plugins: [createPinia()] },
      props: {
        rows,
        schema,
        view: view({
          kind: "timeline",
          dateField: "start",
          endDateField: null,
          titleField: "title",
        }),
        interactionEnabled: true,
        movableRecords: [{ rowKey: "row-1", expectedDigest: digest }],
      },
    });

    const records = wrapper.findAll('[data-testid="timeline-record"]');
    expect(records).toHaveLength(2);
    expect(records[0]!.attributes("draggable")).toBe("true");
    expect(records[0]!.attributes("data-date")).toBe("2026-08-12");
    expect(records[1]!.attributes("draggable")).toBe("false");
    expect(wrapper.text()).not.toContain("row-1");
    expect(wrapper.text()).not.toContain(digest);

    const setData = vi.fn();
    const dataTransfer = { setData, effectAllowed: "none", dropEffect: "none" };
    await records[0]!.trigger("dragstart", { dataTransfer });
    expect(setData).toHaveBeenCalledWith("text/plain", "row-1");

    const track = wrapper.findAll('[data-testid="timeline-track"]')[0]!;
    vi.spyOn(track.element, "getBoundingClientRect").mockReturnValue({
      left: 100,
      right: 900,
      top: 0,
      bottom: 64,
      width: 800,
      height: 64,
      x: 100,
      y: 0,
      toJSON: () => ({}),
    });
    expect(track.attributes("data-start-date")).toBe("2026-08-05");
    expect(track.attributes("data-end-date")).toBe("2026-08-27");
    await track.trigger("drop", { clientX: 500, dataTransfer });

    expect(wrapper.emitted("intent")).toEqual([[
      {
        type: "timeline.record.move",
        rowKey: "row-1",
        targetDate: "2026-08-16",
        expectedDigest: digest,
      },
    ]]);
    expect(rows[0]?.start).toBe("2026-08-12");
  });
});
