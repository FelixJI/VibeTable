import {
  BridgeOperationError,
  BridgeRequestRetiredError,
  BridgeTimeoutError,
} from "@/bridge/hostBridge";
import type { MutationErrorPayload } from "@/contracts";
import { t } from "@/i18n";
import { WorkspaceV2RequestError } from "./workspaceV2HostAdapter";

/**
 * Keep bursty background failures from producing a stack of identical
 * notifications. Keys expire quickly so a later user-initiated retry still
 * receives feedback.
 */
export function createNotificationDeduper(
  windowMs = 3_000,
  now: () => number = Date.now,
): (key: string) => boolean {
  const shownAt = new Map<string, number>();
  return (key: string): boolean => {
    const timestamp = now();
    const previous = shownAt.get(key);
    if (previous !== undefined && timestamp - previous < windowMs) return false;
    shownAt.set(key, timestamp);
    return true;
  };
}

export function relationLookupNoticeKey(error: unknown): string {
  if (error instanceof BridgeTimeoutError) return "relation-lookup:timeout";
  if (error instanceof BridgeOperationError) {
    return `relation-lookup:${error.code ?? "operation"}`;
  }
  return "relation-lookup:generic";
}

/** Map bridge details to stable, localized UI copy; never expose raw messages. */
export function relationLookupErrorMessage(error: unknown): string | null {
  // A locally retired lookup belongs to a workspace the renderer already
  // left; it is not a user-facing failure.
  if (error instanceof BridgeRequestRetiredError) return null;
  if (error instanceof BridgeTimeoutError) {
    return t("workspace.notification.relationLookupTimeout");
  }
  if (error instanceof BridgeOperationError) {
    if (error.code === "CANCELLED") return null;
    if (error.code === "RELATION_LOOKUP_FAILED") {
      return t("workspace.notification.relationLookupFailed");
    }
    return t("workspace.notification.operationFailed");
  }
  return t("workspace.notification.relationLookupFailed");
}

/** Map workspace topology/provider failures to stable, actionable UI copy. */
export function workspaceV2ErrorMessage(error: unknown): string {
  if ((error instanceof BridgeOperationError || error instanceof WorkspaceV2RequestError)
    && error.code === "filehistory.resource_limit") {
    return t("workspaceV2.error.fileHistoryResourceLimit");
  }
  if (error instanceof BridgeOperationError) {
    if (error.code === "workspace.storage_requires_mirrored") {
      return t("workspaceV2.error.storageRequiresMirrored");
    }
    if (error.code === "workspace.network_protocol_unsupported") {
      return t("workspaceV2.error.networkProtocolUnsupported");
    }
  }
  return error instanceof Error ? error.message : String(error);
}

export function mutationRejectionMessage(error: MutationErrorPayload): string {
  switch (error.kind) {
    case "edit_conflict":
      return t("workspace.editRejected.conflict");
    case "schema_mismatch":
      return t("workspace.editRejected.schema");
    case "mutation_validation":
      return t("workspace.editRejected.validation");
    case "not_writable":
      return t("workspace.editRejected.notWritable");
    case "backend_unavailable":
      return t("workspace.editRejected.backendUnavailable");
    default:
      return t("workspace.editRejected.generic");
  }
}
