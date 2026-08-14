from __future__ import annotations

import json
import subprocess
import xml.etree.ElementTree as ET
from pathlib import Path

from qa import fault_injection


def test_release_gate_has_all_required_named_faults() -> None:
    names = set(fault_injection.GO_TESTS)
    assert {
        "TestFaultGateDiskFullReturnsStableActionableError",
        "TestFaultGateFileLockReturnsStableActionableError",
        "TestFaultGateReadOnlyDirectoryReturnsStableActionableError",
        "TestFaultGatePortOccupiedReturnsStableActionableError",
        "TestFaultGateCorruptMigrationStopsBeforeStartup",
        "TestCapturePublishesOnlyVerifiedCompleteSnapshotAndDeduplicatesRevision",
        "TestReadPersistentMutationRevisionCoversCommittedApplyBeforeFinish",
        "TestAuthorityReceiptsCloseFileHistoryAndSnapshotKillWindows",
        "TestSnapshotRestoreCommitsAuthorityAndRecoversFailedSearchRebuildAfterRestart",
        "TestInterruptedInstalledSnapshotRestoreRollsBackBeforeReadiness",
        "TestConflictExternalAttachmentFaultRestoresOldFilesAndTableTransaction",
        "TestRuntimeReopensAndResumesConflictAtPocketBaseReceiptRevision",
    } <= names
    assert "FaultGateKilledSidecar" in fault_injection.DOTNET_TEST
    assert fault_injection.PRODUCT_SCENARIO == "10-sse-reconnect"


def test_go_gate_rejects_success_when_required_tests_are_missing(
    monkeypatch,
    tmp_path: Path,
) -> None:
    event = json.dumps(
        {
            "Action": "pass",
            "Test": fault_injection.GO_TESTS[0],
        }
    )
    completed = subprocess.CompletedProcess(
        args=["go", "test"],
        returncode=0,
        stdout=event + "\n",
        stderr="",
    )
    monkeypatch.setattr(
        fault_injection,
        "_run",
        lambda _command, _cwd: (completed, 0.01),
    )

    result = fault_injection._run_go(tmp_path)

    assert result.status == "failed"
    assert result.error is not None
    assert "required tests did not pass" in result.error


def test_go_gate_rejects_zero_test_success(
    monkeypatch,
    tmp_path: Path,
) -> None:
    completed = subprocess.CompletedProcess(
        args=["go", "test"],
        returncode=0,
        stdout="",
        stderr="",
    )
    monkeypatch.setattr(
        fault_injection,
        "_run",
        lambda _command, _cwd: (completed, 0.01),
    )

    result = fault_injection._run_go(tmp_path)

    assert result.status == "failed"
    assert result.error is not None
    assert "required tests did not pass" in result.error
    assert fault_injection.GO_TESTS[0] in result.error


def test_component_only_is_explicitly_not_the_release_default() -> None:
    args = fault_injection._parser().parse_args([])
    assert args.component_only is False


def test_product_gate_requires_exact_passed_scenario_report(
    monkeypatch,
    tmp_path: Path,
) -> None:
    def fake_run(
        command: list[str],
        _cwd: Path,
    ) -> tuple[subprocess.CompletedProcess[str], float]:
        assert command[-2:] == ["--package-root", str(tmp_path / "package")]
        report = tmp_path / "real-product" / "run" / "product-e2e-report.json"
        report.parent.mkdir(parents=True)
        report.write_text(
            json.dumps(
                {
                    "scenarios": [
                        {
                            "scenario": fault_injection.PRODUCT_SCENARIO,
                            "status": "passed",
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )
        return (
            subprocess.CompletedProcess(
                args=["product-e2e"],
                returncode=0,
                stdout="1/1 passed\n",
                stderr="",
            ),
            0.01,
        )

    monkeypatch.setattr(fault_injection, "_run", fake_run)

    result = fault_injection._run_product(tmp_path, tmp_path / "package")

    assert result.status == "passed"


def test_product_gate_does_not_traverse_disposable_runtime_evidence(
    monkeypatch,
    tmp_path: Path,
) -> None:
    def fake_run(
        _command: list[str],
        _cwd: Path,
    ) -> tuple[subprocess.CompletedProcess[str], float]:
        run_root = tmp_path / "real-product" / "run"
        volatile_runtime = run_root / "_runtime" / "workspace"
        volatile_runtime.mkdir(parents=True)
        (volatile_runtime / "transient.json").write_text("{}", encoding="utf-8")
        (run_root / "product-e2e-report.json").write_text(
            json.dumps(
                {
                    "scenarios": [
                        {
                            "scenario": fault_injection.PRODUCT_SCENARIO,
                            "status": "passed",
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )
        return subprocess.CompletedProcess(["product-e2e"], 0, "", ""), 0.01

    def reject_recursive_scan(_self: Path, _pattern: str):
        raise AssertionError("fault gate must not traverse disposable runtime evidence")

    monkeypatch.setattr(fault_injection, "_run", fake_run)
    monkeypatch.setattr(Path, "rglob", reject_recursive_scan)

    result = fault_injection._run_product(tmp_path)

    assert result.status == "passed"


def _write_trx(path: Path, *, total: int, executed: int, passed: int) -> None:
    root = ET.Element("TestRun")
    summary = ET.SubElement(root, "ResultSummary")
    ET.SubElement(
        summary,
        "Counters",
        {
            "total": str(total),
            "executed": str(executed),
            "passed": str(passed),
            "failed": "0",
            "error": "0",
        },
    )
    ET.ElementTree(root).write(path, encoding="utf-8", xml_declaration=True)


def test_dotnet_gate_requires_exact_trx_count(
    monkeypatch,
    tmp_path: Path,
) -> None:
    def fake_run(
        command: list[str],
        _cwd: Path,
    ) -> tuple[subprocess.CompletedProcess[str], float]:
        results_dir = Path(command[command.index("--results-directory") + 1])
        _write_trx(
            results_dir / "fault-gate.trx",
            total=1,
            executed=1,
            passed=1,
        )
        return (
            subprocess.CompletedProcess(command, 0, stdout="", stderr=""),
            0.01,
        )

    monkeypatch.setattr(fault_injection, "_run", fake_run)
    assert fault_injection._run_dotnet(tmp_path).status == "passed"


def test_dotnet_gate_rejects_zero_test_trx_success(
    monkeypatch,
    tmp_path: Path,
) -> None:
    def fake_run(
        command: list[str],
        _cwd: Path,
    ) -> tuple[subprocess.CompletedProcess[str], float]:
        results_dir = Path(command[command.index("--results-directory") + 1])
        _write_trx(
            results_dir / "fault-gate.trx",
            total=0,
            executed=0,
            passed=0,
        )
        return (
            subprocess.CompletedProcess(command, 0, stdout="", stderr=""),
            0.01,
        )

    monkeypatch.setattr(fault_injection, "_run", fake_run)
    result = fault_injection._run_dotnet(tmp_path)
    assert result.status == "failed"
    assert result.error is not None
    assert "exactly 1 executed/1 passed" in result.error


def test_subprocess_timeout_terminates_tree_and_returns_failure(
    monkeypatch,
    tmp_path: Path,
) -> None:
    class TimedOutProcess:
        pid = 123
        returncode = None
        calls = 0

        def communicate(self, timeout=None):
            self.calls += 1
            if self.calls == 1:
                raise subprocess.TimeoutExpired(["test"], timeout)
            return "", ""

        def poll(self):
            return self.returncode

    process = TimedOutProcess()
    terminated: list[object] = []
    monkeypatch.setattr(fault_injection.subprocess, "Popen", lambda *_a, **_k: process)
    monkeypatch.setattr(
        fault_injection,
        "_terminate_process_tree",
        lambda observed: terminated.append(observed),
    )

    completed, _elapsed = fault_injection._run(
        ["test"],
        tmp_path,
        timeout=1,
    )

    assert completed.returncode == fault_injection.TIMEOUT_RETURNCODE
    assert "timed out" in completed.stderr
    assert terminated == [process]
