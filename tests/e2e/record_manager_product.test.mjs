import assert from "node:assert/strict";
import test from "node:test";
import { setRecordManagerEnabled } from "./record_manager_product.mjs";

function harness({ enabled = true, change = null } = {}) {
  const before = { pluginId: "com.vibetable.examples.record-manager", installationId: "original-installation",
    packageHash: "original-package", status: enabled ? "disabled" : "enabled", revision: 7 };
  const after = { ...before, status: enabled ? "enabled" : "disabled", revision: 8,
    ...(change === "reinstall" ? { installationId: "replacement-installation" } : {}),
    ...(change === "revision" ? { revision: 10 } : {}) };
  const events = []; let committed = false, release, waiting;
  const started = new Promise(resolve => { waiting = resolve; });
  const terminal = new Promise(resolve => { release = () => { committed = true; events.push("lifecycle-terminal"); resolve(); }; });
  const page = {
    getByTestId(id) { assert.equal(id, "plugin-toggle"); return { async click() { events.push("toggle-click"); } }; },
    async waitForFunction(predicate, expected, options) {
      assert.equal(options.timeout, 30000);
      const savedDocument = globalThis.document, savedButton = globalThis.HTMLButtonElement;
      class Toggle { disabled = true; classList = { contains() { return enabled; } }; }
      const toggle = new Toggle();
      globalThis.HTMLButtonElement = Toggle; globalThis.document = { querySelector() { return toggle; } };
      try {
        // The navigation state has appeared, but the command is still busy.
        assert.equal(predicate(expected), false);
        events.push("waiting-for-lifecycle"); waiting();
        await terminal;
        toggle.disabled = false;
        assert.equal(predicate(expected), true);
      } finally { globalThis.document = savedDocument; globalThis.HTMLButtonElement = savedButton; }
    },
  };
  const raw = async (_page, method, params) => {
    assert.equal(method, "plugin.catalog.list"); assert.equal(params.projectKey, "workspace");
    events.push(committed ? "authority-after" : "authority-before");
    return { payload: [committed ? after : before] };
  };
  const recorder = { check(name, passed) { assert.ok(passed, name); } };
  return { events, started, release, run: () => setRecordManagerEnabled(page, recorder, raw, "workspace", enabled) };
}

for (const enabled of [true, false]) {
  test(`record-manager ${enabled ? "enable" : "disable"} waits past early navigation for the lifecycle receipt`, async () => {
    const h = harness({ enabled }); const pending = h.run();
    await h.started;
    assert.deepEqual(h.events, ["authority-before", "toggle-click", "waiting-for-lifecycle"]);
    h.release();
    const result = await pending;
    assert.equal(result.revision, 8);
    assert.equal(result.status, enabled ? "enabled" : "disabled");
    assert.deepEqual(h.events, ["authority-before", "toggle-click", "waiting-for-lifecycle", "lifecycle-terminal", "authority-after"]);
  });
}
for (const change of ["reinstall", "revision"]) {
  test(`record-manager rejects a completed ${change} outside the UI-approved lifecycle transition`, async () => {
    const h = harness({ change }); const pending = h.run(); await h.started; h.release();
    await assert.rejects(pending, /exact lifecycle revision/u);
  });
}
