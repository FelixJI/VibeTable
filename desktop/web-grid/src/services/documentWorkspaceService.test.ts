import { describe, expect, it } from "vitest";
import { createDocumentWorkspaceService, type DocumentWorkspaceIntent } from "./documentWorkspaceService";

describe("documentWorkspaceService", () => {
  it("maps UI operations to opaque-handle intents without paths", () => {
    const intents: DocumentWorkspaceIntent[] = [];
    const service = createDocumentWorkspaceService((intent) => intents.push(intent));
    service.list({ kind: "record", collection: "orders", itemId: 42 }, "workspace");
    service.importFiles({ kind: "record", collection: "orders", itemId: 42 });
    service.open("entry-capability-1");
    service.relink("entry-capability-2");
    service.dragOut("entry-capability-3");
    service.commitRevision("entry-capability-4", "checkpoint", "scheme-main");
    service.previewRevision("entry-capability-4", "revision-opaque-1");
    service.restoreRevision("entry-capability-4", "revision-opaque-1");
    expect(intents).toEqual([
      { type: "document.listRequested", scope: { kind: "record", collection: "orders", itemId: 42 }, authority: "workspace" },
      { type: "document.importRequested", scope: { kind: "record", collection: "orders", itemId: 42 } },
      { type: "document.openRequested", entryHandle: "entry-capability-1" },
      { type: "document.relinkRequested", handle: "entry-capability-2" },
      { type: "document.dragOutRequested", handle: "entry-capability-3" },
      { type: "document.commitRevisionRequested", entryHandle: "entry-capability-4", note: "checkpoint", schemeHandle: "scheme-main" },
      { type: "document.revisionPreviewRequested", entryHandle: "entry-capability-4", revisionHandle: "revision-opaque-1" },
      { type: "document.revisionRestoreRequested", entryHandle: "entry-capability-4", revisionHandle: "revision-opaque-1" },
    ]);
    expect(JSON.stringify(intents)).not.toMatch(/[A-Z]:\\|localPath|filePath/);
  });
});
