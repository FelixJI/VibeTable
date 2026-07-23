import type {
  FilterNode,
  GroupAggregate,
  GroupNode,
  LookupDefinition,
  LookupQueryPlan,
  LookupQueryResponse,
  MaterializedRow,
  OutputType,
  QueryBudget,
  RelationStep,
} from "./contracts.ts";
import { directusReadError, LookupQueryError } from "./errors.ts";
import { aggregateValues, canonicalKey, compareValues, normalizeScalar, type LookupValue } from "./values.ts";

export type ItemRow = Record<string, unknown>;

export interface ItemsServiceLike {
  readByQuery(query: Record<string, unknown>): Promise<ItemRow[]>;
}

export type ItemsServiceConstructor = new (
  collection: string,
  options: Record<string, unknown>,
) => ItemsServiceLike;

export const DEPLOYMENT_BUDGET: Readonly<QueryBudget> = {
  maxRootItems: 25_000,
  maxIntermediateItems: 100_000,
  maxServiceCalls: 512,
  maxMilliseconds: 15_000,
  maxResponseBytes: 8_000_000,
};

const FRONTIER_KEYS = 250;
const READ_PAGE = 500;

class BudgetTracker {
  public readonly limits: QueryBudget;
  private readonly started = Date.now();
  private rootItems = 0;
  private intermediateItems = 0;
  private serviceCalls = 0;

  public constructor(hint: LookupQueryPlan["budgetHint"], deployment = DEPLOYMENT_BUDGET) {
    this.limits = {
      maxRootItems: Math.min(deployment.maxRootItems, hint?.maxRootItems ?? deployment.maxRootItems),
      maxIntermediateItems: Math.min(deployment.maxIntermediateItems, hint?.maxIntermediateItems ?? deployment.maxIntermediateItems),
      maxServiceCalls: Math.min(deployment.maxServiceCalls, hint?.maxServiceCalls ?? deployment.maxServiceCalls),
      maxMilliseconds: Math.min(deployment.maxMilliseconds, hint?.maxMilliseconds ?? deployment.maxMilliseconds),
      maxResponseBytes: Math.min(deployment.maxResponseBytes, hint?.maxResponseBytes ?? deployment.maxResponseBytes),
    };
  }

  public serviceCall(): void {
    this.serviceCalls += 1;
    this.check("service_calls", this.serviceCalls, this.limits.maxServiceCalls);
  }

  public roots(count: number): void {
    this.rootItems += count;
    this.check("root_items", this.rootItems, this.limits.maxRootItems);
  }

  public intermediates(count: number): void {
    this.intermediateItems += count;
    this.check("intermediate_items", this.intermediateItems, this.limits.maxIntermediateItems);
  }

  public time(): void {
    this.check("milliseconds", Date.now() - this.started, this.limits.maxMilliseconds);
  }

  public response(bytes: number): void {
    this.check("response_bytes", bytes, this.limits.maxResponseBytes);
  }

  private check(metric: string, actual: number, limit: number): void {
    if (actual > limit) {
      throw new LookupQueryError(
        "VIBETABLE_LOOKUP_TOO_EXPENSIVE",
        "lookup query exceeded its execution budget",
        { metric, limit },
      );
    }
    if (metric !== "milliseconds" && Date.now() - this.started > this.limits.maxMilliseconds) {
      throw new LookupQueryError(
        "VIBETABLE_LOOKUP_TOO_EXPENSIVE",
        "lookup query exceeded its execution budget",
        { metric: "milliseconds", limit: this.limits.maxMilliseconds },
      );
    }
  }
}

interface FrontierNode {
  collection: string;
  record: ItemRow;
  itemId: unknown;
  junction?: ItemRow;
}

interface ExecutionContext {
  createService(collection: string): ItemsServiceLike;
  budget: BudgetTracker;
}

function unique(values: readonly string[]): string[] {
  return [...new Set(values)];
}

function chunks<T>(values: readonly T[], size: number): T[][] {
  const result: T[][] = [];
  for (let index = 0; index < values.length; index += size) result.push(values.slice(index, index + size));
  return result;
}

async function safeRead(
  context: ExecutionContext,
  collection: string,
  query: Record<string, unknown>,
  details: Readonly<Record<string, unknown>>,
): Promise<ItemRow[]> {
  context.budget.serviceCall();
  context.budget.time();
  try {
    return await context.createService(collection).readByQuery(query);
  } catch (error) {
    throw directusReadError(error, { collection, ...details });
  }
}

async function readAllByIn(
  context: ExecutionContext,
  collection: string,
  keyField: string,
  rawKeys: readonly unknown[],
  fields: readonly string[],
  lookupId: string,
): Promise<ItemRow[]> {
  const keyed = new Map(rawKeys.filter((key) => key !== null && key !== undefined).map((key) => [canonicalKey(key), key]));
  const output: ItemRow[] = [];
  for (const keyChunk of chunks([...keyed.values()], FRONTIER_KEYS)) {
    let offset = 0;
    while (true) {
      const page = await safeRead(
        context,
        collection,
        {
          fields: unique([keyField, ...fields]),
          filter: { [keyField]: { _in: keyChunk } },
          sort: [keyField],
          limit: READ_PAGE,
          offset,
        },
        { lookupId, field: keyField },
      );
      context.budget.intermediates(page.length);
      output.push(...page);
      if (page.length < READ_PAGE) break;
      offset += page.length;
    }
  }
  return output;
}

function definitionCollection(lookup: LookupDefinition, plan: LookupQueryPlan): string {
  return lookup.collection ?? plan.collection;
}

function definitionPrimaryKey(lookup: LookupDefinition, plan: LookupQueryPlan): string {
  return lookup.primaryKey ?? plan.primaryKey;
}

function definitionStartFields(
  lookup: LookupDefinition,
  plan: LookupQueryPlan,
  byId: ReadonlyMap<string, LookupDefinition>,
  visiting = new Set<string>(),
): string[] {
  if (visiting.has(lookup.lookupId)) return [];
  visiting.add(lookup.lookupId);
  const first = lookup.path[0];
  const fields = [definitionPrimaryKey(lookup, plan)];
  if (first) fields.push(first.sourceField);
  else if (lookup.source.kind === "field") fields.push(lookup.source.field);
  else if (lookup.source.kind === "lookup") {
    const dependency = byId.get(lookup.source.lookupId);
    if (dependency) fields.push(...definitionStartFields(dependency, plan, byId, visiting));
  }
  visiting.delete(lookup.lookupId);
  return unique(fields);
}

function fieldsAfterStep(
  lookup: LookupDefinition,
  stepIndex: number,
  collection: string,
  plan: LookupQueryPlan,
  byId: ReadonlyMap<string, LookupDefinition>,
): string[] {
  const next = lookup.path[stepIndex + 1];
  if (next) return [next.sourceField];
  if (lookup.aggregate === "count") return [];
  if (lookup.source.kind === "field") return [lookup.source.field];
  if (lookup.source.kind === "m2a") {
    const field = lookup.source.fields[collection];
    return field ? [field] : [];
  }
  if (lookup.source.kind === "lookup") {
    const dependency = byId.get(lookup.source.lookupId);
    return dependency ? definitionStartFields(dependency, plan, byId) : [];
  }
  return [];
}

function pushNode(
  destination: Map<string, FrontierNode[]>,
  rootKey: string,
  node: FrontierNode,
  budget: BudgetTracker,
): void {
  const current = destination.get(rootKey) ?? [];
  current.push(node);
  destination.set(rootKey, current);
  budget.intermediates(1);
}

async function expandDirect(
  context: ExecutionContext,
  plan: LookupQueryPlan,
  byId: ReadonlyMap<string, LookupDefinition>,
  lookup: LookupDefinition,
  step: RelationStep,
  stepIndex: number,
  frontier: ReadonlyMap<string, readonly FrontierNode[]>,
): Promise<Map<string, FrontierNode[]>> {
  const sourceValues = [...frontier.values()].flatMap((nodes) => nodes.map((node) => node.record[step.sourceField]));
  const target = step.toCollection!;
  const targetField = step.targetField!;
  const rows = await readAllByIn(context, target, targetField, sourceValues, fieldsAfterStep(lookup, stepIndex, target, plan, byId), lookup.lookupId);
  const byKey = new Map<string, ItemRow[]>();
  for (const row of rows) {
    const key = canonicalKey(row[targetField]);
    const bucket = byKey.get(key) ?? [];
    bucket.push(row);
    byKey.set(key, bucket);
  }
  const next = new Map<string, FrontierNode[]>();
  for (const [rootKey, nodes] of frontier) {
    for (const node of nodes) {
      const matches = byKey.get(canonicalKey(node.record[step.sourceField])) ?? [];
      if (step.kind === "m2o" && matches.length > 1) {
        throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "an M2O target key was not unique", { lookupId: lookup.lookupId, relationId: step.relationId });
      }
      for (const row of matches) {
        pushNode(next, rootKey, { collection: target, record: row, itemId: row[targetField] }, context.budget);
      }
    }
  }
  return next;
}

async function expandO2m(
  context: ExecutionContext,
  plan: LookupQueryPlan,
  byId: ReadonlyMap<string, LookupDefinition>,
  lookup: LookupDefinition,
  step: RelationStep,
  stepIndex: number,
  frontier: ReadonlyMap<string, readonly FrontierNode[]>,
): Promise<Map<string, FrontierNode[]>> {
  const parentValues = [...frontier.values()].flatMap((nodes) => nodes.map((node) => node.record[step.sourceField]));
  const target = step.toCollection!;
  const targetField = step.targetField!;
  const targetPrimaryKey = step.destinationPrimaryKey!;
  const rows = await readAllByIn(
    context,
    target,
    targetField,
    parentValues,
    [targetPrimaryKey, ...fieldsAfterStep(lookup, stepIndex, target, plan, byId)],
    lookup.lookupId,
  );
  const byParent = new Map<string, ItemRow[]>();
  for (const row of rows) {
    const key = canonicalKey(row[targetField]);
    const bucket = byParent.get(key) ?? [];
    bucket.push(row);
    byParent.set(key, bucket);
  }
  const next = new Map<string, FrontierNode[]>();
  for (const [rootKey, nodes] of frontier) {
    for (const node of nodes) {
      for (const row of byParent.get(canonicalKey(node.record[step.sourceField])) ?? []) {
        pushNode(next, rootKey, { collection: target, record: row, itemId: row[targetPrimaryKey] }, context.budget);
      }
    }
  }
  return next;
}

async function expandJunction(
  context: ExecutionContext,
  plan: LookupQueryPlan,
  byId: ReadonlyMap<string, LookupDefinition>,
  lookup: LookupDefinition,
  step: RelationStep,
  stepIndex: number,
  frontier: ReadonlyMap<string, readonly FrontierNode[]>,
): Promise<Map<string, FrontierNode[]>> {
  const junction = step.junction!;
  const parentValues = [...frontier.values()].flatMap((nodes) => nodes.map((node) => node.record[step.sourceField]));
  const junctionFields = [junction.targetField];
  if (junction.collectionField) junctionFields.push(junction.collectionField);
  if (lookup.source.kind === "junction" && lookup.source.step === stepIndex) junctionFields.push(lookup.source.field);
  const junctionRows = await readAllByIn(context, junction.collection, junction.sourceField, parentValues, junctionFields, lookup.lookupId);
  const junctionByParent = new Map<string, ItemRow[]>();
  for (const row of junctionRows) {
    const key = canonicalKey(row[junction.sourceField]);
    const bucket = junctionByParent.get(key) ?? [];
    bucket.push(row);
    junctionByParent.set(key, bucket);
  }

  const targetsByCollection = new Map<string, Map<string, ItemRow>>();
  const targetCollections = step.kind === "m2a"
    ? (step.toCollection ? [step.toCollection] : [...step.targetCollections!])
    : [step.toCollection!];
  for (const collection of targetCollections) {
    const collectionJunctions = junctionRows.filter((row) => step.kind !== "m2a" || row[junction.collectionField!] === collection);
    if (collectionJunctions.length === 0) continue;
    const targetPrimaryKey = step.kind === "m2a"
      ? step.targetPrimaryKeys![collection]!
      : step.targetField!;
    const rows = await readAllByIn(
      context,
      collection,
      targetPrimaryKey,
      collectionJunctions.map((row) => row[junction.targetField]),
      fieldsAfterStep(lookup, stepIndex, collection, plan, byId),
      lookup.lookupId,
    );
    const targetById = new Map<string, ItemRow>();
    for (const row of rows) {
      const key = canonicalKey(row[targetPrimaryKey]);
      if (targetById.has(key)) throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "a relation target key was not unique", { lookupId: lookup.lookupId, relationId: step.relationId, collection });
      targetById.set(key, row);
    }
    targetsByCollection.set(collection, targetById);
  }

  const next = new Map<string, FrontierNode[]>();
  for (const [rootKey, nodes] of frontier) {
    for (const node of nodes) {
      const links = junctionByParent.get(canonicalKey(node.record[step.sourceField])) ?? [];
      for (const link of links) {
        const collection = step.kind === "m2a" ? String(link[junction.collectionField!] ?? "") : step.toCollection!;
        if (!targetCollections.includes(collection)) continue;
        const itemId = link[junction.targetField];
        const target = targetsByCollection.get(collection)?.get(canonicalKey(itemId));
        // A permission-hidden target is omitted without exposing that a junction existed.
        if (target) pushNode(next, rootKey, { collection, record: target, itemId, junction: link }, context.budget);
      }
    }
  }
  return next;
}

function terminalValues(lookup: LookupDefinition, nodes: readonly FrontierNode[]): LookupValue[] {
  const source = lookup.source;
  if (source.kind === "junction") return nodes.map((node) => ({ value: node.junction?.[source.field] ?? null, collection: node.collection, itemId: node.itemId }));
  if (source.kind === "m2a") return nodes.map((node) => ({ value: node.record[source.fields[node.collection]!] ?? null, collection: node.collection, itemId: node.itemId }));
  if (source.kind === "field") return nodes.map((node) => ({ value: node.record[source.field] ?? null, collection: node.collection, itemId: node.itemId }));
  return [];
}

async function executeDefinition(
  context: ExecutionContext,
  plan: LookupQueryPlan,
  byId: ReadonlyMap<string, LookupDefinition>,
  lookup: LookupDefinition,
  startRows: readonly ItemRow[],
): Promise<Map<string, unknown>> {
  const startCollection = definitionCollection(lookup, plan);
  const startPrimaryKey = definitionPrimaryKey(lookup, plan);
  let frontier = new Map<string, FrontierNode[]>();
  for (const row of startRows) {
    const itemId = row[startPrimaryKey];
    if (itemId === null || itemId === undefined) {
      throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "a Lookup dependency row is missing its primary key", { lookupId: lookup.lookupId, collection: startCollection, field: startPrimaryKey });
    }
    const key = canonicalKey(itemId);
    if (frontier.has(key)) {
      throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "a Lookup dependency primary key was not unique", { lookupId: lookup.lookupId, collection: startCollection, field: startPrimaryKey });
    }
    frontier.set(key, [{ collection: startCollection, record: row, itemId }]);
  }
  for (const [stepIndex, step] of lookup.path.entries()) {
    frontier = step.kind === "m2o"
      ? await expandDirect(context, plan, byId, lookup, step, stepIndex, frontier)
      : step.kind === "o2m"
        ? await expandO2m(context, plan, byId, lookup, step, stepIndex, frontier)
        : await expandJunction(context, plan, byId, lookup, step, stepIndex, frontier);
    context.budget.time();
  }

  let dependentValues: Map<string, unknown> | undefined;
  let dependency: LookupDefinition | undefined;
  if (lookup.source.kind === "lookup" && lookup.aggregate !== "count") {
    dependency = byId.get(lookup.source.lookupId)!;
    const dependencyPrimaryKey = definitionPrimaryKey(dependency, plan);
    const uniqueTargets = new Map<string, ItemRow>();
    for (const nodes of frontier.values()) {
      for (const node of nodes) {
        const itemId = node.record[dependencyPrimaryKey];
        if (itemId === null || itemId === undefined) {
          throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "a Lookup source target is missing its primary key", { lookupId: lookup.lookupId, dependency: dependency.lookupId, field: dependencyPrimaryKey });
        }
        uniqueTargets.set(canonicalKey(itemId), node.record);
      }
    }
    dependentValues = await executeDefinition(context, plan, byId, dependency, [...uniqueTargets.values()]);
  }

  const result = new Map<string, unknown>();
  for (const row of startRows) {
    const key = canonicalKey(row[startPrimaryKey]);
    const nodes = frontier.get(key) ?? [];
    const values = lookup.source.kind === "lookup"
      ? nodes.map((node): LookupValue => ({
          value: lookup.aggregate === "count"
            ? null
            : dependentValues!.get(canonicalKey(node.record[definitionPrimaryKey(dependency!, plan)])) ?? null,
          collection: node.collection,
          itemId: node.itemId,
        }))
      : terminalValues(lookup, nodes);
    result.set(
      key,
      aggregateValues(
        values,
        lookup.aggregate,
        lookup.outputType,
        lookup.path.at(-1)?.kind === "m2a",
      ),
    );
  }
  return result;
}

function rootFields(plan: LookupQueryPlan): string[] {
  const fields = [plan.primaryKey, ...plan.baseFields.map((field) => field.field)];
  const byId = new Map(plan.lookups.map((lookup) => [lookup.lookupId, lookup]));
  for (const lookup of plan.lookups) {
    if (lookup.expose === false || definitionCollection(lookup, plan) !== plan.collection) continue;
    fields.push(...definitionStartFields(lookup, plan, byId));
  }
  return unique(fields);
}

async function readRoots(context: ExecutionContext, plan: LookupQueryPlan): Promise<ItemRow[]> {
  const output: ItemRow[] = [];
  let offset = 0;
  while (true) {
    const page = await safeRead(
      context,
      plan.collection,
      { fields: rootFields(plan), sort: [plan.primaryKey], limit: READ_PAGE, offset },
      { field: plan.primaryKey },
    );
    context.budget.roots(page.length);
    output.push(...page);
    if (page.length < READ_PAGE) break;
    offset += page.length;
  }
  return output;
}

function filterRow(row: MaterializedRow, node: FilterNode, outputTypes: ReadonlyMap<string, OutputType>): boolean {
  if ("op" in node) return node.op === "and" ? node.children.every((child) => filterRow(row, child, outputTypes)) : node.children.some((child) => filterRow(row, child, outputTypes));
  const value = row.cells[node.fieldRef];
  const outputType = outputTypes.get(node.fieldRef);
  const isNull = value === null || value === undefined;
  switch (node.operator) {
    case "is_null": return isNull;
    case "not_null": return !isNull;
    case "eq": return compareValues(value, node.value, outputType) === 0;
    case "neq": return compareValues(value, node.value, outputType) !== 0;
    case "lt": return !isNull && compareValues(value, node.value, outputType) < 0;
    case "lte": return !isNull && compareValues(value, node.value, outputType) <= 0;
    case "gt": return !isNull && compareValues(value, node.value, outputType) > 0;
    case "gte": return !isNull && compareValues(value, node.value, outputType) >= 0;
    case "in": return (node.value as unknown[]).some((candidate) => compareValues(value, candidate, outputType) === 0);
    case "not_in": return !(node.value as unknown[]).some((candidate) => compareValues(value, candidate, outputType) === 0);
    case "contains": return Array.isArray(value)
      ? value.some((candidate) => canonicalKey(candidate) === canonicalKey(node.value))
      : typeof value === "string" && typeof node.value === "string" && value.includes(node.value);
  }
}

function aggregateGroup(rows: readonly MaterializedRow[], aggregate: GroupAggregate): unknown {
  return aggregateValues(rows.map((row) => ({ value: row.cells[aggregate.fieldRef] })), aggregate.aggregate, aggregate.outputType);
}

function createGroups(
  rows: readonly MaterializedRow[],
  refs: readonly string[],
  aggregates: readonly GroupAggregate[],
  budget: BudgetTracker,
): GroupNode[] {
  const output: GroupNode[] = [];
  const visit = (members: readonly MaterializedRow[], depth: number, path: Array<{ fieldRef: string; key: unknown }>): void => {
    const ref = refs[depth];
    if (!ref) return;
    const buckets = new Map<string, { key: unknown; rows: MaterializedRow[] }>();
    for (const row of members) {
      const key = row.cells[ref];
      const canonical = canonicalKey(key);
      const bucket = buckets.get(canonical) ?? { key, rows: [] };
      bucket.rows.push(row);
      buckets.set(canonical, bucket);
    }
    for (const bucket of [...buckets.values()].sort((a, b) => compareValues(a.key, b.key))) {
      const nextPath = [...path, { fieldRef: ref, key: bucket.key }];
      const aggregateCells = Object.fromEntries(aggregates.map((aggregate) => [aggregate.ref, aggregateGroup(bucket.rows, aggregate)]));
      const childPageCursor = Buffer.from(JSON.stringify({ path: nextPath, offset: 0 }), "utf8").toString("base64url");
      output.push({ path: nextPath, key: bucket.key, count: bucket.rows.length, aggregateCells, childPageCursor });
      budget.intermediates(1);
      visit(bucket.rows, depth + 1, nextPath);
    }
  };
  visit(rows, 0, []);
  return output;
}

export async function executeQuery(
  plan: LookupQueryPlan,
  ItemsService: ItemsServiceConstructor,
  serviceOptions: Record<string, unknown>,
  deploymentBudget = DEPLOYMENT_BUDGET,
): Promise<LookupQueryResponse> {
  const budget = new BudgetTracker(plan.budgetHint, deploymentBudget);
  const context: ExecutionContext = {
    createService: (collection) => new ItemsService(collection, serviceOptions),
    budget,
  };
  const roots = await readRoots(context, plan);
  const rows = new Map<string, MaterializedRow>();
  for (const root of roots) {
    const primaryKey = root[plan.primaryKey];
    if (primaryKey === null || primaryKey === undefined) throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "a root row is missing its primary key", { collection: plan.collection, field: plan.primaryKey });
    const key = canonicalKey(primaryKey);
    if (rows.has(key)) throw new LookupQueryError("VIBETABLE_LOOKUP_SCHEMA_INVALID", "the root primary key was not unique", { collection: plan.collection, field: plan.primaryKey });
    rows.set(key, {
      primaryKey,
      cells: Object.fromEntries(plan.baseFields.map((field) => [field.ref, normalizeScalar(root[field.field], field.outputType)])),
    });
  }

  const byId = new Map(plan.lookups.map((lookup) => [lookup.lookupId, lookup]));
  const exposedLookups = plan.lookups.filter((lookup) => lookup.expose !== false);
  for (const lookup of exposedLookups) {
    const values = await executeDefinition(context, plan, byId, lookup, roots);
    for (const [key, value] of values) rows.get(key)!.cells[lookup.ref] = value;
  }

  const outputTypes = new Map<string, OutputType>([
    ...plan.baseFields.map((field) => [field.ref, field.outputType] as const),
    ...exposedLookups.map((lookup) => [lookup.ref, lookup.outputType] as const),
  ]);
  let materialized = [...rows.values()];
  if (plan.filter) materialized = materialized.filter((row) => filterRow(row, plan.filter!, outputTypes));
  materialized.sort((left, right) => {
    for (const sort of plan.sort ?? []) {
      const result = compareValues(left.cells[sort.fieldRef], right.cells[sort.fieldRef], outputTypes.get(sort.fieldRef));
      if (result !== 0) return sort.direction === "asc" ? result : -result;
    }
    return compareValues(left.primaryKey, right.primaryKey);
  });
  const groups = createGroups(materialized, plan.groupBy ?? [], plan.groupAggregates ?? [], budget);
  const total = materialized.length;
  const page = plan.page ?? { offset: 0, limit: 100 };
  const response: LookupQueryResponse = {
    contract: plan.contract,
    generation: plan.generation,
    revisions: plan.revisions,
    rootTotal: roots.length,
    total,
    rows: materialized.slice(page.offset, page.offset + page.limit),
    groups,
    page,
    stableTieBreaker: plan.primaryKey,
  };
  budget.time();
  budget.response(Buffer.byteLength(JSON.stringify(response), "utf8"));
  return response;
}
