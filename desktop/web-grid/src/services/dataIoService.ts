import { useHostBridge } from "./bridgeContext";
import type {
  ApplyImportResult,
  ColumnSchema,
  DataTaskStatus,
  ExportFormat,
  ExportResult,
  ImportPlan,
  LookupListResult,
  LookupOutputType,
  SchemaDescribeResult,
  SchemaSnapshotV2,
  SessionPathGrant,
} from "@/contracts";
import { t } from "@/i18n";
import { computed, ref } from "vue";

export interface ImportPreviewSession {
  readonly grant: SessionPathGrant;
  readonly plan: ImportPlan;
  readonly mode: "create_only";
}

/** One explicit relation mapping submitted to `data.previewImport`. */
export interface ImportColumnMappingPayload {
  readonly sourceColumn: string;
  readonly targetField: string;
  readonly relationId: string;
  readonly matchField: string;
}

/** A unique-enabled active field of a relation target table. */
export interface RelationMatchFieldOption {
  readonly fieldId: string;
  readonly displayName: string;
}

/** A configurable single-value relation of the import target collection. */
export interface RelationImportOption {
  /** Source-table physicalName; the existing DTO `targetField` semantics. */
  readonly targetField: string;
  /** Stable composite relation identity from the public catalog. */
  readonly relationId: string;
  readonly targetCollection: string;
  readonly targetDisplayName: string;
  /** Source column heading shown to the user. */
  readonly sourceDisplayName: string;
  readonly matchFields: readonly RelationMatchFieldOption[];
}

/** A valid read-only Lookup definition offered for export selection. */
export interface LookupExportOption {
  readonly lookupId: string;
  readonly displayName: string;
  readonly outputType: LookupOutputType;
}

/** Lookup catalog + the revision export must bind for Lookup columns. */
export interface ExportLookupContext {
  readonly options: readonly LookupExportOption[];
  /** Source table `schema.describe.schema.schemaRevision`. */
  readonly lookupRevision: string;
}

export interface ExportLookupSelection {
  readonly lookupIds: readonly string[];
  readonly lookupRevision: string;
}

const SCHEMA_ACCEPTS = [
  "vibetable.relation-capabilities.v1",
  "vibetable.lookup-query.v1",
] as const;

export function useDataIoService() {
  const bridge = useHostBridge();
  const activeTaskId = ref<string | null>(null);
  const busy = computed(() => activeTaskId.value !== null);
  let catalogGeneration = 0;

  async function runTask(
    kind: "data.import" | "data.export",
    params: Readonly<Record<string, unknown>>,
  ): Promise<unknown> {
    if (activeTaskId.value) {
      throw new Error("A data task is already running.");
    }
    let status = await bridge.request("task.create", { kind, params }) as DataTaskStatus;
    activeTaskId.value = status.taskId;
    try {
      while (status.state === "queued" || status.state === "running") {
        await new Promise((resolve) => window.setTimeout(resolve, 100));
        status = await bridge.request("task.status", {
          taskId: status.taskId,
        }) as DataTaskStatus;
      }
      if (status.state !== "succeeded") {
        throw new Error(status.error ?? `Data task ended as ${status.state}.`);
      }
      if (kind === "data.import"
          && status.result
          && typeof status.result === "object"
          && Array.isArray((status.result as { failedRows?: unknown }).failedRows)
          && ((status.result as { failedRows: unknown[] }).failedRows.length > 0)) {
        throw new Error(
          status.progress?.message
          ?? `Import failed for ${(status.result as { failedRows: unknown[] }).failedRows.length} row(s).`,
        );
      }
      return status.result;
    } finally {
      activeTaskId.value = null;
    }
  }

  async function cancelActive(): Promise<void> {
    const taskId = activeTaskId.value;
    if (taskId) {
      await bridge.request("task.cancel", { taskId });
    }
  }

  async function requestPreview(
    grant: SessionPathGrant,
    collection: string,
    schemaRevision: string,
    columnMapping: readonly ImportColumnMappingPayload[],
  ): Promise<ImportPlan> {
    return await bridge.request("data.previewImport", {
      grantId: grant.grantId,
      collection,
      schemaRevision,
      mode: "create_only",
      columnMapping: columnMapping.map((item) => ({
        sourceColumn: item.sourceColumn,
        targetField: item.targetField,
        relationId: item.relationId,
        matchField: item.matchField,
      })),
    }) as ImportPlan;
  }

  async function previewImport(
    collection: string,
    schemaRevision: string,
  ): Promise<ImportPreviewSession> {
    const grant = await bridge.request("data.importSourceRequested", {
      accept: [".xlsx", ".xlsm", ".csv"],
    }) as SessionPathGrant;
    const plan = await requestPreview(grant, collection, schemaRevision, []);
    return { grant, plan, mode: "create_only" };
  }

  async function previewImportWithGrant(
    grant: SessionPathGrant,
    collection: string,
    schemaRevision: string,
    columnMapping: readonly ImportColumnMappingPayload[],
  ): Promise<ImportPlan> {
    return await requestPreview(grant, collection, schemaRevision, columnMapping);
  }

  async function describeSchema(collection: string) {
    const requestGeneration = ++catalogGeneration;
    const result = await bridge.request("schema.describe", {
      collection,
      requestGeneration,
      accepts: SCHEMA_ACCEPTS,
    }) as SchemaDescribeResult;
    if (
      result.contract !== "vibetable.schema-describe.v1"
      || result.collection !== collection
      || result.requestGeneration !== requestGeneration
    ) {
      throw new Error(t("dataIo.catalog.mismatch"));
    }
    return result.schema;
  }

  async function loadRelationImportOptions(
    collection: string,
  ): Promise<readonly RelationImportOption[]> {
    const schema = await describeSchema(collection);
    const relationColumns = new Map(
      schema.columns
        .filter((column): column is ColumnSchema & { readonly relationId: string } =>
          column.kind === "relation" && column.editable && !!column.relationId)
        .map((column) => [column.relationId, column]),
    );
    const descriptors = schema.normalizedRelations.filter((relation) =>
      relation.sourceCollection === collection
      && relation.kind === "m2o"
      && relation.state === "valid"
      && !!relation.relatedCollection
      && relationColumns.has(relation.relationId));
    const options = await Promise.all(descriptors.map(async (relation) => {
      const column = relationColumns.get(relation.relationId);
      if (!column || !relation.relatedCollection) return null;
      const table = await bridge.request("schema.getTable", {
        tableId: relation.relatedCollection,
      }) as SchemaSnapshotV2;
      if (table.contract !== "vibetable.schema.v2" || table.tableId !== relation.relatedCollection) {
        throw new Error(t("dataIo.catalog.mismatch"));
      }
      const matchFields: readonly RelationMatchFieldOption[] = table.fields
        .filter((field) =>
          field.lifecycle.state === "active" && field.constraints.unique.enabled)
        .map((field) => ({
          fieldId: field.identity.fieldId,
          displayName: field.displayName,
        }));
      return {
        targetField: column.name,
        relationId: relation.relationId,
        targetCollection: relation.relatedCollection,
        targetDisplayName: table.displayName,
        sourceDisplayName: column.title,
        matchFields,
      } satisfies RelationImportOption;
    }));
    return options.filter((option): option is RelationImportOption => option !== null);
  }

  async function loadExportLookupContext(
    collection: string,
  ): Promise<ExportLookupContext> {
    const [schema, listed] = await Promise.all([
      describeSchema(collection),
      bridge.request("lookup.list", { collection }) as Promise<LookupListResult>,
    ]);
    if (listed.collection !== collection) {
      throw new Error(t("dataIo.catalog.mismatch"));
    }
    return {
      options: listed.definitions
        .filter((definition) => definition.state === "valid")
        .map((definition) => ({
          lookupId: definition.lookupId,
          displayName: definition.displayName,
          outputType: definition.outputType,
        })),
      // Data IO binds lookupRevision to the source table's schema revision;
      // lookup.list.lookupRevision is the separate computed-query revision.
      lookupRevision: schema.schemaRevision,
    };
  }

  async function applyImport(
    session: ImportPreviewSession,
  ): Promise<ApplyImportResult> {
    return await runTask("data.import", {
      grantId: session.grant.grantId,
      collection: session.plan.collection,
      token: session.plan.token.token,
      mode: session.mode,
      idempotencyPrefix: crypto.randomUUID(),
    }) as ApplyImportResult;
  }

  async function exportData(
    collection: string,
    query: Readonly<Record<string, unknown>>,
    format: ExportFormat = "csv",
    lookup?: ExportLookupSelection,
  ): Promise<ExportResult> {
    const grant = await bridge.request("data.exportTargetRequested", {
      defaultName: `${collection}-export.${format}`,
      format,
    }) as SessionPathGrant;
    const lookupIds = lookup ? [...lookup.lookupIds] : [];
    return await runTask("data.export", {
      grantId: grant.grantId,
      collection,
      query,
      format,
      includeRelations: true,
      lookupIds,
      ...(lookupIds.length > 0 ? { lookupRevision: lookup?.lookupRevision } : {}),
    }) as ExportResult;
  }

  return {
    activeTaskId,
    busy,
    previewImport,
    previewImportWithGrant,
    loadRelationImportOptions,
    loadExportLookupContext,
    applyImport,
    exportData,
    cancelActive,
  };
}
