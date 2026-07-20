import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
  validatePublishRequest,
  validateReconcileHeadRequest,
  validateLinkRequest,
  validateRegisterDocumentRequest,
  computeReconcileResult,
  revisionsMatch,
  WORKSPACE_INDEX_COLLECTIONS,
  MAX_PUBLISH_BATCH,
  type PublishRevisionRequest,
  type RegisterDocumentRequest,
} from "../workspace-index-helpers.ts";

describe("validatePublishRequest", () => {
  const validReq: PublishRevisionRequest = {
    documentId: "doc-1",
    schemeId: "scheme-1",
    revisionId: "rev-1",
    parentRevisionId: null,
    sequence: 1,
    versionLabel: "main/V1",
    kind: "formal",
    hash: "abcdef12345678",
    size: 100,
    mimeType: "text/plain",
    createdBy: "user-1",
    deviceId: "device-1",
    comment: "initial",
  };

  it("accepts valid request", () => {
    assert.equal(validatePublishRequest(validReq), null);
  });

  it("rejects missing documentId", () => {
    assert.ok(validatePublishRequest({ ...validReq, documentId: "" }));
  });

  it("rejects missing hash", () => {
    assert.ok(validatePublishRequest({ ...validReq, hash: "" }));
  });

  it("rejects invalid kind", () => {
    assert.ok(validatePublishRequest({ ...validReq, kind: "bogus" as never }));
  });

  it("rejects non-object", () => {
    assert.ok(validatePublishRequest(null));
    assert.ok(validatePublishRequest("string"));
  });
});

describe("validateRegisterDocumentRequest", () => {
  const valid: RegisterDocumentRequest = {
    workspaceId: "230c956f-fc22-401c-bdbc-9ce61e5dd758",
    workspaceName: "VibeTable Workspace",
    documentId: "e24f262e-06d0-4410-8097-74aac3efca30",
    fileName: "预算.xlsx",
    mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    schemeId: "ca971db6-b25d-4375-8609-94c12d011c1c",
    revisionId: "a1a3151c-a88e-4c8f-827a-628b69ee2888",
    hash: "a".repeat(64),
    size: 1024,
  };

  it("accepts a global document without a record link", () => {
    assert.equal(validateRegisterDocumentRequest(valid), null);
  });

  it("accepts a declared record link", () => {
    assert.equal(validateRegisterDocumentRequest({
      ...valid,
      itemCollection: "vibetable_demo",
      itemId: "item-1",
      linkType: "attachment",
    }, new Set(["vibetable_demo"])), null);
  });

  it("accepts a numeric record key for string-backed polymorphic links", () => {
    assert.equal(validateRegisterDocumentRequest({
      ...valid,
      itemCollection: "vibetable_demo",
      itemId: 42,
      linkType: "attachment",
    }, new Set(["vibetable_demo"])), null);
  });

  it("rejects paths and executable-style path injection in fileName", () => {
    assert.ok(validateRegisterDocumentRequest({ ...valid, fileName: "folder/file.txt" }));
    assert.ok(validateRegisterDocumentRequest({ ...valid, fileName: "..\\file.txt" }));
  });

  it("rejects partial or undeclared record scope", () => {
    assert.ok(validateRegisterDocumentRequest({ ...valid, itemCollection: "vibetable_demo" }));
    assert.ok(validateRegisterDocumentRequest({
      ...valid,
      itemCollection: "unknown",
      itemId: "item-1",
    }, new Set(["vibetable_demo"])));
  });
});

describe("validateReconcileHeadRequest", () => {
  it("accepts valid request", () => {
    assert.equal(
      validateReconcileHeadRequest({
        documentId: "d1",
        schemeId: "s1",
        expectedHeadRevisionId: "rev-1",
        newHeadRevisionId: "rev-2",
      }),
      null
    );
  });

  it("rejects missing newHeadRevisionId", () => {
    assert.ok(
      validateReconcileHeadRequest({
        documentId: "d1",
        schemeId: "s1",
        expectedHeadRevisionId: "rev-1",
        newHeadRevisionId: "",
      })
    );
  });
});

describe("computeReconcileResult", () => {
  it("returns updated when current matches expected", () => {
    const result = computeReconcileResult("rev-1", "rev-1", "rev-2");
    assert.equal(result.status, "updated");
    assert.equal(result.actualHeadRevisionId, "rev-2");
  });

  it("returns conflict when current does not match expected", () => {
    const result = computeReconcileResult("rev-X", "rev-1", "rev-2");
    assert.equal(result.status, "conflict");
    assert.equal(result.actualHeadRevisionId, "rev-X");
  });
});

describe("validateLinkRequest", () => {
  // The allow-list is now dynamic: any collection the tenant declares.
  const allowed = new Set(["vibetable_demo", "vibetable_customers"]);

  it("accepts valid request with a declared collection", () => {
    assert.equal(
      validateLinkRequest(
        {
          documentId: "doc-1",
          itemCollection: "vibetable_demo",
          itemId: "c-001",
          linkType: "primary",
        },
        allowed
      ),
      null
    );
  });

  it("accepts a numeric business-record key", () => {
    assert.equal(
      validateLinkRequest(
        {
          documentId: "doc-1",
          itemCollection: "vibetable_demo",
          itemId: 7,
          linkType: "attachment",
        },
        allowed
      ),
      null
    );
  });

  it("rejects a collection not in the declared manifest", () => {
    assert.ok(
      validateLinkRequest(
        {
          documentId: "doc-1",
          itemCollection: "directus_users",
          itemId: "u-1",
        },
        allowed
      )
    );
  });

  it("rejects a workspace-index collection as a link target", () => {
    assert.ok(
      validateLinkRequest(
        {
          documentId: "doc-1",
          itemCollection: "vibetable_documents",
          itemId: "d-1",
        },
        allowed
      )
    );
  });

  it("accepts undefined linkType (defaults to primary)", () => {
    assert.equal(
      validateLinkRequest(
        {
          documentId: "doc-1",
          itemCollection: "vibetable_customers",
          itemId: "p-1",
        },
        allowed
      ),
      null
    );
  });
});

describe("revisionsMatch", () => {
  it("returns true when revisionId and hash match", () => {
    assert.ok(
      revisionsMatch(
        { id: "rev-1", hash: "abc123" },
        {
          documentId: "d1",
          schemeId: "s1",
          revisionId: "rev-1",
          parentRevisionId: null,
          sequence: 1,
          versionLabel: "V1",
          kind: "formal",
          hash: "abc123",
          size: 0,
          mimeType: "",
          createdBy: null,
          deviceId: null,
          comment: null,
        }
      )
    );
  });

  it("returns false when hash differs", () => {
    assert.ok(
      !revisionsMatch(
        { id: "rev-1", hash: "abc123" },
        {
          documentId: "d1",
          schemeId: "s1",
          revisionId: "rev-1",
          parentRevisionId: null,
          sequence: 1,
          versionLabel: "V1",
          kind: "formal",
          hash: "different",
          size: 0,
          mimeType: "",
          createdBy: null,
          deviceId: null,
          comment: null,
        }
      )
    );
  });
});

describe("constants", () => {
  it("MAX_PUBLISH_BATCH is 100", () => {
    assert.equal(MAX_PUBLISH_BATCH, 100);
  });

  it("WORKSPACE_INDEX_COLLECTIONS lists the six document-system collections", () => {
    assert.ok(WORKSPACE_INDEX_COLLECTIONS.has("vibetable_workspaces"));
    assert.ok(WORKSPACE_INDEX_COLLECTIONS.has("vibetable_workspace_folders"));
    assert.ok(WORKSPACE_INDEX_COLLECTIONS.has("vibetable_documents"));
    assert.ok(WORKSPACE_INDEX_COLLECTIONS.has("vibetable_document_schemes"));
    assert.ok(WORKSPACE_INDEX_COLLECTIONS.has("vibetable_document_revisions"));
    assert.ok(WORKSPACE_INDEX_COLLECTIONS.has("vibetable_document_links"));
    // Business collections are NOT workspace-index collections.
    assert.ok(!WORKSPACE_INDEX_COLLECTIONS.has("vibetable_demo"));
    assert.equal(WORKSPACE_INDEX_COLLECTIONS.size, 6);
  });
});
