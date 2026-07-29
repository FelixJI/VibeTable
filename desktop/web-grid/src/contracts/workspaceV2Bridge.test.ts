import { describe, expect, it } from "vitest";
import { parseWorkspaceV2Reply } from "./workspaceV2Bridge";

const reply = {
  method: "fileHistory.listDocuments",
  wire: {
    scope: "workspace",
    workspaceId: "11111111-1111-4111-8111-111111111111",
    sessionEpoch: 7,
    operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    sequence: 19,
  },
  ok: true,
  result: {
    documents: [{
      contractVersion: "2.0",
      documentId: "22222222-2222-4222-8222-222222222222",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      relativePath: "季度规划.docx",
      status: "active",
      effectiveRevisionId: "33333333-3333-4333-8333-333333333333",
      nextRevisionOrdinal: 4,
      nextFormalVersion: 4,
    }],
  },
  error: null,
} as const;

describe("workspace v2 document list reply", () => {
  it("strictly parses canonical FileDocument projections", () => {
    const parsed = parseWorkspaceV2Reply(reply);
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "fileHistory.listDocuments") {
      expect(parsed.result.documents[0]?.documentId)
        .toBe("22222222-2222-4222-8222-222222222222");
    }
  });

  it("rejects unknown nested document fields", () => {
    const invalid = structuredClone(reply) as unknown as {
      result: { documents: Array<Record<string, unknown>> };
    };
    invalid.result.documents[0]!.displayName = "must be derived by the OS adapter";
    expect(() => parseWorkspaceV2Reply(invalid)).toThrow(
      "file document has unknown or missing fields",
    );
  });
});

describe("workspace v2 provisional file revision results", () => {
  it("parses restore results before canonical ordinal and Vn allocation", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "fileHistory.restore",
      result: {
        revisionId: "33333333-3333-4333-8333-333333333333",
        revisionOrdinal: 0,
        localSequence: 7,
        formalVersion: null,
      },
    });
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "fileHistory.restore") {
      expect(parsed.result).toMatchObject({
        revisionOrdinal: 0,
        localSequence: 7,
        formalVersion: null,
      });
    }
  });

  it("rejects provisional results without localSequence", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "fileHistory.upgrade",
      result: {
        revisionId: "33333333-3333-4333-8333-333333333333",
        revisionOrdinal: 0,
        localSequence: null,
        formalVersion: null,
      },
    })).toThrow("localSequence");
  });
});

describe("workspace v2 snapshot request results", () => {
  it("accepts the initial mutation revision before any authority write", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "snapshot.request",
      result: {
        operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        state: "succeeded",
        snapshotId: "33333333-3333-4333-8333-333333333333",
        mutationRevision: 0,
      },
    });
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "snapshot.request") {
      expect(parsed.result.mutationRevision).toBe(0);
    }
  });
});

describe("workspace v2 row and cell history restore", () => {
  it("strictly parses the append-only history page", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "history.query",
      result: {
        collection: "orders",
        itemId: "row-1",
        changeSets: [{
          rootRevisionId: "revision-2",
          changeSetId: "change-set-2",
          activityId: "activity-2",
          action: "update",
          timestamp: "2026-07-29T10:00:00Z",
          actor: { userId: "user-1", displayName: "用户 A" },
          scalarChanges: [{ field: "status", before: "new", after: "done" }],
          relationChanges: [],
          itemId: "row-1",
          recordLabel: "订单 1",
          revisionIds: ["revision-2"],
          affectedRecords: 1,
          recordChanges: [{
            revisionId: "revision-2",
            itemId: "row-1",
            recordLabel: "订单 1",
            action: "update",
            scalarChanges: [{ field: "status", before: "new", after: "done" }],
            relationChanges: [],
          }],
        }],
        total: 1,
        capabilityHash: "sha256:capability",
        schemaRevision: "schema:2",
        scope: "row",
        field: null,
        hasMore: false,
        archivedDefaultRevisionIds: {},
      },
    });
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "history.query") {
      expect(parsed.result.changeSets[0]?.rootRevisionId).toBe("revision-2");
      expect(parsed.result.total).toBe(1);
    }
  });

  it("rejects open or malformed history-page projections", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "history.query",
      result: {
        collection: "orders",
        itemId: null,
        changeSets: [],
        total: 0,
        capabilityHash: "sha256:capability",
        schemaRevision: "schema:2",
        scope: "table",
        field: null,
        hasMore: false,
        archivedDefaultRevisionIds: {},
        databasePath: "must-not-leak",
      },
    })).toThrow("history page has unknown or missing fields");
  });

  it("strictly parses preview and coordinated apply results", () => {
    const preview = parseWorkspaceV2Reply({
      ...reply,
      method: "history.previewRestore",
      result: {
        collection: "orders",
        itemId: "row-1",
        targetRevision: "revision-1",
        currentHash: "sha256:current",
        schemaRevision: "schema-1",
        scalarChanges: [{ field: "status", before: "done", after: "new" }],
        relationChanges: [],
        diagnostics: [],
        token: "restore-token",
        expiresAt: "2026-07-29T10:00:00Z",
        scope: "cell",
        field: "status",
        canApply: true,
        restorableFields: ["status"],
      },
    });
    expect(preview.ok).toBe(true);
    if (preview.ok && preview.method === "history.previewRestore") {
      expect(preview.result.scalarChanges[0]?.after).toBe("new");
    }

    const applied = parseWorkspaceV2Reply({
      ...reply,
      method: "history.applyRestore",
      result: {
        collection: "orders",
        itemId: "row-1",
        restoredToRevision: "revision-1",
        newRevisionId: "revision-2",
        item: { id: "row-1", status: "new" },
        mutationRevision: 42,
      },
    });
    expect(applied.ok).toBe(true);
    if (applied.ok && applied.method === "history.applyRestore") {
      expect(applied.result.mutationRevision).toBe(42);
      expect(applied.result.item.status).toBe("new");
    }
  });

  it("rejects unknown nested preview fields and missing mutation revisions", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "history.previewRestore",
      result: {
        collection: "orders",
        itemId: "row-1",
        targetRevision: "revision-1",
        currentHash: "sha256:current",
        schemaRevision: "schema-1",
        scalarChanges: [{
          field: "status",
          before: "done",
          after: "new",
          writable: true,
        }],
        relationChanges: [],
        diagnostics: [],
        token: "restore-token",
        expiresAt: "2026-07-29T10:00:00Z",
        scope: "cell",
        field: "status",
        canApply: true,
        restorableFields: ["status"],
      },
    })).toThrow("history scalar change has unknown or missing fields");

    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "history.applyRestore",
      result: {
        collection: "orders",
        itemId: "row-1",
        restoredToRevision: "revision-1",
        newRevisionId: "revision-2",
        item: { id: "row-1", status: "new" },
      },
    })).toThrow("history.applyRestore result has unknown or missing fields");
  });
});

describe("workspace v2 pending external file changes", () => {
  const pendingReply = {
    ...reply,
    method: "fileHistory.listPendingChanges",
    result: {
      changes: [{
        changeId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        relativePath: "归档/季度规划.docx",
        missing: false,
        observedHash: `sha256:${"ab".repeat(32)}`,
        observedSize: 1024,
        reason: "ambiguous identity",
        candidateDocumentIds: ["22222222-2222-4222-8222-222222222222"],
        createdAt: "2026-07-28T09:00:00Z",
        updatedAt: "2026-07-28T09:00:00Z",
      }],
    },
  } as const;

  it("strictly parses persisted identity decisions", () => {
    const parsed = parseWorkspaceV2Reply(pendingReply);
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "fileHistory.listPendingChanges") {
      expect(parsed.result.changes[0]?.relativePath)
        .toBe("归档/季度规划.docx");
    }
  });

  it("rejects unknown pending-change fields", () => {
    const invalid = structuredClone(pendingReply) as unknown as {
      result: { changes: Array<Record<string, unknown>> };
    };
    invalid.result.changes[0]!.guessedAction = "move";
    expect(() => parseWorkspaceV2Reply(invalid)).toThrow(
      "pending file change has unknown or missing fields",
    );
  });
});

describe("workspace v2 conflict projections", () => {
  it("parses non-empty list summaries independently from typed detail items", () => {
    const conflictId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const listed = parseWorkspaceV2Reply({
      ...reply,
      method: "conflict.list",
      result: {
        conflicts: [{
          conflictId,
          state: "pending",
          createdAt: "2026-07-28T09:00:00Z",
          itemCount: 2,
        }],
        nextCursor: null,
      },
    });
    expect(listed.ok).toBe(true);
    if (listed.ok && listed.method === "conflict.list") {
      expect(listed.result.conflicts[0]).toMatchObject({ conflictId, itemCount: 2 });
    }

    const inspected = parseWorkspaceV2Reply({
      ...reply,
      method: "conflict.inspect",
      result: {
        conflictId,
        state: "pending",
        items: [
          {
            conflictId,
            itemId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
            path: "Projects",
            kind: "table",
            state: "pending",
            localSummary: "Local table",
            replicaSummary: "Replica table",
            baseSummary: "Base table",
            dependencies: ["relation:Customers", "automation:Notify", "plugin:Calendar"],
            selected: "local",
          },
          {
            conflictId,
            itemId: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
            path: "files/brief.docx",
            kind: "file",
            state: "pending",
            localSummary: "Local file",
            replicaSummary: "Replica file",
            baseSummary: "Base file",
            dependencies: [],
            selected: "both",
          },
        ],
      },
    });
    expect(inspected.ok).toBe(true);
    if (inspected.ok && inspected.method === "conflict.inspect") {
      expect(inspected.result.items.map((item) => [item.kind, item.selected]))
        .toEqual([["table", "local"], ["file", "both"]]);
      expect(inspected.result.items[0]?.dependencies).toHaveLength(3);
    }
  });

  it("rejects list items that masquerade as detail projections", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "conflict.list",
      result: {
        conflicts: [{
          conflictId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
          state: "pending",
          createdAt: "2026-07-28T09:00:00Z",
          itemCount: 1,
          path: "must-not-appear-in-summary",
        }],
        nextCursor: null,
      },
    })).toThrow("conflict summary has unknown or missing fields");
  });
});

describe("workspace v2 repository and extraction replies", () => {
  it("strictly parses an extraction plan", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "snapshot.previewExtract",
      result: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        displayName: "季度规划.docx",
        size: 1024,
        expiresAt: "2026-07-28T10:10:00Z",
      },
    });
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "snapshot.previewExtract") {
      expect(parsed.result.size).toBe(1024);
    }
  });

  it("rejects unknown repository verification evidence", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "repository.verify",
      result: {
        state: "verified",
        snapshotCount: 1,
        objectCount: 7,
        corruptSnapshotIds: [],
        repaired: true,
      },
    })).toThrow("repository.verify result has unknown or missing fields");
  });
});

describe("workspace v2 retention protection status", () => {
  const statusReply = {
    ...reply,
    method: "retention.status",
    result: {
      repositoryUsageBytes: 3 * 1024 ** 3,
      repositoryLimitBytes: 2 * 1024 ** 3,
      automaticSnapshotsPaused: true,
      warningCode: "snapshot.repository_limit_reached",
      integrityStatus: "verified",
      integrityFailure: null,
      lastIncrementalCheckAt: "2026-07-28T09:00:00Z",
      lastFullCheckAt: "2026-07-01T09:00:00Z",
      maintenanceFailure: "retention repository temporarily unavailable",
      maintenanceFailureStage: "sweep",
      lastMaintenanceFailureAt: "2026-07-29T09:00:00Z",
    },
  } as const;

  it("parses durable quota and integrity evidence", () => {
    const parsed = parseWorkspaceV2Reply(statusReply);
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "retention.status") {
      expect(parsed.result.automaticSnapshotsPaused).toBe(true);
      expect(parsed.result.integrityStatus).toBe("verified");
      expect(parsed.result.maintenanceFailureStage).toBe("sweep");
    }
  });

  it("rejects inconsistent warning and corruption states", () => {
    const invalid = {
      ...statusReply,
      result: {
        ...statusReply.result,
        automaticSnapshotsPaused: false,
      },
    };
    expect(() => parseWorkspaceV2Reply(invalid))
      .toThrow("retention status is inconsistent");

    const corrupt = {
      ...statusReply,
      result: {
        ...statusReply.result,
        integrityStatus: "corrupt",
      },
    };
    expect(() => parseWorkspaceV2Reply(corrupt))
      .toThrow("retention status is inconsistent");

    const incompleteMaintenance = {
      ...statusReply,
      result: {
        ...statusReply.result,
        maintenanceFailureStage: null,
      },
    };
    expect(() => parseWorkspaceV2Reply(incompleteMaintenance))
      .toThrow("retention status is inconsistent");
  });
});

describe("workspace v2 snapshot import reply", () => {
  const importReply = {
    ...reply,
    method: "snapshot.import",
    result: {
      operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      snapshotId: "44444444-4444-4444-8444-444444444444",
      sourceWorkspaceId: "55555555-5555-4555-8555-555555555555",
      sourceSnapshotId: "66666666-6666-4666-8666-666666666666",
      state: "restoreRequired",
    },
  } as const;

  it("requires the imported snapshot to be restored before opening", () => {
    const parsed = parseWorkspaceV2Reply(importReply);
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "snapshot.import") {
      expect(parsed.result.snapshotId)
        .toBe("44444444-4444-4444-8444-444444444444");
      expect(parsed.result.sourceWorkspaceId)
        .toBe("55555555-5555-4555-8555-555555555555");
      expect(parsed.result.sourceSnapshotId)
        .toBe("66666666-6666-4666-8666-666666666666");
      expect(parsed.result.state).toBe("restoreRequired");
    }
  });

  it("rejects an import result that claims to be immediately published", () => {
    const invalid = structuredClone(importReply) as unknown as {
      result: { state: string };
    };
    invalid.result.state = "published";
    expect(() => parseWorkspaceV2Reply(invalid))
      .toThrow("snapshot import state is invalid");
  });
});

describe("workspace v2 snapshot open and key rotation replies", () => {
  it("parses the new workspace session returned by open-as-new", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "snapshot.openAsNewWorkspace",
      result: {
        workspaceId: "99999999-9999-4999-8999-999999999999",
        sessionEpoch: 8,
        state: "openedWritable",
      },
    });
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "snapshot.openAsNewWorkspace") {
      expect(parsed.result.sessionEpoch).toBe(8);
    }
  });

  it("requires a protection point in every key-rotation plan", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "repository.previewKeyRotation",
      result: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        expiresAt: "2026-07-28T10:10:00Z",
        protectionRequired: false,
      },
    })).toThrow("repository key rotation must require protection");
  });

  it("does not claim a Web-visible recovery key after rotation", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "repository.applyKeyRotation",
      result: {
        operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        state: "hostRestartRequired",
        newRecoveryKeyAvailable: false,
      },
    });
    expect(parsed.ok).toBe(true);
  });
});

describe("workspace v2 storage relocation replies", () => {
  it("strictly parses a durable relocation plan", () => {
    const parsed = parseWorkspaceV2Reply({
      ...reply,
      method: "workspace.storage.preview",
      wire: {
        scope: "global",
        operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        sequence: 20,
      },
      result: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        workspaceId: "11111111-1111-4111-8111-111111111111",
        action: "relocate",
        source: {
          selectedRoot: "D:\\Workspaces\\Quarter",
          activityRoot: null,
          mode: "direct",
        },
        target: {
          selectedRoot: "E:\\Workspaces\\Quarter",
          activityRoot: null,
          mode: "direct",
        },
        bytesToCopy: 4096,
        requiresClosedSession: true,
        warnings: ["The verified source copy is retained after relocation."],
        expiresAt: "2026-07-28T10:10:00Z",
        verificationReceiptId: null,
      },
    });
    expect(parsed.ok).toBe(true);
    if (parsed.ok && parsed.method === "workspace.storage.preview") {
      expect(parsed.result.target.selectedRoot)
        .toBe("E:\\Workspaces\\Quarter");
    }
  });

  it("rejects a plan that can mutate an open session in place", () => {
    expect(() => parseWorkspaceV2Reply({
      ...reply,
      method: "workspace.storage.preview",
      wire: {
        scope: "global",
        operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        sequence: 20,
      },
      result: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        workspaceId: "11111111-1111-4111-8111-111111111111",
        action: "relocate",
        source: {
          selectedRoot: "D:\\Workspaces\\Quarter",
          activityRoot: null,
          mode: "direct",
        },
        target: {
          selectedRoot: "E:\\Workspaces\\Quarter",
          activityRoot: null,
          mode: "direct",
        },
        bytesToCopy: 4096,
        requiresClosedSession: false,
        warnings: [],
        expiresAt: "2026-07-28T10:10:00Z",
        verificationReceiptId: null,
      },
    })).toThrow("storage plan must require a closed session");
  });

  it("accepts topology and cache actions but rejects unknown storage actions", () => {
    const makeReply = (action: string) => ({
      ...reply,
      method: "workspace.storage.preview",
      wire: {
        scope: "global",
        operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        sequence: 20,
      },
      result: {
        planId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        workspaceId: "11111111-1111-4111-8111-111111111111",
        action,
        source: {
          selectedRoot: "E:\\Replica\\Quarter",
          activityRoot: "C:\\Activity\\Quarter",
          mode: "mirrored",
        },
        target: {
          selectedRoot: "E:\\Replica\\Quarter",
          activityRoot: null,
          mode: "mirrored",
        },
        bytesToCopy: 0,
        requiresClosedSession: true,
        warnings: [],
        expiresAt: "2026-07-28T10:10:00Z",
        verificationReceiptId: "verify-1",
      },
    });
    expect(parseWorkspaceV2Reply(makeReply("convertTopology")).ok).toBe(true);
    expect(parseWorkspaceV2Reply(makeReply("releaseActivityCache")).ok).toBe(true);
    expect(() => parseWorkspaceV2Reply(makeReply("overwriteInPlace"))).toThrow(
      "storage action is invalid",
    );
  });
});
