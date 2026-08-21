export function classifyWorkspaceSearchObservation({ acceptedGeneration, state, generation }) {
  if (!Number.isInteger(acceptedGeneration) || !Number.isInteger(generation)) return "invalid";
  if (state === "building") return generation === acceptedGeneration ? "pending" : "invalid";
  if (state === "ready") return generation > acceptedGeneration ? "terminal" : "invalid";
  if (state === "failed" || state === "degraded") {
    return generation === acceptedGeneration ? "terminal" : "invalid";
  }
  return "invalid";
}
