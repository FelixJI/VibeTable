import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useWorkCalendarStore, WORK_CALENDAR_STORAGE_KEY } from "./workCalendarStore";

describe("workCalendarStore", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("persists overrides and restores the default rule", () => {
    const store = useWorkCalendarStore();
    store.setOverride("2026-07-18", "workday", "调休上班");
    expect(store.day("2026-07-18")).toMatchObject({ kind: "workday", name: "调休上班" });
    expect(localStorage.getItem(WORK_CALENDAR_STORAGE_KEY)).toContain("调休上班");
    store.clearOverride("2026-07-18");
    expect(store.day("2026-07-18").kind).toBe("weekend");
  });
});
