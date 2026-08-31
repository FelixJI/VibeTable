import assert from "node:assert/strict";
import test from "node:test";

import {
  captureDialogFocusLeaseInPage,
  hasDialogFocusLeaseRestoredFocusInPage,
  hasDialogFocusLeaseTerminalInPage,
  readDialogFocusLeaseEvidenceInPage,
} from "./dialog_focus_terminal.mjs";

function withDialogFocus(events, run) {
  globalThis.window = {
    __vibetableE2EBridgeDiagnostics: {
      dialogFocus: {
        cursor: events.at(-1)?.cursor ?? 0,
        events,
      },
    },
  };
  try {
    return run(window.__vibetableE2EBridgeDiagnostics.dialogFocus);
  } finally {
    delete globalThis.window;
  }
}

function expectStructuredFailure(run, code, reason = undefined) {
  assert.throws(run, (error) => {
    const failure = JSON.parse(error.message);
    assert.equal(failure.code, code);
    if (reason !== undefined) assert.equal(failure.reason, reason);
    return true;
  });
}

test("captures the latest still-open claimed lease for the requested dialog target", () => {
  withDialogFocus([
    { cursor: 1, leaseId: 1, state: "claimed", target: "json" },
    { cursor: 2, leaseId: 1, state: "released", target: "json" },
    { cursor: 3, leaseId: 1, state: "restored", via: "captured", target: "json" },
    { cursor: 4, leaseId: 2, state: "claimed", target: "attachment" },
    { cursor: 5, leaseId: 3, state: "claimed", target: "json" },
  ], () => {
    assert.deepEqual(captureDialogFocusLeaseInPage({ operation: "capture", target: "json" }), {
      cursor: 5,
      leaseId: 3,
      target: "json",
    });
  });
});

test("rejects a latest completed claim instead of falling back to an older open claim", () => {
  withDialogFocus([
    { cursor: 1, leaseId: 1, state: "claimed", target: "json" },
    { cursor: 2, leaseId: 2, state: "claimed", target: "json" },
    { cursor: 3, leaseId: 2, state: "released", target: "json" },
    { cursor: 4, leaseId: 2, state: "restored", via: "captured", target: "json" },
  ], () => {
    expectStructuredFailure(
      () => captureDialogFocusLeaseInPage({ operation: "capture", target: "json" }),
      "DIALOG_FOCUS_LEASE_NOT_OPEN",
    );
  });
});

test("rejects a latest released claim that has not reached a terminal", () => {
  withDialogFocus([
    { cursor: 1, leaseId: 7, state: "claimed", target: "json" },
    { cursor: 2, leaseId: 7, state: "released", target: "json" },
  ], () => {
    expectStructuredFailure(
      () => captureDialogFocusLeaseInPage({ operation: "capture", target: "json" }),
      "DIALOG_FOCUS_LEASE_NOT_OPEN",
    );
  });
});

for (const { label, events } of [
  {
    label: "duplicate claim for the same target",
    events: [
      { cursor: 1, leaseId: 7, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 7, state: "claimed", target: "json" },
    ],
  },
  {
    label: "reuse after a completed lease",
    events: [
      { cursor: 1, leaseId: 7, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 7, state: "released", target: "json" },
      { cursor: 3, leaseId: 7, state: "restored", via: "captured", target: "json" },
      { cursor: 4, leaseId: 7, state: "claimed", target: "json" },
    ],
  },
  {
    label: "reuse across dialog targets",
    events: [
      { cursor: 1, leaseId: 7, state: "claimed", target: "attachment" },
      { cursor: 2, leaseId: 7, state: "cancelled", reason: "stale", target: "attachment" },
      { cursor: 3, leaseId: 7, state: "claimed", target: "json" },
    ],
  },
]) {
  test(`rejects visible lease identity reuse from ${label}`, () => {
    withDialogFocus(events, () => {
      expectStructuredFailure(
        () => captureDialogFocusLeaseInPage({ operation: "capture", target: "json" }),
        "DIALOG_FOCUS_LEASE_ID_REUSED",
      );
    });
  });
}

test("accepts a released pending chain and waits for its single restored terminal", () => {
  const capture = { cursor: 1, leaseId: 7, target: "json" };
  const events = [
    { cursor: 1, leaseId: 7, state: "claimed", target: "json" },
    { cursor: 2, leaseId: 7, state: "released", target: "json" },
    { cursor: 3, leaseId: 7, state: "pending", reason: "row", target: "json" },
    { cursor: 4, leaseId: 7, state: "pending", reason: "cell", target: "json" },
  ];
  withDialogFocus(events, (dialogFocus) => {
    assert.equal(hasDialogFocusLeaseTerminalInPage({
      operation: "has-terminal",
      capture,
    }), false);
    dialogFocus.cursor = 5;
    dialogFocus.events.push({
      cursor: 5,
      leaseId: 7,
      state: "restored",
      via: "reprojected",
      target: "json",
    });
    assert.equal(hasDialogFocusLeaseTerminalInPage({
      operation: "has-terminal",
      capture,
    }), true);
    assert.deepEqual(readDialogFocusLeaseEvidenceInPage({
      operation: "read-evidence",
      capture,
    }), {
      capture,
      events: dialogFocus.events,
      terminal: dialogFocus.events.at(-1),
    });
  });
});

test("requires restored lease evidence and the current logical cell to own DOM focus atomically", () => {
  const capture = { cursor: 1, leaseId: 7, target: "json" };
  const cell = () => ({
    isConnected: true,
    tagName: "DIV",
    className: "tabulator-cell vt-json-cell vt-structured-cell",
    getAttribute: (name) => ({
      "tabulator-field": "payload",
      role: "gridcell",
      "data-testid": null,
    })[name] ?? null,
  });
  const oldTarget = cell();
  const currentTarget = cell();
  const oldRoot = { isConnected: true, querySelectorAll: () => [oldTarget] };
  const currentRoot = { isConnected: true, querySelectorAll: () => [currentTarget] };
  let roots = [oldRoot, currentRoot];
  globalThis.document = {
    activeElement: oldTarget,
    hasFocus: () => true,
    querySelectorAll: (selector) => selector === ".grid-host > .tabulator-mount.tabulator"
      ? roots
      : roots.flatMap((root) => root.querySelectorAll()),
  };
  try {
    withDialogFocus([
      { cursor: 1, leaseId: 7, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 7, state: "released", target: "json" },
      { cursor: 3, leaseId: 7, state: "restored", via: "reprojected", target: "json" },
    ], () => {
      assert.equal(hasDialogFocusLeaseRestoredFocusInPage({
        operation: "has-restored-focus",
        capture,
        field: "payload",
        occurrence: 0,
      }), false);
      roots = [currentRoot];
      assert.equal(hasDialogFocusLeaseRestoredFocusInPage({
        operation: "has-restored-focus",
        capture,
        field: "payload",
        occurrence: 0,
      }), false);
      document.activeElement = currentTarget;
      assert.deepEqual(hasDialogFocusLeaseRestoredFocusInPage({
        operation: "has-restored-focus",
        capture,
        field: "payload",
        occurrence: 0,
      }), {
        restored: true,
        documentHasFocus: true,
        activeTag: "DIV",
        activeClass: "tabulator-cell vt-json-cell vt-structured-cell",
        activeField: "payload",
        activeRole: "gridcell",
        activeTestId: null,
      });
      document.hasFocus = () => false;
      assert.equal(hasDialogFocusLeaseRestoredFocusInPage({
        operation: "has-restored-focus",
        capture,
        field: "payload",
        occurrence: 0,
      }), false);
    });
  } finally {
    delete globalThis.document;
  }
});

test("throws a structured failure as soon as the captured lease is cancelled", () => {
  const capture = { cursor: 1, leaseId: 7, target: "json" };
  withDialogFocus([
    { cursor: 1, leaseId: 7, state: "claimed", target: "json" },
    { cursor: 2, leaseId: 7, state: "released", target: "json" },
    { cursor: 3, leaseId: 7, state: "pending", reason: "row", target: "json" },
    { cursor: 4, leaseId: 7, state: "cancelled", reason: "scope", target: "json" },
  ], () => {
    expectStructuredFailure(
      () => hasDialogFocusLeaseTerminalInPage({ operation: "has-terminal", capture }),
      "DIALOG_FOCUS_LEASE_CANCELLED",
      "scope",
    );
  });
});

for (const { label, events } of [
  {
    label: "directly after claim",
    events: [
      { cursor: 1, leaseId: 11, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 11, state: "cancelled", reason: "window", target: "json" },
    ],
  },
  {
    label: "after release and pending reprojection",
    events: [
      { cursor: 1, leaseId: 11, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 11, state: "released", target: "json" },
      { cursor: 3, leaseId: 11, state: "pending", reason: "row", target: "json" },
      { cursor: 4, leaseId: 11, state: "cancelled", reason: "scope", target: "json" },
    ],
  },
]) {
  test(`accepts cancellation ${label} and reports it as the terminal failure`, () => {
    const capture = { cursor: 1, leaseId: 11, target: "json" };
    withDialogFocus(events, () => {
      for (const [operation, inspect] of [
        ["has-terminal", hasDialogFocusLeaseTerminalInPage],
        ["read-evidence", readDialogFocusLeaseEvidenceInPage],
      ]) {
        expectStructuredFailure(
          () => inspect({ operation, capture }),
          "DIALOG_FOCUS_LEASE_CANCELLED",
          events.at(-1).reason,
        );
      }
    });
  });
}

test("rebuilds capture evidence without caller-supplied business or path fields", () => {
  const suppliedCapture = {
    cursor: 1,
    leaseId: 12,
    target: "json",
    rowKey: "private-row",
    message: "private-message",
    path: "C:\\private\\workspace",
  };
  withDialogFocus([
    { cursor: 1, leaseId: 12, state: "claimed", target: "json" },
    { cursor: 2, leaseId: 12, state: "released", target: "json" },
    { cursor: 3, leaseId: 12, state: "restored", via: "captured", target: "json" },
  ], () => {
    assert.equal(hasDialogFocusLeaseTerminalInPage({
      operation: "has-terminal",
      capture: suppliedCapture,
    }), true);
    const evidence = readDialogFocusLeaseEvidenceInPage({
      operation: "read-evidence",
      capture: suppliedCapture,
    });
    assert.deepEqual(evidence.capture, { cursor: 1, leaseId: 12, target: "json" });
    const artifact = JSON.stringify(evidence);
    assert.equal(artifact.includes("private-row"), false);
    assert.equal(artifact.includes("private-message"), false);
    assert.equal(artifact.includes("private\\workspace"), false);
  });
});

for (const { label, reason, events } of [
  {
    label: "pending before release",
    reason: "PENDING_BEFORE_RELEASE",
    events: [
      { cursor: 1, leaseId: 9, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 9, state: "pending", reason: "row", target: "json" },
    ],
  },
  {
    label: "terminal before release",
    reason: "TERMINAL_BEFORE_RELEASE",
    events: [
      { cursor: 1, leaseId: 9, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 9, state: "restored", via: "captured", target: "json" },
    ],
  },
  {
    label: "duplicate release",
    reason: "RELEASE_DUPLICATE",
    events: [
      { cursor: 1, leaseId: 9, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 9, state: "released", target: "json" },
      { cursor: 3, leaseId: 9, state: "released", target: "json" },
    ],
  },
  {
    label: "two restored terminals",
    reason: "TERMINAL_DUPLICATE",
    events: [
      { cursor: 1, leaseId: 9, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 9, state: "released", target: "json" },
      { cursor: 3, leaseId: 9, state: "restored", via: "captured", target: "json" },
      { cursor: 4, leaseId: 9, state: "restored", via: "reprojected", target: "json" },
    ],
  },
  {
    label: "restored then cancelled terminals",
    reason: "TERMINAL_DUPLICATE",
    events: [
      { cursor: 1, leaseId: 9, state: "claimed", target: "json" },
      { cursor: 2, leaseId: 9, state: "released", target: "json" },
      { cursor: 3, leaseId: 9, state: "restored", via: "captured", target: "json" },
      { cursor: 4, leaseId: 9, state: "cancelled", reason: "external", target: "json" },
    ],
  },
]) {
  test(`rejects ${label} in both terminal and evidence reads`, () => {
    const capture = { cursor: 1, leaseId: 9, target: "json" };
    withDialogFocus(events, () => {
      for (const [operation, inspect] of [
        ["has-terminal", hasDialogFocusLeaseTerminalInPage],
        ["read-evidence", readDialogFocusLeaseEvidenceInPage],
      ]) {
        expectStructuredFailure(
          () => inspect({ operation, capture }),
          "DIALOG_FOCUS_LEASE_SEQUENCE_INVALID",
          reason,
        );
      }
    });
  });
}
