// Isolated-native-clipboard guard for the WebView product E2E entry.
//
// Gate: only the ephemeral GitHub-hosted runner or an explicit
// VIBETABLE_NATIVE_CLIPBOARD_TESTS=1 desktop may touch the real clipboard;
// every other environment fails closed before any probe or bridge request.
// Before the first write the locked Python ctypes probe proves all-format
// emptiness; before every later write it re-checks that the clipboard still
// holds this run's last confirmed state via GetClipboardSequenceNumber.
// Writes are confirmed by exact text (no whitespace normalization) and the
// observed sequence number, and cleanup clears only that confirmed state.
// On this isolated desktop the probe and the Host write are NOT a
// transaction: a timed-out or failed Host write may already have landed or
// still be in flight, so cleanup never treats a momentary empty clipboard as
// settlement of an unconfirmed write. Unconfirmed writes leave the clipboard
// untouched and the cleanup result indeterminate (a failure). Foreign or
// externally changed content is likewise preserved and fails honestly.

import { spawn } from "node:child_process";

export const NATIVE_CLIPBOARD_GATE_MESSAGE =
  "Native clipboard E2E requires an isolated desktop with an empty clipboard: "
  + "run it on a GitHub-hosted runner, or set VIBETABLE_NATIVE_CLIPBOARD_TESTS=1 "
  + "on a dedicated isolated test machine. Without that opt-in the real "
  + "clipboard is never touched.";

export function isIsolatedNativeClipboardDesktop(env = process.env) {
  return (env.GITHUB_ACTIONS === "true" && env.RUNNER_ENVIRONMENT === "github-hosted")
    || env.VIBETABLE_NATIVE_CLIPBOARD_TESTS === "1";
}

// Spawns the Python probe as a real child process and writes the request JSON
// to stdin explicitly (promisified execFile cannot be relied on to forward
// `input`). Resolves with the parsed JSON report; rejects with labeled
// errors that never contain clipboard content.
export function createPythonClipboardProbe(pythonExecutable, { probePath, timeoutMs = 20_000 }) {
  return (command, payload) => new Promise((resolve, reject) => {
    const child = spawn(pythonExecutable, [probePath, command], {
      stdio: ["pipe", "pipe", "pipe"],
      env: { ...process.env, PYTHONUTF8: "1" },
      windowsHide: true,
    });
    let stdout = "";
    let settled = false;
    const settle = (fn, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      fn(value);
    };
    const timer = setTimeout(() => {
      child.kill();
      settle(reject, new Error(`clipboard probe command ${command} timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.on("error", (error) => settle(reject, error));
    child.on("close", (code) => {
      if (code !== 0) {
        settle(reject, new Error(`clipboard probe command ${command} exited with code ${code}`));
        return;
      }
      try {
        settle(resolve, JSON.parse(stdout));
      } catch {
        settle(reject, new Error(`clipboard probe command ${command} returned invalid JSON`));
      }
    });
    child.stdin.on("error", (error) => {
      settle(reject, new Error(`clipboard probe stdin failed for ${command}: ${error?.message ?? error}`));
    });
    child.stdin.end(payload === null || payload === undefined ? "" : `${JSON.stringify(payload)}\n`);
  });
}

export function createIsolatedNativeClipboardGuard({ env = process.env, runProbe }) {
  if (typeof runProbe !== "function") {
    throw new Error("an injected native clipboard probe runner is required");
  }
  let armed = false;
  let released = false;
  let ownedSequence = null;
  let unconfirmedWrites = 0;
  const ownedTexts = new Set();

  function requireGate() {
    if (!isIsolatedNativeClipboardDesktop(env)) throw new Error(NATIVE_CLIPBOARD_GATE_MESSAGE);
    if (released) throw new Error("the isolated native clipboard guard was already released");
  }

  async function confirmOwned(text, refusalContext) {
    const confirmed = await runProbe("confirm-owned", { text });
    if (confirmed?.ok !== true) {
      throw new Error(`${refusalContext}: ${confirmed?.error ?? "probe failed without a report"}`);
    }
    if (confirmed.verified !== true) {
      throw new Error(
        `${refusalContext} refused (${confirmed.refused ?? "unverified"}): the clipboard no `
        + "longer holds exactly this run's write");
    }
    ownedSequence = confirmed.sequence;
    ownedTexts.add(text);
  }

  return {
    async seed(page, text, { bridgeRequest }) {
      requireGate();
      if (!armed) {
        const inspection = await runProbe("inspect", null);
        if (inspection?.ok !== true) {
          throw new Error(
            "the isolated clipboard could not be inspected before the first native write: "
            + `${inspection?.error ?? "probe failed without a report"}`);
        }
        if (inspection.empty !== true) {
          throw new Error(
            "the isolated test desktop clipboard must be empty (all formats) before the first "
            + `native write; found ${inspection.formatCount} format(s) and refusing to overwrite it`);
        }
        ownedSequence = inspection.sequence;
        armed = true;
      } else {
        const state = await runProbe("verify-sequence", { expectedSequence: ownedSequence });
        if (state?.ok !== true) {
          throw new Error(
            "clipboard ownership could not be verified before a native write: "
            + `${state?.error ?? "probe failed without a report"}`);
        }
        if (state.verified !== true) {
          throw new Error(
            "the clipboard changed outside this run between native writes; refusing to overwrite it");
        }
      }
      // A Host write may land or time out independently of this bookkeeping;
      // only a successful confirm-owned settles it.
      unconfirmedWrites += 1;
      const written = await bridgeRequest(page, "clipboard.writeText", { text });
      const read = await bridgeRequest(page, "clipboard.readText", {});
      if (written.payload?.written !== true || read.payload?.text !== text) {
        // Best effort: bind ownership to what actually landed before failing,
        // so cleanup can still clear exactly this run's write.
        try {
          await confirmOwned(text, "post-write clipboard confirmation");
          unconfirmedWrites -= 1;
        } catch {
          // The write's settlement stays unconfirmed; cleanup stays honest.
        }
        throw new Error("Native Host clipboard round trip failed");
      }
      await confirmOwned(text, "post-write clipboard confirmation");
      unconfirmedWrites -= 1;
    },

    // Registers an externally issued native write (e.g. the product's own
    // Ctrl+C Host SetText) BEFORE it is triggered: if that write times out
    // or lands late, cleanup must stay indeterminate until adopt confirms
    // the exact landed text, instead of clearing the previous owned state
    // and reporting a false clean.
    beginUnconfirmedNativeWrite() {
      requireGate();
      if (!armed) {
        throw new Error(
          "cannot register a native clipboard write before the guard verified the isolated clipboard");
      }
      unconfirmedWrites += 1;
    },

    // Adopts only a pre-declared exact expected output of the product's own
    // copy: the observed text must equal one candidate byte-for-byte, and the
    // probe re-verifies it before ownership is claimed. Observed content is
    // never included in errors or logs.
    async adopt(page, expectedTexts, { bridgeRequest }) {
      requireGate();
      if (!armed) {
        throw new Error("cannot adopt native clipboard text before the guard verified the isolated clipboard");
      }
      const read = await bridgeRequest(page, "clipboard.readText", {});
      const observed = read.payload?.text;
      const exact = [...expectedTexts].find((candidate) => candidate === observed);
      if (exact === undefined) {
        throw new Error(
          "the product-written clipboard text does not exactly match any pre-declared expected "
          + `output (${expectedTexts.length} candidate(s)); refusing to adopt it`);
      }
      await confirmOwned(exact, "product-written clipboard adoption");
      unconfirmedWrites = Math.max(0, unconfirmedWrites - 1);
    },

    async release() {
      if (!armed || released) {
        return { status: "unused" };
      }
      released = true;
      if (unconfirmedWrites > 0) {
        // A timed-out or failed Host write may already have landed or still
        // be in flight; observing an empty clipboard now cannot settle it.
        // Preserve everything and report the outcome as indeterminate
        // instead of clearing or waiting.
        let observed;
        try {
          observed = await runProbe("inspect", null);
        } catch (error) {
          return {
            status: "indeterminate",
            unconfirmedWrites,
            message: `native clipboard state could not be observed: ${error?.message ?? String(error)}`,
          };
        }
        if (observed?.ok !== true) {
          return {
            status: "indeterminate",
            unconfirmedWrites,
            message: `native clipboard state could not be observed: ${observed?.error ?? "no report"}`,
          };
        }
        return {
          status: "indeterminate",
          unconfirmedWrites,
          observedEmpty: observed.empty === true,
          formatCount: typeof observed.formatCount === "number" ? observed.formatCount : null,
        };
      }
      let outcome;
      try {
        outcome = await runProbe("clear-owned", {
          ownedTexts: [...ownedTexts],
          expectedSequence: ownedSequence,
        });
      } catch (error) {
        return {
          status: "error",
          message: `native clipboard cleanup probe failed: ${error?.message ?? String(error)}`,
        };
      }
      if (outcome?.ok !== true) {
        return {
          status: "error",
          message: "native clipboard cleanup probe reported a failure: "
            + `${outcome?.error ?? "no report"}`,
        };
      }
      if (outcome.alreadyEmpty === true) {
        return { status: "clean", cleared: false, alreadyEmpty: true };
      }
      if (outcome.cleared === true && outcome.emptyAfter === true) {
        return { status: "clean", cleared: true, alreadyEmpty: false };
      }
      return {
        status: "kept-foreign-content",
        cleared: false,
        refused: outcome.refused ?? "unverified",
        formatCount: typeof outcome.formatCount === "number" ? outcome.formatCount : null,
      };
    },
  };
}

// Folds the cleanup outcome into the scenario result: non-clean outcomes fail
// the run honestly; an already-failed run keeps its original error and gains
// `cleanupError`.
export function applyNativeClipboardCleanupResult(result, cleanup) {
  const report = cleanup ?? { status: "error", message: "missing native clipboard cleanup report" };
  result.nativeClipboardCleanup = report;
  if (report.status === "clean" || report.status === "unused") return false;
  const message = report.status === "kept-foreign-content"
    ? `the system clipboard no longer holds this run's confirmed state (${report.refused ?? "unverified"}); `
      + "the changed content was preserved instead of cleared"
    : report.status === "indeterminate"
      ? `${report.unconfirmedWrites} clipboard write(s) issued by this run were never confirmed `
        + "(a Host write may have landed or still be in flight); nothing was cleared and any "
        + "content was preserved"
      : report.message ?? "native clipboard cleanup failed";
  result.cleanupError = message;
  if (result.status === "passed") {
    result.status = "failed";
    result.error = { name: "Error", code: "NATIVE_CLIPBOARD_CLEANUP_FAILED", message };
  }
  return true;
}
