import type { Accountability } from "@directus/types";
import { createInspector } from "@directus/schema";

export const RELATION_IMPORT_CONTRACT = "vibetable-relation-import.v1" as const;

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const MAX_ROWS = 1000;
const MAX_RELATIONS_PER_ROW = 100;
const MAX_FIELDS_PER_COLLECTION = 512;

type Identity = string | number;

export interface RelationImportSchemaProof {
  collections: readonly string[];
  fields: Readonly<Record<string, readonly string[]>>;
  uniqueFields: Readonly<Record<string, readonly string[]>>;
  relationIds: readonly string[];
}

export interface RelationImportResolution {
  targetField: string;
  relationId: string;
  targetCollection: string;
  targetPrimaryKey: string;
  matchField: string;
  sourceValue: unknown;
  state: "matched" | "create";
  matchedPrimaryKey?: Identity;
  createValues?: Readonly<Record<string, unknown>>;
}

export interface RelationImportRow {
  values: Readonly<Record<string, unknown>>;
  relations: readonly RelationImportResolution[];
}

export interface RelationImportRequest {
  contract: typeof RELATION_IMPORT_CONTRACT;
  idempotencyKey: string;
  sourceCollection: string;
  sourcePrimaryKey: string;
  mode: "create" | "upsert";
  upsertKey?: string;
  schemaProof: RelationImportSchemaProof;
  rows: readonly RelationImportRow[];
}

export interface RelationImportResolutionResult {
  rowIndex: number;
  relationId: string;
  state: "matched" | "created";
  targetPrimaryKey: string;
}

export interface RelationImportResult {
  requestId: string;
  outcome: "committed";
  createdSourceRowKeys: string[];
  updatedSourceRowKeys: string[];
  createdTargetRowKeys: Array<{
    relationId: string;
    collection: string;
    primaryKey: string;
  }>;
  resolvedRelations: RelationImportResolutionResult[];
}

export type ValidationResult = { ok: true } | { ok: false; error: string };

export class RelationImportError extends Error {
  readonly code:
    | "SCHEMA_PROOF_MISMATCH"
    | "RELATION_MATCH_AMBIGUOUS"
    | "RELATION_MATCH_CHANGED"
    | "SOURCE_MATCH_AMBIGUOUS";

  constructor(
    code:
      | "SCHEMA_PROOF_MISMATCH"
      | "RELATION_MATCH_AMBIGUOUS"
      | "RELATION_MATCH_CHANGED"
      | "SOURCE_MATCH_AMBIGUOUS",
    message: string,
  ) {
    super(message);
    this.name = "RelationImportError";
    this.code = code;
  }
}

function invalid(error: string): ValidationResult {
  return { ok: false, error };
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function validIdentifier(value: unknown): value is string {
  return typeof value === "string" && IDENTIFIER.test(value);
}

function validIdentity(value: unknown): value is Identity {
  return (
    (typeof value === "string" && value.length > 0 && value.length <= 256) ||
    (typeof value === "number" && Number.isFinite(value))
  );
}

function validMatchValue(value: unknown): boolean {
  return (
    (typeof value === "string" && value.length > 0 && value.length <= 4096) ||
    (typeof value === "number" && Number.isFinite(value)) ||
    typeof value === "boolean"
  );
}

function hasOnlyKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  const allowed = new Set(keys);
  return Object.keys(value).every((key) => allowed.has(key));
}

/** Validate only the deliberately narrow, pre-compiled import plan wire format. */
export function validateRelationImport(
  value: unknown,
  headerKey?: string,
): ValidationResult {
  if (!isObject(value)) return invalid("request body is required");
  if (!hasOnlyKeys(value, [
    "contract",
    "idempotencyKey",
    "sourceCollection",
    "sourcePrimaryKey",
    "mode",
    "upsertKey",
    "schemaProof",
    "rows",
  ])) return invalid("request contains unsupported properties");
  const request = value as unknown as Partial<RelationImportRequest>;
  if (request.contract !== RELATION_IMPORT_CONTRACT) return invalid("unsupported relation import contract");
  if (typeof request.idempotencyKey !== "string" || !STABLE_ID.test(request.idempotencyKey)) return invalid("idempotencyKey is invalid");
  if (headerKey !== undefined && headerKey !== request.idempotencyKey) return invalid("Idempotency-Key does not match the request");
  if (!validIdentifier(request.sourceCollection) || !validIdentifier(request.sourcePrimaryKey)) return invalid("source identifiers are invalid");
  if (request.mode !== "create" && request.mode !== "upsert") return invalid("mode must be create or upsert");
  if (request.mode === "upsert" && !validIdentifier(request.upsertKey)) return invalid("upsertKey is required for upsert mode");
  if (request.mode === "create" && request.upsertKey !== undefined) return invalid("upsertKey is only valid in upsert mode");

  const proof = request.schemaProof;
  if (!isObject(proof) || !hasOnlyKeys(proof, ["collections", "fields", "uniqueFields", "relationIds"])) return invalid("schemaProof is invalid");
  if (!Array.isArray(proof.collections) || proof.collections.length === 0 || proof.collections.some((item) => !validIdentifier(item))) return invalid("schemaProof collections are invalid");
  if (new Set(proof.collections).size !== proof.collections.length) return invalid("schemaProof collections contain duplicates");
  if (!isObject(proof.fields)) return invalid("schemaProof fields are invalid");
  if (!isObject(proof.uniqueFields)) return invalid("schemaProof uniqueFields are invalid");
  if (Object.keys(proof.fields).some((collection) => !proof.collections.includes(collection))) return invalid("schemaProof fields reference an undeclared collection");
  if (Object.keys(proof.uniqueFields).some((collection) => !proof.collections.includes(collection))) return invalid("schemaProof uniqueFields reference an undeclared collection");
  for (const collection of proof.collections) {
    const fields = proof.fields[collection];
    if (!Array.isArray(fields) || fields.length === 0 || fields.length > MAX_FIELDS_PER_COLLECTION || fields.some((field) => !validIdentifier(field))) return invalid("schemaProof field allow-list is invalid");
    if (new Set(fields).size !== fields.length) return invalid("schemaProof field allow-list contains duplicates");
    const uniqueFields = proof.uniqueFields[collection];
    if (!Array.isArray(uniqueFields) || uniqueFields.some((field) => !fields.includes(field))) return invalid("schemaProof uniqueFields are invalid");
    if (new Set(uniqueFields).size !== uniqueFields.length) return invalid("schemaProof uniqueFields contain duplicates");
  }
  if (!Array.isArray(proof.relationIds) || proof.relationIds.some((id) => typeof id !== "string" || !STABLE_ID.test(id))) return invalid("schemaProof relationIds are invalid");
  if (new Set(proof.relationIds).size !== proof.relationIds.length) return invalid("schemaProof relationIds contain duplicates");
  const collectionAllowList = new Set(proof.collections);
  const relationAllowList = new Set(proof.relationIds);
  const sourceFields = new Set(proof.fields[request.sourceCollection] ?? []);
  if (!collectionAllowList.has(request.sourceCollection) || !sourceFields.has(request.sourcePrimaryKey)) return invalid("source identifiers are outside schemaProof");
  if (request.upsertKey && !sourceFields.has(request.upsertKey)) return invalid("upsertKey is outside schemaProof");
  if (request.upsertKey && !(proof.uniqueFields[request.sourceCollection] ?? []).includes(request.upsertKey)) return invalid("upsertKey is not proven unique");

  if (!Array.isArray(request.rows) || request.rows.length === 0 || request.rows.length > MAX_ROWS) return invalid(`rows must contain between 1 and ${MAX_ROWS} entries`);
  for (const row of request.rows) {
    if (!isObject(row) || !hasOnlyKeys(row, ["values", "relations"]) || !isObject(row.values) || !Array.isArray(row.relations) || row.relations.length > MAX_RELATIONS_PER_ROW) return invalid("row is invalid");
    if (Object.keys(row.values).some((field) => !sourceFields.has(field))) return invalid("row contains a source field outside schemaProof");
    if (request.mode === "upsert" && (!Object.hasOwn(row.values, request.upsertKey!) || !validMatchValue(row.values[request.upsertKey!]))) return invalid("upsert row has an invalid upsertKey value");
    const targetFields = new Set<string>();
    for (const resolution of row.relations) {
      if (!isObject(resolution) || !hasOnlyKeys(resolution, [
        "targetField",
        "relationId",
        "targetCollection",
        "targetPrimaryKey",
        "matchField",
        "sourceValue",
        "state",
        "matchedPrimaryKey",
        "createValues",
      ])) return invalid("relation resolution is invalid");
      if (!validIdentifier(resolution.targetField) || !validIdentifier(resolution.targetCollection) || !validIdentifier(resolution.targetPrimaryKey) || !validIdentifier(resolution.matchField)) return invalid("relation resolution identifiers are invalid");
      if (typeof resolution.relationId !== "string" || !STABLE_ID.test(resolution.relationId)) return invalid("relationId is invalid");
      if (!sourceFields.has(resolution.targetField) || !collectionAllowList.has(resolution.targetCollection) || !relationAllowList.has(resolution.relationId)) return invalid("relation resolution is outside schemaProof");
      const allowedTargetFields = new Set(proof.fields[resolution.targetCollection] ?? []);
      if (!allowedTargetFields.has(resolution.targetPrimaryKey) || !allowedTargetFields.has(resolution.matchField)) return invalid("relation target fields are outside schemaProof");
      if (!(proof.uniqueFields[resolution.targetCollection] ?? []).includes(resolution.matchField)) return invalid("relation matchField is not proven unique");
      if (!validMatchValue(resolution.sourceValue)) return invalid("relation sourceValue is invalid");
      if (targetFields.has(resolution.targetField)) return invalid("row contains duplicate relation target fields");
      if (Object.hasOwn(row.values, resolution.targetField)) return invalid("relation targetField cannot also be supplied as a source value");
      targetFields.add(resolution.targetField);
      if (resolution.state !== "matched" && resolution.state !== "create") return invalid("relation resolution state is invalid");
      if (resolution.state === "matched" && !validIdentity(resolution.matchedPrimaryKey)) return invalid("matched relation requires matchedPrimaryKey");
      if (resolution.state === "create" && resolution.matchedPrimaryKey !== undefined) return invalid("create relation cannot declare matchedPrimaryKey");
      if (resolution.createValues !== undefined) {
        if (resolution.state !== "create" || !isObject(resolution.createValues)) return invalid("createValues are only valid for create relations");
        if (Object.keys(resolution.createValues).some((field) => !allowedTargetFields.has(field))) return invalid("createValues contain a target field outside schemaProof");
      }
    }
  }
  return { ok: true };
}

type SchemaCollection = {
  primary?: string;
  fields?: Record<string, {
    is_unique?: boolean;
    isUnique?: boolean;
    schema?: { is_unique?: boolean };
  } | unknown> | readonly string[];
};
type SchemaRelation = {
  collection?: string;
  field?: string;
  related_collection?: string | null;
};

function schemaHasField(collection: SchemaCollection, field: string): boolean {
  if (Array.isArray(collection.fields)) return collection.fields.includes(field);
  return Boolean(collection.fields && field in collection.fields);
}

function schemaDeclaresUnique(collection: SchemaCollection, field: string): boolean {
  if (collection.primary === field) return true;
  if (Array.isArray(collection.fields)) return false;
  const fields = collection.fields as Record<string, unknown> | undefined;
  const candidate = fields?.[field] as {
    is_unique?: boolean;
    isUnique?: boolean;
    schema?: { is_unique?: boolean };
  } | undefined;
  return candidate?.is_unique === true
    || candidate?.isUnique === true
    || candidate?.schema?.is_unique === true;
}

async function isLiveUnique(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  database: any,
  collectionName: string,
  collection: SchemaCollection,
  field: string,
): Promise<boolean> {
  if (schemaDeclaresUnique(collection, field)) return true;
  try {
    const column = await createInspector(database).columnInfo(collectionName, field);
    return column.is_primary_key || column.is_unique;
  } catch {
    return false;
  }
}

async function assertLiveSchema(
  request: RelationImportRequest,
  schema: unknown,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  database: any,
): Promise<void> {
  const overview = schema as {
    collections?: Record<string, SchemaCollection>;
    relations?: readonly SchemaRelation[];
  };
  const collections = overview?.collections;
  if (!collections) throw new RelationImportError("SCHEMA_PROOF_MISMATCH", "schema proof no longer matches the live schema");
  for (const collectionName of request.schemaProof.collections) {
    const collection = collections[collectionName];
    const fields = request.schemaProof.fields[collectionName] ?? [];
    if (!collection || fields.some((field) => !schemaHasField(collection, field))) {
      throw new RelationImportError("SCHEMA_PROOF_MISMATCH", "schema proof no longer matches the live schema");
    }
    for (const uniqueField of request.schemaProof.uniqueFields[collectionName] ?? []) {
      if (!schemaHasField(collection, uniqueField)
        || !await isLiveUnique(database, collectionName, collection, uniqueField)) {
        throw new RelationImportError("SCHEMA_PROOF_MISMATCH", "schema proof contains a field that is not live-unique");
      }
    }
  }
  if (collections[request.sourceCollection]?.primary !== request.sourcePrimaryKey) {
    throw new RelationImportError("SCHEMA_PROOF_MISMATCH", "schema proof no longer matches the live schema");
  }
  for (const row of request.rows) {
    for (const relation of row.relations) {
      if (collections[relation.targetCollection]?.primary !== relation.targetPrimaryKey) {
        throw new RelationImportError("SCHEMA_PROOF_MISMATCH", "schema proof no longer matches the live schema");
      }
      if (!(overview.relations ?? []).some((candidate) =>
        candidate.collection === request.sourceCollection &&
        candidate.field === relation.targetField &&
        candidate.related_collection === relation.targetCollection
      )) {
        throw new RelationImportError("SCHEMA_PROOF_MISMATCH", "schema proof no longer matches the live schema");
      }
    }
  }
}

async function exactMatch(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  items: any,
  primaryKey: string,
  matchField: string,
  value: unknown,
): Promise<Record<string, unknown>[]> {
  return await items.readByQuery({
    fields: [primaryKey],
    filter: { [matchField]: { _eq: value } },
    limit: 2,
  }) as Record<string, unknown>[];
}

function isUniqueConstraintError(error: unknown): boolean {
  if (!isObject(error)) return false;
  const code = String(error.code ?? "").toUpperCase();
  const errno = Number(error.errno ?? Number.NaN);
  return code === "23505"
    || code === "SQLITE_CONSTRAINT"
    || code === "SQLITE_CONSTRAINT_UNIQUE"
    || code === "SQLITE_CONSTRAINT_PRIMARYKEY"
    || code === "ER_DUP_ENTRY"
    || code === "E11000"
    || errno === 1062
    || errno === 19
    || errno === 2601
    || errno === 2627;
}

/** Execute a Python-compiled import plan atomically under current accountability. */
export async function applyRelationImportInTransaction(
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ItemsService: any,
  schema: unknown,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  database: any,
  accountability: Accountability,
  request: RelationImportRequest,
): Promise<RelationImportResult> {
  await assertLiveSchema(request, schema, database);
  const result: RelationImportResult = {
    requestId: request.idempotencyKey,
    outcome: "committed",
    createdSourceRowKeys: [],
    updatedSourceRowKeys: [],
    createdTargetRowKeys: [],
    resolvedRelations: [],
  };

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  await database.transaction(async (trx: any) => {
    const sourceItems = new ItemsService(request.sourceCollection, { schema, knex: trx, accountability });
    for (const [rowIndex, row] of request.rows.entries()) {
      const values: Record<string, unknown> = { ...row.values };
      for (const relation of row.relations) {
        const targetItems = new ItemsService(relation.targetCollection, { schema, knex: trx, accountability });
        const matches = await exactMatch(targetItems, relation.targetPrimaryKey, relation.matchField, relation.sourceValue);
        if (matches.length > 1) {
          throw new RelationImportError("RELATION_MATCH_AMBIGUOUS", "relation exact match is ambiguous");
        }

        let targetKey: unknown;
        let resolutionState: "matched" | "created" = "matched";
        if (matches.length === 1) {
          targetKey = matches[0]![relation.targetPrimaryKey];
          if (relation.state === "matched" && String(targetKey) !== String(relation.matchedPrimaryKey)) {
            throw new RelationImportError("RELATION_MATCH_CHANGED", "relation exact match changed after preview");
          }
        } else if (relation.state === "matched") {
          throw new RelationImportError("RELATION_MATCH_CHANGED", "relation exact match changed after preview");
        } else {
          const createValues: Record<string, unknown> = {
            ...(relation.createValues ?? {}),
            [relation.matchField]: relation.sourceValue,
          };
          try {
            targetKey = await trx.transaction(async (savepoint: unknown) => {
              const savepointItems = new ItemsService(relation.targetCollection, {
                schema,
                knex: savepoint,
                accountability,
              });
              return await savepointItems.createOne(createValues);
            });
            resolutionState = "created";
            result.createdTargetRowKeys.push({
              relationId: relation.relationId,
              collection: relation.targetCollection,
              primaryKey: String(targetKey),
            });
          } catch (error) {
            if (!isUniqueConstraintError(error)) throw error;
            const racedMatches = await exactMatch(
              targetItems,
              relation.targetPrimaryKey,
              relation.matchField,
              relation.sourceValue,
            );
            if (racedMatches.length !== 1) {
              throw new RelationImportError(
                "RELATION_MATCH_CHANGED",
                "relation exact match changed during create-if-missing",
              );
            }
            targetKey = racedMatches[0]![relation.targetPrimaryKey];
            resolutionState = "matched";
          }
        }
        if (!validIdentity(targetKey)) throw new RelationImportError("RELATION_MATCH_CHANGED", "relation target identity is unavailable");
        values[relation.targetField] = targetKey;
        result.resolvedRelations.push({
          rowIndex,
          relationId: relation.relationId,
          state: resolutionState,
          targetPrimaryKey: String(targetKey),
        });
      }

      if (request.mode === "create") {
        const created = await sourceItems.createOne(values);
        result.createdSourceRowKeys.push(String(created));
        continue;
      }
      const matches = await exactMatch(sourceItems, request.sourcePrimaryKey, request.upsertKey!, row.values[request.upsertKey!]);
      if (matches.length > 1) throw new RelationImportError("SOURCE_MATCH_AMBIGUOUS", "source upsert match is ambiguous");
      if (matches.length === 0) {
        const created = await sourceItems.createOne(values);
        result.createdSourceRowKeys.push(String(created));
      } else {
        const key = matches[0]![request.sourcePrimaryKey];
        if (!validIdentity(key)) throw new RelationImportError("SOURCE_MATCH_AMBIGUOUS", "source upsert identity is unavailable");
        await sourceItems.updateOne(key, values);
        result.updatedSourceRowKeys.push(String(key));
      }
    }
  });
  return result;
}

export function mapRelationImportError(error: unknown): {
  status: number;
  body: unknown;
  cacheable: boolean;
} {
  if (error instanceof RelationImportError) {
    const status = error.code === "SCHEMA_PROOF_MISMATCH" ? 409 : 422;
    return {
      status,
      body: { errors: [{ message: error.message, extensions: { code: error.code } }] },
      cacheable: true,
    };
  }
  const code = isObject(error) && typeof error.code === "string" ? error.code : "";
  if (code === "FORBIDDEN" || code === "INVALID_CREDENTIALS") {
    return {
      status: 403,
      body: { errors: [{ message: "relation import is not permitted", extensions: { code: "RELATION_IMPORT_FORBIDDEN" } }] },
      cacheable: true,
    };
  }
  return {
    status: 500,
    body: { errors: [{ message: "relation import failed", extensions: { code: "RELATION_IMPORT_FAILED" } }] },
    cacheable: false,
  };
}
