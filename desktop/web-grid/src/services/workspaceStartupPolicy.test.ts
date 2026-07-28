import { describe, expect, it } from "vitest";
import type { WorkspaceRegistryEntryV2 } from "@/contracts/workspaceV2";
import { decideWorkspaceStartup } from "./workspaceStartupPolicy";

function workspace(
  workspaceId: string,
  lastOpenedAt: string | null,
  lastKnownHealth: WorkspaceRegistryEntryV2["lastKnownHealth"] = "healthy",
): WorkspaceRegistryEntryV2 {
  return {
    contractVersion: "2.0",
    workspaceId,
    displayName: workspaceId,
    selectedRoot: `D:\\Workspaces\\${workspaceId}`,
    activityRoot: null,
    storageKind: "fixed",
    coordinationStrength: "strong",
    lastOpenedAt,
    lastKnownHealth,
    lastSnapshotAt: null,
    lastSyncAt: null,
    pendingSync: false,
  };
}

describe("workspace startup policy", () => {
  it("waits for the shell bootstrap and never replaces an open session", () => {
    expect(decideWorkspaceStartup(false, false, "lastWorkspace", [])).toEqual({
      kind: "wait",
    });
    expect(decideWorkspaceStartup(true, true, "lastWorkspace", [])).toEqual({
      kind: "keepCurrent",
    });
  });

  it("opens the most recently used workspace by default", () => {
    expect(decideWorkspaceStartup(
      true,
      false,
      "lastWorkspace",
      [
        workspace("older", "2026-07-27T08:00:00Z"),
        workspace("latest", "2026-07-28T08:00:00Z"),
      ],
    )).toEqual({ kind: "open", workspaceId: "latest" });
  });

  it("honors Workspace Center and fails closed for unavailable last workspaces", () => {
    expect(decideWorkspaceStartup(
      true,
      false,
      "workspaceCenter",
      [workspace("latest", "2026-07-28T08:00:00Z")],
    )).toEqual({ kind: "workspaceCenter", reason: "preference" });
    expect(decideWorkspaceStartup(
      true,
      false,
      "lastWorkspace",
      [workspace("offline", "2026-07-28T08:00:00Z", "offline")],
    )).toEqual({ kind: "workspaceCenter", reason: "unavailable" });
    expect(decideWorkspaceStartup(
      true,
      false,
      "lastWorkspace",
      [workspace("corrupt", "2026-07-28T08:00:00Z", "corrupt")],
    )).toEqual({ kind: "workspaceCenter", reason: "unavailable" });
  });

  it("stays in Workspace Center when no workspace has ever opened", () => {
    expect(decideWorkspaceStartup(
      true,
      false,
      "lastWorkspace",
      [workspace("new", null)],
    )).toEqual({ kind: "workspaceCenter", reason: "empty" });
  });
});
