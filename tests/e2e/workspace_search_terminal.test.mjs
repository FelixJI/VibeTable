import assert from "node:assert/strict";
import test from "node:test";
import vm from "node:vm";

import { classifyWorkspaceSearchObservation, waitForWorkspaceSearchRebuildTerminal } from "./workspace_search_terminal.mjs";

function observationPage(observations) {
  let current = 0;
  const element = {
    getAttribute(name) {
      const observation = observations[Math.min(current, observations.length - 1)];
      return name === "data-state" ? observation.state
        : observation.generation == null ? null : String(observation.generation);
    },
  };
  return {
    async waitForFunction(predicate, input) {
      // Execute the actual browser predicate in its own global scope, as
      // Playwright does. Replay observed DOM states without wall-clock sleeps.
      for (; current < observations.length; current += 1) {
        const result = vm.runInNewContext(`(${predicate.toString()})(input)`, {
          input, document: { querySelector: () => element },
        });
        if (result) return;
      }
      throw new Error("page.waitForFunction: Timeout; observed states exhausted");
    },
    getByTestId() {
      return { locator: () => ({ evaluate: async (callback) => callback(element) }) };
    },
  };
}

test("accepts a published terminal already rendered when rebuild response capture returns", async () => {
  const accepted = { state: "building", generation: 15 };
  const terminal = await waitForWorkspaceSearchRebuildTerminal(
    observationPage([{ state: "ready", generation: 16 }]), accepted,
  );
  assert.deepEqual(terminal, { state: "ready", generation: 16, accepted });
});

test("continues through an observed building state to its published terminal", async () => {
  const accepted = { state: "building", generation: 15 };
  const terminal = await waitForWorkspaceSearchRebuildTerminal(observationPage([
    accepted, { state: "ready", generation: 16 },
  ]), accepted);
  assert.equal(terminal.generation, 16);
});

test("does not accept an old ready frame or a mismatched failure as this rebuild's terminal", async () => {
  for (const observation of [
    { state: "ready", generation: 14 },
    { state: "ready", generation: 15 },
    { state: "failed", generation: 16 },
    { state: "degraded", generation: 14 },
    { state: "building", generation: 16 },
    { state: "ready", generation: Number.NaN },
  ]) {
    await assert.rejects(waitForWorkspaceSearchRebuildTerminal(
      observationPage([observation]), { state: "building", generation: 15 },
    ), /Timeout/, JSON.stringify(observation));
  }
});

test("waits past a stale ready frame and returns matching failure without treating it as success", async () => {
  const accepted = { state: "building", generation: 15 };
  for (const state of ["failed", "degraded"]) {
    const terminal = await waitForWorkspaceSearchRebuildTerminal(observationPage([
      { state: "ready", generation: 15 }, { state, generation: 15 },
    ]), accepted);
    assert.deepEqual(terminal, { state, generation: 15, accepted });
  }
});

test("does not coerce a missing DOM generation to accepted generation zero", async () => {
  await assert.rejects(waitForWorkspaceSearchRebuildTerminal(
    observationPage([{ state: "failed", generation: null }]),
    { state: "building", generation: 0 },
  ), /Timeout/);
});

test("ready belongs only to a generation published after rebuild acceptance", () => {
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "ready",
    generation: 8,
  }), "invalid");
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "ready",
    generation: 9,
  }), "terminal");
});

test("failed and degraded may terminate the accepted generation", () => {
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "failed",
    generation: 8,
  }), "terminal");
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "degraded",
    generation: 9,
  }), "invalid");
});

test("rejects older, non-terminal, and malformed observations", () => {
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "failed",
    generation: 7,
  }), "invalid");
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "building",
    generation: 8,
  }), "pending");
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "building",
    generation: 9,
  }), "invalid");
  assert.equal(classifyWorkspaceSearchObservation({
    acceptedGeneration: 8,
    state: "ready",
    generation: Number.NaN,
  }), "invalid");
});
