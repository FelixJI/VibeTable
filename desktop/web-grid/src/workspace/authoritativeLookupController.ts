import { onScopeDispose, watch } from "vue";

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
  /** True only for an applied result or a ready context with no Lookups to query. */
  refresh(): Promise<boolean>;
}

export interface AuthoritativeLookupDependencies {
  readonly currentTable: () => string | null;
  readonly tablePage: () => TablePage | null;
  readonly columns: () => readonly ColumnSchema[] | null;
  readonly datasetReady: () => boolean;
  readonly schemaRevision: () => string | null;
  readonly dataRevision: () => number | null;
  readonly contextGeneration: () => number;
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
  let disposed = false;
  onScopeDispose(() => {
    disposed = true;
    requestGeneration += 1;
  });

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
    { immediate: true, flush: "sync" },
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

  async function refresh(): Promise<boolean> {
    if (disposed) return false;
    const generation = ++requestGeneration;
    const contextGeneration = dependencies.contextGeneration();
    const collection = dependencies.currentTable();
    const page = dependencies.tablePage();
    const columns = dependencies.columns();
    const relationSchema = dependencies.relationSchema();
    const capabilities = dependencies.capabilities();
    const lookups = dependencies.lookups();
    const dataRevision = dependencies.dataRevision();
    if (
      !collection
      || !page
      || page.table !== collection
      || !columns
      || !dependencies.datasetReady()
      || !capabilities
      || !relationSchema
      || relationSchema.collection !== collection
      || relationSchema.schemaRevision !== dependencies.schemaRevision()
      || dataRevision === null
    ) return false;
    if (lookups.length === 0) return true;
    if (!capabilities.lookupQueryV1) return false;
    const stillCurrent = (): boolean =>
      generation === requestGeneration
      && contextGeneration === dependencies.contextGeneration()
      && collection === dependencies.currentTable();
    const fieldRefs = buildLookupProjectionFieldRefs(lookups);
    const fieldRefByName = new Map(columns.map(column => [
      column.name,
      column.fieldId ?? `${collection}.${column.name}`,
    ]));
    const source = interactiveQuery ?? page.querySnapshot?.normalizedQuery ?? {};
    try {
      const { filters, sorts, groups } = buildAuthoritativeLookupViewQuery(
        source,
        fieldRefByName,
      );
      const result = await dependencies.queryLookups({
        collection,
        fieldRefs,
        query: {
          filters,
          sorts,
          groups,
          offset: page.offset,
          limit: Math.min(page.limit, 500),
        },
      });
      if (
        !stillCurrent()
        || dependencies.dataRevision() !== dataRevision
        || result.snapshot.dataRevision !== dataRevision
      ) return false;
      return dependencies.acceptResult(result, dataRevision);
    } catch (error) {
      if (!stillCurrent()) return false;
      const content = relationLookupErrorMessage(error);
      if (content && shouldShowNotification(relationLookupNoticeKey(error))) {
        dependencies.reportError(content);
      }
      return false;
    }
  }

  function recordQuery(query: TableQuery): void {
    requestGeneration += 1;
    interactiveQuery = query;
  }

  return { recordQuery, refresh };
}
