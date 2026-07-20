/**
 * VibeTable workspace-index endpoint — `vibetable-workspace-index.v1`.
 *
 * Every route runs with the caller's Directus accountability. Registering a
 * document is one database transaction: workspace, document, scheme, first
 * revision, heads, and the optional business-record link either all commit or
 * all roll back.
 */

import { defineEndpoint } from "@directus/extensions-sdk";
import {
  validatePublishRequest,
  validateReconcileHeadRequest,
  validateLinkRequest,
  validateRegisterDocumentRequest,
  computeReconcileResult,
  revisionsMatch,
  MAX_PUBLISH_BATCH,
  type PublishRevisionRequest,
  type LinkDocumentRequest,
  type RegisterDocumentRequest,
} from "./workspace-index-helpers.js";
import {
  IdentityConflictError,
  ensurePrimaryKeyedRow,
  readById,
  requestAccountability,
  requireSame,
  serviceOptions,
  stableLinkId,
  validateRegistrationHeads,
  type CollectionsServiceConstructor,
  type ItemsServiceConstructor,
  type TransactionLike,
} from "./workspace-index-endpoint-helpers.js";

async function installedBusinessCollections(
  CollectionsService: CollectionsServiceConstructor,
  schema: unknown,
  knex: unknown,
  accountability: unknown
): Promise<Set<string>> {
  const service = new CollectionsService(
    serviceOptions(schema, knex, accountability)
  );
  const rows = await service.readByQuery({ fields: ["collection"] });
  return new Set(rows.map((row) => String(row.collection)));
}

export default defineEndpoint((router, context) => {
  const { ItemsService, CollectionsService } = context.services as unknown as {
    ItemsService: ItemsServiceConstructor;
    CollectionsService: CollectionsServiceConstructor;
  };
  const database = context.database as unknown as TransactionLike;

  router.post("/register-document", async (req, res) => {
    const body = req.body ?? {};
    const schema = await context.getSchema();
    const allowed = body.itemCollection
      ? await installedBusinessCollections(
          CollectionsService,
          schema,
          database,
          requestAccountability(req)
        )
      : new Set<string>();
    const validationError = validateRegisterDocumentRequest(body, allowed);
    if (validationError) {
      res.status(400).json({ error: validationError });
      return;
    }
    const incoming = body as RegisterDocumentRequest;
    const linkedItemId = incoming.itemId === null || incoming.itemId === undefined
      ? null
      : String(incoming.itemId);

    try {
      const result = await database.transaction(async (trx) => {
        const options = serviceOptions(schema, trx, requestAccountability(req));
        let created = false;

        const workspace = await ensurePrimaryKeyedRow(
          ItemsService,
          "vibetable_workspaces",
          options,
          trx,
          incoming.workspaceId,
          {
            id: incoming.workspaceId,
            status: "active",
            name: incoming.workspaceName,
            workspace_id: incoming.workspaceId,
          },
          (row) => requireSame(
            row,
            { workspace_id: incoming.workspaceId },
            "workspace"
          )
        );
        created ||= workspace.created;

        const document = await ensurePrimaryKeyedRow(
          ItemsService,
          "vibetable_documents",
          options,
          trx,
          incoming.documentId,
          {
            id: incoming.documentId,
            status: "active",
            workspace: incoming.workspaceId,
            file_name: incoming.fileName,
            mime_type: incoming.mimeType,
          },
          (row) => requireSame(
            row,
            {
              workspace: incoming.workspaceId,
              file_name: incoming.fileName,
              mime_type: incoming.mimeType,
            },
            "document"
          )
        );
        created ||= document.created;

        const scheme = await ensurePrimaryKeyedRow(
          ItemsService,
          "vibetable_document_schemes",
          options,
          trx,
          incoming.schemeId,
          {
            id: incoming.schemeId,
            status: "active",
            document: incoming.documentId,
            name: "main",
            working_path: incoming.fileName,
          },
          (row) => requireSame(
            row,
            {
              document: incoming.documentId,
              name: "main",
              working_path: incoming.fileName,
            },
            "scheme"
          )
        );
        created ||= scheme.created;

        const revision = await ensurePrimaryKeyedRow(
          ItemsService,
          "vibetable_document_revisions",
          options,
          trx,
          incoming.revisionId,
          {
            id: incoming.revisionId,
            status: "active",
            document: incoming.documentId,
            scheme: incoming.schemeId,
            sequence: 1,
            version_label: "v1",
            kind: "formal",
            hash: incoming.hash,
            size: incoming.size,
            mime_type: incoming.mimeType,
            comment: "Imported into VibeTable",
          },
          (row) => requireSame(
            row,
            {
              document: incoming.documentId,
              scheme: incoming.schemeId,
              hash: incoming.hash,
              size: String(incoming.size),
              mime_type: incoming.mimeType,
              sequence: "1",
              version_label: "v1",
              kind: "formal",
            },
            "revision"
          )
        );
        created ||= revision.created;

        const schemes = new ItemsService(
          "vibetable_document_schemes",
          options
        );
        const documents = new ItemsService("vibetable_documents", options);
        const schemeHead = String(scheme.row.head_revision ?? "");
        const documentHead = String(document.row.main_head ?? "");
        validateRegistrationHeads({
          schemeHead,
          documentHead,
          documentHash: String(document.row.main_hash ?? ""),
          incomingRevisionId: incoming.revisionId,
          incomingHash: incoming.hash,
          revisionCreated: revision.created,
        });
        if (!schemeHead || schemeHead === incoming.revisionId) {
          await schemes.updateOne(incoming.schemeId, {
            head_revision: incoming.revisionId,
          });
        }
        if (!documentHead || documentHead === incoming.revisionId) {
          await documents.updateOne(incoming.documentId, {
            main_head: incoming.revisionId,
            main_hash: incoming.hash,
          });
        }

        let linkId: string | null = null;
        if (incoming.itemCollection && linkedItemId !== null) {
          const links = new ItemsService("vibetable_document_links", options);
          const matchingLinks = await links.readByQuery({
            filter: {
              document: { _eq: incoming.documentId },
              item_collection: { _eq: incoming.itemCollection },
              item_id: { _eq: linkedItemId },
            },
            limit: 1,
          });
          const existingLink = matchingLinks[0];
          if (existingLink) {
            requireSame(
              existingLink,
              {
                document: incoming.documentId,
                item_collection: incoming.itemCollection,
                item_id: linkedItemId,
                link_type: incoming.linkType ?? "attachment",
              },
              "document link"
            );
            linkId = String(existingLink.id);
          } else {
            const candidateLinkId = stableLinkId(
              incoming.documentId,
              incoming.itemCollection,
              linkedItemId
            );
            const link = await ensurePrimaryKeyedRow(
              ItemsService,
              "vibetable_document_links",
              options,
              trx,
              candidateLinkId,
              {
                id: candidateLinkId,
                status: "active",
                document: incoming.documentId,
                item_collection: incoming.itemCollection,
                item_id: linkedItemId,
                link_type: incoming.linkType ?? "attachment",
              },
              (row) => requireSame(
                row,
                {
                  document: incoming.documentId,
                  item_collection: incoming.itemCollection!,
                  item_id: linkedItemId,
                  link_type: incoming.linkType ?? "attachment",
                },
                "document link"
              )
            );
            created ||= link.created;
            linkId = candidateLinkId;
          }
        }

        return {
          documentId: incoming.documentId,
          status: created ? "created" : "already-exists",
          linkId,
        };
      });
      res.json({ data: result });
    } catch (error) {
      if (error instanceof IdentityConflictError) {
        res.status(409).json({ error: error.message });
        return;
      }
      throw error;
    }
  });

  router.post("/publish", async (req, res) => {
    const body = req.body ?? {};
    const revisionRequests = Array.isArray(body.revisions)
      ? body.revisions
      : [body];
    if (revisionRequests.length > MAX_PUBLISH_BATCH) {
      res.status(400).json({ error: `batch exceeds max ${MAX_PUBLISH_BATCH}` });
      return;
    }
    for (const raw of revisionRequests) {
      const error = validatePublishRequest(raw);
      if (error) {
        res.status(400).json({ error });
        return;
      }
    }

    const schema = await context.getSchema();
    try {
      const results = await database.transaction(async (trx) => {
        const options = serviceOptions(schema, trx, requestAccountability(req));
        const output: Array<{ status: string; revisionId: string }> = [];
        for (const raw of revisionRequests) {
          const incoming = raw as PublishRevisionRequest;
          const ensured = await ensurePrimaryKeyedRow(
            ItemsService,
            "vibetable_document_revisions",
            options,
            trx,
            incoming.revisionId,
            {
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
            },
            (row) => {
              if (!revisionsMatch(row, incoming)) {
                throw new IdentityConflictError(
                  `revision ${incoming.revisionId} exists with different content (immutable conflict)`
                );
              }
            }
          );
          output.push({
            status: ensured.created ? "created" : "already-exists",
            revisionId: incoming.revisionId,
          });
        }
        return output;
      });
      res.json({ data: results });
    } catch (error) {
      if (error instanceof IdentityConflictError) {
        res.status(409).json({ error: error.message });
        return;
      }
      throw error;
    }
  });

  router.post("/link", async (req, res) => {
    const schema = await context.getSchema();
    const allowed = await installedBusinessCollections(
      CollectionsService,
      schema,
      database,
      requestAccountability(req)
    );
    const error = validateLinkRequest(req.body, allowed);
    if (error) {
      res.status(400).json({ error });
      return;
    }
    const incoming = req.body as LinkDocumentRequest;
    const linkedItemId = String(incoming.itemId);
    const linkId = stableLinkId(
      incoming.documentId,
      incoming.itemCollection,
      linkedItemId
    );

    try {
      const result = await database.transaction(async (trx) => {
        const options = serviceOptions(schema, trx, requestAccountability(req));
        const links = new ItemsService("vibetable_document_links", options);
        const matches = await links.readByQuery({
          filter: {
            document: { _eq: incoming.documentId },
            item_collection: { _eq: incoming.itemCollection },
            item_id: { _eq: linkedItemId },
          },
          limit: 1,
        });
        if (matches[0]) {
          requireSame(
            matches[0],
            { link_type: incoming.linkType ?? "primary" },
            "document link"
          );
          return { status: "already-exists", linkId: String(matches[0].id) };
        }
        const ensured = await ensurePrimaryKeyedRow(
          ItemsService,
          "vibetable_document_links",
          options,
          trx,
          linkId,
          {
            id: linkId,
            status: "active",
            document: incoming.documentId,
            item_collection: incoming.itemCollection,
            item_id: linkedItemId,
            link_type: incoming.linkType ?? "primary",
          },
          (row) => requireSame(
            row,
            {
              document: incoming.documentId,
              item_collection: incoming.itemCollection,
              item_id: linkedItemId,
              link_type: incoming.linkType ?? "primary",
            },
            "document link"
          )
        );
        return {
          status: ensured.created ? "created" : "already-exists",
          linkId,
        };
      });
      res.json({ data: result });
    } catch (error) {
      if (error instanceof IdentityConflictError) {
        res.status(409).json({ error: error.message });
        return;
      }
      throw error;
    }
  });

  router.post("/unlink", async (req, res) => {
    const linkId = req.body?.linkId;
    if (typeof linkId !== "string" || !linkId) {
      res.status(400).json({ error: "linkId is required" });
      return;
    }
    const schema = await context.getSchema();
    const links = new ItemsService(
      "vibetable_document_links",
      serviceOptions(schema, database, requestAccountability(req))
    );
    await links.deleteOne(linkId);
    res.json({ data: { deleted: linkId } });
  });

  router.post("/reconcile-head", async (req, res) => {
    const error = validateReconcileHeadRequest(req.body);
    if (error) {
      res.status(400).json({ error });
      return;
    }
    const { schemeId, expectedHeadRevisionId, newHeadRevisionId } = req.body;
    const schema = await context.getSchema();
    const schemes = new ItemsService(
      "vibetable_document_schemes",
      serviceOptions(schema, database, requestAccountability(req))
    );
    const existing = await readById(schemes, schemeId);
    if (!existing) {
      res.status(404).json({ error: `scheme ${schemeId} not found` });
      return;
    }

    const currentHead = String(existing.head_revision ?? "");
    const result = computeReconcileResult(
      currentHead,
      expectedHeadRevisionId,
      newHeadRevisionId
    );
    if (result.status === "conflict") {
      res.status(409).json({
        data: result,
        error: `CAS conflict: expected ${expectedHeadRevisionId}, actual ${currentHead}`,
      });
      return;
    }
    await schemes.updateOne(schemeId, { head_revision: newHeadRevisionId });
    res.json({ data: result });
  });
});
