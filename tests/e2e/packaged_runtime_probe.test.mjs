import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  activateCreatedTable,
  createFirstTable,
  locateProductPage,
  requireStableFirstTable,
  runCli,
} from "./packaged_runtime_probe.mjs";


function healthySample(overrides = {}) {
  return {
    tableId: "tbl_runtime_baseline",
    activeTableId: "tbl_runtime_baseline",
    tableSummaryVisible: true,
    errorOverlayVisible: false,
    rowCount: 0,
    ...overrides,
  };
}


test("first table setup activates the created table before waiting for its dataset", async () => {
  const events = [];
  const tableId = "tbl_runtime_baseline";
  let activated = false;
  const filteredName = {};
  const nameInput = {
    locator: (selector) => {
      assert.equal(selector, "input");
      return { fill: async (value) => { events.push(`name:${value}`); } };
    },
    waitFor: async (options) => {
      assert.deepEqual(options, { state: "hidden", timeout: 30_000 });
      events.push("create-hidden");
    },
  };
  const row = {
    waitFor: async (options) => {
      assert.deepEqual(options, { state: "visible", timeout: 30_000 });
      events.push("row-visible");
    },
    locator: (selector) => {
      if (selector === "small") return { innerText: async () => tableId };
      assert.equal(selector, "button.table-select");
      return {
        click: async () => {
          activated = true;
          events.push("select");
        },
      };
    },
  };
  const page = {
    locator: (selector) => {
      if (selector === ".table-row") {
        return {
          filter: ({ has }) => {
            assert.equal(has, filteredName);
            return { last: () => row };
          },
        };
      }
      assert.equal(selector, ".table-row.table-item--active small");
      return {
        innerText: async () => {
          assert.equal(activated, true);
          events.push("identity");
          return tableId;
        },
      };
    },
    getByTestId: (testId) => {
      if (testId === "nav-tables" || testId === "sidebar-new-table"
        || testId === "create-table-submit" || testId === "field-close-button") {
        return { click: async () => { events.push(testId); } };
      }
      if (testId === "create-table-name-input") return nameInput;
      if (testId === "sidebar-table-name") {
        return {
          filter: ({ hasText }) => {
            assert.equal(hasText, "Runtime baseline");
            return filteredName;
          },
        };
      }
      if (testId === "field-display-name") {
        return {
          waitFor: async (options = {}) => {
            events.push(options.state === "hidden" ? "drawer-hidden" : "drawer-visible");
          },
        };
      }
      assert.equal(testId, "table-summary");
      return { waitFor: async (options) => {
        assert.equal(activated, true);
        assert.deepEqual(options, { state: "visible", timeout: 30_000 });
        events.push("summary");
      } };
    },
    waitForEvent: async (event, options) => {
      assert.equal(event, "dialog");
      assert.deepEqual(options, { timeout: 2_000 });
      return { accept: async () => {}, message: () => "discard" };
    },
  };

  assert.equal(await createFirstTable(page), tableId);

  const activation = events.slice(events.indexOf("drawer-hidden"));
  assert.deepEqual(activation, ["drawer-hidden", "select", "identity", "summary"]);
});


test("created table activation rejects a different active identity", async () => {
  let waitedForSummary = false;
  const row = { locator: () => ({ click: async () => {} }) };
  const page = {
    locator: () => ({ innerText: async () => "tbl_other" }),
    getByTestId: () => ({
      waitFor: async () => { waitedForSummary = true; },
    }),
  };

  await assert.rejects(
    activateCreatedTable(page, row, "tbl_runtime_baseline"),
    /expected tbl_runtime_baseline, observed tbl_other/,
  );

  assert.equal(waitedForSummary, false);
});


test("first table oracle requires a continuous stable grid window", async () => {
  const times = [0, 400, 1_000];
  const result = await requireStableFirstTable({
    expectedTableId: "tbl_runtime_baseline",
    stableForMs: 1_000,
    pollMs: 50,
    now: () => times.shift(),
    sleep: async () => {},
    sample: async () => healthySample(),
  });

  assert.deepEqual(result, {
    status: "passed",
    tableId: "tbl_runtime_baseline",
    sameTableIdentity: true,
    tableSummaryVisible: true,
    errorOverlayVisible: false,
    rowCount: 0,
    stableWindowMs: 1_000,
  });
});


test("first sample duration is excluded from the continuous stable window", async () => {
  let current = 0;
  let samples = 0;

  await requireStableFirstTable({
    expectedTableId: "tbl_runtime_baseline",
    stableForMs: 1_000,
    pollMs: 500,
    now: () => current,
    sleep: async (duration) => { current += duration; },
    sample: async () => {
      samples += 1;
      if (samples === 1) current = 1_500;
      return healthySample();
    },
  });

  assert.equal(samples, 3);
});


for (const [name, interruption, message] of [
  ["identity", { activeTableId: "tbl_other" }, /table identity changed/],
  ["error overlay", { errorOverlayVisible: true }, /error overlay/],
  ["missing summary", { tableSummaryVisible: false }, /summary is not visible/],
  ["row count", { rowCount: 1 }, /row count changed/],
]) {
  test(`first table oracle rejects a mid-window ${name} interruption`, async () => {
    const samples = [healthySample(), healthySample(interruption)];
    const times = [0, 500];

    await assert.rejects(
      requireStableFirstTable({
        expectedTableId: "tbl_runtime_baseline",
        stableForMs: 1_000,
        pollMs: 50,
        now: () => times.shift(),
        sleep: async () => {},
        sample: async () => samples.shift(),
      }),
      message,
    );
  });
}


test("first table oracle fails closed when a Playwright sample throws", async () => {
  await assert.rejects(
    requireStableFirstTable({
      expectedTableId: "tbl_runtime_baseline",
      now: () => 0,
      sleep: async () => {},
      sample: async () => { throw new Error("page disconnected"); },
    }),
    /page disconnected/,
  );
});


test("first table oracle rejects invalid timing controls", async () => {
  await assert.rejects(
    requireStableFirstTable({
      expectedTableId: "tbl_runtime_baseline",
      stableForMs: true,
      sample: async () => healthySample(),
    }),
    /invalid stable window/,
  );
  await assert.rejects(
    requireStableFirstTable({
      expectedTableId: "tbl_runtime_baseline",
      pollMs: Number.POSITIVE_INFINITY,
      sample: async () => healthySample(),
    }),
    /invalid stable poll interval/,
  );
});


test("product page locator ignores about:blank and scans every context", async () => {
  const product = { url: () => "https://app.vibetable.local/workspace" };
  const browser = {
    contexts: () => [
      { pages: () => [{ url: () => "about:blank" }] },
      { pages: () => [product] },
    ],
  };

  assert.equal(await locateProductPage({ browser }), product);
});


test("product page locator waits for an about:blank target to navigate", async () => {
  let url = "about:blank";
  let current = 0;
  const page = { url: () => url };
  const browser = { contexts: () => [{ pages: () => [page] }] };

  const located = await locateProductPage({
    browser,
    timeoutMs: 1_000,
    now: () => current,
    sleep: async (duration) => {
      current += duration;
      url = "https://app.vibetable.local/";
    },
  });

  assert.equal(located, page);
});


for (const [name, argv, probe, expectedMessage] of [
  [
    "invalid arguments",
    (reportPath) => ["--json-report", reportPath, "broken"],
    async () => healthySample(),
    "invalid argument list",
  ],
  [
    "CDP or UI failure",
    (reportPath) => ["--cdp-url", "http://127.0.0.1:9222", "--json-report", reportPath],
    async () => { throw new Error("page disconnected"); },
    "page disconnected",
  ],
]) {
  test(`CLI writes a structured failed report for ${name}`, async () => {
    const root = await fs.mkdtemp(path.join(os.tmpdir(), "vibetable-runtime-probe-"));
    try {
      const reportPath = path.join(root, "probe.json");
      const exitCode = await runCli(argv(reportPath), {
        probe,
        writeStatus: () => {},
      });
      const report = JSON.parse(await fs.readFile(reportPath, "utf8"));

      assert.equal(exitCode, 1);
      assert.equal(report.status, "failed");
      assert.equal(report.evidenceKind, "packaged-runtime-ui-probe");
      assert.match(report.errors[0].message, new RegExp(expectedMessage));
    } finally {
      await fs.rm(root, { recursive: true, force: true });
    }
  });
}
