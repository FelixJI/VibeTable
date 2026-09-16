import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";

import type { WorkspaceSessionV2 } from "@/contracts/workspaceV2";
import { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

function session(workspaceId: string, sessionEpoch: number): WorkspaceSessionV2 {
  return {
    contractVersion: "2.0",
    workspaceId,
    sessionEpoch,
    state: "openedWritable",
    openMode: "writable",
    writable: true,
    provisional: false,
    phase: "idle",
    errorCode: null,
  };
}

describe("workspaceProtectionStore operation lease", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("ignores a stale completion after the workspace epoch invalidates its lease", () => {
    const workspaceSession = useWorkspaceSessionStore();
    workspaceSession.configureCapabilities(["workspace.session.v2"]);
    const protection = useWorkspaceProtectionStore();

    workspaceSession.applySession(session("workspace-a", 1));
    const staleLease = protection.beginOperation("retention.get");
    expect(staleLease).not.toBeNull();

    workspaceSession.applySession(session("workspace-b", 2));
    const currentLease = protection.beginOperation("snapshot.list");
    expect(currentLease).not.toBeNull();
    expect(protection.busyOperation).toBe("snapshot.list");

    expect(protection.finishOperation(staleLease!, "stale failure")).toBe(false);
    expect(protection.busyOperation).toBe("snapshot.list");
    expect(protection.operationError).toBeNull();

    expect(protection.finishOperation(currentLease!)).toBe(true);
    expect(protection.busyOperation).toBeNull();
  });
});

describe("workspaceProtectionStore conflict projections", () => {
  beforeEach(() => setActivePinia(createPinia()));

  function conflictItem(itemId: string, selected: "local" | "replica" | null) {
    return {
      conflictId: "conflict-1",
      itemId,
      path: "orders",
      kind: "table" as const,
      state: "pending" as const,
      localSummary: "local",
      replicaSummary: "replica",
      baseSummary: "base",
      dependencies: [],
      selected,
    };
  }

  it("keeps setConflicts an authoritative full replacement, including empty inspections", () => {
    const protection = useWorkspaceProtectionStore();
    protection.setConflictSets([{
      conflictId: "conflict-1",
      state: "pending",
      createdAt: "2026-07-28T09:00:00Z",
      itemCount: 1,
    }]);
    protection.setConflicts([conflictItem("item-1", "local")]);
    expect(protection.conflicts).toHaveLength(1);

    // conflict.inspect may legitimately replace the details with an empty set;
    // the summary list survives only because emptiness there is not authority.
    protection.setConflicts([]);
    expect(protection.conflicts).toEqual([]);
    expect(protection.conflictSets).toHaveLength(1);
  });

  it("drops inspected details when conflict.list removes the owning set", () => {
    const protection = useWorkspaceProtectionStore();
    protection.setConflictSets([{
      conflictId: "conflict-1",
      state: "pending",
      createdAt: "2026-07-28T09:00:00Z",
      itemCount: 1,
    }]);
    protection.setConflicts([conflictItem("item-1", "local")]);

    protection.setConflictSets([]);
    expect(protection.conflictSets).toEqual([]);
    expect(protection.conflicts).toEqual([]);
  });
});
