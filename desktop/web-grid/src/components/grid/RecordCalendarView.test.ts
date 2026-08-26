import { mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { describe, expect, it, vi } from "vitest";

import RecordCalendarView from "./RecordCalendarView.vue";
import type { PresetView } from "@/contracts";

function view(overrides: Partial<PresetView> = {}): PresetView {
  return {
    id: "calendar",
    name: "Calendar",
    kind: "calendar",
    filters: [],
    sorts: [],
    visibleFields: [],
    layout: "calendar",
    dateField: "due",
    titleField: "title",
    ...overrides,
  } as PresetView;
}

describe("RecordCalendarView", () => {
  it("emits an opaque move intent only for a controller-authorized record", async () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const wrapper = mount(RecordCalendarView, {
      props: {
        rows: [
          { rowKey: "row-1", due: "2026-08-13", title: "可移动", __vibetableDigest: digest },
          { rowKey: "row-2", due: "2026-08-14", title: "不可移动", __vibetableDigest: digest },
        ],
        schema: [],
        view: view(),
        interactionEnabled: true,
        movableRecords: [{ rowKey: "row-1", expectedDigest: digest }],
      },
      global: { plugins: [createPinia()] },
    });

    const records = wrapper.findAll('[data-testid="calendar-record"]');
    expect(records).toHaveLength(2);
    expect(records[0]!.attributes("draggable")).toBe("true");
    expect(records[1]!.attributes("draggable")).toBe("false");
    expect(wrapper.text()).not.toContain("row-1");
    expect(wrapper.text()).not.toContain(digest);

    const setData = vi.fn();
    const dataTransfer = { setData, effectAllowed: "none" };
    await records[0]!.trigger("dragstart", { dataTransfer });
    expect(setData).toHaveBeenCalledWith("text/plain", "row-1");
    expect(dataTransfer.effectAllowed).toBe("move");
    const target = wrapper.find('[data-testid="calendar-day"][data-date="2026-08-20"]');
    expect(target.exists()).toBe(true);
    await target.trigger("drop");

    expect(wrapper.emitted("intent")).toEqual([[
      {
        type: "calendar.record.move",
        rowKey: "row-1",
        targetDate: "2026-08-20",
        expectedDigest: digest,
      },
    ]]);
  });

  it("groups valid dates, limits visible items and navigates months", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-13T12:00:00"));
    const wrapper = mount(RecordCalendarView, {
      props: {
        rows: [
          { rowKey: 1, due: "2026-08-13", title: "One" },
          { rowKey: 2, due: "2026-08-13", title: "Two" },
          { rowKey: 3, due: "2026-08-13", title: "Three" },
          { rowKey: 4, due: "2026-08-13", title: "Four" },
          { rowKey: 5, due: "invalid", title: "Ignored" },
        ],
        schema: [],
        view: view(),
      },
      global: { plugins: [createPinia()] },
    });

    expect(wrapper.text()).toContain("One");
    expect(wrapper.text()).toContain("另有 1 条");
    expect(wrapper.text()).not.toContain("Four");
    expect(wrapper.text()).not.toContain("Ignored");
    expect(wrapper.find("article.today").exists()).toBe(true);
    expect(wrapper.findAll(".calendar-actions button")).toHaveLength(3);

    const initial = wrapper.find("header strong").text();
    await wrapper.findAll(".calendar-actions button")[2]!.trigger("click");
    expect(wrapper.find("header strong").text()).not.toBe(initial);
    await wrapper.findAll(".calendar-actions button")[1]!.trigger("click");
    expect(wrapper.find("header strong").text()).toBe(initial);
    vi.useRealTimers();
  });

  it("renders fallback titles and an empty calendar without a date field", async () => {
    const wrapper = mount(RecordCalendarView, {
      props: {
        rows: [{ rowKey: "row-7", due: new Date("2026-09-02") }],
        schema: [],
        view: view({ titleField: undefined }),
      },
      global: { plugins: [createPinia()] },
    });
    expect(wrapper.text()).toContain("row-7");

    await wrapper.setProps({ view: view({ dateField: undefined }) });
    expect(wrapper.findAll(".calendar-days article span")).toHaveLength(0);
  });
});
