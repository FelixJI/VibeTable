import type { JsonObject, JsonValue } from "./types.js";

export type DataOperator = "eq" | "ne" | "in" | "contains" | "starts_with" | "ends_with" | "gt" | "lt" | "gte" | "lte" | "between" | "is_null" | "is_not_null";
export interface DataFilter {
  readonly field?: string;
  readonly operator?: DataOperator;
  readonly value?: JsonValue;
  readonly logic?: "AND" | "OR";
  readonly filters?: readonly DataFilter[];
  readonly groupLogic?: "AND" | "OR";
}
export interface DataSort {
  readonly field: string;
  readonly direction?: "asc" | "desc";
  readonly nullsLast?: boolean;
}
export interface DataField {
  readonly fieldId: string;
  readonly displayName: string;
  readonly logicalType: string;
  readonly resultType: string;
  readonly resultElementType?: string;
  readonly readonly: boolean;
  readonly nullable: boolean;
  readonly display: JsonObject | null;
  readonly options: readonly JsonObject[];
  readonly constraints: JsonObject | null;
  readonly filterOperators: readonly DataOperator[];
  readonly sortable: boolean;
}
export interface DataTable {
  readonly collection: string;
  readonly displayName: string;
  readonly schemaRevision: string;
}
export interface DataCatalog {
  readonly contract: "vibetable.plugin-data.v2";
  readonly tables: readonly DataTable[];
}
export interface DataDescription extends DataTable {
  readonly contract: "vibetable.plugin-data.v2";
  readonly fields: readonly DataField[];
}
export interface DataDescribeRequest {
  readonly accepts: readonly ["vibetable.plugin-data.v2"];
  readonly collection?: string;
}
export interface DataQueryRequest {
  readonly contract: "vibetable.plugin-query.v2";
  readonly collection: string;
  /** Canonical field IDs discovered through data.describe. */
  readonly fields: readonly string[];
  readonly ids?: readonly string[];
  readonly filters?: readonly DataFilter[];
  readonly sorts?: readonly DataSort[];
  readonly pageSize?: number;
  /** Opaque, single-use continuation bound to this execution and exact request. */
  readonly cursor?: string;
}
export interface ComputedCell {
  readonly state: string;
  readonly value: JsonValue;
  readonly fresh: boolean;
}
export interface DataQueryPage {
  readonly contract: "vibetable.plugin-query-page.v2";
  readonly items: readonly JsonObject[];
  readonly nextCursor: string | null;
  readonly filteredRows: number;
  readonly totalRows: number;
  readonly schemaRevision: string;
  readonly dataRevision: number;
  /** False when a projected computed value is not fresh. */
  readonly complete: boolean;
}
