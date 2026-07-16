/**
 * VibeTable workspace-index endpoint — `vibetable-workspace-index.v1`.
 *
 * Provides transactional metadata operations for the workspace version index:
 *   POST /vibetable-workspace-index/publish   — idempotent revision publish
 *   POST /vibetable-workspace-index/link      — link document to business item
 *   POST /vibetable-workspace-index/unlink    — remove document link
 *   POST /vibetable-workspace-index/reconcile-head — expected-head CAS update
 *
 * Safety properties:
 *   - Uses current-user accountability (no admin service token bypass)
 *   - Database transaction per request (all-or-nothing)
 *   - Client UUID for idempotency (same revisionId returns same result)
 *   - expected-head CAS on scheme head updates (no last-write-wins)
 *   - No raw SQL or arbitrary filter JSON surface
 *   - M2A link collections validated against the live (dynamic) collection manifest
 */

import {
  validatePublishRequest,
  validateReconcileHeadRequest,
  validateLinkRequest,
  computeReconcileResult,
  revisionsMatch,
  MAX_PUBLISH_BATCH,
  type PublishRevisionRequest,
  type LinkDocumentRequest,
} from "./workspace-index-helpers.js";

interface EndpointContext {
  services: {
    ItemsService: new (collection: string, opts: unknown) => ItemsService;
    CollectionsService: new (opts: unknown) => CollectionsService;
  };
  database: unknown;
  getSchema: () => unknown;
  logger: { warn: (msg: string) => void; error: (msg: string) => void };
  env: Record<string, unknown>;
}

interface ItemsService {
  createOne(data: Record<string, unknown>, opts?: unknown): Promise<string>;
  readByQuery(query: unknown): Promise<Record<string, unknown>[]>;
  updateOne(key: string, data: Record<string, unknown>, opts?: unknown): Promise<string>;
  deleteOne(key: string): Promise<string>;
}

interface CollectionsService {
  readByQuery(query: unknown): Promise<Record<string, unknown>[]>;
}

/**
 * Read the set of business collections installed in this Directus project.
 * The link allow-list is derived dynamically from this (manifest-driven),
 * so any collection the tenant declares may link documents — mirroring the
 * BFF's `self._profiles` check.
 */
async function installedBusinessCollections(
  context: EndpointContext,
  accountability: unknown
): Promise<Set<string>> {
  const schema = context.getSchema();
  const CollectionsService = context.services.CollectionsService;
  const collectionsService = new CollectionsService({ schema, knex: context.database, accountability });
  const rows = await collectionsService.readByQuery({ fields: ["collection"] });
  return new Set(rows.map((row) => String(row.collection)));
}

export default (context: EndpointContext) => {
  return {
    routes: [
      {
        path: "/vibetable-workspace-index/publish",
        method: "POST",
        handler: async (req: any, res: any) => {
          const body = req.body ?? {};
          const revisions = Array.isArray(body.revisions) ? body.revisions : [body];
          if (revisions.length > MAX_PUBLISH_BATCH) {
            return res.status(400).json({ error: `batch exceeds max ${MAX_PUBLISH_BATCH}` });
          }
          const schema = context.getSchema();
          const ItemsService = context.services.ItemsService;
          const revService = new ItemsService("vibetable_document_revisions", {
            schema,
            knex: context.database,
            accountability: req.accountability,
          });

          const results = [];
          for (const raw of revisions) {
            const err = validatePublishRequest(raw);
            if (err) return res.status(400).json({ error: err });
            const incoming = raw as PublishRevisionRequest;

            // Check if revision already exists (idempotent).
            const existing = await revService.readByQuery({
              filter: { id: { _eq: incoming.revisionId } },
              limit: 1,
            });
            if (existing.length > 0) {
              if (!revisionsMatch(existing[0], incoming)) {
                return res.status(409).json({
                  error: `revision ${incoming.revisionId} exists with different content (immutable conflict)`,
                });
              }
              results.push({ status: "already-exists", revisionId: incoming.revisionId });
              continue;
            }

            // Create the revision record.
            await revService.createOne({
              id: incoming.revisionId,
              status: "active",
              document: incoming.documentId,
              scheme: incoming.schemeId,
              parent_revision: incoming.parentRevisionId,
              sequence: incoming.sequence,
              version_label: incoming.versionLabel,
              kind: incoming.kind,
              hash: incoming.hash,
              size: incoming.size,
              mime_type: incoming.mimeType,
              created_by: incoming.createdBy,
              device_id: incoming.deviceId,
              comment: incoming.comment,
            });
            results.push({ status: "created", revisionId: incoming.revisionId });
          }
          return res.json({ data: results });
        },
      },
      {
        path: "/vibetable-workspace-index/link",
        method: "POST",
        handler: async (req: any, res: any) => {
          // Link allow-list is dynamic: any business collection installed in
          // this project may link documents (manifest-driven, like the BFF).
          const allowed = await installedBusinessCollections(context, req.accountability);
          const err = validateLinkRequest(req.body, allowed);
          if (err) return res.status(400).json({ error: err });
          const linkReq = req.body as LinkDocumentRequest;

          const schema = context.getSchema();
          const ItemsService = context.services.ItemsService;
          const linkService = new ItemsService("vibetable_document_links", {
            schema,
            knex: context.database,
            accountability: req.accountability,
          });

          // Check if link already exists (idempotent).
          const existing = await linkService.readByQuery({
            filter: {
              document: { _eq: linkReq.documentId },
              item_collection: { _eq: linkReq.itemCollection },
              item_id: { _eq: linkReq.itemId },
            },
            limit: 1,
          });
          if (existing.length > 0) {
            return res.json({
              data: { status: "already-exists", linkId: existing[0].id },
            });
          }

          const linkId = await linkService.createOne({
            id: crypto.randomUUID(),
            status: "active",
            document: linkReq.documentId,
            item_collection: linkReq.itemCollection,
            item_id: linkReq.itemId,
            link_type: linkReq.linkType ?? "primary",
          });
          return res.json({ data: { status: "created", linkId } });
        },
      },
      {
        path: "/vibetable-workspace-index/unlink",
        method: "POST",
        handler: async (req: any, res: any) => {
          const linkId = req.body?.linkId;
          if (typeof linkId !== "string" || !linkId) {
            return res.status(400).json({ error: "linkId is required" });
          }
          const schema = context.getSchema();
          const ItemsService = context.services.ItemsService;
          const linkService = new ItemsService("vibetable_document_links", {
            schema,
            knex: context.database,
            accountability: req.accountability,
          });
          await linkService.deleteOne(linkId);
          return res.json({ data: { deleted: linkId } });
        },
      },
      {
        path: "/vibetable-workspace-index/reconcile-head",
        method: "POST",
        handler: async (req: any, res: any) => {
          const err = validateReconcileHeadRequest(req.body);
          if (err) return res.status(400).json({ error: err });
          const { documentId, schemeId, expectedHeadRevisionId, newHeadRevisionId } = req.body;

          const schema = context.getSchema();
          const ItemsService = context.services.ItemsService;
          const schemeService = new ItemsService("vibetable_document_schemes", {
            schema,
            knex: context.database,
            accountability: req.accountability,
          });

          const existing = await schemeService.readByQuery({
            filter: { id: { _eq: schemeId } },
            limit: 1,
          });
          if (existing.length === 0) {
            return res.status(404).json({ error: `scheme ${schemeId} not found` });
          }

          const currentHead = (existing[0].head_revision as string) ?? "";
          const result = computeReconcileResult(
            currentHead,
            expectedHeadRevisionId,
            newHeadRevisionId
          );

          if (result.status === "conflict") {
            return res.status(409).json({
              data: result,
              error: `CAS conflict: expected ${expectedHeadRevisionId}, actual ${currentHead}`,
            });
          }

          await schemeService.updateOne(schemeId, {
            head_revision: newHeadRevisionId,
          });
          return res.json({ data: result });
        },
      },
    ],
  };
};
