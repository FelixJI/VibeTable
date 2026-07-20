/**
 * Pure helper functions for the vibetable-workspace-index endpoint.
 *
 * These functions are pure (no I/O) so they can be unit-tested with
 * `node --test` without a Directus runtime. The endpoint delegates to
 * them after validating the request shape.
 */

export interface PublishRevisionRequest {
  readonly documentId: string;
  readonly schemeId: string;
  readonly revisionId: string;
  readonly parentRevisionId: string | null;
  readonly sequence: number;
  readonly versionLabel: string;
  readonly kind: "snapshot" | "formal" | "restore";
  readonly hash: string;
  readonly size: number;
  readonly mimeType: string;
  readonly createdBy: string | null;
  readonly deviceId: string | null;
  readonly comment: string | null;
}

export interface PublishResult {
  readonly status: "created" | "already-exists";
  readonly revisionId: string;
}

export interface ReconcileHeadRequest {
  readonly documentId: string;
  readonly schemeId: string;
  readonly expectedHeadRevisionId: string;
  readonly newHeadRevisionId: string;
}

export interface ReconcileHeadResult {
  readonly status: "updated" | "conflict";
  readonly actualHeadRevisionId: string;
  readonly expectedHeadRevisionId: string;
}

export interface LinkDocumentRequest {
  readonly documentId: string;
  readonly itemCollection: string;
  readonly itemId: string | number;
  readonly linkType: "primary" | "reference" | "attachment";
}

export interface LinkResult {
  readonly status: "created" | "already-exists";
  readonly linkId: string;
}

export interface RegisterDocumentRequest {
  readonly workspaceId: string;
  readonly workspaceName: string;
  readonly documentId: string;
  readonly fileName: string;
  readonly mimeType: string;
  readonly schemeId: string;
  readonly revisionId: string;
  readonly hash: string;
  readonly size: number;
  readonly itemCollection?: string | null;
  readonly itemId?: string | number | null;
  readonly linkType?: "primary" | "reference" | "attachment";
}

/**
 * Workspace-index collections. These hold document metadata and must
 * never themselves be link targets (an item_collection must point at a
 * real business collection, not back into the index).
 */
export const WORKSPACE_INDEX_COLLECTIONS = new Set([
  "vibetable_workspaces",
  "vibetable_workspace_folders",
  "vibetable_documents",
  "vibetable_document_schemes",
  "vibetable_document_revisions",
  "vibetable_document_links",
]);

/**
 * Validate a publish-revision request. Returns an error string or null.
 */
export function validatePublishRequest(req: unknown): string | null {
  if (typeof req !== "object" || req === null) return "request must be an object";
  const r = req as Record<string, unknown>;
  if (typeof r.documentId !== "string" || !r.documentId) return "documentId is required";
  if (typeof r.schemeId !== "string" || !r.schemeId) return "schemeId is required";
  if (typeof r.revisionId !== "string" || !r.revisionId) return "revisionId is required";
  if (typeof r.sequence !== "number" || r.sequence < 1) return "sequence must be >= 1";
  if (typeof r.hash !== "string" || r.hash.length < 8) return "hash is too short";
  if (!["snapshot", "formal", "restore"].includes(r.kind as string)) return "invalid kind";
  return null;
}

/**
 * Compare two revision payloads for idempotency. Returns true if they
 * represent the same content (same revisionId + same hash).
 */
export function revisionsMatch(
  existing: Record<string, unknown>,
  incoming: PublishRevisionRequest
): boolean {
  return (
    String(existing.id ?? existing.revisionId ?? "") === incoming.revisionId &&
    existing.hash === incoming.hash
  );
}

/**
 * Validate a reconcile-head request. Returns an error string or null.
 */
export function validateReconcileHeadRequest(req: unknown): string | null {
  if (typeof req !== "object" || req === null) return "request must be an object";
  const r = req as Record<string, unknown>;
  if (typeof r.documentId !== "string" || !r.documentId) return "documentId is required";
  if (typeof r.schemeId !== "string" || !r.schemeId) return "schemeId is required";
  if (typeof r.expectedHeadRevisionId !== "string") return "expectedHeadRevisionId is required";
  if (typeof r.newHeadRevisionId !== "string" || !r.newHeadRevisionId) return "newHeadRevisionId is required";
  return null;
}

/**
 * Determine the reconcile-head result based on current vs expected head.
 * If current matches expected → updated. Otherwise → conflict (preserve both).
 */
export function computeReconcileResult(
  currentHead: string,
  expected: string,
  newHead: string
): ReconcileHeadResult {
  if (currentHead === expected) {
    return { status: "updated", actualHeadRevisionId: newHead, expectedHeadRevisionId: expected };
  }
  // Conflict: current head does not match expected. Do NOT overwrite.
  return { status: "conflict", actualHeadRevisionId: currentHead, expectedHeadRevisionId: expected };
}

/**
 * Validate a link-document request. Returns an error string or null.
 *
 * `allowedCollections` is the set of business collections installed in
 * this Directus project (the live capability manifest). Any collection in
 * that set may link documents; the workspace-index collections themselves
 * and unknown collections are rejected to prevent arbitrary data injection.
 */
export function validateLinkRequest(
  req: unknown,
  allowedCollections: ReadonlySet<string> = new Set()
): string | null {
  if (typeof req !== "object" || req === null) return "request must be an object";
  const r = req as Record<string, unknown>;
  if (typeof r.documentId !== "string" || !r.documentId) return "documentId is required";
  if (typeof r.itemCollection !== "string" || !r.itemCollection) return "itemCollection is required";
  const itemCollection = r.itemCollection as string;
  if (WORKSPACE_INDEX_COLLECTIONS.has(itemCollection))
    return `itemCollection ${itemCollection} is a workspace-index collection and cannot be a link target`;
  if (!allowedCollections.has(itemCollection))
    return `itemCollection ${itemCollection} is not a declared collection`;
  const validItemId =
    (typeof r.itemId === "string" && r.itemId.length > 0 && r.itemId.length <= 128) ||
    (typeof r.itemId === "number" && Number.isSafeInteger(r.itemId));
  if (!validItemId) return "itemId must be a non-empty string or safe integer";
  if (r.linkType !== undefined && !["primary", "reference", "attachment"].includes(r.linkType as string))
    return "invalid linkType";
  return null;
}

/** Validate metadata for a native-host document import. No local path is accepted. */
export function validateRegisterDocumentRequest(
  req: unknown,
  allowedCollections: ReadonlySet<string> = new Set()
): string | null {
  if (typeof req !== "object" || req === null) return "request must be an object";
  const r = req as Record<string, unknown>;
  for (const key of ["workspaceId", "documentId", "schemeId", "revisionId"] as const) {
    if (typeof r[key] !== "string" || !r[key]) return `${key} is required`;
  }
  if (typeof r.workspaceName !== "string" || !r.workspaceName.trim())
    return "workspaceName is required";
  if (typeof r.fileName !== "string" || !r.fileName || /[\\/\0-\x1f]/.test(r.fileName))
    return "fileName must be a single safe path component";
  if (r.fileName === "." || r.fileName === "..") return "fileName is invalid";
  if (typeof r.mimeType !== "string" || r.mimeType.length > 128) return "invalid mimeType";
  if (typeof r.hash !== "string" || !/^[a-fA-F0-9]{64}$/.test(r.hash))
    return "hash must be a SHA-256 hex digest";
  if (typeof r.size !== "number" || !Number.isSafeInteger(r.size) || r.size < 0)
    return "size must be a non-negative integer";

  const hasCollection = typeof r.itemCollection === "string" && r.itemCollection.length > 0;
  const hasItem =
    (typeof r.itemId === "string" && r.itemId.length > 0 && r.itemId.length <= 128) ||
    (typeof r.itemId === "number" && Number.isSafeInteger(r.itemId));
  if (hasCollection !== hasItem) return "itemCollection and itemId must be supplied together";
  if (hasCollection) {
    const linkError = validateLinkRequest(
      {
        documentId: r.documentId,
        itemCollection: r.itemCollection,
        itemId: r.itemId,
        linkType: r.linkType ?? "attachment",
      },
      allowedCollections
    );
    if (linkError) return linkError;
  }
  return null;
}

/**
 * Maximum batch size for publish requests.
 */
export const MAX_PUBLISH_BATCH = 100;
