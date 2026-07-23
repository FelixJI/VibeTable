import type { DashboardQueryAst, FieldType, QueryFieldSchema } from "./query";
import type { DomainDiagnostic } from "./types";
import type { FilterExpression } from "./filters";

export interface CollectionSchemaSnapshot {
  readonly collection: string;
  readonly fields: readonly QueryFieldSchema[];
}

export interface FieldReference {
  readonly panelId: string;
  readonly collection: string;
  readonly field: string;
  readonly role: "dimension" | "metric" | "filter" | "group" | "sort" | "time" | "option";
  readonly expectedType?: FieldType;
}

export interface FieldDriftDiagnostic extends DomainDiagnostic {
  readonly panelId: string;
  readonly collection: string;
  readonly field?: string;
  readonly compatibleFields?: readonly string[];
}

export function referencesForQuery(panelId: string, query: DashboardQueryAst): FieldReference[] {
  const references: FieldReference[] = [];
  const add = (field: string, role: FieldReference["role"]): void => {
    if (field !== "*") references.push({ panelId, collection: query.collection, field, role });
  };
  query.dimensions?.forEach((field) => add(field, "dimension"));
  query.metrics?.forEach((metric) => add(metric.field, "metric"));
  query.groupBy?.forEach((field) => add(field, "group"));
  query.sorts?.forEach((sort) => add(sort.field, "sort"));
  if (query.timeField) add(query.timeField, "time");
  if (query.filter) collectFilterReferences(query.filter, (field) => add(field, "filter"));
  const seen = new Set<string>();
  return references.filter((reference) => {
    const key = `${reference.field}:${reference.role}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function diagnoseFieldDrift(
  references: readonly FieldReference[],
  collections: readonly CollectionSchemaSnapshot[],
): FieldDriftDiagnostic[] {
  const schemas = new Map(collections.map((collection) => [collection.collection, collection]));
  const diagnostics: FieldDriftDiagnostic[] = [];
  for (const reference of references) {
    const collection = schemas.get(reference.collection);
    if (!collection) {
      diagnostics.push({
        code: "dashboard_collection_missing",
        message: `Collection ${reference.collection} no longer exists or is not readable.`,
        severity: "error",
        panelId: reference.panelId,
        collection: reference.collection,
      });
      continue;
    }
    const field = collection.fields.find((candidate) => candidate.name === reference.field);
    if (!field) {
      diagnostics.push({
        code: "dashboard_field_missing",
        message: `Field ${reference.collection}.${reference.field} no longer exists or is not readable.`,
        path: reference.role,
        severity: "error",
        panelId: reference.panelId,
        collection: reference.collection,
        field: reference.field,
        compatibleFields: compatibleReplacementFields(reference, collection.fields),
      });
      continue;
    }
    if (field.readable === false) {
      diagnostics.push({
        code: "dashboard_field_unreadable",
        message: `Field ${reference.collection}.${reference.field} is no longer readable.`,
        path: reference.role,
        severity: "error",
        panelId: reference.panelId,
        collection: reference.collection,
        field: reference.field,
      });
      continue;
    }
    if (reference.expectedType && !fieldTypesCompatible(reference.expectedType, field.type)) {
      diagnostics.push({
        code: "dashboard_field_type_changed",
        message: `Field ${reference.collection}.${reference.field} changed from ${reference.expectedType} to ${field.type}.`,
        path: reference.role,
        severity: "error",
        panelId: reference.panelId,
        collection: reference.collection,
        field: reference.field,
        compatibleFields: compatibleReplacementFields(reference, collection.fields),
      });
    }
  }
  return dedupe(diagnostics);
}

export function compatibleReplacementFields(
  reference: FieldReference,
  fields: readonly QueryFieldSchema[],
): string[] {
  return fields
    .filter((field) => field.readable !== false)
    .filter((field) => !reference.expectedType || fieldTypesCompatible(reference.expectedType, field.type))
    .map((field) => field.name)
    .filter((name) => name !== reference.field)
    .sort((a, b) => a.localeCompare(b));
}

export function fieldTypesCompatible(expected: FieldType, actual: FieldType): boolean {
  if (expected === actual) return true;
  if ((expected === "integer" || expected === "decimal") &&
      (actual === "integer" || actual === "decimal")) return true;
  if ((expected === "date" || expected === "date-time") &&
      (actual === "date" || actual === "date-time")) return true;
  if ((expected === "uuid" || expected === "user" || expected === "relation") &&
      (actual === "uuid" || actual === "user" || actual === "relation")) return true;
  return false;
}

function collectFilterReferences(filter: FilterExpression, visit: (field: string) => void): void {
  if ("and" in filter) {
    filter.and.forEach((item) => collectFilterReferences(item, visit));
  } else {
    visit(filter.field);
  }
}

function dedupe(values: readonly FieldDriftDiagnostic[]): FieldDriftDiagnostic[] {
  const seen = new Set<string>();
  return values.filter((item) => {
    const key = `${item.code}:${item.panelId}:${item.collection}:${item.field ?? ""}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}
