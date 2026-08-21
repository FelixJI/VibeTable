import type { SearchStatus } from "@/contracts/generated/workbench";

export type WorkspaceSearchObservationRelation = "pending" | "terminal" | "invalid";

export interface WorkspaceSearchObservation {
  acceptedGeneration: number;
  state: SearchStatus["state"] | string | null;
  generation: number;
}

export function classifyWorkspaceSearchObservation(
  observation: WorkspaceSearchObservation,
): WorkspaceSearchObservationRelation;
