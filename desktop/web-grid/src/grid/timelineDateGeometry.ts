import { parseDateKey } from "@/calendar/workCalendar";

export interface TimelineDayRange {
  readonly startDate: string;
  readonly endDate: string;
  readonly dayCount: number;
}

const TIMELINE_INTERACTION_PADDING_DAYS = 7;
const MIN_CANONICAL_DATE_KEY = "0100-01-01";
const MAX_CANONICAL_DATE_KEY = "9999-12-31";

function dateOrdinal(dateKey: string): number | null {
  if (!parseDateKey(dateKey)) return null;
  const [year, month, day] = dateKey.split("-").map(Number);
  const adjustedYear = year! - (month! <= 2 ? 1 : 0);
  const era = Math.floor(adjustedYear / 400);
  const yearOfEra = adjustedYear - era * 400;
  const shiftedMonth = month! + (month! > 2 ? -3 : 9);
  const dayOfYear = Math.floor((153 * shiftedMonth + 2) / 5) + day! - 1;
  return era * 146_097
    + yearOfEra * 365
    + Math.floor(yearOfEra / 4)
    - Math.floor(yearOfEra / 100)
    + dayOfYear;
}

function dateKeyFromOrdinal(ordinal: number): string {
  const era = Math.floor(ordinal / 146_097);
  const dayOfEra = ordinal - era * 146_097;
  const yearOfEra = Math.floor(
    (dayOfEra
      - Math.floor(dayOfEra / 1_460)
      + Math.floor(dayOfEra / 36_524)
      - Math.floor(dayOfEra / 146_096)) / 365,
  );
  let year = yearOfEra + era * 400;
  const dayOfYear = dayOfEra
    - (365 * yearOfEra + Math.floor(yearOfEra / 4) - Math.floor(yearOfEra / 100));
  const shiftedMonth = Math.floor((5 * dayOfYear + 2) / 153);
  const day = dayOfYear - Math.floor((153 * shiftedMonth + 2) / 5) + 1;
  const month = shiftedMonth + (shiftedMonth < 10 ? 3 : -9);
  year += month <= 2 ? 1 : 0;
  return `${String(year).padStart(4, "0")}-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}

export function createTimelineDayRange(
  dateKeys: readonly string[],
): TimelineDayRange | null {
  const ordinals = dateKeys.flatMap((dateKey): number[] => {
    const ordinal = dateOrdinal(dateKey);
    return ordinal === null ? [] : [ordinal];
  });
  if (ordinals.length === 0) return null;
  const startOrdinal = Math.min(...ordinals);
  const endOrdinal = Math.max(...ordinals);
  return {
    startDate: dateKeyFromOrdinal(startOrdinal),
    endDate: dateKeyFromOrdinal(endOrdinal),
    dayCount: endOrdinal - startOrdinal + 1,
  };
}

export function createTimelineInteractionRange(
  dateKeys: readonly string[],
): TimelineDayRange | null {
  const contentRange = createTimelineDayRange(dateKeys);
  if (!contentRange) return null;
  const startOrdinal = dateOrdinal(contentRange.startDate);
  const endOrdinal = dateOrdinal(contentRange.endDate);
  if (startOrdinal === null || endOrdinal === null) return null;
  const minimumOrdinal = dateOrdinal(MIN_CANONICAL_DATE_KEY);
  const maximumOrdinal = dateOrdinal(MAX_CANONICAL_DATE_KEY);
  if (minimumOrdinal === null || maximumOrdinal === null) return null;
  const paddedStart = Math.max(
    minimumOrdinal,
    startOrdinal - TIMELINE_INTERACTION_PADDING_DAYS,
  );
  const paddedEnd = Math.min(
    maximumOrdinal,
    endOrdinal + TIMELINE_INTERACTION_PADDING_DAYS,
  );
  return {
    startDate: dateKeyFromOrdinal(paddedStart),
    endDate: dateKeyFromOrdinal(paddedEnd),
    dayCount: paddedEnd - paddedStart + 1,
  };
}

export function timelineDatePosition(
  dateKey: string,
  range: TimelineDayRange,
): number | null {
  const ordinal = dateOrdinal(dateKey);
  const startOrdinal = dateOrdinal(range.startDate);
  if (ordinal === null || startOrdinal === null || range.dayCount < 1) return null;
  return Math.min(
    (range.dayCount - 1) / range.dayCount,
    Math.max(0, (ordinal - startOrdinal) / range.dayCount),
  );
}

export function timelineDateAtTrackOffset(
  range: TimelineDayRange,
  offset: number,
  width: number,
): string | null {
  const startOrdinal = dateOrdinal(range.startDate);
  if (startOrdinal === null
    || range.dayCount < 1
    || !Number.isFinite(offset)
    || !Number.isFinite(width)
    || width <= 0) {
    return null;
  }
  const ratio = Math.min(1, Math.max(0, offset / width));
  const dayOffset = Math.min(range.dayCount - 1, Math.floor(ratio * range.dayCount));
  return dateKeyFromOrdinal(startOrdinal + dayOffset);
}
