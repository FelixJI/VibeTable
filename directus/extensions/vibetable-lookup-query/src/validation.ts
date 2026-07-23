import { CONTRACT, type FilterNode, type LookupDefinition, type LookupQueryPlan, type OutputType } from "./contracts.ts";
import { LookupQueryError } from "./errors.ts";

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]{0,126}$/;
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,126}$/;
const SYSTEM_COLLECTIONS = new Set(["directus_files", "directus_users"]);
const NUMERIC = new Set(["integer", "decimal"]);
const ORDERABLE = new Set(["string", "integer", "decimal", "date", "time", "datetime", "uuid"]);

function invalid(message: string, path?: string): never {
  throw new LookupQueryError(
    "VIBETABLE_LOOKUP_PLAN_INVALID",
    message,
    path ? { path } : undefined,
  );
}

function unsupported(message: string, path?: string): never {
  throw new LookupQueryError(
    "VIBETABLE_LOOKUP_UNSUPPORTED",
    message,
    path ? { path } : undefined,
  );
}

function object(value: unknown, path: string): asserts value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) invalid(`${path} must be an object`, path);
}

export function validateIdentifier(value: unknown, path: string, collection = false): asserts value is string {
  if (typeof value !== "string" || !IDENTIFIER.test(value)) invalid(`${path} is not a valid identifier`, path);
  if (collection && value.startsWith("directus_") && !SYSTEM_COLLECTIONS.has(value)) {
    unsupported(`${path} references a protected Directus system collection`, path);
  }
}

function validateStableId(value: unknown, path: string): asserts value is string {
  if (typeof value !== "string" || !STABLE_ID.test(value)) invalid(`${path} is not a valid stable reference`, path);
}

export function validateOutputType(value: unknown, path: string): asserts value is OutputType {
  object(value, path);
  const kinds = ["string", "integer", "decimal", "boolean", "date", "time", "datetime", "uuid", "json"];
  if (!kinds.includes(String(value.kind))) invalid(`${path}.kind is invalid`, `${path}.kind`);
  if (value.kind === "decimal") {
    if (!Number.isSafeInteger(value.scale) || Number(value.scale) < 0 || Number(value.scale) > 18) {
      invalid(`${path}.scale must be an integer from 0 to 18 for decimal output`, `${path}.scale`);
    }
  } else if (value.scale !== undefined) {
    invalid(`${path}.scale is only valid for decimal output`, `${path}.scale`);
  }
}

function validateFilter(node: unknown, refs: ReadonlySet<string>, path: string, depth = 0): asserts node is FilterNode {
  if (depth > 16) invalid("filter nesting exceeds 16", path);
  object(node, path);
  if (node.op === "and" || node.op === "or") {
    if (!Array.isArray(node.children) || node.children.length === 0 || node.children.length > 64) {
      invalid(`${path}.children must contain 1 to 64 filters`, `${path}.children`);
    }
    node.children.forEach((child, index) => validateFilter(child, refs, `${path}.children[${index}]`, depth + 1));
    return;
  }
  validateStableId(node.fieldRef, `${path}.fieldRef`);
  if (!refs.has(node.fieldRef)) invalid(`${path}.fieldRef is unknown`, `${path}.fieldRef`);
  const operators = ["eq", "neq", "lt", "lte", "gt", "gte", "in", "not_in", "contains", "is_null", "not_null"];
  if (!operators.includes(String(node.operator))) invalid(`${path}.operator is invalid`, `${path}.operator`);
  if ((node.operator === "in" || node.operator === "not_in") && (!Array.isArray(node.value) || node.value.length > 500)) {
    invalid(`${path}.value must be an array of at most 500 values`, `${path}.value`);
  }
  if (!(["is_null", "not_null"] as unknown[]).includes(node.operator) && node.value === undefined) {
    invalid(`${path}.value is required`, `${path}.value`);
  }
}

function validateLookup(
  lookup: unknown,
  index: number,
  rootCollection: string,
  rootPrimaryKey: string,
): asserts lookup is LookupDefinition {
  const path = `lookups[${index}]`;
  object(lookup, path);
  validateStableId(lookup.lookupId, `${path}.lookupId`);
  validateStableId(lookup.ref, `${path}.ref`);
  if (lookup.collection !== undefined) validateIdentifier(lookup.collection, `${path}.collection`, true);
  if (lookup.primaryKey !== undefined) validateIdentifier(lookup.primaryKey, `${path}.primaryKey`);
  if (lookup.expose !== undefined && typeof lookup.expose !== "boolean") invalid(`${path}.expose must be boolean`, `${path}.expose`);
  const definitionCollection = typeof lookup.collection === "string" ? lookup.collection : rootCollection;
  const definitionPrimaryKey = typeof lookup.primaryKey === "string" ? lookup.primaryKey : rootPrimaryKey;
  if (lookup.expose !== false && definitionCollection !== rootCollection) {
    invalid(`${path} exposes a definition outside the plan root collection`, `${path}.collection`);
  }
  validateIdentifier(definitionPrimaryKey, `${path}.primaryKey`);
  validateOutputType(lookup.outputType, `${path}.outputType`);
  const aggregates = ["scalar", "list", "distinct", "count", "count_non_null", "sum", "avg", "min", "max"];
  if (!aggregates.includes(String(lookup.aggregate))) invalid(`${path}.aggregate is invalid`, `${path}.aggregate`);
  const relationPath = lookup.path;
  if (!Array.isArray(relationPath)) invalid(`${path}.path must be an array`, `${path}.path`);

  let collection = definitionCollection;
  let multi = false;
  relationPath.forEach((rawStep, stepIndex) => {
    const stepPath = `${path}.path[${stepIndex}]`;
    object(rawStep, stepPath);
    validateStableId(rawStep.relationId, `${stepPath}.relationId`);
    if (!["m2o", "o2m", "m2m", "m2a"].includes(String(rawStep.kind))) invalid(`${stepPath}.kind is invalid`, `${stepPath}.kind`);
    validateIdentifier(rawStep.fromCollection, `${stepPath}.fromCollection`, true);
    validateIdentifier(rawStep.sourceField, `${stepPath}.sourceField`);
    if (rawStep.fromCollection !== collection) invalid(`${stepPath}.fromCollection does not continue the preceding path`, `${stepPath}.fromCollection`);
    if (rawStep.kind === "m2a") {
      if (rawStep.destinationPrimaryKey !== undefined) invalid(`${stepPath}.destinationPrimaryKey is replaced by targetPrimaryKeys for M2A`, `${stepPath}.destinationPrimaryKey`);
      if (rawStep.targetField !== undefined) invalid(`${stepPath}.targetField is replaced by targetPrimaryKeys for M2A`, `${stepPath}.targetField`);
      if (!Array.isArray(rawStep.targetCollections) || rawStep.targetCollections.length === 0 || rawStep.targetCollections.length > 32) {
        invalid(`${stepPath}.targetCollections must contain 1 to 32 collections`, `${stepPath}.targetCollections`);
      }
      const unique = new Set<string>();
      for (const target of rawStep.targetCollections) {
        validateIdentifier(target, `${stepPath}.targetCollections`, true);
        if (unique.has(target)) invalid(`${stepPath}.targetCollections contains duplicates`, `${stepPath}.targetCollections`);
        unique.add(target);
      }
      object(rawStep.targetPrimaryKeys, `${stepPath}.targetPrimaryKeys`);
      for (const target of unique) {
        validateIdentifier(rawStep.targetPrimaryKeys[target], `${stepPath}.targetPrimaryKeys.${target}`);
      }
      for (const target of Object.keys(rawStep.targetPrimaryKeys)) {
        if (!unique.has(target)) invalid(`${stepPath}.targetPrimaryKeys contains an undeclared collection`, `${stepPath}.targetPrimaryKeys.${target}`);
      }
      if (rawStep.toCollection !== undefined) {
        validateIdentifier(rawStep.toCollection, `${stepPath}.toCollection`, true);
        if (!unique.has(rawStep.toCollection)) invalid(`${stepPath}.toCollection is not declared in targetCollections`, `${stepPath}.toCollection`);
        collection = rawStep.toCollection;
      } else if (stepIndex !== relationPath.length - 1) {
        invalid(`${stepPath}.toCollection is required when traversal continues after M2A`, `${stepPath}.toCollection`);
      }
    } else {
      validateIdentifier(rawStep.targetField, `${stepPath}.targetField`);
      validateIdentifier(rawStep.toCollection, `${stepPath}.toCollection`, true);
      if (rawStep.kind === "o2m") {
        validateIdentifier(rawStep.destinationPrimaryKey, `${stepPath}.destinationPrimaryKey`);
      } else if (rawStep.destinationPrimaryKey !== undefined) {
        validateIdentifier(rawStep.destinationPrimaryKey, `${stepPath}.destinationPrimaryKey`);
      }
      collection = rawStep.toCollection;
      if (rawStep.targetCollections !== undefined) invalid(`${stepPath}.targetCollections is only valid for M2A`, `${stepPath}.targetCollections`);
      if (rawStep.targetPrimaryKeys !== undefined) invalid(`${stepPath}.targetPrimaryKeys is only valid for M2A`, `${stepPath}.targetPrimaryKeys`);
    }
    if (rawStep.kind === "m2m" || rawStep.kind === "m2a") {
      object(rawStep.junction, `${stepPath}.junction`);
      validateIdentifier(rawStep.junction.collection, `${stepPath}.junction.collection`, true);
      validateIdentifier(rawStep.junction.sourceField, `${stepPath}.junction.sourceField`);
      validateIdentifier(rawStep.junction.targetField, `${stepPath}.junction.targetField`);
      if (rawStep.kind === "m2a") validateIdentifier(rawStep.junction.collectionField, `${stepPath}.junction.collectionField`);
      else if (rawStep.junction.collectionField !== undefined) invalid(`${stepPath}.junction.collectionField is only valid for M2A`, `${stepPath}.junction.collectionField`);
    } else if (rawStep.junction !== undefined) {
      invalid(`${stepPath}.junction is only valid for M2M/M2A`, `${stepPath}.junction`);
    }
    multi ||= rawStep.kind !== "m2o";
  });

  const source = lookup.source;
  object(source, `${path}.source`);
  if (source.kind === "field") {
    validateIdentifier(source.field, `${path}.source.field`);
  } else if (source.kind === "junction") {
    if (!Number.isSafeInteger(source.step)) invalid(`${path}.source.step is invalid`, `${path}.source.step`);
    const sourceStep = Number(source.step);
    if (sourceStep < 0 || sourceStep >= relationPath.length) invalid(`${path}.source.step is invalid`, `${path}.source.step`);
    const step = relationPath[sourceStep] as Record<string, unknown> | undefined;
    if (!step || (step.kind !== "m2m" && step.kind !== "m2a") || sourceStep !== relationPath.length - 1) {
      unsupported("junction sources are supported only on the terminal M2M/M2A step", `${path}.source`);
    }
    validateIdentifier(source.field, `${path}.source.field`);
  } else if (source.kind === "m2a") {
    const last = relationPath.at(-1) as Record<string, unknown> | undefined;
    if (!last || last.kind !== "m2a") invalid(`${path}.source.m2a requires a terminal M2A step`, `${path}.source`);
    object(source.fields, `${path}.source.fields`);
    const allowed = new Set(last.targetCollections as string[]);
    const mappings = Object.entries(source.fields);
    if (mappings.length === 0) invalid(`${path}.source.fields cannot be empty`, `${path}.source.fields`);
    for (const [target, field] of mappings) {
      if (!allowed.has(target)) invalid(`${path}.source.fields contains an undeclared collection`, `${path}.source.fields.${target}`);
      validateIdentifier(field, `${path}.source.fields.${target}`);
    }
    const requiredTargets = typeof last.toCollection === "string"
      ? [last.toCollection]
      : [...allowed];
    for (const target of requiredTargets) {
      if (!(target in source.fields)) invalid(`${path}.source.fields is missing collection '${target}'`, `${path}.source.fields.${target}`);
    }
  } else if (source.kind === "lookup") {
    validateStableId(source.lookupId, `${path}.source.lookupId`);
  } else {
    invalid(`${path}.source.kind is invalid`, `${path}.source.kind`);
  }

  const terminalStep = relationPath.at(-1) as Record<string, unknown> | undefined;
  if (
    terminalStep?.kind === "m2a"
    && terminalStep.toCollection === undefined
    && source.kind === "field"
    && lookup.aggregate !== "count"
  ) {
    invalid(`${path}.source must map fields per collection for an unselected terminal M2A`, `${path}.source`);
  }

  if (multi && lookup.aggregate === "scalar") invalid(`${path}.aggregate scalar is invalid after a multi-valued relation`, `${path}.aggregate`);
  if (lookup.aggregate === "sum" && !NUMERIC.has(lookup.outputType.kind)) invalid(`${path}.aggregate requires numeric output`, `${path}.aggregate`);
  if (lookup.aggregate === "avg" && lookup.outputType.kind !== "decimal") invalid(`${path}.aggregate avg requires decimal output`, `${path}.aggregate`);
  if ((lookup.aggregate === "min" || lookup.aggregate === "max") && !ORDERABLE.has(lookup.outputType.kind)) invalid(`${path}.aggregate requires orderable output`, `${path}.aggregate`);
  if ((lookup.aggregate === "count" || lookup.aggregate === "count_non_null") && lookup.outputType.kind !== "integer") invalid(`${path}.outputType must be integer for counts`, `${path}.outputType`);
}

function validateDependencyDag(lookups: readonly LookupDefinition[]): void {
  const byId = new Map(lookups.map((lookup) => [lookup.lookupId, lookup]));
  const visiting = new Set<string>();
  const visited = new Set<string>();
  const visit = (id: string): void => {
    if (visiting.has(id)) invalid("lookup dependency cycle detected", `lookups.${id}`);
    if (visited.has(id)) return;
    const lookup = byId.get(id);
    if (!lookup) invalid(`lookup dependency '${id}' does not exist`, `lookups.${id}`);
    visiting.add(id);
    if (lookup.source.kind === "lookup") visit(lookup.source.lookupId);
    visiting.delete(id);
    visited.add(id);
  };
  for (const lookup of lookups) visit(lookup.lookupId);
}

function compatibleLookupSource(consumer: LookupDefinition, dependency: LookupDefinition): boolean {
  if (consumer.aggregate === "count" || consumer.aggregate === "count_non_null") {
    return consumer.outputType.kind === "integer";
  }
  if (consumer.aggregate === "avg") {
    return NUMERIC.has(dependency.outputType.kind) && consumer.outputType.kind === "decimal";
  }
  if (consumer.aggregate === "sum") {
    return NUMERIC.has(dependency.outputType.kind) && NUMERIC.has(consumer.outputType.kind);
  }
  return consumer.outputType.kind === dependency.outputType.kind;
}

export function validatePlan(value: unknown): asserts value is LookupQueryPlan {
  object(value, "request");
  if (value.contract !== CONTRACT) invalid(`unsupported contract '${String(value.contract)}'`, "contract");
  if (typeof value.generation !== "string" && typeof value.generation !== "number") invalid("generation is required", "generation");
  validateIdentifier(value.collection, "collection", true);
  const rootCollection = value.collection;
  validateIdentifier(value.primaryKey, "primaryKey");
  const rootPrimaryKey = value.primaryKey;
  object(value.revisions, "revisions");
  for (const key of ["schema", "permission", "lookup"] as const) {
    if (typeof value.revisions[key] !== "string" || value.revisions[key].length === 0 || value.revisions[key].length > 256) invalid(`revisions.${key} is required`, `revisions.${key}`);
  }
  const baseFields = value.baseFields;
  if (!Array.isArray(baseFields) || baseFields.length > 128) invalid("baseFields must contain at most 128 fields", "baseFields");
  const refs = new Set<string>();
  for (const [index, raw] of baseFields.entries()) {
    object(raw, `baseFields[${index}]`);
    validateStableId(raw.ref, `baseFields[${index}].ref`);
    validateIdentifier(raw.field, `baseFields[${index}].field`);
    validateOutputType(raw.outputType, `baseFields[${index}].outputType`);
    if (refs.has(raw.ref)) invalid(`duplicate field ref '${raw.ref}'`, `baseFields[${index}].ref`);
    refs.add(raw.ref);
  }
  const lookups = value.lookups;
  if (!Array.isArray(lookups) || lookups.length > 64) invalid("lookups must contain at most 64 definitions", "lookups");
  const lookupIds = new Set<string>();
  const lookupRefs = new Set<string>();
  lookups.forEach((lookup, index) => {
    validateLookup(lookup, index, rootCollection, rootPrimaryKey);
    if (lookupIds.has(lookup.lookupId)) invalid(`duplicate lookupId '${lookup.lookupId}'`, `lookups[${index}].lookupId`);
    if (refs.has(lookup.ref) || lookupRefs.has(lookup.ref)) invalid(`duplicate field ref '${lookup.ref}'`, `lookups[${index}].ref`);
    lookupIds.add(lookup.lookupId);
    lookupRefs.add(lookup.ref);
    if (lookup.expose !== false) refs.add(lookup.ref);
  });
  validateDependencyDag(lookups as LookupDefinition[]);
  const byLookupId = new Map((lookups as LookupDefinition[]).map((lookup) => [lookup.lookupId, lookup]));
  for (const lookup of lookups as LookupDefinition[]) {
    if (lookup.source.kind !== "lookup") continue;
    const dependency = byLookupId.get(lookup.source.lookupId)!;
    let terminalCollection = lookup.collection ?? rootCollection;
    for (const step of lookup.path) {
      if (step.kind === "m2a" && !step.toCollection) {
        invalid(`lookup '${lookup.lookupId}' needs a selected M2A collection before a Lookup source`, `lookups.${lookup.lookupId}.path`);
      }
      terminalCollection = step.toCollection!;
    }
    const dependencyCollection = dependency.collection ?? rootCollection;
    if (terminalCollection !== dependencyCollection) {
      invalid(`lookup '${lookup.lookupId}' dependency starts in a different collection`, `lookups.${lookup.lookupId}.source`);
    }
    if (!compatibleLookupSource(lookup, dependency)) {
      invalid(`lookup '${lookup.lookupId}' output type is incompatible with its dependency`, `lookups.${lookup.lookupId}.outputType`);
    }
  }
  if (value.filter !== undefined) validateFilter(value.filter, refs, "filter");
  if (value.sort !== undefined) {
    if (!Array.isArray(value.sort) || value.sort.length > 16) invalid("sort must contain at most 16 entries", "sort");
    value.sort.forEach((sort, index) => {
      object(sort, `sort[${index}]`);
      if (!refs.has(String(sort.fieldRef))) invalid(`sort[${index}].fieldRef is unknown`, `sort[${index}].fieldRef`);
      if (sort.direction !== "asc" && sort.direction !== "desc") invalid(`sort[${index}].direction is invalid`, `sort[${index}].direction`);
    });
  }
  if (value.groupBy !== undefined) {
    if (!Array.isArray(value.groupBy) || value.groupBy.length > 8) invalid("groupBy must contain at most 8 refs", "groupBy");
    value.groupBy.forEach((ref, index) => {
      if (!refs.has(String(ref))) invalid(`groupBy[${index}] is unknown`, `groupBy[${index}]`);
    });
  }
  if (value.groupAggregates !== undefined) {
    if (!Array.isArray(value.groupAggregates) || value.groupAggregates.length > 32) invalid("groupAggregates must contain at most 32 entries", "groupAggregates");
    const aggregateRefs = new Set<string>();
    value.groupAggregates.forEach((aggregate, index) => {
      object(aggregate, `groupAggregates[${index}]`);
      validateStableId(aggregate.ref, `groupAggregates[${index}].ref`);
      if (aggregateRefs.has(aggregate.ref)) invalid(`duplicate group aggregate ref '${aggregate.ref}'`, `groupAggregates[${index}].ref`);
      aggregateRefs.add(aggregate.ref);
      if (!refs.has(String(aggregate.fieldRef))) invalid(`groupAggregates[${index}].fieldRef is unknown`, `groupAggregates[${index}].fieldRef`);
      if (!["count", "count_non_null", "sum", "avg", "min", "max"].includes(String(aggregate.aggregate))) invalid(`groupAggregates[${index}].aggregate is invalid`, `groupAggregates[${index}].aggregate`);
      validateOutputType(aggregate.outputType, `groupAggregates[${index}].outputType`);
      if ((aggregate.aggregate === "count" || aggregate.aggregate === "count_non_null") && aggregate.outputType.kind !== "integer") invalid(`groupAggregates[${index}] count output must be integer`, `groupAggregates[${index}].outputType`);
      if (aggregate.aggregate === "sum" && !NUMERIC.has(String(aggregate.outputType.kind))) invalid(`groupAggregates[${index}] numeric aggregate has a non-numeric output`, `groupAggregates[${index}].outputType`);
      if (aggregate.aggregate === "avg" && aggregate.outputType.kind !== "decimal") invalid(`groupAggregates[${index}] average output must be decimal`, `groupAggregates[${index}].outputType`);
      if ((aggregate.aggregate === "min" || aggregate.aggregate === "max") && !ORDERABLE.has(String(aggregate.outputType.kind))) invalid(`groupAggregates[${index}] ordered aggregate has a non-orderable output`, `groupAggregates[${index}].outputType`);
    });
  }
  if (value.page !== undefined) {
    const page = value.page;
    object(page, "page");
    if (!Number.isSafeInteger(page.offset) || Number(page.offset) < 0) invalid("page.offset must be a non-negative integer", "page.offset");
    if (!Number.isSafeInteger(page.limit) || Number(page.limit) < 1 || Number(page.limit) > 10_000) invalid("page.limit must be from 1 to 10000", "page.limit");
  }
  if (value.budgetHint !== undefined) {
    object(value.budgetHint, "budgetHint");
    const allowed = new Set(["maxRootItems", "maxIntermediateItems", "maxServiceCalls", "maxMilliseconds", "maxResponseBytes"]);
    for (const [key, hint] of Object.entries(value.budgetHint)) {
      if (!allowed.has(key) || !Number.isSafeInteger(hint) || Number(hint) < 1) invalid(`budgetHint.${key} must be a positive integer`, `budgetHint.${key}`);
    }
  }
}

export function dependencyOrder(lookups: readonly LookupDefinition[]): readonly LookupDefinition[] {
  const byId = new Map(lookups.map((lookup) => [lookup.lookupId, lookup]));
  const result: LookupDefinition[] = [];
  const visited = new Set<string>();
  const visit = (lookup: LookupDefinition): void => {
    if (visited.has(lookup.lookupId)) return;
    if (lookup.source.kind === "lookup") {
      const dependency = byId.get(lookup.source.lookupId);
      if (dependency) visit(dependency);
    }
    visited.add(lookup.lookupId);
    result.push(lookup);
  };
  for (const lookup of lookups) visit(lookup);
  return result;
}
