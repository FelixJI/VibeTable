export function ownsWorkspaceSearchTerminal({ acceptedGeneration, state, generation }) {
  if (!Number.isInteger(acceptedGeneration) || !Number.isInteger(generation)) return false;
  if (state === "ready") return generation > acceptedGeneration;
  if (state === "failed" || state === "degraded") return generation >= acceptedGeneration;
  return false;
}
