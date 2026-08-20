import assert from "node:assert/strict";
import test from "node:test";

import { ownsWorkspaceSearchTerminal } from "./workspace_search_terminal.mjs";

test("ready belongs only to a generation published after rebuild acceptance", () => {
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "ready",
    generation: 8,
  }), false);
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "ready",
    generation: 9,
  }), true);
});

test("failed and degraded may terminate the accepted generation", () => {
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "failed",
    generation: 8,
  }), true);
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "degraded",
    generation: 9,
  }), true);
});

test("rejects older, non-terminal, and malformed observations", () => {
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "failed",
    generation: 7,
  }), false);
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "building",
    generation: 9,
  }), false);
  assert.equal(ownsWorkspaceSearchTerminal({
    acceptedGeneration: 8,
    state: "ready",
    generation: Number.NaN,
  }), false);
});
