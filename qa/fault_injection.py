#!/usr/bin/env python3
"""Fail-closed workspace/snapshot v2 fault-injection release gate.

The gate requires explicit named tests to pass. A successful command that ran
zero matching tests is treated as a failure. The Go slice covers the durable
workspace v2 snapshot, restore, retention, package, write-coordination and
replica boundaries. The final scenario drives the real packaged WPF/WebView2
product, kills only its child sidecar, and verifies recovery without duplicate
realtime application.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
from collections.abc import Sequence
from contextlib import suppress
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from pathlib import Path

from scripts.toolchain_metadata import resolve_executable

ROOT = Path(__file__).resolve().parents[1]
SIDECAR = ROOT / "sidecar"
EVIDENCE_ROOT = Path(
    os.environ.get(
        "VIBETABLE_FAULT_EVIDENCE_ROOT",
        str(ROOT / "build" / "qa" / "fault-injection"),
    )
)
PRODUCT_RUNNER = ROOT / "tests" / "e2e" / "product_e2e_runner.py"
INFRASTRUCTURE_TESTS = (
    ROOT
    / "desktop"
    / "tests"
    / "VibeTable.Infrastructure.Tests"
    / "VibeTable.Infrastructure.Tests.csproj"
)

GO_TESTS = (
    "TestFaultGateDiskFullReturnsStableActionableError",
    "TestFaultGateFileLockReturnsStableActionableError",
    "TestFaultGateReadOnlyDirectoryReturnsStableActionableError",
    "TestFaultGatePortOccupiedReturnsStableActionableError",
    "TestFaultGateCorruptMigrationStopsBeforeStartup",
    "TestFaultGateRealWritableProbeDoesNotLeaveSentinel",
    "TestCapturePublishesOnlyVerifiedCompleteSnapshotAndDeduplicatesRevision",
    "TestCatalogFailureNeverPublishesPartialRecord",
    "TestDurableCatalogPublishesAtomicallyAndReopens",
    "TestStaleAuthorityCannotCapture",
    "TestSchedulerDebouncesIdleButCapsContinuousEditing",
    "TestSchedulerSkipsUnchangedRevisionAndBacksOffFailures",
    "TestJournalPersistenceFailureAfterMutationRollsBack",
    "TestFailedHealthRollsBackWithoutMixedState",
    "TestStageVerifiedPersistenceFailureStopsBeforeRollback",
    "TestRecoverRejectsUnknownJournalStageWithoutMutatingWorkspace",
    "TestApplyRejectsStalePlanAndUnsafeInventory",
    "TestPreviewFailsClosedForMissingRootOrChild",
    "TestApplyRejectsSameRevisionInventoryDigestDrift",
    "TestInspectRejectsTraversalDuplicateAndResourceBomb",
    "TestInspectRejectsExcessivePathAndCompressionRatio",
    "TestImporterFailsBeforeStagingForCorruptOrWrongWorkspacePackage",
    "TestImporterAbortsStagingWhenPublicationReceiptIsInvalid",
    "TestFenceTransferInvalidatesOldAuthorityWithoutChangingOtherCounters",
    "TestPersistentCoordinatorFailsClosedUntilPreparedMutationResolved",
    "TestPersistentQueueRetriesIdempotentlyAcrossRestart",
    "TestPersistentQueueRecoversExpiredInflightLeaseAfterRestart",
    "TestAdvisoryDAGDetectsCorruptionAndMissingParent",
    "TestAdvisoryDAGKeepsConcurrentImmutableHeads",
    "TestWriteImmutableConcurrentDifferentContentNeverOverwrites",
    "TestFilesystemRemoteReplicatesAndIndependentlyReopensRoots",
    "TestRuntimeFailsClosedForIdentityParamsAndEpoch",
    "TestReadPersistentMutationRevisionCoversCommittedApplyBeforeFinish",
    "TestAuthorityReceiptsCloseFileHistoryAndSnapshotKillWindows",
    "TestSnapshotRestoreCommitsAuthorityAndRecoversFailedSearchRebuildAfterRestart",
    "TestInterruptedInstalledSnapshotRestoreRollsBackBeforeReadiness",
    "TestConflictExternalAttachmentFaultRestoresOldFilesAndTableTransaction",
    "TestRuntimeReopensAndResumesConflictAtPocketBaseReceiptRevision",
)
GO_PACKAGES = (
    "./internal/startup",
    "./internal/snapshot",
    "./internal/restore",
    "./internal/retention",
    "./internal/snapshotpkg",
    "./internal/writecoordinator",
    "./internal/replica",
    "./internal/workspacev2",
)
DOTNET_TEST = (
    "VibeTable.Infrastructure.Tests.PocketBase.PocketBaseSupervisorTests."
    "FaultGateKilledSidecarPublishesDegradedThenRestartsReady"
)
PRODUCT_SCENARIO = "10-sse-reconnect"
SUBPROCESS_TIMEOUT_SECONDS = 15 * 60
TIMEOUT_RETURNCODE = 124


@dataclass(frozen=True)
class CaseResult:
    name: str
    status: str
    elapsed: float
    command: list[str]
    returncode: int
    evidence: str
    error: str | None = None


def _go_executable_version(executable: str) -> str | None:
    try:
        completed = subprocess.run(
            [executable, "version"],
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=10,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if completed.returncode:
        return None
    for token in completed.stdout.split():
        if token.startswith("go1"):
            return token.removeprefix("go")
    return None


def _resolve(tool: str) -> str:
    if tool == "go":
        go_mod = ROOT / "tools" / "recovery-tools" / "go.mod"
        go_version = next(
            (
                line.removeprefix("go ").strip()
                for line in go_mod.read_text(encoding="utf-8").splitlines()
                if line.startswith("go ")
            ),
            "",
        )
        if not go_version:
            raise RuntimeError("recovery Go toolchain version is missing")
        suffix = "go.exe" if os.name == "nt" else "go"
        candidates = [
            str(ROOT / ".tools" / f"go-{go_version}" / "go" / "bin" / suffix),
            str(ROOT / ".tools" / "go" / "bin" / suffix),
            str(ROOT / ".tools" / "go-full" / "go" / "bin" / suffix),
        ]
        path_go = shutil.which("go")
        if path_go:
            candidates.append(path_go)
        checked: set[str] = set()
        for candidate in candidates:
            key = os.path.normcase(os.path.abspath(candidate))
            if key in checked or not Path(candidate).is_file():
                continue
            checked.add(key)
            if _go_executable_version(candidate) == go_version:
                return candidate
        raise RuntimeError(f"Go {go_version} toolchain is required but was not found")
    return resolve_executable(tool, repo_root=ROOT) or tool


def _terminate_process_tree(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        with suppress(OSError, subprocess.TimeoutExpired):
            subprocess.run(
                ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                check=False,
                capture_output=True,
                timeout=30,
            )
    else:
        with suppress(ProcessLookupError):
            os.killpg(process.pid, signal.SIGKILL)
    try:
        process.wait(timeout=30)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=30)


def _timeout_text(value: str | bytes | None) -> str:
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value or ""


def _run(
    command: list[str],
    cwd: Path,
    *,
    timeout: int = SUBPROCESS_TIMEOUT_SECONDS,
) -> tuple[subprocess.CompletedProcess[str], float]:
    started = time.monotonic()
    environment = os.environ.copy()
    go_cache = ROOT / ".codex-go-cache"
    go_tmp = ROOT / ".codex-test-tmp"
    go_cache.mkdir(parents=True, exist_ok=True)
    go_tmp.mkdir(parents=True, exist_ok=True)
    environment.setdefault("GOCACHE", str(go_cache))
    environment.setdefault("GOTMPDIR", str(go_tmp))
    popen_kwargs: dict[str, object] = {}
    if os.name == "nt":
        popen_kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
    else:
        popen_kwargs["start_new_session"] = True
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            **popen_kwargs,
        )
    except OSError as exc:
        completed = subprocess.CompletedProcess(
            args=command,
            returncode=127,
            stdout="",
            stderr=str(exc),
        )
        return completed, time.monotonic() - started
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        returncode = process.returncode
    except subprocess.TimeoutExpired as exc:
        _terminate_process_tree(process)
        final_stdout, final_stderr = process.communicate()
        stdout = _timeout_text(exc.output) + (final_stdout or "")
        stderr = _timeout_text(exc.stderr) + (final_stderr or "")
        stderr += f"\nprocess tree timed out after {timeout}s and was terminated\n"
        returncode = TIMEOUT_RETURNCODE
    completed = subprocess.CompletedProcess(
        args=command,
        returncode=returncode,
        stdout=stdout,
        stderr=stderr,
    )
    return completed, time.monotonic() - started


def _run_go(run_root: Path) -> CaseResult:
    pattern = "^(" + "|".join(GO_TESTS) + ")$"
    try:
        go = _resolve("go")
    except (OSError, RuntimeError) as exc:
        evidence = run_root / "go-tests.jsonl"
        evidence.write_text(str(exc) + "\n", encoding="utf-8")
        return CaseResult(
            name="go-workspace-v2-durability-faults",
            status="failed",
            elapsed=0.0,
            command=["go", "test"],
            returncode=127,
            evidence=str(evidence),
            error=str(exc),
        )
    command = [
        go,
        "test",
        "-json",
        *GO_PACKAGES,
        "-run",
        pattern,
        "-count=1",
    ]
    completed, elapsed = _run(command, SIDECAR)
    evidence = run_root / "go-tests.jsonl"
    evidence.write_text(
        completed.stdout + completed.stderr,
        encoding="utf-8",
    )
    passed: set[str] = set()
    for line in completed.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Action") == "pass" and event.get("Test"):
            passed.add(str(event["Test"]))
    missing = sorted(set(GO_TESTS) - passed)
    error = None
    if completed.returncode:
        error = f"go test exited {completed.returncode}"
    elif missing:
        error = "required tests did not pass: " + ", ".join(missing)
    return CaseResult(
        name="go-workspace-v2-durability-faults",
        status="passed" if error is None else "failed",
        elapsed=elapsed,
        command=command,
        returncode=completed.returncode,
        evidence=str(evidence),
        error=error,
    )


def _run_dotnet(run_root: Path) -> CaseResult:
    results_dir = run_root / "dotnet-results"
    results_dir.mkdir(parents=True, exist_ok=True)
    trx_path = results_dir / "fault-gate.trx"
    command = [
        _resolve("dotnet"),
        "test",
        str(INFRASTRUCTURE_TESTS),
        "--configuration",
        "Release",
        "--filter",
        f"FullyQualifiedName={DOTNET_TEST}",
        "--logger",
        f"trx;LogFileName={trx_path.name}",
        "--results-directory",
        str(results_dir),
    ]
    completed, elapsed = _run(command, ROOT)
    evidence = run_root / "dotnet-sidecar-recovery.log"
    output = completed.stdout + completed.stderr
    evidence.write_text(output, encoding="utf-8")
    counts: dict[str, int] | None = None
    if trx_path.is_file():
        try:
            counters = ET.parse(trx_path).find(".//{*}Counters")
            if counters is not None:
                counts = {
                    name: int(counters.attrib.get(name, "0"))
                    for name in ("total", "executed", "passed", "failed", "error")
                }
        except (ET.ParseError, OSError, ValueError):
            counts = None
    error = None
    if completed.returncode:
        error = f"dotnet test exited {completed.returncode}"
    elif counts != {
        "total": 1,
        "executed": 1,
        "passed": 1,
        "failed": 0,
        "error": 0,
    }:
        error = (
            "required Host recovery test did not report exactly 1 executed/1 passed "
            f"in TRX (observed: {counts!r})"
        )
    return CaseResult(
        name="host-degraded-auto-restart",
        status="passed" if error is None else "failed",
        elapsed=elapsed,
        command=command,
        returncode=completed.returncode,
        evidence=str(evidence),
        error=error,
    )


def _run_product(run_root: Path, package_root: Path | None = None) -> CaseResult:
    product_evidence = run_root / "real-product"
    command = [
        sys.executable,
        str(PRODUCT_RUNNER),
        "--scenario",
        PRODUCT_SCENARIO,
        "--evidence-root",
        str(product_evidence),
    ]
    if package_root is not None:
        command.extend(["--package-root", str(package_root)])
    completed, elapsed = _run(command, ROOT)
    evidence = run_root / "product-sidecar-kill.log"
    output = completed.stdout + completed.stderr
    evidence.write_text(output, encoding="utf-8")
    # The runner writes the aggregate report directly below its timestamped run
    # directory. Do not recursively traverse `_runtime`: the product removes
    # that mutable tree during shutdown and Windows raises FileNotFoundError if
    # a directory disappears between pathlib's scan steps.
    reports = sorted(product_evidence.glob("*/product-e2e-report.json"))
    observed = False
    if reports:
        try:
            payload = json.loads(reports[-1].read_text(encoding="utf-8"))
            scenarios = payload.get("scenarios", [])
            observed = (
                len(scenarios) == 1
                and scenarios[0].get("scenario") == PRODUCT_SCENARIO
                and scenarios[0].get("status") == "passed"
            )
        except (OSError, json.JSONDecodeError, AttributeError):
            observed = False
    error = None
    if completed.returncode:
        error = f"real WPF/WebView2 scenario exited {completed.returncode}"
    elif not observed:
        error = "required real sidecar-kill/reconcile scenario did not report 1/1 passed"
    return CaseResult(
        name="real-host-sidecar-kill-reconcile",
        status="passed" if error is None else "failed",
        elapsed=elapsed,
        command=command,
        returncode=completed.returncode,
        evidence=str(evidence),
        error=error,
    )


def run_gate(
    *,
    include_product: bool = True,
    package_root: Path | None = None,
) -> tuple[int, Path, list[CaseResult]]:
    run_root = EVIDENCE_ROOT / datetime.now(UTC).strftime("%Y%m%dT%H%M%S%fZ")
    run_root.mkdir(parents=True, exist_ok=False)
    results = [_run_go(run_root), _run_dotnet(run_root)]
    if include_product and all(result.status == "passed" for result in results):
        results.append(_run_product(run_root, package_root))
    elif include_product:
        results.append(
            CaseResult(
                name="real-host-sidecar-kill-reconcile",
                status="failed",
                elapsed=0,
                command=[],
                returncode=1,
                evidence=str(run_root),
                error="prerequisite fault tests failed",
            )
        )
    code = 0 if all(result.status == "passed" for result in results) else 1
    report = run_root / "fault-injection-report.json"
    report.write_text(
        json.dumps(
            {
                "status": "passed" if code == 0 else "failed",
                "generatedAt": datetime.now(UTC).isoformat(),
                "productScenarioRequired": include_product,
                "results": [asdict(result) for result in results],
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    return code, report, results


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--component-only",
        action="store_true",
        help="run only named Go/.NET component gates; release CI must not use this",
    )
    parser.add_argument("--list", action="store_true")
    parser.add_argument("--package-root", type=Path)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.list:
        print("\n".join((*GO_TESTS, DOTNET_TEST, PRODUCT_SCENARIO)))
        return 0
    code, report, results = run_gate(
        include_product=not args.component_only,
        package_root=args.package_root,
    )
    for result in results:
        print(
            f"[{result.status.upper()}] {result.name} "
            f"({result.elapsed:.2f}s) evidence={result.evidence}"
        )
        if result.error:
            print(f"  {result.error}", file=sys.stderr)
    print(f"report: {report}")
    return code


if __name__ == "__main__":
    raise SystemExit(main())
