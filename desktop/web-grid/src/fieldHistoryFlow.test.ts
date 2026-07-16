/**
 * Tests for the G1 fieldHistoryFlow state machine.
 */
import { describe, it, assert } from "vitest";
import {
  applied,
  confirmRestore,
  initialFieldHistoryModel,
  loaded,
  loadHistory,
  previewReady,
  requestPreview,
  reset,
  startApply,
  startPreview,
  startRead,
} from "./fieldHistoryFlow";
import type {
  HistoryPage,
  RestorePreview,
  RestoreResult,
} from "./contracts";

function makePage(): HistoryPage {
  return {
    collection: "vibetable_contracts",
    itemId: "c-001",
    changeSets: [
      {
        rootRevisionId: "rev-10",
        activityId: "act-10",
        action: "update",
        timestamp: "2026-07-15T10:00:00Z",
        actor: { userId: "u-1", displayName: "Ada" },
        scalarChanges: [{ field: "title", before: "Old", after: "New" }],
        relationChanges: [],
      },
    ],
    total: 1,
    capabilityHash: "hash",
    schemaRevision: "vibetable-g1.0",
  };
}

function makePreview(diagnostics: RestorePreview["diagnostics"] = []): RestorePreview {
  return {
    collection: "vibetable_contracts",
    itemId: "c-001",
    targetRevision: "rev-9",
    currentHash: "hash",
    schemaRevision: "vibetable-g1.0",
    scalarChanges: [{ field: "title", before: "New", after: "Old" }],
    relationChanges: [],
    diagnostics,
    token: "rst-token",
    expiresAt: "2026-07-15T10:05:00Z",
  };
}

function makeResult(): RestoreResult {
  return {
    collection: "vibetable_contracts",
    itemId: "c-001",
    restoredToRevision: "rev-9",
    newRevisionId: "rev-11",
    item: { title: "Old" },
  };
}

describe("fieldHistoryFlow transitions", () => {
  it("startRead transitions to loading", () => {
    const model = startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001");
    assert.equal(model.state, "loading");
    assert.equal(model.collection, "vibetable_contracts");
  });

  it("loaded transitions to loaded with page", () => {
    const model = loaded(startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001"), makePage());
    assert.equal(model.state, "loaded");
    assert.equal(model.page?.changeSets.length, 1);
  });

  it("startPreview requires loaded state", () => {
    const model = startPreview(initialFieldHistoryModel, "rev-9");
    assert.equal(model.state, "error");
  });

  it("startPreview from loaded transitions to previewing", () => {
    const loaded_model = loaded(startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001"), makePage());
    const model = startPreview(loaded_model, "rev-9");
    assert.equal(model.state, "previewing");
  });

  it("previewReady with no error diagnostics transitions to previewReady", () => {
    const previewing = startPreview(
      loaded(startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001"), makePage()),
      "rev-9",
    );
    const model = previewReady(previewing, makePreview([]));
    assert.equal(model.state, "previewReady");
  });

  it("previewReady with error diagnostics transitions to error", () => {
    const previewing = startPreview(
      loaded(startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001"), makePage()),
      "rev-9",
    );
    const model = previewReady(
      previewing,
      makePreview([
        { field: "legacy", classification: "schema_retired", severity: "error", code: "deleted", message: "Field deleted" },
      ]),
    );
    assert.equal(model.state, "error");
    assert.include(model.errorMessage ?? "", "Field deleted");
  });

  it("startApply requires previewReady", () => {
    const model = startApply(initialFieldHistoryModel);
    assert.equal(model.state, "error");
  });

  it("applied transitions to applied", () => {
    const ready = previewReady(
      startPreview(loaded(startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001"), makePage()), "rev-9"),
      makePreview([]),
    );
    const model = applied(ready, makeResult());
    assert.equal(model.state, "applied");
    assert.equal(model.result?.newRevisionId, "rev-11");
  });

  it("reset returns to idle", () => {
    const model = reset(loaded(startRead(initialFieldHistoryModel, "vibetable_contracts", "c-001"), makePage()));
    assert.equal(model.state, "idle");
  });
});

describe("fieldHistoryFlow async orchestration", () => {
  it("loadHistory succeeds", async () => {
    const requesters = {
      readHistory: async () => makePage(),
      previewRestore: async () => makePreview(),
      applyRestore: async () => makeResult(),
    };
    const model = await loadHistory(initialFieldHistoryModel, requesters, "vibetable_contracts", "c-001");
    assert.equal(model.state, "loaded");
  });

  it("loadHistory fails gracefully", async () => {
    const requesters = {
      readHistory: async () => {
        throw new Error("network error");
      },
      previewRestore: async () => makePreview(),
      applyRestore: async () => makeResult(),
    };
    const model = await loadHistory(initialFieldHistoryModel, requesters, "vibetable_contracts", "c-001");
    assert.equal(model.state, "error");
    assert.include(model.errorMessage ?? "", "network error");
  });

  it("requestPreview succeeds from loaded", async () => {
    const requesters = {
      readHistory: async () => makePage(),
      previewRestore: async () => makePreview([]),
      applyRestore: async () => makeResult(),
    };
    const loadedModel = await loadHistory(initialFieldHistoryModel, requesters, "vibetable_contracts", "c-001");
    const model = await requestPreview(loadedModel, requesters, "rev-9");
    assert.equal(model.state, "previewReady");
  });

  it("confirmRestore succeeds from previewReady", async () => {
    const requesters = {
      readHistory: async () => makePage(),
      previewRestore: async () => makePreview([]),
      applyRestore: async () => makeResult(),
    };
    const loadedModel = await loadHistory(initialFieldHistoryModel, requesters, "vibetable_contracts", "c-001");
    const previewModel = await requestPreview(loadedModel, requesters, "rev-9");
    const model = await confirmRestore(previewModel, requesters);
    assert.equal(model.state, "applied");
  });

  it("confirmRestore detects conflict", async () => {
    const requesters = {
      readHistory: async () => makePage(),
      previewRestore: async () => makePreview([]),
      applyRestore: async () => {
        throw new Error("item changed since preview (conflict)");
      },
    };
    const loadedModel = await loadHistory(initialFieldHistoryModel, requesters, "vibetable_contracts", "c-001");
    const previewModel = await requestPreview(loadedModel, requesters, "rev-9");
    const model = await confirmRestore(previewModel, requesters);
    assert.equal(model.state, "conflict");
  });
});
