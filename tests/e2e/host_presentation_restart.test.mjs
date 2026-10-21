import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "../../desktop/web-grid/node_modules/jsdom/lib/api.js";

// The packaged runner has a CLI entry point. Exercise its small async seam
// without importing that entry point or starting a browser/Host.
const source = fs.readFileSync(new URL("./webview_product_scenarios.mjs", import.meta.url), "utf8");
const extract = (start, end) => source.slice(source.indexOf(start), source.indexOf(end));
const openNaturalAgingWorkspaceInPage = eval(`(${extract(
  "function openNaturalAgingWorkspaceInPage", "async function resumeNaturalRetentionAging",
)})`);
const activateRestartedWorkspaceSource = extract(
  "async function activateRestartedWorkspace", "async function main",
);

function restartedWorkspaceHarness({ start = "home", switches }) {
  const workspaceId = "11111111-1111-4111-8111-111111111111";
  const evidence = { switchCalls: 0, cardWaits: 0, cardClicks: 0, sessionWaits: 0 };
  let releaseSessionWait = null;
  const rawWorkspaceV2Request = async (_page, method, params) => {
    assert.equal(method, "workspace.switch");
    assert.deepEqual(params, { targetWorkspaceId: workspaceId, openMode: "writable" });
    const outcome = switches[Math.min(evidence.switchCalls, switches.length - 1)];
    evidence.switchCalls += 1;
    if (outcome instanceof Error) throw outcome;
    return outcome;
  };
  const hasActiveNaturalAgingWorkspaceSessionInPage = () => {
    evidence.sessionWaits += 1;
    return false;
  };
  const openNaturalAgingWorkspaceInPageForRestart = () => {
    evidence.cardClicks += 1;
    releaseSessionWait?.();
    return true;
  };
  const page = {
    getByTestId(id) {
      if (id === "workspace-center" || id === "home-view") {
        const visible = id === (start === "center" ? "workspace-center" : "home-view");
        return { waitFor: () => visible
          ? Promise.resolve()
          // A hidden element's visibility wait stays pending, never rejects:
          // the product race is won by whichever view is actually shown.
          : new Promise(() => {}) };
      }
      assert.equal(id, `workspace-delete-${workspaceId}`);
      return { waitFor: async () => {
        evidence.cardWaits += 1;
      } };
    },
    waitForFunction(callback, argument) {
      assert.equal(callback, hasActiveNaturalAgingWorkspaceSessionInPage);
      assert.equal(argument, undefined);
      return new Promise(resolve => {
        releaseSessionWait = resolve;
      });
    },
    async evaluate(callback, argument) {
      assert.equal(callback, openNaturalAgingWorkspaceInPage);
      assert.equal(argument, `workspace-delete-${workspaceId}`);
      return openNaturalAgingWorkspaceInPageForRestart();
    },
  };
  const activateRestartedWorkspace = eval(`(${activateRestartedWorkspaceSource})`);
  return {
    page,
    workspaceId,
    evidence,
    run: () => activateRestartedWorkspace(page, workspaceId),
  };
}

test("restarted workspace activation binds the UUID through the direct switch response", async () => {
  const switched = { result: { workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 2, state: "openedWritable" } };
  const harness = restartedWorkspaceHarness({ switches: [switched] });
  const outcome = await harness.run();
  assert.equal(outcome.start, "home");
  assert.deepEqual(outcome.switched, switched);
  assert.equal(harness.evidence.switchCalls, 1);
  assert.equal(harness.evidence.sessionWaits, 0);
  assert.equal(harness.evidence.cardWaits, 0);
  assert.equal(harness.evidence.cardClicks, 0);
});

test("restarted workspace activation opens the UUID card once when no session exists yet", async () => {
  const noSession = new Error('workspace.switch failed closed: {"code":"workspace.session_required"}');
  const switched = { result: { workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 1, state: "openedWritable" } };
  const harness = restartedWorkspaceHarness({ start: "center", switches: [noSession, switched] });
  const outcome = await harness.run();
  assert.equal(outcome.start, "center");
  assert.deepEqual(outcome.switched, switched);
  assert.equal(harness.evidence.switchCalls, 2);
  assert.equal(harness.evidence.cardWaits, 1);
  assert.equal(harness.evidence.cardClicks, 1);
});

test("restarted workspace activation propagates an unknown switch failure without retry", async () => {
  const failure = new Error('workspace.switch failed closed: {"code":"workspace.operation_failed"}');
  const harness = restartedWorkspaceHarness({ switches: [failure] });
  await assert.rejects(harness.run(), /workspace.operation_failed/);
  assert.equal(harness.evidence.switchCalls, 1);
  assert.equal(harness.evidence.cardWaits, 0);
  assert.equal(harness.evidence.cardClicks, 0);
});

async function resumeDom(mutate = () => {}) {
  const expected = {
    workspaceId: "workspace", tableId: "table", revision: "saved-revision",
    fields: { title: "title", status: "status", first: "first", second: "second" },
    state: {
      keyword: "preserved", density: "compact",
      columns: [
        { name: "title", width: 280, order: 0, frozen: true, visible: true },
        { name: "status", width: 100, order: 1, frozen: false, visible: false },
        { name: "second", width: 100, order: 2, frozen: false, visible: true },
        { name: "first", width: 100, order: 3, frozen: false, visible: true },
      ],
      sorts: [{ field: "title", direction: "asc" }],
      filters: [
        { field: "title", operator: "eq", value: "preserved" },
        { field: "status", operator: "eq", value: "", logic: "OR" },
      ],
    },
  };
  const dom = new JSDOM(`
    <button data-testid="nav-tables"></button>
    <div data-testid="view-keyword"><input value="preserved"></div>
    <div data-testid="view-query-controls" aria-busy="false"></div>
    <div data-testid="view-density">紧凑</div>
    <button data-testid="view-filter-trigger"></button>
    <div class="grid-wrapper density-compact" aria-busy="false">
      <div class="tabulator-header"><div class="tabulator-headers">
        <div class="tabulator-col tabulator-frozen" tabulator-field="title" aria-sort="ascending" data-width="280"><div class="tabulator-arrow"></div></div>
        <div class="tabulator-col" tabulator-field="status" style="display:none" data-width="100"></div>
        <div class="tabulator-col" tabulator-field="second" aria-sort="none" data-width="100"></div>
        <div class="tabulator-col" tabulator-field="first" aria-sort="none" data-width="100"></div>
      </div></div>
    </div>
    <div class="control-card--wide">
      <div class="filter-node"><span class="field-select">Title</span><span class="operator-select">等于</span><div class="value-input"><input value="preserved"></div></div>
      <div class="filter-node"><span class="joiner-select">或</span><span class="field-select">Status</span><span class="operator-select">等于</span><div class="value-input"><input value=""></div></div>
    </div>`);
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.getComputedStyle = dom.window.getComputedStyle.bind(dom.window);
  // JSDOM supplies DOM semantics, not layout. Supply only browser geometry.
  dom.window.HTMLElement.prototype.getBoundingClientRect = function () {
    const hidden = this.style.display === "none";
    return { width: hidden ? 0 : Number(this.dataset.width ?? 100), height: hidden ? 0 : 30 };
  };
  mutate(document);
  class Locator {
    constructor(elements) { this.elements = elements; }
    first() { return new Locator(this.elements.slice(0, 1)); }
    locator(selector) { return new Locator(this.elements.flatMap(element => [...element.querySelectorAll(selector)])); }
    async inputValue() { return this.elements[0].value; }
    async innerText() { return this.elements[0].textContent; }
    async isVisible() { return (this.elements[0]?.getBoundingClientRect().width ?? 0) > 0; }
    async evaluateAll(callback) { return callback(this.elements); }
    async click() {}
  }
  const page = {
    locator: selector => new Locator([...document.querySelectorAll(selector.replaceAll(":visible", ""))]),
    getByTestId: id => new Locator([...document.querySelectorAll(`[data-testid="${id}"]`)]),
    evaluate: async (callback, argument) => callback(argument),
    waitForFunction: async (callback, argument) => assert.ok(callback(argument), "readiness condition"),
  };
  const recorder = { check(name, passed) { assert.ok(passed, name); } };
  const fs = { readFile: async () => JSON.stringify(expected) };
  const activateRestartedWorkspace = async () => ({
    start: "home",
    switched: {
      result: { workspaceId: expected.workspaceId, sessionEpoch: 2, state: "openedWritable" },
    },
  });
  const selectTable = async () => {};
  const rawBridgeRequest = async (_page, method) => {
    assert.equal(method, "gridState.get", "resume must not reapply Host state to repair the UI");
    return { type: method, payload: { revision: expected.revision, state: expected.state } };
  };
  const equivalentPresentationState = eval(`(${extract(
    "function equivalentPresentationState", "async function scenario13",
  )})`);
  // Command controls have their own UI/real-package assertions; this probe isolates presentation restoration.
  const resumeHostCommands = async () => {};
  const resumeHostPresentation = eval(`(${extract(
    "async function resumeHostPresentation", "function equivalentPresentationState",
  )})`);
  return resumeHostPresentation(page, recorder, "state.json");
}

test("Host restart accepts the restored DOM presentation", async () => {
  await resumeDom();
});

test("Host restart accepts subpixel width rounding", async () => {
  await resumeDom(document => {
    document.querySelector('[tabulator-field="title"]').dataset.width = "280.5";
  });
});

const brokenPresentations = {
  width: document => { document.querySelector('[tabulator-field="title"]').dataset.width = "210"; },
  "width beyond rounding": document => { document.querySelector('[tabulator-field="title"]').dataset.width = "282"; },
  frozen: document => { document.querySelector('[tabulator-field="title"]').classList.remove("tabulator-frozen"); },
  order: document => { document.querySelector(".tabulator-headers").append(document.querySelector('[tabulator-field="second"]')); },
  "frozen reordered column": document => { document.querySelector('[tabulator-field="second"]').classList.add("tabulator-frozen"); },
  sort: document => { document.querySelector('[tabulator-field="title"]').setAttribute("aria-sort", "none"); },
  density: document => { document.querySelector(".grid-wrapper").className = "grid-wrapper density-normal"; },
  operator: document => { document.querySelectorAll(".operator-select")[1].textContent = "为空"; },
  field: document => { document.querySelectorAll(".field-select")[1].textContent = "Title"; },
  hidden: document => { document.querySelector('[tabulator-field="status"]').style.display = "block"; },
  joiner: document => { document.querySelector(".joiner-select").textContent = "且"; },
  "empty equality": document => { document.querySelectorAll(".value-input input")[1].value = "changed"; },
};
for (const [name, mutate] of Object.entries(brokenPresentations)) {
  test(`Host restart rejects stale DOM ${name} even when Host state is correct`, async () => {
    await assert.rejects(resumeDom(mutate));
  });
}

// ---- S11 plugin host restart helpers ----

const verifiedProcessMembers = eval(`(${extract(
  "function verifiedProcessMembers", "function countProcessMembers",
)})`);
const countProcessMembers = eval(`(${extract(
  "function countProcessMembers", "async function waitForPackagedMemberExit",
)})`);
const waitForPackagedMemberExitSource = extract(
  "async function waitForPackagedMemberExit", "async function listRetainedPluginPackages",
);

function memberSnapshot(pids, processName = "node.exe") {
  return { status: "completed", action: "observe-processes", members: pids.map(pid => ({
    pid, processName, identityVerified: true,
  })) };
}

test("waiting for a packaged member exit polls the exact pid until it disappears", async () => {
  const waitForPackagedMemberExit = eval(`(${waitForPackagedMemberExitSource})`);
  const snapshots = [
    memberSnapshot([4242, 9000]),
    memberSnapshot([9000]),
  ];
  const observations = [];
  const requestPackagedProcessKill = async (_runtime, action, reason) => {
    assert.equal(action, "observe-processes");
    assert.equal(reason, "verify member 4242 exited");
    observations.push(0);
    return snapshots[Math.min(observations.length - 1, snapshots.length - 1)];
  };
  const result = await waitForPackagedMemberExit({}, 4242, 2_000);
  assert.equal(result.pid, 4242);
  assert.equal(countProcessMembers(result.snapshot.members, "node.exe"), 1);
  assert.ok(!result.snapshot.members.some(member => member.pid === 4242));
  assert.equal(observations.length, 2);
});

test("waiting for a packaged member exit propagates an observation failure immediately", async () => {
  const waitForPackagedMemberExit = eval(`(${waitForPackagedMemberExitSource})`);
  let calls = 0;
  const requestPackagedProcessKill = async () => {
    calls += 1;
    throw new Error("Python orchestrator did not acknowledge the observe-processes fault request");
  };
  await assert.rejects(
    waitForPackagedMemberExit({}, 4242, 60_000),
    /acknowledge the observe-processes fault request/,
  );
  assert.equal(calls, 1);
});

test("waiting for a packaged member exit rejects a snapshot without verified identities", async () => {
  const waitForPackagedMemberExit = eval(`(${waitForPackagedMemberExitSource})`);
  let snapshot = null;
  const requestPackagedProcessKill = async () => snapshot;
  for (const invalid of [
    { status: "completed", action: "observe-processes" },
    {
      status: "completed",
      action: "observe-processes",
      members: [{ pid: 4242, processName: "node.exe", identityVerified: false }],
    },
  ]) {
    snapshot = invalid;
    await assert.rejects(
      waitForPackagedMemberExit({}, 4242, 60_000),
      /not a verified legal snapshot/,
    );
  }
});


const mutationBoundaryFilesSource = extract("async function mutationBoundaryFiles", "async function waitForPluginTerminal");
const pickerTestRoot = fileURLToPath(new URL("../../build/task480", import.meta.url));
function createPickerControls() {
  fs.mkdirSync(pickerTestRoot, { recursive: true });
  return fs.mkdtempSync(path.join(pickerTestRoot, "s11-picker-"));
}
function removePickerControls(directory) {
  assert.equal(path.dirname(path.resolve(directory)), path.resolve(pickerTestRoot));
  for (const name of ["attachment-replacement-source.txt", "attachment-target.txt", "52-input.csv", "52-output.bin"]) {
    const target = path.join(directory, name);
    if (fs.existsSync(target)) fs.unlinkSync(target);
  }
  fs.rmdirSync(directory);
}

for (const existingTarget of [true, false]) {
  test(`S11 file grants restore scoped Host controls after success (target exists: ${existingTarget})`, async () => {
    const directory = createPickerControls();
    const fsPromises = fs.promises;
    try {
      const runtime = { controlsDir: directory };
      const readControl = path.join(directory, "attachment-replacement-source.txt");
      const writeControl = path.join(directory, "attachment-target.txt");
      fs.writeFileSync(readControl, "previous source\n"); if (existingTarget) fs.writeFileSync(writeControl, "previous target\n");
      const bytes = Buffer.from("scoped synthetic bytes\n");
      fs.writeFileSync(path.join(directory, "52-input.csv"), bytes);
      let result;
      const frame = { locator(selector) {
        if (selector === "#files") return { async click() {
          const source = fs.readFileSync(readControl, "utf8").trim();
          const target = fs.readFileSync(writeControl, "utf8").trim();
          assert.equal(source, path.join(directory, "52-input.csv"));
          assert.equal(target, path.join(directory, "52-output.bin"));
          fs.copyFileSync(source, target);
          result = JSON.stringify({ kind: "files", status: "succeeded", bytes: bytes.length });
        } };
        assert.equal(selector, "#result");
        return { filter() { return { async waitFor() {} }; }, async innerText() { return result; } };
      } };
      const recorded = [];
      const recorder = { check(name, passed, evidence) { assert.ok(passed, name); recorded.push(evidence); } };
      const run = new Function("fs", "path", `return (${mutationBoundaryFilesSource});`)(fsPromises, path);
      await run({}, recorder, runtime, frame);
      assert.equal(recorded[0].picker, "Host test-mode controls");
      assert.equal(fs.readFileSync(readControl, "utf8"), "previous source\n");
      if (existingTarget) assert.equal(fs.readFileSync(writeControl, "utf8"), "previous target\n");
      else assert.equal(fs.existsSync(writeControl), false);
    } finally { removePickerControls(directory); }
  });
}

test("S11 restores its scoped picker controls when a real file grant fails", async () => {
  const directory = createPickerControls();
  try {
    const readControl = path.join(directory, "attachment-replacement-source.txt");
    const writeControl = path.join(directory, "attachment-target.txt");
    const previousRead = Buffer.from([0xff, 0, 0x61, 13, 10]);
    const previousWrite = Buffer.from([0xfe, 0x62, 13, 10]);
    fs.writeFileSync(readControl, previousRead); fs.writeFileSync(writeControl, previousWrite);
    const frame = { locator() { return { async click() { throw new Error("file grant rejected"); } }; } };
    const run = new Function("fs", "path", `return (${mutationBoundaryFilesSource});`)(fs.promises, path);
    await assert.rejects(run({}, {}, { controlsDir: directory }, frame), /file grant rejected/);
    assert.deepEqual(fs.readFileSync(readControl), previousRead);
    assert.deepEqual(fs.readFileSync(writeControl), previousWrite);
  } finally { removePickerControls(directory); }
});

test("S11 propagates an unreadable target control without invoking the file grant", async () => {
  const directory = createPickerControls();
  try {
    const readControl = path.join(directory, "attachment-replacement-source.txt");
    const writeControl = path.join(directory, "attachment-target.txt");
    fs.writeFileSync(readControl, "previous source\n");
    const failure = Object.assign(new Error("target control denied"), { code: "EACCES" });
    const fileSystem = { ...fs.promises, async readFile(file, ...args) {
      if (file === writeControl) throw failure;
      return fs.promises.readFile(file, ...args);
    } };
    const frame = { locator() { assert.fail("must not invoke a grant before control state is saved"); } };
    const run = new Function("fs", "path", `return (${mutationBoundaryFilesSource});`)(fileSystem, path);
    await assert.rejects(run({}, {}, { controlsDir: directory }, frame), error => error === failure);
    assert.equal(fs.readFileSync(readControl, "utf8"), "previous source\n");
    assert.equal(fs.existsSync(writeControl), false);
  } finally { removePickerControls(directory); }
});


const migrateHistoricalPluginSource = extract("async function migrateHistoricalPlugin", "async function resumePluginHostRestart").replaceAll("import.meta.url", "importMetaUrl");
for (const invalid of [null, "plan-target", "reinstall"]) {
  test(`S11 historical upgrade uses the inspected Host lease and preserves controls (${invalid})`, async () => {
    const directory = createPickerControls();
    const sourceControl = path.join(directory, "plugin-source.txt");
    const targetControl = path.join(directory, "plugin-preservation-target.txt");
    fs.writeFileSync(sourceControl, "original source\n"); fs.writeFileSync(targetControl, "original export\n");
    const legacy = { pluginId: "com.vibetable.e2e.legacy-worker", installationId: "legacy000000001",
      projectKey: "local:workspace", revision: 1, packageHash: "old", blockingReasons: ["plugin_worker_retired"] };
    const upgraded = { ...legacy, installationId: invalid === "reinstall" ? "replacement" : legacy.installationId,
      version: "2.0.0", revision: 2, packageHash: "new", status: "enabled", blockingReasons: [],
      manifest: { $schema: "vibetable.plugin-manifest.v2", compatibility: { pluginApi: "3.x" } } };
    const plan = { projectKey: legacy.projectKey, targetInstallationId: invalid === "plan-target" ? "replacement" : legacy.installationId,
      targetRevision: 1, packageHash: "new", manifest: { ...upgraded.manifest, pluginId: legacy.pluginId, version: "2.0.0" } };
    const events = []; let committed = false, capture = 0, fileRoundtrip = 0, archiveRead = 0;
    const frame = { locator(selector) { return {
      filter() { return { async waitFor() { assert.equal(selector, "#status"); } }; },
      async innerText() { assert.equal(selector, "#result"); return '{"status":"succeeded"}'; },
    }; } };
    const page = { getByTestId(id) { return {
      async click() {
        assert.ok(["nav-plugins", "plugin-upgrade", "nav-tables", "plugin-app-return", "plugin-preservation-export",
          `nav-plugin-app-${legacy.pluginId}/open-boundary`].includes(id), `unexpected lifecycle command ${id}`);
        events.push(id);
        if (id === "plugin-upgrade") assert.equal(fs.readFileSync(sourceControl, "utf8").trim(), path.join(directory, "legacy-worker-api3.vtplugin"));
        if (id === "plugin-preservation-export") assert.ok(committed && fileRoundtrip === 1);
      },
      async innerText() { assert.equal(id, "plugin-upgrade-permission-delta"); return "added query permission"; },
    }; }, locator(selector) {
      assert.equal(selector, "button.plugin-row"); return { filter({ hasText }) { assert.equal(hasText, legacy.pluginId); return { async click() {} }; } };
    }, frameLocator(selector) { assert.equal(selector, ".surface-shell iframe"); assert.ok(committed); return frame; } };
    const run = new Function("fs", "path", "fileURLToPath", "importMetaUrl", "execFileSync", "findCatalogEntry", "rawBridgeRequest",
      "beginBridgeMessageCapture", "waitForCapturedBridgeMessage", "commitInspectedApplicationUpgrade", "selectTable", "mutationBoundaryFiles",
      `return (${migrateHistoricalPluginSource});`)(fs.promises, path, () => directory, import.meta.url,
      (_python, args) => { archiveRead++; assert.equal(JSON.parse(args.at(-1)).installationId, legacy.installationId);
        return JSON.stringify({ migrationValidated: true, originalPackagePreserved: true }); },
      (catalog, id) => catalog.find(item => item.pluginId === id),
      async (_page, method) => { assert.equal(method, "plugin.catalog.list"); return { payload: [committed ? upgraded : legacy] }; },
      async () => { capture++; },
      async () => capture === 1 ? { type: "plugin.install.inspect", payload: plan }
        : { type: "pluginPreservation.exportCompleted", payload: { missingPackages: 0, cancelled: false } },
      async (_page, inspected) => { assert.equal(inspected, plan); events.push("approved-upgrade"); committed = true; return upgraded; },
      async (_page, name) => { assert.equal(name, "E2E Plugin Target"); },
      async (_page, _recorder, _runtime, selected) => { assert.equal(selected, frame); fileRoundtrip++; });
    const recorder = { check(name, passed) { assert.ok(passed, name); } };
    const execute = () => run(page, recorder, { controlsDir: directory, pythonExecutable: "locked-python", pluginStatePath: path.join(directory, "state.json") },
      { projectKey: legacy.projectKey }, legacy);
    try {
      if (invalid) await assert.rejects(execute(), /exact existing installation|without reinstalling/u);
      else { assert.equal((await execute()).installationId, legacy.installationId); assert.equal(fileRoundtrip, 1); assert.equal(archiveRead, 1);
        assert.ok(events.indexOf("approved-upgrade") > events.indexOf("plugin-upgrade")); }
      assert.equal(fs.readFileSync(sourceControl, "utf8"), "original source\n");
      assert.equal(fs.readFileSync(targetControl, "utf8"), "original export\n");
    } finally { fs.unlinkSync(sourceControl); fs.unlinkSync(targetControl); fs.rmdirSync(directory); }
  });
}
