import { nextTick, reactive, readonly, watch } from "vue";

import type {
  ColumnSchema,
  LookupSourcePageIntent,
  LookupValueProvenance,
  RelationTargetRef,
} from "@/contracts";

interface NavigationGridRow {
  getIndex(): string | number;
  scrollTo(position: "center", ifVisible: boolean): Promise<void>;
  select(): void;
  getElement(): HTMLElement;
}

interface NavigationGrid {
  getRows(): NavigationGridRow[];
}

export interface LookupProvenanceState {
  readonly show: boolean;
  readonly loading: boolean;
  readonly error: string | null;
  readonly items: readonly LookupValueProvenance[];
  readonly total: number;
  readonly totalKnown: boolean;
  readonly hasMore: boolean;
}

interface LookupNavigationTarget {
  readonly source: LookupValueProvenance;
  readonly open: "locate" | "content" | "attachment";
  readonly fieldId: string | null;
}

export type LookupProvenanceIntent =
  | { readonly type: "scope.retire" }
  | { readonly type: "sources.open"; readonly page: LookupSourcePageIntent }
  | { readonly type: "sources.close" }
  | { readonly type: "sources.loadMore" }
  | { readonly type: "source.navigate"; readonly source: LookupValueProvenance }
  | { readonly type: "source.openTarget"; readonly target: RelationTargetRef }
  | { readonly type: "source.locate"; readonly target: LookupNavigationTarget };

export interface LookupProvenanceController {
  readonly state: LookupProvenanceState;
  dispatch(intent: LookupProvenanceIntent): Promise<void>;
}

export interface LookupProvenanceDependencies {
  readonly readPage: (request: {
    readonly fieldRef: string;
    readonly sourceRecordId: string;
    readonly offset: number;
    readonly limit: number;
  }) => Promise<{
    readonly provenance: readonly LookupValueProvenance[];
    readonly provenanceTotal: number;
    readonly provenanceTotalKnown: boolean;
    readonly provenanceHasMore: boolean;
  }>;
  readonly selectTable: (collection: string) => void;
  readonly navigateTables: () => void;
  readonly queryRecord: (collection: string, primaryKey: string, itemId: string) => void;
  readonly getCurrentTable: () => string | null;
  readonly getSchemaContext: () => {
    readonly collection: string | null;
    readonly primaryKey: string | null;
  };
  readonly getRows: () => readonly Readonly<Record<string, unknown>>[];
  readonly getGrid: () => unknown;
  readonly getColumns: () => readonly ColumnSchema[];
  readonly openContent: (rowKey: string | number) => void;
  readonly openAttachment: (rowKey: string | number, column: ColumnSchema) => void;
  readonly reportLocated: (source: LookupValueProvenance) => void;
  readonly reportFiltered: (source: LookupValueProvenance) => void;
  readonly errorMessage: (error: unknown) => string | null;
}

export function createLookupProvenanceController(
  dependencies: LookupProvenanceDependencies,
): LookupProvenanceController {
  const state = reactive({
    show: false,
    loading: false,
    error: null as string | null,
    fieldRef: "",
    sourceRecordId: "",
    items: [] as LookupValueProvenance[],
    total: 0,
    totalKnown: true,
    hasMore: false,
  });
  let sourceEpoch = 0;
  let navigation: (LookupNavigationTarget & { queryRequested: boolean }) | null = null;

  function closeSources(): void {
    sourceEpoch += 1;
    state.show = false;
    state.loading = false;
  }

  function retireScope(): void {
    sourceEpoch += 1;
    navigation = null;
    Object.assign(state, {
      show: false,
      loading: false,
      error: null,
      fieldRef: "",
      sourceRecordId: "",
      items: [],
      total: 0,
      totalKnown: true,
      hasMore: false,
    });
  }

  async function loadMoreSources(): Promise<void> {
    if (state.loading || !state.hasMore) return;
    const epoch = sourceEpoch;
    state.loading = true;
    state.error = null;
    try {
      const page = await dependencies.readPage({
        fieldRef: state.fieldRef,
        sourceRecordId: state.sourceRecordId,
        offset: state.items.length,
        limit: 100,
      });
      if (epoch !== sourceEpoch || !state.show) return;
      state.items.push(...page.provenance);
      state.total = page.provenanceTotal;
      state.totalKnown = page.provenanceTotalKnown;
      state.hasMore = page.provenanceHasMore;
    } catch (error) {
      if (epoch === sourceEpoch && state.show) state.error = dependencies.errorMessage(error);
    } finally {
      if (epoch === sourceEpoch) state.loading = false;
    }
  }

  function locate(target: LookupNavigationTarget): void {
    navigation = { ...target, queryRequested: false };
    dependencies.selectTable(target.source.collection);
    dependencies.navigateTables();
  }

  watch(
    [dependencies.getCurrentTable, dependencies.getSchemaContext],
    ([collection, schema]) => {
      if (
        !navigation
        || navigation.queryRequested
        || collection !== navigation.source.collection
        || schema.collection !== navigation.source.collection
        || !schema.primaryKey
      ) return;
      navigation.queryRequested = true;
      dependencies.queryRecord(
        navigation.source.collection,
        schema.primaryKey,
        navigation.source.itemId,
      );
    },
  );

  watch(
    [dependencies.getRows, dependencies.getGrid],
    async ([rows, rawGrid]) => {
      const target = navigation;
      if (!target?.queryRequested || !rawGrid) return;
      const matchingRow = rows.find(row => String(row.rowKey) === target.source.itemId);
      if (!matchingRow) return;
      const rowKey = matchingRow.rowKey;
      if (typeof rowKey !== "string" && typeof rowKey !== "number") return;
      await nextTick();
      if (navigation !== target) return;
      try {
        const row = (rawGrid as NavigationGrid)
          .getRows()
          .find(candidate => String(candidate.getIndex()) === String(rowKey));
        if (!row) throw new Error("lookup source row is no longer rendered");
        await row.scrollTo("center", true);
        if (navigation !== target) return;
        row.select();
        row.getElement().classList.add("vt-row-selected");
        if (target.open === "content") {
          dependencies.openContent(rowKey);
        } else if (target.open === "attachment") {
          const column = dependencies.getColumns().find(item => item.fieldId === target.fieldId);
          if (column) dependencies.openAttachment(rowKey, column);
        }
        dependencies.reportLocated(target.source);
      } catch {
        if (navigation === target) dependencies.reportFiltered(target.source);
      } finally {
        if (navigation === target) navigation = null;
      }
    },
  );

  async function dispatch(intent: LookupProvenanceIntent): Promise<void> {
    switch (intent.type) {
      case "scope.retire":
        retireScope();
        return;
      case "sources.open":
        sourceEpoch += 1;
        Object.assign(state, {
          show: true,
          loading: false,
          error: null,
          fieldRef: intent.page.fieldRef,
          sourceRecordId: intent.page.sourceRecordId,
          items: [...intent.page.cell.provenance],
          total: intent.page.cell.provenanceTotal,
          totalKnown: intent.page.cell.provenanceTotalKnown,
          hasMore: intent.page.cell.provenanceHasMore,
        });
        return;
      case "sources.close":
        closeSources();
        return;
      case "sources.loadMore":
        await loadMoreSources();
        return;
      case "source.navigate":
        locate({ source: intent.source, open: "locate", fieldId: null });
        return;
      case "source.openTarget":
        locate({
          source: {
            collection: intent.target.collection,
            collectionLabel: intent.target.collection,
            itemId: intent.target.itemId,
            recordLabel: intent.target.label,
            fieldId: "",
            fieldLabel: "",
            value: null,
          },
          open: "locate",
          fieldId: null,
        });
        return;
      case "source.locate":
        locate(intent.target);
    }
  }

  return {
    state: readonly(state),
    dispatch,
  };
}
