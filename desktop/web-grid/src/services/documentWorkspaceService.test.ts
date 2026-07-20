import { describe, expect, it } from "vitest";
import { createDocumentWorkspaceService, type DocumentWorkspaceIntent } from "./documentWorkspaceService";

describe("documentWorkspaceService", () => {
  it("maps UI operations to opaque-handle intents without paths", () => {
    const intents: DocumentWorkspaceIntent[] = [];
    const service = createDocumentWorkspaceService((intent) => intents.push(intent));
    service.list({ kind: "record", collection: "orders", itemId: 42 }, "workspace");
    service.open("entry-capability-1");
    service.relink("entry-capability-2");
    expect(intents).toEqual([
      { type: "document.listRequested", scope: { kind: "record", collection: "orders", itemId: 42 }, authority: "workspace" },
      { type: "document.openRequested", entryHandle: "entry-capability-1" },
      { type: "document.relinkRequested", entryHandle: "entry-capability-2" },
    ]);
    expect(JSON.stringify(intents)).not.toMatch(/[A-Z]:\\|localPath|filePath/);
  });
});
