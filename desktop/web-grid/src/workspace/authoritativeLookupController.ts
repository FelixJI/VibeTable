import { watch } from "vue";

import type {
  ColumnSchema,
  LookupDefinition,
  LookupQueryResult,
  RelationLookupCapabilities,
  SchemaSnapshot,
  TablePage,
  TableQuery,
} from "@/contracts";
import {
  buildAuthoritativeLookupViewQuery,
  buildLookupProjectionFieldRefs,
} from "@/services/relationLookupQuery";
import type { useRelationLookupService } from "@/services/relationLookupService";
import {
  createNotificationDeduper,
  relationLookupErrorMessage,
  relationLookupNoticeKey,
} from "@/services/notificationPolicy";

type QueryLookups = ReturnType<typeof useRelationLookupService>["queryLookups"];

export interface AuthoritativeLookupController {
  recordQuery(query: TableQuery): void;
  refresh(): Promise<void>;
}

export interface AuthoritativeLookupDependencies {
  readonly currentTable: () => string | null;
  readonly tablePage: () => TablePage | null;
  readonly loadedRows: () => readonly Record<string, unknown>[];
  readonly columns: () => readonly ColumnSchema[] | null;
  readonly datasetReady: () => boolean;
  readonly schemaRevision: () => string | null;
  readonly dataRevision: () => number | null;
  readonly relationSchema: () => SchemaSnapshot | null;
  readonly capabilities: () => RelationLookupCapabilities | null;
  readonly lookups: () => readonly LookupDefinition[];
  readonly resetContext: () => void;
  readonly loadContext: (collection: string) => Promise<unknown>;
  readonly queryLookups: QueryLookups;
  readonly acceptResult: (result: LookupQueryResult, currentDataRevision: number) => boolean;
  readonly clearEditRejection: () => void;
  readonly reportError: (content: string) => void;
}

export function createAuthoritativeLookupController(
  dependencies: AuthoritativeLookupDependencies,
): AuthoritativeLookupController {
  const shouldShowNotification = createNotificationDeduper();
  let requestGeneration = 0;
  let interactiveQuery: TableQuery | null = null;

  watch(
    dependencies.currentTable,
    (collection) => {
      dependencies.clearEditRejection();
      interactiveQuery = null;
      requestGeneration += 1;
      if (!collection) {
        dependencies.resetContext();
        return;
      }
      void dependencies.loadContext(collection);
    },
    { immediate: true },
  );

  watch(
    [dependencies.currentTable, dependencies.schemaRevision],
    ([collection, schemaRevision]) => {
      if (
        collection
        && schemaRevision
        && dependencies.relationSchema()?.schemaRevision !== schemaRevision
      ) void dependencies.loadContext(collection);
    },
  );

  watch(
    [
      () => dependencies.relationSchema()?.lookupRevision,
      () => dependencies.capabilities()?.lookupQueryV1,
      dependencies.datasetReady,
      dependencies.dataRevision,
    ],
    () => { void refresh(); },
  );

  async function refresh(): Promise<void> {
    const generation = ++requestGeneration;
    const collection = dependencies.currentTable();
    const page = dependencies.tablePage();
    const columns = dependencies.columns();
    const capabilities = dependencies.capabilities();
    const lookups = dependencies.lookups();
    const dataRevision = dependencies.dataRevision();
    if (
      !collection
      || !page
      || !columns
      || !dependencies.datasetReady()
      || !capabilities?.lookupQueryV1
      || (lookups.length === 0 && !columns.some(column => column.kind === "relation"))
      || dataRevision === null
    ) return;
    const fieldRefs = buildLookupProjectionFieldRefs(lookups);
    const fieldRefByName = new Map(columns.map(column => [
      column.name,
      column.fieldId ?? `${collection}.${column.name}`,
    ]));
    const source = interactiveQuery ?? page.querySnapshot?.normalizedQuery ?? {};
    try {
      const queries: Array<Parameters<QueryLookups>[0]["query"]> = [];
      if (lookups.length === 0) {
        // Labels decorate the rows already loaded across all cursor windows.
        // Reapplying the first page's filters/offset would miss later windows
        // or rows whose related display value just stopped matching a filter.
        const ids = [...new Set(dependencies.loadedRows().flatMap((row) => {
          const id = row.rowKey ?? row.id;
          return typeof id === "string" || typeof id === "number" ? [String(id)] : [];
        }))];
        for (let offset = 0; offset < ids.length; offset += 200) {
          const batch = ids.slice(offset, offset + 200);
          queries.push({
            filters: [{ field: dependencies.relationSchema()?.primaryKey ?? "id", operator: "in", value: batch }],
            sorts: [], groups: [], offset: 0, limit: batch.length,
          });
        }
      } else {
        const { filters, sorts, groups } = buildAuthoritativeLookupViewQuery(source, fieldRefByName);
        queries.push({ filters, sorts, groups, offset: page.offset, limit: Math.min(page.limit, 500) });
      }
      for (const query of queries) {
        if (generation !== requestGeneration || dependencies.dataRevision() !== dataRevision) return;
        const result = await dependencies.queryLookups({ collection, fieldRefs, query });
        if (
          generation !== requestGeneration
          || dependencies.dataRevision() !== dataRevision
          || result.snapshot.dataRevision !== dataRevision
        ) return;
        dependencies.acceptResult(result, dataRevision);
      }
    } catch (error) {
      if (generation !== requestGeneration) return;
      const content = relationLookupErrorMessage(error);
      if (content && shouldShowNotification(relationLookupNoticeKey(error))) {
        dependencies.reportError(content);
      }
    }
  }

  function recordQuery(query: TableQuery): void {
    requestGeneration += 1;
    interactiveQuery = query;
  }

  return { recordQuery, refresh };
}
