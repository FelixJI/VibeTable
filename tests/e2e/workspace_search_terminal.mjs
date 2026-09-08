import { classifyWorkspaceSearchObservation } from "../../desktop/web-grid/src/search/workspaceSearchLifecycle.mjs";

export { classifyWorkspaceSearchObservation };

export async function waitForWorkspaceSearchRebuildTerminal(page, accepted, timeout = 120_000) {
  // Rebuild acceptance already proves entry into building. DOM observation may
  // start after publication, so require the matching terminal, not a past frame.
  await page.waitForFunction(
    ({ expectedGeneration }) => {
      const index = document.querySelector(".index-state");
      const state = index?.getAttribute("data-state");
      const rawGeneration = index?.getAttribute("data-generation");
      if (rawGeneration == null || rawGeneration.trim() === "") return false;
      const generation = Number(rawGeneration);
      if (!Number.isInteger(generation)) return false;
      if (state === "ready") return generation > expectedGeneration;
      return (state === "failed" || state === "degraded") && generation === expectedGeneration;
    },
    { expectedGeneration: accepted.generation },
    { timeout },
  );
  const terminal = await page.getByTestId("workspace-search-view")
    .locator(".index-state")
    .evaluate((element) => ({
      state: element.getAttribute("data-state"),
      generation: Number(element.getAttribute("data-generation")),
    }));
  if (classifyWorkspaceSearchObservation({
    acceptedGeneration: accepted.generation,
    ...terminal,
  }) !== "terminal") {
    throw new Error(
      `WorkspaceSearch terminal does not belong to accepted rebuild: ${JSON.stringify({
        accepted,
        terminal,
      })}`,
    );
  }
  return { ...terminal, accepted };
}
