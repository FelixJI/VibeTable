import { computed, ref } from "vue";
import { defineStore } from "pinia";
import {
  resolveWorkCalendarDay,
  sanitizeOverrides,
  type WorkCalendarOverride,
  type WorkCalendarOverrideKind,
} from "@/calendar/workCalendar";

export const WORK_CALENDAR_STORAGE_KEY = "vt:work-calendar:v1";

export function readStoredWorkCalendar(storage: Storage = localStorage): WorkCalendarOverride[] {
  try {
    const raw = storage.getItem(WORK_CALENDAR_STORAGE_KEY);
    return raw ? sanitizeOverrides(JSON.parse(raw)) : [];
  } catch {
    return [];
  }
}

export const useWorkCalendarStore = defineStore("work-calendar", () => {
  const overrides = ref<WorkCalendarOverride[]>(readStoredWorkCalendar());
  const overrideCount = computed(() => overrides.value.length);

  function persist(): void {
    try {
      localStorage.setItem(WORK_CALENDAR_STORAGE_KEY, JSON.stringify(overrides.value));
    } catch {
      // Storage can be unavailable in hardened WebViews. The in-memory rules
      // remain usable for the current session.
    }
  }

  function setOverride(date: string, kind: WorkCalendarOverrideKind, name = ""): void {
    overrides.value = sanitizeOverrides([
      ...overrides.value.filter((item) => item.date !== date),
      { date, kind, name },
    ]);
    persist();
  }

  function clearOverride(date: string): void {
    overrides.value = overrides.value.filter((item) => item.date !== date);
    persist();
  }

  function getOverride(date: string): WorkCalendarOverride | undefined {
    return overrides.value.find((item) => item.date === date);
  }

  function day(date: string) {
    return resolveWorkCalendarDay(date, overrides.value);
  }

  return { overrides, overrideCount, setOverride, clearOverride, getOverride, day };
});
