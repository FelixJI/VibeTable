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
});
