import type { WorkspaceRegistryEntryV2 } from "@/contracts/workspaceV2";
import type { WorkspaceStartupPolicy } from "@/stores/uiStore";

export type WorkspaceStartupDecision =
  | { readonly kind: "wait" }
  | { readonly kind: "keepCurrent" }
  | { readonly kind: "workspaceCenter"; readonly reason: "preference" | "empty" | "unavailable" }
  | { readonly kind: "open"; readonly workspaceId: string };

function openedAt(entry: WorkspaceRegistryEntryV2): number {
  if (!entry.lastOpenedAt) return Number.NEGATIVE_INFINITY;
  const parsed = Date.parse(entry.lastOpenedAt);
  return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

export function decideWorkspaceStartup(
  enabled: boolean,
  hasOpenWorkspace: boolean,
  policy: WorkspaceStartupPolicy,
  workspaces: readonly WorkspaceRegistryEntryV2[],
): WorkspaceStartupDecision {
  if (!enabled) return { kind: "wait" };
  if (hasOpenWorkspace) return { kind: "keepCurrent" };
  if (policy === "workspaceCenter") {
    return { kind: "workspaceCenter", reason: "preference" };
  }

  const last = [...workspaces]
    .filter((entry) => openedAt(entry) !== Number.NEGATIVE_INFINITY)
    .sort((left, right) => openedAt(right) - openedAt(left))[0];
  if (!last) return { kind: "workspaceCenter", reason: "empty" };
  if (last.lastKnownHealth === "offline" || last.lastKnownHealth === "corrupt") {
    return { kind: "workspaceCenter", reason: "unavailable" };
  }
  return { kind: "open", workspaceId: last.workspaceId };
}
