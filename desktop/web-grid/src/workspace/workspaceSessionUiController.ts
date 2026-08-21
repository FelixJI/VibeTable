import { ref, type Ref } from "vue";

import type { WorkspaceV2UiAction } from "@/services/workspaceV2UiPort";
import type { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";
import type { useWorkspaceProtectionStore } from "@/stores/workspaceProtectionStore";
import type { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

export interface WorkspaceSessionUiController {
  readonly activationPending: Ref<boolean>;
  execute(action: WorkspaceV2UiAction): Promise<boolean>;
  open(workspaceId: string): Promise<boolean>;
}

export interface WorkspaceSessionUiDependencies {
  readonly session: Pick<ReturnType<typeof useWorkspaceSessionStore>,
    | "enabled" | "activeWorkspaceId" | "isTransitioning"
    | "beginSwitch" | "failSwitch">;
  readonly protection: Pick<ReturnType<typeof useWorkspaceProtectionStore>,
    "beginOperation" | "finishOperation">;
  readonly documents: Pick<ReturnType<typeof useDocumentWorkspaceStore>,
    "removeActiveDocument">;
  readonly showCenter: Ref<boolean>;
  readonly request: (action: WorkspaceV2UiAction) => Promise<unknown>;
  readonly errorMessage: (error: unknown) => string;
  readonly initializeConsumers: () => void;
}

type AcquiredExecutionOutcome = "succeeded" | "failed" | "stale";

export function createWorkspaceSessionUiController(
  dependencies: WorkspaceSessionUiDependencies,
): WorkspaceSessionUiController {
  const activationPending = ref(false);

  async function executeAcquired(
    action: WorkspaceV2UiAction,
    lease: NonNullable<ReturnType<typeof dependencies.protection.beginOperation>>,
  ): Promise<AcquiredExecutionOutcome> {
    try {
      await dependencies.request(action);
      if (action.method === "fileHistory.unlink") {
        dependencies.documents.removeActiveDocument(action.params.documentId);
      }
      dependencies.protection.finishOperation(lease);
      return "succeeded";
    } catch (error) {
      const message = dependencies.errorMessage(error);
      const owned = dependencies.protection.finishOperation(lease, message);
      if (!owned) return "stale";
      if (
        action.method === "workspace.open" || action.method === "workspace.switch"
      ) {
        dependencies.session.failSwitch(message);
      }
      return "failed";
    }
  }

  async function execute(action: WorkspaceV2UiAction): Promise<boolean> {
    if (!dependencies.session.enabled) return false;
    const lease = dependencies.protection.beginOperation(action.method);
    if (!lease) return false;
    return (await executeAcquired(action, lease)) === "succeeded";
  }

  async function open(workspaceId: string): Promise<boolean> {
    if (workspaceId === dependencies.session.activeWorkspaceId) {
      dependencies.showCenter.value = false;
      return true;
    }
    if (!dependencies.session.enabled || dependencies.session.isTransitioning) return false;
    const action: WorkspaceV2UiAction = dependencies.session.activeWorkspaceId
      ? {
          method: "workspace.switch",
          params: { targetWorkspaceId: workspaceId, openMode: "writable" },
        }
      : {
          method: "workspace.open",
          params: { workspaceId, openMode: "writable" },
        };
    const lease = dependencies.protection.beginOperation(action.method);
    if (!lease) return false;
    if (!dependencies.session.beginSwitch(workspaceId)) {
      dependencies.protection.finishOperation(lease);
      return false;
    }
    activationPending.value = true;
    let outcome: AcquiredExecutionOutcome;
    try {
      outcome = await executeAcquired(action, lease);
      if (outcome !== "stale") {
        dependencies.showCenter.value = outcome === "failed";
      }
    } finally {
      activationPending.value = false;
    }
    const opened = outcome === "succeeded"
      || (outcome === "stale" && dependencies.session.activeWorkspaceId === workspaceId);
    if (outcome === "stale" && opened) dependencies.showCenter.value = false;
    if (opened) dependencies.initializeConsumers();
    return opened;
  }

  return { activationPending, execute, open };
}
