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
    service.compare(
      "entry-capability-4",
      "44444444-4444-4444-8444-444444444444",
      "55555555-5555-4555-8555-555555555555",
    );
    service.cancelDiff("entry-capability-4");
    const operationId = intents[5]?.type === "document.diffRequested"
      ? intents[5].operationId
      : null;
    expect(operationId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
    expect(intents).toEqual([
      {
        type: "document.listRequested",
        scope: { kind: "record", collection: "orders", itemId: 42 },
        authority: "workspace",
        query: {
          logic: "and",
          filters: [{ field: "status", operator: "eq", value: "active" }],
          sort: [{ field: "effectiveRevisionCreatedAt", direction: "desc" }],
          limit: 100,
          cursor: null,
        },
      },
      { type: "document.importRequested", scope: { kind: "record", collection: "orders", itemId: 42 } },
      { type: "document.openRequested", entryHandle: "entry-capability-1" },
      { type: "document.relinkRequested", handle: "entry-capability-2" },
      { type: "document.dragOutRequested", handle: "entry-capability-3" },
      {
        type: "document.diffRequested",
        entryHandle: "entry-capability-4",
        operationId,
        historicalRevisionId: "44444444-4444-4444-8444-444444444444",
        expectedEffectiveRevisionId: "55555555-5555-4555-8555-555555555555",
      },
      {
        type: "document.diffCancelRequested",
        entryHandle: "entry-capability-4",
        operationId,
      },
    ]);
    expect(JSON.stringify(intents)).not.toMatch(/[A-Z]:\\|localPath|filePath/);
  });
});
