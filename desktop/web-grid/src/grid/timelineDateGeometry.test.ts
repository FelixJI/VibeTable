import { describe, expect, it } from "vitest";

import {
  createTimelineDayRange,
  createTimelineInteractionRange,
  timelineDateAtTrackOffset,
  timelineDatePosition,
} from "./timelineDateGeometry";

describe("timelineDateGeometry", () => {
  it("treats dates across a daylight-saving transition as consecutive logical days", () => {
    const range = createTimelineDayRange([
      "2026-03-10",
      "2026-03-07",
      "invalid",
    ]);

    expect(range).toEqual({
      startDate: "2026-03-07",
      endDate: "2026-03-10",
      dayCount: 4,
    });
    expect(timelineDatePosition("2026-03-09", range!)).toBe(0.5);
  });

  it("maps and clamps track offsets to inclusive date boundaries", () => {
    const range = createTimelineDayRange(["2026-08-12", "2026-08-20"]);

    expect(timelineDateAtTrackOffset(range!, -20, 800)).toBe("2026-08-12");
    expect(timelineDateAtTrackOffset(range!, 400, 800)).toBe("2026-08-16");
    expect(timelineDateAtTrackOffset(range!, 900, 800)).toBe("2026-08-20");
    expect(timelineDateAtTrackOffset(range!, 100, 0)).toBeNull();
  });

  it("keeps a one-day range stable and rejects invalid inputs", () => {
    const range = createTimelineDayRange(["2026-08-12"]);

    expect(timelineDatePosition("2026-08-12", range!)).toBe(0);
    expect(timelineDateAtTrackOffset(range!, 50, 100)).toBe("2026-08-12");
    expect(createTimelineDayRange(["invalid"])).toBeNull();
    expect(timelineDatePosition("invalid", range!)).toBeNull();
    expect(timelineDateAtTrackOffset(range!, Number.NaN, 100)).toBeNull();
  });

  it("pads a single point so adjacent logical dates remain reachable", () => {
    const range = createTimelineInteractionRange(["2026-08-12"]);

    expect(range).toEqual({
      startDate: "2026-08-05",
      endDate: "2026-08-19",
      dayCount: 15,
    });
    expect(timelineDateAtTrackOffset(range!, 650, 1_500)).toBe("2026-08-11");
    expect(timelineDateAtTrackOffset(range!, 850, 1_500)).toBe("2026-08-13");
  });

  it("extends the interaction viewport beyond existing minimum and maximum dates", () => {
    const range = createTimelineInteractionRange(["2026-08-12", "2026-08-20"]);

    expect(range).toEqual({
      startDate: "2026-08-05",
      endDate: "2026-08-27",
      dayCount: 23,
    });
    expect(timelineDateAtTrackOffset(range!, 0, 2_300)).toBe("2026-08-05");
    expect(timelineDateAtTrackOffset(range!, 2_300, 2_300)).toBe("2026-08-27");
  });

  it("clamps interaction padding to the canonical four-digit date domain", () => {
    expect(createTimelineInteractionRange(["0100-01-01"])).toEqual({
      startDate: "0100-01-01",
      endDate: "0100-01-08",
      dayCount: 8,
    });
    expect(createTimelineInteractionRange(["9999-12-31"])).toEqual({
      startDate: "9999-12-24",
      endDate: "9999-12-31",
      dayCount: 8,
    });
  });
});
