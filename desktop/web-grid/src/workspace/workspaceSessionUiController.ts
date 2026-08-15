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

export function createWorkspaceSessionUiController(
  dependencies: WorkspaceSessionUiDependencies,
): WorkspaceSessionUiController {
  const activationPending = ref(false);

  async function execute(action: WorkspaceV2UiAction): Promise<boolean> {
    if (!dependencies.session.enabled || !dependencies.protection.beginOperation(action.method)) {
      return false;
    }
    try {
      await dependencies.request(action);
      if (action.method === "fileHistory.unlink") {
        dependencies.documents.removeActiveDocument(action.params.documentId);
      }
      dependencies.protection.finishOperation();
      return true;
    } catch (error) {
      const message = dependencies.errorMessage(error);
      dependencies.protection.finishOperation(message);
      if (action.method === "workspace.open" || action.method === "workspace.switch") {
        dependencies.session.failSwitch(message);
      }
      return false;
    }
  }

  async function open(workspaceId: string): Promise<boolean> {
    if (workspaceId === dependencies.session.activeWorkspaceId) {
      dependencies.showCenter.value = false;
      return true;
    }
    if (!dependencies.session.isTransitioning) dependencies.session.beginSwitch(workspaceId);
    activationPending.value = true;
    let opened = false;
    try {
      opened = dependencies.session.activeWorkspaceId
        ? await execute({
          method: "workspace.switch",
          params: { targetWorkspaceId: workspaceId, openMode: "writable" },
        })
        : await execute({
          method: "workspace.open",
          params: { workspaceId, openMode: "writable" },
        });
      dependencies.showCenter.value = !opened;
    } finally {
      activationPending.value = false;
    }
    if (opened) dependencies.initializeConsumers();
    return opened;
  }

  return { activationPending, execute, open };
}
