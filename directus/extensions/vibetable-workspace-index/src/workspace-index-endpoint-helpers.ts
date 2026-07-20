import { createHash } from "node:crypto";

export type ItemRow = Record<string, unknown>;

export interface ItemsServiceLike {
  createOne(data: Record<string, unknown>, opts?: unknown): Promise<string>;
  readByQuery(query: unknown): Promise<ItemRow[]>;
  updateOne(key: string, data: Record<string, unknown>, opts?: unknown): Promise<string>;
  deleteOne(key: string): Promise<string>;
}

export type ItemsServiceConstructor = new (
  collection: string,
  options: Record<string, unknown>
) => ItemsServiceLike;

export interface CollectionsServiceLike {
  readByQuery(query: unknown): Promise<ItemRow[]>;
}

export type CollectionsServiceConstructor = new (
  options: Record<string, unknown>
) => CollectionsServiceLike;

export interface TransactionLike {
  transaction<T>(callback: (trx: TransactionLike) => Promise<T>): Promise<T>;
}

export class IdentityConflictError extends Error {}

export function serviceOptions(
  schema: unknown,
  knex: unknown,
  accountability: unknown
): Record<string, unknown> {
  return { schema, knex, accountability };
}

export function requestAccountability(req: unknown): unknown {
  return (req as { accountability?: unknown }).accountability;
}

export async function readById(
  service: ItemsServiceLike,
  id: string
): Promise<ItemRow | null> {
  const rows = await service.readByQuery({
    filter: { id: { _eq: id } },
    limit: 1,
  });
  return rows[0] ?? null;
}

/**
 * Create a primary-keyed row while tolerating a concurrent identical creator.
 * The insert runs in a savepoint so PostgreSQL can recover from a unique-key
 * violation and the root transaction can reread the concurrent winner.
 */
export async function ensurePrimaryKeyedRow(
  ItemsService: ItemsServiceConstructor,
  collection: string,
  options: Record<string, unknown>,
  rootTransaction: TransactionLike,
  id: string,
  data: Record<string, unknown>,
  validate: (row: ItemRow) => void
): Promise<{ row: ItemRow; created: boolean }> {
  const service = new ItemsService(collection, options);
  let row = await readById(service, id);
  if (row) {
    validate(row);
    return { row, created: false };
  }

  try {
    await rootTransaction.transaction(async (savepoint) => {
      const savepointService = new ItemsService(collection, {
        ...options,
        knex: savepoint,
      });
      await savepointService.createOne(data);
    });
  } catch (error) {
    row = await readById(service, id);
    if (!row) throw error;
    validate(row);
    return { row, created: false };
  }

  row = await readById(service, id);
  if (!row) throw new Error(`${collection} ${id} was not readable after create`);
  validate(row);
  return { row, created: true };
}

export function requireSame(
  row: ItemRow,
  expected: Readonly<Record<string, string>>,
  label: string
): void {
  for (const [field, value] of Object.entries(expected)) {
    if (String(row[field] ?? "") !== value) {
      throw new IdentityConflictError(`${label} identity conflict`);
    }
  }
}

export function stableLinkId(
  documentId: string,
  itemCollection: string,
  itemId: string
): string {
  const bytes = createHash("sha256")
    .update(`${documentId}\0${itemCollection}\0${itemId}`, "utf8")
    .digest();
  bytes[6] = (bytes[6]! & 0x0f) | 0x50;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const hex = bytes.subarray(0, 16).toString("hex");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`;
}

export function validateRegistrationHeads(input: {
  schemeHead: string;
  documentHead: string;
  documentHash: string;
  incomingRevisionId: string;
  incomingHash: string;
  revisionCreated: boolean;
}): void {
  const {
    schemeHead,
    documentHead,
    documentHash,
    incomingRevisionId,
    incomingHash,
    revisionCreated,
  } = input;
  if (schemeHead && documentHead && schemeHead !== documentHead) {
    throw new IdentityConflictError("document and scheme heads disagree");
  }
  const establishedHead = schemeHead || documentHead;
  if (revisionCreated && establishedHead && establishedHead !== incomingRevisionId) {
    throw new IdentityConflictError("initial revision identity conflict");
  }
  if (
    !revisionCreated
    && establishedHead
    && establishedHead !== incomingRevisionId
    && (!schemeHead || !documentHead)
  ) {
    throw new IdentityConflictError("document and scheme heads are incomplete");
  }
  if (
    establishedHead === incomingRevisionId
    && documentHash
    && documentHash !== incomingHash
  ) {
    throw new IdentityConflictError("document head hash identity conflict");
  }
}
