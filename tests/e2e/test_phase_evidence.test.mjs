import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { observeTestPhases } from "./test_phase_evidence.mjs";

const self = fileURLToPath(import.meta.url);

if (process.env.THEME_PHASE_EVIDENCE_CHILD) {
  const mode = process.env.THEME_PHASE_EVIDENCE_CHILD;
  const isClose = mode === "close" || mode.startsWith("after-");
  const usesAfterHook = mode.startsWith("after-");
  const phase = isClose ? "close Edge" : "wait for connected Tabulator cell";
  test(`theme probe ${mode}`, { timeout: 50 }, async (t) => {
    const phases = observeTestPhases(t);
    const waitForAbort = () => new Promise((resolve) => {
      const keepAlive = setTimeout(resolve, 500);
      t.signal.addEventListener("abort", () => {
        clearTimeout(keepAlive);
        setTimeout(resolve, 10);
      }, { once: true });
    });
    const close = () => phases.phase("close Edge", () => (
      mode === "after-independent"
        ? new Promise((resolve) => setTimeout(resolve, 80))
        : waitForAbort()
    ));
    if (usesAfterHook) {
      t.after(async () => {
        try {
          await close();
        } finally {
          phases.close();
        }
      }, { timeout: mode === "after-independent" ? 250 : 50 });
    }
    try {
      if (mode === "success") {
        await phases.phase("complete immediately", async () => {});
        return;
      }
      if (mode === "assertion") {
        await phases.phase("assertion boundary", async () => {
          throw new Error("original assertion boundary failure");
        });
      }
      if (!isClose) await phases.phase(phase, waitForAbort);
    } finally {
      if (!usesAfterHook) {
        try {
          if (isClose) await close();
        } finally {
          phases.close();
        }
      }
    }
  });
} else {
  function childResult(mode) {
    const child = spawnSync(process.execPath, ["--test", "--test-reporter=tap", self], {
      encoding: "utf8",
      timeout: 5_000,
      env: { ...process.env, NODE_TEST_CONTEXT: undefined, THEME_PHASE_EVIDENCE_CHILD: mode },
    });
    assert.equal(child.error, undefined, child.error?.message);
    assert.equal(child.signal, null, child.stdout + child.stderr);
    return { output: child.stdout + child.stderr, status: child.status };
  }

  test("theme probe timeout body reports the pending phase once", () => {
    const { output, status } = childResult("body");
    assert.equal(status, 1, output);
    assert.match(output, /failureType: 'testTimeoutFailure'/);
    assert.match(output, /pending phase "wait for connected Tabulator cell" after \d+ms \(total \d+ms\)/);
    assert.equal((output.match(/pending phase/g) ?? []).length, 1, output);
  });

  test("theme probe timeout close reports the pending phase once", () => {
    const { output, status } = childResult("close");
    assert.equal(status, 1, output);
    assert.match(output, /failureType: 'testTimeoutFailure'/);
    assert.match(output, /pending phase "close Edge" after \d+ms \(total \d+ms\)/);
    assert.equal((output.match(/pending phase/g) ?? []).length, 1, output);
  });

  test("theme probe cleanup hook has an independent timeout budget", () => {
    const legacy = childResult("close");
    assert.equal(legacy.status, 1, legacy.output);
    assert.match(legacy.output, /failureType: 'testTimeoutFailure'/);

    const { output, status } = childResult("after-independent");
    assert.equal(status, 0, output);
    assert.doesNotMatch(output, /pending phase/);
  });

  test("theme probe cleanup hook timeout reports the pending phase once", () => {
    const { output, status } = childResult("after-timeout");
    assert.equal(status, 1, output);
    assert.match(output, /failureType: 'hookFailed'/);
    assert.match(output, /pending phase "close Edge" after \d+ms \(total \d+ms\)/);
    assert.equal((output.match(/pending phase/g) ?? []).length, 1, output);
  });

  test("completed phase stays quiet in TAP", () => {
    const { output, status } = childResult("success");
    assert.equal(status, 0, output);
    assert.doesNotMatch(output, /pending phase/);
  });

  test("ordinary failure keeps its original TAP error", () => {
    const { output, status } = childResult("assertion");
    assert.equal(status, 1, output);
    assert.match(output, /original assertion boundary failure/);
    assert.doesNotMatch(output, /pending phase/);
  });
}
