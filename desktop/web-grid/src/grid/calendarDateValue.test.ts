import { describe, expect, it } from "vitest";

import {
  calendarDateKey,
  isCalendarDateValue,
  replaceCalendarDateValue,
} from "./calendarDateValue";

describe("calendarDateValue", () => {
  it("replaces only the date portion of supported date and datetime values", () => {
    expect(calendarDateKey("2026-07-01 00:00:00.000Z", "date")).toBe("2026-07-01");
    expect(isCalendarDateValue("2026-07-01 00:00:00.000Z", "date")).toBe(true);
    expect(isCalendarDateValue("2026-07-01 14:30:00.000Z", "date")).toBe(false);
    expect(replaceCalendarDateValue("2026-07-01", "2026-07-20", "date"))
      .toBe("2026-07-20");
    expect(replaceCalendarDateValue(
      "2026-07-01 00:00:00.000Z",
      "2026-07-20",
      "date",
    )).toBe("2026-07-20");
    expect(replaceCalendarDateValue(
      "2026-07-01 14:30:00.000Z",
      "2026-07-20",
      "date",
    )).toBeNull();
    expect(replaceCalendarDateValue(
      "2026-07-01T14:30:45+08:00",
      "2026-07-20",
      "datetime",
    )).toBe("2026-07-20T14:30:45+08:00");
    expect(replaceCalendarDateValue(
      "2026-07-01T14:30:45.123Z",
      "2026-07-20",
      "datetime",
    )).toBe("2026-07-20T14:30:45.123Z");
    expect(replaceCalendarDateValue(
      "2026-07-01 14:30:45.123Z",
      "2026-07-20",
      "datetime",
    )).toBe("2026-07-20 14:30:45.123Z");
    expect(replaceCalendarDateValue("2026-07-01T14:30bogus", "2026-07-20", "datetime"))
      .toBeNull();
    expect(replaceCalendarDateValue("2026-07-01T25:61:90+99:99", "2026-07-20", "datetime"))
      .toBeNull();
    expect(replaceCalendarDateValue("2026-07-01T14:30", "2026-02-30", "datetime"))
      .toBeNull();
  });
});
