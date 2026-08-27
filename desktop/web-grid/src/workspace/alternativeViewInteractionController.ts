import type { ColumnSchema, PresetView } from "@/contracts";
import { isCalendarDateValue, replaceCalendarDateValue } from "@/grid/calendarDateValue";

const ROW_DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/u;

export interface KanbanLaneOption {
  readonly optionId: string;
  readonly label: string;
}

export interface KanbanInteractionState {
  readonly enabled: boolean;
  readonly lanes: readonly KanbanLaneOption[];
}

export interface CalendarMovableRecord {
  readonly rowKey: string | number;
  readonly expectedDigest: string;
}

export interface CalendarInteractionState {
  readonly enabled: boolean;
  readonly movableRecords: readonly CalendarMovableRecord[];
}

export interface TimelineMovableRecord {
  readonly rowKey: string | number;
  readonly expectedDigest: string;
}

export interface TimelineInteractionState {
  readonly enabled: boolean;
  readonly movableRecords: readonly TimelineMovableRecord[];
}

export interface KanbanCardMoveIntent {
  readonly type: "kanban.card.move";
  readonly rowKey: string | number;
  readonly targetOptionId: string;
  readonly expectedDigest: string;
}

export interface CalendarRecordMoveIntent {
  readonly type: "calendar.record.move";
  readonly rowKey: string | number;
  readonly targetDate: string;
  readonly expectedDigest: string;
}

export interface TimelineRecordMoveIntent {
  readonly type: "timeline.record.move";
  readonly rowKey: string | number;
  readonly targetDate: string;
  readonly expectedDigest: string;
}

export type AlternativeViewInteractionIntent =
  | KanbanCardMoveIntent
  | CalendarRecordMoveIntent
  | TimelineRecordMoveIntent;

export interface AlternativeViewInteractionController {
  kanbanState(): KanbanInteractionState;
  calendarState(): CalendarInteractionState;
  timelineState(): TimelineInteractionState;
  dispatch(intent: AlternativeViewInteractionIntent): boolean;
}

export interface AlternativeViewInteractionDependencies {
  readonly getActiveView: () => PresetView | null;
  readonly getSchema: () => readonly ColumnSchema[];
  readonly getRows: () => readonly Readonly<Record<string, unknown>>[];
  readonly updateCell: (
    rowKey: string | number,
    column: string,
    oldValue: unknown,
    newValue: unknown,
    expectedDigest: string | null,
  ) => void;
}

interface KanbanAuthority {
  readonly field: string;
  readonly lanes: readonly KanbanLaneOption[];
}

interface CalendarAuthority {
  readonly field: string;
}

interface TimelineAuthority {
  readonly field: string;
}

function resolveKanbanAuthority(
  dependencies: AlternativeViewInteractionDependencies,
): KanbanAuthority | null {
  const view = dependencies.getActiveView();
  const field = view?.kind === "kanban" ? view.groupField : null;
  if (!field) return null;
  const column = dependencies.getSchema().find(candidate => candidate.name === field);
  if (!column?.editable || column.filterInput !== "select") return null;
  const options = column.filterOptions ?? [];
  if (options.length === 0) return null;
  const optionIds = new Set<string>();
  const lanes: KanbanLaneOption[] = [];
  for (const option of options) {
    if (!option.value || optionIds.has(option.value)) return null;
    optionIds.add(option.value);
    lanes.push({ optionId: option.value, label: option.label });
  }
  return { field, lanes };
}

function resolveCalendarAuthority(
  dependencies: AlternativeViewInteractionDependencies,
): CalendarAuthority | null {
  const view = dependencies.getActiveView();
  const field = view?.kind === "calendar" ? view.dateField : null;
  if (!field) return null;
  const column = dependencies.getSchema().find(candidate => candidate.name === field);
  if (!column?.editable || column.dataType !== "date") return null;
  return { field };
}

function resolveTimelineAuthority(
  dependencies: AlternativeViewInteractionDependencies,
): TimelineAuthority | null {
  const view = dependencies.getActiveView();
  const field = view?.kind === "timeline" && !view.endDateField ? view.dateField : null;
  if (!field) return null;
  const column = dependencies.getSchema().find(candidate => candidate.name === field);
  if (!column?.editable || column.dataType !== "date") return null;
  return { field };
}

function movableDateRecords(
  dependencies: AlternativeViewInteractionDependencies,
  field: string,
): readonly TimelineMovableRecord[] {
  return dependencies.getRows().flatMap((row): TimelineMovableRecord[] => {
    const rowKey = row.rowKey;
    const expectedDigest = row.__vibetableDigest;
    if ((typeof rowKey !== "string" && typeof rowKey !== "number")
      || typeof expectedDigest !== "string"
      || !ROW_DIGEST_PATTERN.test(expectedDigest)
      || !isCalendarDateValue(row[field], "date")) {
      return [];
    }
    return [{ rowKey, expectedDigest }];
  });
}

export function createAlternativeViewInteractionController(
  dependencies: AlternativeViewInteractionDependencies,
): AlternativeViewInteractionController {
  return {
    kanbanState() {
      const authority = resolveKanbanAuthority(dependencies);
      return authority
        ? { enabled: true, lanes: authority.lanes }
        : { enabled: false, lanes: [] };
    },
    calendarState() {
      const authority = resolveCalendarAuthority(dependencies);
      if (!authority) return { enabled: false, movableRecords: [] };
      return { enabled: true, movableRecords: movableDateRecords(dependencies, authority.field) };
    },
    timelineState() {
      const authority = resolveTimelineAuthority(dependencies);
      if (!authority) return { enabled: false, movableRecords: [] };
      return { enabled: true, movableRecords: movableDateRecords(dependencies, authority.field) };
    },
    dispatch(intent) {
      if (intent.type === "calendar.record.move") {
        const authority = resolveCalendarAuthority(dependencies);
        if (!authority || !ROW_DIGEST_PATTERN.test(intent.expectedDigest)) return false;
        const row = dependencies.getRows().find(candidate => candidate.rowKey === intent.rowKey);
        if (!row || row.__vibetableDigest !== intent.expectedDigest) return false;
        const oldValue = row[authority.field];
        const newValue = replaceCalendarDateValue(oldValue, intent.targetDate, "date");
        if (newValue === null || newValue === oldValue) return false;
        dependencies.updateCell(
          intent.rowKey,
          authority.field,
          oldValue,
          newValue,
          intent.expectedDigest,
        );
        return true;
      }
      if (intent.type === "timeline.record.move") {
        const authority = resolveTimelineAuthority(dependencies);
        if (!authority || !ROW_DIGEST_PATTERN.test(intent.expectedDigest)) return false;
        const row = dependencies.getRows().find(candidate => candidate.rowKey === intent.rowKey);
        if (!row || row.__vibetableDigest !== intent.expectedDigest) return false;
        const oldValue = row[authority.field];
        const newValue = replaceCalendarDateValue(oldValue, intent.targetDate, "date");
        if (newValue === null || newValue === oldValue) return false;
        dependencies.updateCell(
          intent.rowKey,
          authority.field,
          oldValue,
          newValue,
          intent.expectedDigest,
        );
        return true;
      }
      const authority = resolveKanbanAuthority(dependencies);
      if (!authority) return false;
      if (!authority.lanes.some(lane => lane.optionId === intent.targetOptionId)) return false;
      if (!ROW_DIGEST_PATTERN.test(intent.expectedDigest)) return false;
      const row = dependencies.getRows().find(candidate => candidate.rowKey === intent.rowKey);
      if (!row || row.__vibetableDigest !== intent.expectedDigest) return false;
      const oldValue = row[authority.field];
      if (oldValue === intent.targetOptionId) return false;
      dependencies.updateCell(
        intent.rowKey,
        authority.field,
        oldValue,
        intent.targetOptionId,
        intent.expectedDigest,
      );
      return true;
    },
  };
}
