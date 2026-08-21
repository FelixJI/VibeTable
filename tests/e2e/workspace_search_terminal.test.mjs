import assert from "node:assert/strict";
import test from "node:test";

import { classifyWorkspaceSearchObservation } from "./workspace_search_terminal.mjs";

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
