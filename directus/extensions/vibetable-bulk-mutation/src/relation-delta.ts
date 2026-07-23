import type { Accountability } from "@directus/types";
import { createHash } from "node:crypto";

export const RELATION_DELTA_CONTRACT = "vibetable-relation-delta.v1" as const;

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const REVISION = /^[a-f0-9]{64}$/;

export interface RelationJunctionPlan {
  collection: string;
  sourceField: string;
  targetField: string;
  collectionField?: string | null;
  contextFields: readonly string[];
}

export interface RelationMutationPlan {
  relationId: string;
  kind: "o2m" | "m2m" | "m2a";
  sourceCollection: string;
  sourcePrimaryKey: string;
  sourceItemId: string | number;
  sourceDateUpdatedField?: string | null;
  expectedDateUpdated?: string | null;
  relatedCollection?: string | null;
  relatedPrimaryKey?: string | null;
  manyField?: string | null;
  allowedCollections?: readonly string[];
  targetPrimaryKeys?: Readonly<Record<string, string>>;
  junction?: RelationJunctionPlan | null;
  adds: readonly {
    collection: string;
    itemId: string | number;
    junctionValues?: Readonly<Record<string, unknown>>;
  }[];
  updates: readonly {
    junctionId: string | number;
    values: Readonly<Record<string, unknown>>;
    expectedRevision?: string;
  }[];
  removes: readonly {
    collection: string;
    itemId: string | number;
    junctionId?: string | number;
    expectedRevision?: string;
  }[];
}

export interface RelationDeltaRequest {
  contract: typeof RELATION_DELTA_CONTRACT;
  idempotencyKey: string;
  expectedSchemaRevision: string;
  /**
   * SHA-256 of the relation-specific live schema proof.
   */
  schemaProof: string;
  relation: RelationMutationPlan;
}

export interface RelationDeltaResult {
  requestId: string;
  outcome: "committed";
  createdJunctionIds: string[];
  updatedJunctionIds: string[];
  deletedJunctionIds: string[];
  linkedItemIds: string[];
  unlinkedItemIds: string[];
}

export interface ValidationResult {
  ok: boolean;
  error?: string;
}

export class RelationDeltaConflictError extends Error {
  readonly code = "EDIT_CONFLICT";

  constructor(message = "relation changed since preview") {
    super(message);
    this.name = "RelationDeltaConflictError";
  }
}

function validIdentifier(value: unknown): value is string {
  return typeof value === "string" && IDENTIFIER.test(value);
}

function invalid(error: string): ValidationResult {
  return { ok: false, error };
}

function validIdentity(value: unknown): boolean {
  return (typeof value === "string" && value.length > 0 && value.length <= 256)
    || typeof value === "number";
}

export function validateRelationDelta(
  value: unknown,
  headerKey?: string,
): ValidationResult {
  if (!value || typeof value !== "object" || Array.isArray(value)) return invalid("request body is required");
  const request = value as Partial<RelationDeltaRequest>;
  if (request.contract !== RELATION_DELTA_CONTRACT) return invalid("unsupported relation delta contract");
  if (typeof request.idempotencyKey !== "string" || !STABLE_ID.test(request.idempotencyKey)) return invalid("idempotencyKey is invalid");
  if (headerKey !== undefined && headerKey !== request.idempotencyKey) return invalid("Idempotency-Key does not match the request");
  if (typeof request.expectedSchemaRevision !== "string" || request.expectedSchemaRevision.length === 0) return invalid("expectedSchemaRevision is required");
  if (typeof request.schemaProof !== "string" || !REVISION.test(request.schemaProof)) return invalid("schemaProof must be a SHA-256 digest");
  const relation = request.relation as Partial<RelationMutationPlan> | undefined;
  if (!relation || typeof relation !== "object") return invalid("relation is required");
  if (typeof relation.relationId !== "string" || !STABLE_ID.test(relation.relationId)) return invalid("relationId is invalid");
  if (!(["o2m", "m2m", "m2a"] as unknown[]).includes(relation.kind)) return invalid("relation kind is invalid");
  if (!validIdentifier(relation.sourceCollection) || !validIdentifier(relation.sourcePrimaryKey)) return invalid("source identifiers are invalid");
  if (!validIdentity(relation.sourceItemId)) return invalid("sourceItemId is invalid");
  if (relation.sourceDateUpdatedField != null && !validIdentifier(relation.sourceDateUpdatedField)) return invalid("sourceDateUpdatedField is invalid");
  if (relation.expectedDateUpdated != null && !relation.sourceDateUpdatedField) return invalid("expectedDateUpdated requires sourceDateUpdatedField");
  for (const key of ["adds", "updates", "removes"] as const) {
    if (!Array.isArray(relation[key]) || relation[key]!.length > 1000) return invalid(`${key} must be an array of at most 1000 entries`);
  }
  if (relation.kind === "o2m") {
    if (!validIdentifier(relation.relatedCollection) || !validIdentifier(relation.relatedPrimaryKey) || !validIdentifier(relation.manyField)) return invalid("O2M physical fields are incomplete");
    if (relation.junction != null || relation.updates!.length > 0) return invalid("O2M does not accept junction mutations");
  } else {
    const junction = relation.junction;
    if (!junction || !validIdentifier(junction.collection) || !validIdentifier(junction.sourceField) || !validIdentifier(junction.targetField)) return invalid("junction fields are incomplete");
    if (!Array.isArray(junction.contextFields) || junction.contextFields.some((field) => !validIdentifier(field))) return invalid("junction contextFields are invalid");
    if (new Set(junction.contextFields).size !== junction.contextFields.length) return invalid("junction contextFields contain duplicates");
    if (relation.kind === "m2m") {
      if (!validIdentifier(relation.relatedCollection) || !validIdentifier(relation.relatedPrimaryKey)) return invalid("M2M target identifiers are incomplete");
      if (junction.collectionField != null) return invalid("M2M cannot declare collectionField");
    } else {
      if (!validIdentifier(junction.collectionField)) return invalid("M2A collectionField is required");
      if (!Array.isArray(relation.allowedCollections) || relation.allowedCollections.length === 0 || relation.allowedCollections.some((item) => !validIdentifier(item))) return invalid("M2A allowedCollections are invalid");
      if (!relation.targetPrimaryKeys || typeof relation.targetPrimaryKeys !== "object") return invalid("M2A targetPrimaryKeys are required");
      for (const collection of relation.allowedCollections) {
        if (!validIdentifier(relation.targetPrimaryKeys[collection])) return invalid("M2A target primary key is missing");
      }
    }
  }
  const allowedContexts = new Set(relation.junction?.contextFields ?? []);
  for (const add of relation.adds!) {
    if (!add || !validIdentifier(add.collection) || !validIdentity(add.itemId)) return invalid("relation add target is invalid");
    if (add.junctionValues && Object.keys(add.junctionValues).some((field) => !allowedContexts.has(field))) return invalid("relation add contains an undeclared junction field");
  }
  for (const update of relation.updates!) {
    if (!update || !validIdentity(update.junctionId) || !update.values || Object.keys(update.values).some((field) => !allowedContexts.has(field))) return invalid("relation junction update is invalid");
    if (relation.kind !== "o2m" && (typeof update.expectedRevision !== "string" || !REVISION.test(update.expectedRevision))) return invalid("junction update expectedRevision is required");
  }
  for (const remove of relation.removes!) {
    if (!remove || !validIdentifier(remove.collection) || !validIdentity(remove.itemId)) return invalid("relation remove target is invalid");
    if (relation.kind !== "o2m" && !validIdentity(remove.junctionId)) return invalid("junctionId is required for M2M/M2A removal");
    if (relation.kind !== "o2m" && (typeof remove.expectedRevision !== "string" || !REVISION.test(remove.expectedRevision))) return invalid("junction remove expectedRevision is required");
  }
  const allowedTargets = new Set(relation.kind === "m2a" ? relation.allowedCollections : [relation.relatedCollection]);
  if ([...relation.adds!, ...relation.removes!].some((item) => !allowedTargets.has(item.collection))) return invalid("target collection is outside the relation allow-list");
  return { ok: true };
}

function stringify(value: string | number): string {
  return String(value);
}

type LiveSchemaCollection = {
  primary?: string;
  fields?: Record<string, unknown> | readonly string[];
};

type LiveSchemaRelation = {
  collection?: string;
  field?: string;
  related_collection?: string | null;
  meta?: {
    one_collection_field?: string | null;
    one_allowed_collections?: readonly string[] | null;
  } | null;
};

type LiveSchemaOverview = {
  collections?: Record<string, LiveSchemaCollection>;
  relations?: readonly LiveSchemaRelation[];
};

function liveFieldExists(collection: LiveSchemaCollection | undefined, field: string): boolean {
  if (!collection) return false;
  if (Array.isArray(collection.fields)) return collection.fields.includes(field);
  return Boolean(collection.fields && field in collection.fields);
}

function hasLiveRelation(
  relations: readonly LiveSchemaRelation[],
  collection: string,
  field: string,
  relatedCollection: string | null,
): boolean {
  return relations.some((candidate) =>
    candidate.collection === collection
    && candidate.field === field
    && candidate.related_collection === relatedCollection
  );
}

/**
 * Recompute the canonical proof for exactly the physical schema used by a
 * relation delta. This intentionally excludes presentation metadata and the
 * opaque application schema revision, neither of which Directus getSchema()
 * exposes. Missing or drifted fields/relations fail closed before a write.
 */
export function computeRelationSchemaProof(
  relation: RelationMutationPlan,
  schema: unknown,
): string {
  const overview = schema as LiveSchemaOverview;
  const relations = overview?.relations ?? [];
  const mismatch = (): never => {
    throw new RelationDeltaConflictError("relation schema changed since preview");
  };
  if (!overview?.collections) mismatch();
  const collections = overview.collections as Record<string, LiveSchemaCollection>;
  const source = collections[relation.sourceCollection];
  if (
    source?.primary !== relation.sourcePrimaryKey
    || !liveFieldExists(source, relation.sourcePrimaryKey)
    || (relation.sourceDateUpdatedField != null
      && !liveFieldExists(source, relation.sourceDateUpdatedField))
  ) mismatch();

  const proof: Record<string, unknown> = {
    version: 1,
    kind: relation.kind,
    source: {
      collection: relation.sourceCollection,
      primaryKey: relation.sourcePrimaryKey,
      dateUpdatedField: relation.sourceDateUpdatedField ?? null,
    },
  };
  if (relation.kind === "o2m") {
    const target = collections[relation.relatedCollection!];
    if (
      target?.primary !== relation.relatedPrimaryKey
      || !liveFieldExists(target, relation.relatedPrimaryKey!)
      || !liveFieldExists(target, relation.manyField!)
      || !hasLiveRelation(
        relations,
        relation.relatedCollection!,
        relation.manyField!,
        relation.sourceCollection,
      )
    ) mismatch();
    proof.target = {
      collection: relation.relatedCollection,
      primaryKey: relation.relatedPrimaryKey,
      manyField: relation.manyField,
    };
  } else {
    const junction = relation.junction!;
    const junctionCollection = collections[junction.collection];
    const requiredJunctionFields = [
      junction.sourceField,
      junction.targetField,
      ...junction.contextFields,
      ...(junction.collectionField ? [junction.collectionField] : []),
    ];
    if (
      !junctionCollection
      || requiredJunctionFields.some((field) => !liveFieldExists(junctionCollection, field))
      || !hasLiveRelation(
        relations,
        junction.collection,
        junction.sourceField,
        relation.sourceCollection,
      )
    ) mismatch();
    if (relation.kind === "m2m") {
      const target = collections[relation.relatedCollection!];
      if (
        target?.primary !== relation.relatedPrimaryKey
        || !liveFieldExists(target, relation.relatedPrimaryKey!)
        || !hasLiveRelation(
          relations,
          junction.collection,
          junction.targetField,
          relation.relatedCollection!,
        )
      ) mismatch();
    } else {
      for (const collection of relation.allowedCollections ?? []) {
        const target = collections[collection];
        if (
          target?.primary !== relation.targetPrimaryKeys?.[collection]
          || !liveFieldExists(target, relation.targetPrimaryKeys![collection]!)
        ) mismatch();
      }
      const m2aRelation = relations.find((candidate) =>
        candidate.collection === junction.collection
        && candidate.field === junction.targetField
        && candidate.related_collection === null
      );
      const liveAllowed = [...(m2aRelation?.meta?.one_allowed_collections ?? [])].sort();
      const requestedAllowed = [...(relation.allowedCollections ?? [])].sort();
      if (
        !m2aRelation
        || m2aRelation.meta?.one_collection_field !== junction.collectionField
        || JSON.stringify(liveAllowed) !== JSON.stringify(requestedAllowed)
      ) mismatch();
    }
    proof.target = relation.kind === "m2m"
      ? { collection: relation.relatedCollection, primaryKey: relation.relatedPrimaryKey }
      : {
          collections: [...(relation.allowedCollections ?? [])].sort(),
          primaryKeys: Object.fromEntries(
            Object.entries(relation.targetPrimaryKeys ?? {}).sort(([left], [right]) => left.localeCompare(right)),
          ),
        };
    proof.junction = {
      collection: junction.collection,
      sourceField: junction.sourceField,
      targetField: junction.targetField,
      collectionField: junction.collectionField ?? null,
      contextFields: [...junction.contextFields].sort(),
    };
  }
  return createHash("sha256").update(JSON.stringify(proof), "utf8").digest("hex");
}

export function computeJunctionRevision(
  row: Readonly<Record<string, unknown>>,
  relation: RelationMutationPlan,
): string {
  const junction = relation.junction!;
  const values: Record<string, unknown> = {};
  for (const field of [...junction.contextFields].sort()) values[field] = row[field] ?? null;
  const payload = {
    id: String(row.id),
    source: String(row[junction.sourceField]),
    target: String(row[junction.targetField]),
    collection: junction.collectionField ? String(row[junction.collectionField]) : null,
    values,
  };
  return createHash("sha256").update(JSON.stringify(payload), "utf8").digest("hex");
}

async function checkedJunctionRow(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  junctionItems: any,
  relation: RelationMutationPlan,
  junctionId: string | number,
  expectedRevision: string,
  expectedTarget?: { collection: string; itemId: string | number },
): Promise<Record<string, unknown>> {
  const junction = relation.junction!;
  const fields = [
    "id",
    junction.sourceField,
    junction.targetField,
    ...junction.contextFields,
    ...(junction.collectionField ? [junction.collectionField] : []),
  ];
  const current = await junctionItems.readOne(junctionId, { fields }) as Record<string, unknown>;
  const belongsToSource = String(current[junction.sourceField]) === String(relation.sourceItemId);
  const matchesTarget = expectedTarget === undefined || (
    String(current[junction.targetField]) === String(expectedTarget.itemId)
    && (!junction.collectionField || String(current[junction.collectionField]) === expectedTarget.collection)
  );
  if (!belongsToSource || !matchesTarget || computeJunctionRevision(current, relation) !== expectedRevision) {
    throw new RelationDeltaConflictError();
  }
  return current;
}

async function lockRow(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  trx: any,
  collection: string,
  primaryKey: string,
  value: string | number,
): Promise<void> {
  const clientName = String(trx.client?.config?.client ?? "").toLowerCase();
  if (clientName.includes("sqlite")) {
    const changed = await trx(collection).where(primaryKey, value).update({ [primaryKey]: value });
    if (changed !== 1) throw new RelationDeltaConflictError();
    return;
  }
  const current = await trx(collection).where(primaryKey, value).forUpdate().first(primaryKey);
  if (!current) throw new RelationDeltaConflictError();
}

export async function applyRelationDeltaInTransaction(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ItemsService: any,
  schema: unknown,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  database: any,
  accountability: Accountability,
  request: RelationDeltaRequest,
): Promise<RelationDeltaResult> {
  const relation = request.relation;
  const liveSchemaProof = computeRelationSchemaProof(relation, schema);
  if (request.schemaProof !== liveSchemaProof) {
    throw new RelationDeltaConflictError("relation schema changed since preview");
  }
  const result: RelationDeltaResult = {
    requestId: request.idempotencyKey,
    outcome: "committed",
    createdJunctionIds: [],
    updatedJunctionIds: [],
    deletedJunctionIds: [],
    linkedItemIds: [],
    unlinkedItemIds: [],
  };
  await database.transaction(async (trx: unknown) => {
    await lockRow(trx, relation.sourceCollection, relation.sourcePrimaryKey, relation.sourceItemId);
    const source = new ItemsService(relation.sourceCollection, { schema, knex: trx, accountability });
    if (relation.sourceDateUpdatedField && relation.expectedDateUpdated) {
      const current = await source.readOne(relation.sourceItemId, { fields: [relation.sourcePrimaryKey, relation.sourceDateUpdatedField] });
      if (String(current[relation.sourceDateUpdatedField]) !== relation.expectedDateUpdated) {
        throw new RelationDeltaConflictError("source item changed");
      }
    } else {
      // Permission-check the source row even when no optimistic timestamp is available.
      await source.readOne(relation.sourceItemId, { fields: [relation.sourcePrimaryKey] });
    }

    if (relation.kind === "o2m") {
      const targets = new ItemsService(relation.relatedCollection, { schema, knex: trx, accountability });
      for (const add of relation.adds) {
        await targets.updateOne(add.itemId, { [relation.manyField!]: relation.sourceItemId });
        result.linkedItemIds.push(stringify(add.itemId));
      }
      for (const remove of relation.removes) {
        await lockRow(trx, relation.relatedCollection!, relation.relatedPrimaryKey!, remove.itemId);
        const current = await targets.readOne(remove.itemId, {
          fields: [relation.relatedPrimaryKey!, relation.manyField!],
        }) as Record<string, unknown>;
        if (String(current[relation.manyField!]) !== String(relation.sourceItemId)) {
          throw new RelationDeltaConflictError();
        }
        await targets.updateOne(remove.itemId, { [relation.manyField!]: null });
        result.unlinkedItemIds.push(stringify(remove.itemId));
      }
      return;
    }

    const junction = relation.junction!;
    const junctionItems = new ItemsService(junction.collection, { schema, knex: trx, accountability });
    for (const add of relation.adds) {
      const values: Record<string, unknown> = {
        [junction.sourceField]: relation.sourceItemId,
        [junction.targetField]: add.itemId,
        ...(add.junctionValues ?? {}),
      };
      if (relation.kind === "m2a") values[junction.collectionField!] = add.collection;
      const created = await junctionItems.createOne(values);
      result.createdJunctionIds.push(stringify(created));
      result.linkedItemIds.push(stringify(add.itemId));
    }
    for (const update of relation.updates) {
      await lockRow(trx, junction.collection, "id", update.junctionId);
      await checkedJunctionRow(
        junctionItems,
        relation,
        update.junctionId,
        update.expectedRevision!,
      );
      await junctionItems.updateOne(update.junctionId, update.values);
      result.updatedJunctionIds.push(stringify(update.junctionId));
    }
    for (const remove of relation.removes) {
      await lockRow(trx, junction.collection, "id", remove.junctionId!);
      await checkedJunctionRow(
        junctionItems,
        relation,
        remove.junctionId!,
        remove.expectedRevision!,
        { collection: remove.collection, itemId: remove.itemId },
      );
      await junctionItems.deleteOne(remove.junctionId);
      result.deletedJunctionIds.push(stringify(remove.junctionId!));
      result.unlinkedItemIds.push(stringify(remove.itemId));
    }
  });
  return result;
}

export function mapRelationDeltaError(error: unknown): {
  status: number;
  body: unknown;
  cacheable: boolean;
} | null {
  if (!(error instanceof RelationDeltaConflictError)) return null;
  return {
    status: 409,
    body: {
      errors: [{
        message: "relation changed since preview",
        extensions: { code: "EDIT_CONFLICT" },
      }],
    },
    cacheable: true,
  };
}
