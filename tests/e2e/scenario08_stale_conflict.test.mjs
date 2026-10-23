import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";
import { isDeepStrictEqual } from "node:util";

const source = await fs.readFile(new URL("./webview_product_scenarios.mjs", import.meta.url), "utf8");
const scenarioSource = source.slice(
  source.indexOf("async function scenario08("),
  source.indexOf("async function requestPackagedProcessKill("),
);
const captureSource = source.slice(
  source.indexOf("async function beginBridgeMessageCapture("),
  source.indexOf("async function beginWorkspaceV2MethodCapture("),
);

test("scene08 submits a real stale draft and requires its owned rejection plus unchanged authority", async () => {
  const field = "payload";
  const original = { id: "row-1", [field]: null, __vibetableDigest: "digest-before" };
  let authority = structuredClone(original);
  let draft = null;
  let modalOpen = false;
  let noticeVisible = false;
  let submitted = null;
  let competitorCommitted = false;
  let competitorRendered = false;
  // Match jsonValueFormatter: objects have a compact label and serialized JSON in the child title.
  const jsonSpan = { textContent: "—", title: "空值", getAttribute(name) { return name === "title" ? this.title : null; } };
  const renderedCell = {
    get textContent() { return jsonSpan.textContent; },
    getAttribute(name) { return name === "aria-haspopup" ? "dialog" : null; },
    querySelector(selector) { assert.equal(selector, ".vt-json-value"); return jsonSpan; },
  };
  const document = {
    querySelector(selector) {
      assert.equal(selector, `.tabulator-cell[tabulator-field="${field}"]`);
      return renderedCell;
    },
    querySelectorAll(selector) { return [this.querySelector(selector)]; },
  };
  const requests = [];
  const checks = [];
  const listeners = new Set();
  const window = {
    __vibetableE2EBridgeDiagnostics: { requests },
    chrome: { webview: {
      addEventListener: (_type, listener) => listeners.add(listener),
      removeEventListener: (_type, listener) => listeners.delete(listener),
    } },
  };
  const deliverRejection = requestId => {
    const message = { type: "table.editRejected", requestId,
      payload: { kind: "edit_conflict", operation: "updateCell" } };
    for (const listener of [...listeners]) listener({ data: message });
    // The product deliberately ignores unowned broadcasts.
    noticeVisible = requestId !== null && requests.some(item => item.requestId === requestId);
  };
  const cell = {
    first() { return this; },
    async waitFor() {},
    async press(key) {
      assert.equal(key, "Enter");
      assert.equal(competitorCommitted, false, "capture the user draft before the competing commit");
      draft = { originalValue: structuredClone(authority[field]), expectedDigest: authority.__vibetableDigest };
      modalOpen = true;
    },
  };
  const page = {
    locator: () => cell,
    getByTestId(id) {
      return {
        async click() {
          if (id !== "json-editor-save") return;
          assert.equal(modalOpen, true);
          assert.equal(competitorCommitted, true);
          assert.equal(competitorRendered, true, "wait for the authoritative row to render before stale save");
          assert.equal(draft.expectedDigest, original.__vibetableDigest);
          assert.deepEqual(draft.originalValue, original[field]);
          submitted = { requestId: "ui-owned-edit", requestType: "table.updateCellRequested" };
          requests.push(submitted);
          modalOpen = false;
          deliverRejection(submitted.requestId);
        },
        async fill(value) { assert.equal(modalOpen, true); draft.value = JSON.parse(value); },
        async inputValue() { return JSON.stringify(draft.value); },
        async waitFor(options) {
          if (id === "edit-rejection-notice") assert.equal(noticeVisible, true, "no UI-owned edit can display the conflict notice");
          if (id === "json-editor-modal") assert.equal(modalOpen, options?.state !== "hidden");
        },
        async innerText() { return "数据已在其他位置更新，请重新加载"; },
        async isVisible() { return id === "edit-rejection-reload" && noticeVisible; },
      };
    },
    async evaluate(fn, arg) { return vm.runInNewContext(`(${fn.toString()})(argument)`, { window, argument: arg }); },
    async waitForFunction(predicate, arg) {
      const evaluate = () => vm.runInNewContext(`(${predicate.toString()})(argument)`, { document, argument: arg });
      if (typeof arg === "string") {
        assert.equal(competitorCommitted, false);
        assert.equal(evaluate(), true, "the JSON cell exposes its editor");
        return;
      }
      assert.equal(competitorCommitted, true);
      assert.equal(evaluate(), false, "a committed but unrendered value must not release the wait");
      // A same-table authority refresh replaces the grid row, not the open modal's captured guard.
      assert.equal(authority.__vibetableDigest, "digest-after");
      jsonSpan.textContent = `{…} · ${Object.keys(authority[field]).length} 个键`;
      jsonSpan.title = JSON.stringify(authority[field]);
      assert.equal(renderedCell.textContent.includes("competitor-write"), false);
      assert.equal(evaluate(), true, "the real predicate must recognize the rendered JSON value");
      competitorRendered = true;
    },
  };
  const query = () => ({ payload: { rows: [structuredClone(authority)], snapshot: { schemaRevision: "schema-1" } } });
  const context = {
    window,
    isDeepStrictEqual: (a, b) => isDeepStrictEqual(JSON.parse(JSON.stringify(a)), JSON.parse(JSON.stringify(b))),
    waitForShell: async () => {},
    createSimpleTable: async () => ({ tableId: "items", field: { physicalName: field } }),
    createSingleFieldTable: async (_page, _name, _title, type) => {
      assert.equal(type, "json");
      return { tableId: "items", field: { physicalName: field } };
    },
    selectTable: async () => {},
    insertRowFromToolbar: async () => {},
    rawBridgeRequest: async (_page, type) => { assert.equal(type, "query.page"); return query(); },
    beginRawBridgeRequest: async (_page, type, payload) => {
      assert.equal(type, "mutation.apply");
      assert.equal(payload.operations[0].expectedDigest, original.__vibetableDigest);
      authority = { ...authority, ...payload.operations[0].values, __vibetableDigest: "digest-after" };
      competitorCommitted = true;
      return "external-request";
    },
    waitForRawBridgeRequest: async () => ({ payload: { status: "applied" } }),
    isAppliedMutationResponse: () => true,
    postRawBridgeNotification: async () => deliverRejection(null),
    waitForCapturedBridgeMessage: async () => {
      const message = window.__vibetableE2EBridgeCapture.message;
      assert.ok(message, "the UI action must produce a terminal");
      return message;
    },
  };
  const scenario = vm.runInNewContext(`${captureSource}\n${scenarioSource}\nscenario08`, context);
  await scenario(page, { check(name, passed) { checks.push(name); assert.equal(passed, true, name); } });
  assert.ok(submitted, "exercise the renderer's owned mutation rather than a raw notification");
  assert.deepEqual(draft.value, { value: "stale-user-write" });
  assert.deepEqual(JSON.parse(JSON.stringify(authority[field])), { value: "competitor-write" });
  assert.ok(checks.some(name => name.includes("authoritative")), "verify the competing value at the authority after rejection");
});
