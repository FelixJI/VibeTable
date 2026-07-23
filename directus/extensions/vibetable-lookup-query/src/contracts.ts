export const CONTRACT = "vibetable-lookup-query.v1" as const;

export type ScalarKind =
  | "string"
  | "integer"
  | "decimal"
  | "boolean"
  | "date"
  | "time"
  | "datetime"
  | "uuid"
  | "json";

export interface OutputType {
  kind: ScalarKind;
  /** Required for decimal averages and used to normalize all decimal output. */
  scale?: number;
}

export interface BaseField {
  ref: string;
  field: string;
  outputType: OutputType;
}

export interface JunctionDescriptor {
  collection: string;
  sourceField: string;
  targetField: string;
  /** Required for M2A and contains a Directus collection name. */
  collectionField?: string;
}

export interface RelationStep {
  relationId: string;
  kind: "m2o" | "o2m" | "m2m" | "m2a";
  fromCollection: string;
  /**
   * Required for direct relations. For M2A it selects one declared branch;
   * omit it only when M2A is terminal and the source has per-collection fields.
   */
  toCollection?: string;
  /** Key/FK on the current collection. */
  sourceField: string;
  /** Key/FK on the destination collection; required except for M2A. */
  targetField?: string;
  /** Destination collection PK; required for O2M provenance. */
  destinationPrimaryKey?: string;
  junction?: JunctionDescriptor;
  /** Explicit allow-list for an M2A frontier. */
  targetCollections?: readonly string[];
  /** Explicit destination PK for every allowed M2A collection. */
  targetPrimaryKeys?: Readonly<Record<string, string>>;
}

export type LookupSource =
  | { kind: "field"; field: string }
  | { kind: "junction"; step: number; field: string }
  | {
      kind: "m2a";
      fields: Readonly<Record<string, string>>;
    }
  | { kind: "lookup"; lookupId: string };

export type LookupAggregate =
  | "scalar"
  | "list"
  | "distinct"
  | "count"
  | "count_non_null"
  | "sum"
  | "avg"
  | "min"
  | "max";

export interface LookupDefinition {
  lookupId: string;
  ref: string;
  /** Defaults to the plan root only for exposed root definitions. */
  collection?: string;
  /** Defaults to the plan root primary key only for exposed root definitions. */
  primaryKey?: string;
  /** Referenced target-collection definitions set this false. */
  expose?: boolean;
  path: readonly RelationStep[];
  source: LookupSource;
  aggregate: LookupAggregate;
  outputType: OutputType;
}

export type FilterOperator =
  | "eq"
  | "neq"
  | "lt"
  | "lte"
  | "gt"
  | "gte"
  | "in"
  | "not_in"
  | "contains"
  | "is_null"
  | "not_null";

export type FilterNode =
  | { op: "and" | "or"; children: readonly FilterNode[] }
  | { fieldRef: string; operator: FilterOperator; value?: unknown };

export interface SortSpec {
  fieldRef: string;
  direction: "asc" | "desc";
}

export interface GroupAggregate {
  ref: string;
  fieldRef: string;
  aggregate: "count" | "count_non_null" | "sum" | "avg" | "min" | "max";
  outputType: OutputType;
}

export interface QueryBudget {
  maxRootItems: number;
  maxIntermediateItems: number;
  maxServiceCalls: number;
  maxMilliseconds: number;
  maxResponseBytes: number;
}

export interface LookupQueryPlan {
  contract: typeof CONTRACT;
  generation: string | number;
  collection: string;
  primaryKey: string;
  revisions: {
    schema: string;
    permission: string;
    lookup: string;
  };
  baseFields: readonly BaseField[];
  lookups: readonly LookupDefinition[];
  filter?: FilterNode;
  sort?: readonly SortSpec[];
  groupBy?: readonly string[];
  groupAggregates?: readonly GroupAggregate[];
  page?: { offset: number; limit: number };
  /** Hints only lower server limits; they can never raise them. */
  budgetHint?: Partial<QueryBudget>;
}

export interface MaterializedRow {
  primaryKey: unknown;
  cells: Record<string, unknown>;
}

export interface GroupPathPart {
  fieldRef: string;
  key: unknown;
}

export interface GroupNode {
  path: readonly GroupPathPart[];
  key: unknown;
  count: number;
  aggregateCells: Record<string, unknown>;
  childPageCursor: string;
}

export interface LookupQueryResponse {
  contract: typeof CONTRACT;
  generation: string | number;
  revisions: LookupQueryPlan["revisions"];
  /** Visible root rows before the plan filter is evaluated. */
  rootTotal: number;
  /** Rows remaining after the plan filter is evaluated. */
  total: number;
  rows: readonly MaterializedRow[];
  groups: readonly GroupNode[];
  page: { offset: number; limit: number };
  stableTieBreaker: string;
}
