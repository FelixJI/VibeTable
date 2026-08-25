import type { ColumnSchema, PresetView } from "@/contracts";

const ROW_DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/u;

export interface KanbanLaneOption {
  readonly optionId: string;
  readonly label: string;
}

export interface KanbanInteractionState {
  readonly enabled: boolean;
  readonly lanes: readonly KanbanLaneOption[];
}

export type AlternativeViewInteractionIntent = {
  readonly type: "kanban.card.move";
  readonly rowKey: string | number;
  readonly targetOptionId: string;
  readonly expectedDigest: string;
};

export interface AlternativeViewInteractionController {
  kanbanState(): KanbanInteractionState;
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
    dispatch(intent) {
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
