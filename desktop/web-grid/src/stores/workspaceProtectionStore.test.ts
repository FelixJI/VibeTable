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
