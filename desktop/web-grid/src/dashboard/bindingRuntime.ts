import type {
  ColumnDataType,
  DashboardPanelQueryPayload,
  DashboardPanelType,
  DashboardQueryLimitsPayload,
  DashboardQueryResultPayload,
  FilterCondition,
} from "@/contracts";
import type { DomainDiagnostic } from "./types";

export interface BindingFieldSchema {
  /** QueryPort reference. The editor displays label and never exposes this value as text input. */
  readonly ref: string;
  readonly fieldId: string;
  readonly label: string;
  readonly dataType: ColumnDataType;
  readonly filterOperators: readonly string[];
  readonly groupable: boolean;
  readonly summaryOperations: readonly string[];
}

export interface BindingCollectionSchema {
  readonly collectionId: string;
  readonly revision: string;
  readonly fields: readonly BindingFieldSchema[];
}

export interface SchemaCatalog {
  describe(collectionId: string, signal: AbortSignal): Promise<BindingCollectionSchema>;
}

export interface BindingQueryExecutor {
  execute(
    panelType: DashboardPanelType,
    query: DashboardPanelQueryPayload,
    signal: AbortSignal,
  ): Promise<DashboardQueryResultPayload>;
}

export interface DashboardBinding {
  readonly panelId: string;
  readonly panelType: DashboardPanelType;
  readonly query: DashboardPanelQueryPayload;
}

export interface BindingRuntimeContext {
  readonly limits: DashboardQueryLimitsPayload;
  readonly runtimeFilters: readonly FilterCondition[];
}

export type BindingResult =
  | ({ readonly state: "ready"; readonly diagnostics: readonly DomainDiagnostic[] } & DashboardQueryResultPayload)
  | { readonly state: "drift"; readonly diagnostics: readonly DomainDiagnostic[] }
  | { readonly state: "cancelled"; readonly diagnostics: readonly DomainDiagnostic[] }
  | {
      readonly state: "error";
      readonly diagnostics: readonly DomainDiagnostic[];
      readonly error: { readonly code: string; readonly message: string };
    };

/**
 * The single read-side binding seam shared by Dashboard and future interface surfaces.
 * It owns schema drift checks, query limits, cancellation, and product error mapping.
 */
export class BindingRuntime {
  constructor(
    private readonly schemas: SchemaCatalog,
    private readonly executor: BindingQueryExecutor,
  ) {}

  async evaluate(
    binding: DashboardBinding,
    context: BindingRuntimeContext,
    signal: AbortSignal,
  ): Promise<BindingResult> {
    try {
      signal.throwIfAborted();
      const schema = await this.schemas.describe(binding.query.collection, signal);
      signal.throwIfAborted();
      const query = boundedQuery({
        ...binding.query,
        filters: [...(binding.query.filters ?? []), ...context.runtimeFilters],
      } as DashboardPanelQueryPayload, binding.panelType, context.limits);
      const diagnostics = validateBinding(query, schema);
      if (diagnostics.some((item) => item.severity === "error")) {
        return { state: "drift", diagnostics };
      }
      const result = await this.executor.execute(binding.panelType, query, signal);
      signal.throwIfAborted();
      return {
        state: "ready",
        rows: result.rows,
        truncated: result.truncated,
        maxPoints: result.maxPoints,
        diagnostics,
      };
    } catch (error) {
      if (signal.aborted || isAbortError(error)) return { state: "cancelled", diagnostics: [] };
      return {
        state: "error",
        diagnostics: [],
        error: { code: errorCode(error), message: errorMessage(error) },
      };
    }
  }
}

/** Clone the closed current query AST without collapsing any repeated binding. */
export function canonicalDashboardQuery(query: DashboardPanelQueryPayload): DashboardPanelQueryPayload {
  return structuredClone(query);
}

export function validateBinding(
  query: DashboardPanelQueryPayload,
  schema: BindingCollectionSchema,
): DomainDiagnostic[] {
  const diagnostics: DomainDiagnostic[] = [];
  const fields = new Map(schema.fields.map((field) => [field.ref, field]));
  const resolve = (ref: string, path: string): BindingFieldSchema | null => {
    const field = fields.get(ref);
    if (!field) diagnostics.push(diagnostic("binding.field_missing", `Field '${ref}' no longer exists.`, path));
    return field ?? null;
  };

  if (query.kind === "records") {
    query.fields.forEach((ref, index) => resolve(ref, `query.fields.${index}`));
  } else {
    (query.dimensions ?? []).forEach((ref, index) => {
      const field = resolve(ref, `query.dimensions.${index}`);
      if (field && !field.groupable) {
        diagnostics.push(diagnostic("binding.group_unsupported", `'${field.label}' cannot be grouped.`, `query.dimensions.${index}`));
      }
    });
    query.measures.forEach((measure, index) => {
      if (measure.op === "count" && !measure.field) return;
      if (!measure.field) {
        diagnostics.push(diagnostic("binding.measure_field_required", `${measure.op} requires a field.`, `query.measures.${index}.field`));
        return;
      }
      const field = resolve(measure.field, `query.measures.${index}.field`);
      if (field && !field.summaryOperations.includes(measure.op)) {
        diagnostics.push(diagnostic("binding.summary_unsupported", `${measure.op} is unavailable for '${field.label}'.`, `query.measures.${index}.op`));
      }
    });
  }

  (query.filters ?? []).forEach((filter, index) => {
    const field = resolve(filter.field, `query.filters.${index}.field`);
    if (field && !field.filterOperators.includes(filter.operator)) {
      diagnostics.push(diagnostic("binding.operator_unsupported", `${filter.operator} is unavailable for '${field.label}'.`, `query.filters.${index}.operator`));
    }
  });
  if (query.kind === "records") {
    (query.sorts ?? []).forEach((sort, index) => resolve(sort.field, `query.sorts.${index}.field`));
  } else if (query.timeBucket) {
    const field = resolve(query.timeBucket.field, "query.timeBucket.field");
    if (field && !["date", "datetime"].includes(field.dataType)) {
      diagnostics.push(diagnostic("binding.time_unsupported", `'${field.label}' is not a date/time field.`, "query.timeBucket.field"));
    }
  }
  return diagnostics;
}

function boundedQuery(
  query: DashboardPanelQueryPayload,
  panelType: DashboardPanelType,
  limits: DashboardQueryLimitsPayload,
): DashboardPanelQueryPayload {
  if (query.kind === "records") {
    return { ...query, limit: Math.min(query.limit ?? limits.maxListRows, limits.maxListRows) };
  }
  const panelLimit = panelType === "pie" || panelType === "donut"
    ? limits.maxPieSlices
    : panelType === "time-series" || panelType === "line"
      ? limits.maxSeriesPoints
      : panelType === "bar"
        ? limits.maxCategoryPoints
        : limits.maxPanelPoints;
  const limit = Math.min(query.limit ?? panelLimit, panelLimit, 5_000);
  const topN = query.topN == null ? query.topN : Math.min(query.topN, limit);
  return { ...query, limit, topN };
}

function diagnostic(code: string, message: string, path: string): DomainDiagnostic {
  return { code, message, path, severity: "error" };
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function errorCode(error: unknown): string {
  if (typeof error === "object" && error !== null && "code" in error && typeof error.code === "string") return error.code;
  return "binding.query_failed";
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
