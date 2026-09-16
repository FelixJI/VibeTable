"""Run isolated packaged UI stages, transporting replicas only after normal close."""

from __future__ import annotations

import json
import subprocess
import time
from datetime import UTC, datetime
from pathlib import Path

from tests.e2e.directory_replica_payloads import exchange_replica_payloads, seed_replica_payloads
from tests.e2e.packaged_replica_hosts import (
    OpenedReplicaHost,
    ReplicaHostSpec,
    opened_replica_hosts,
)

SCENARIO_ID = "24-directory-replica-conflict"
NODE_RUNNER = Path(__file__).with_name("webview_product_scenarios.mjs")


class _StageError(RuntimeError):
    pass


def _run_stage(
    node: str,
    stage: str,
    host: OpenedReplicaHost,
    state_path: Path,
    stages: list[dict[str, object]],
) -> None:
    command = [
        node,
        str(NODE_RUNNER),
        "--scenario",
        SCENARIO_ID,
        "--replica-stage",
        stage,
        "--replica-state",
        str(state_path),
        "--cdp-url",
        host.cdp_url,
        "--evidence-dir",
        str(host.evidence_dir),
        "--controls-dir",
        str(host.controls_dir),
        "--data-root",
        str(host.local_data_root),
    ]
    result_path = host.evidence_dir / f"{SCENARIO_ID}-result.json"
    result: dict[str, object] = {"scenario": SCENARIO_ID, "stage": stage, "status": "failed"}
    stages.append(result)
    stdout: str | bytes | None = None
    stderr: str | bytes | None = None
    primary: Exception | None = None
    try:
        try:
            completed = subprocess.run(
                command, capture_output=True, text=True, encoding="utf-8", timeout=180, check=False
            )
            stdout, stderr = completed.stdout, completed.stderr
            result["nodeExitCode"] = completed.returncode
        except subprocess.TimeoutExpired as exc:
            stdout, stderr = exc.stdout, exc.stderr
            result["error"] = {
                "code": "NODE_RUNNER_TIMEOUT",
                "timeoutSeconds": 180,
                "message": str(exc),
            }
            raise
        if not result_path.is_file():
            raise _StageError(f"{stage}: Node result missing")
        observed = json.loads(result_path.read_text(encoding="utf-8"))
        if not isinstance(observed, dict) or (
            observed.get("scenario") != SCENARIO_ID or observed.get("stage") != stage
        ):
            raise _StageError(f"{stage}: Node result identity mismatch")
        result.update(observed)
        if completed.returncode != 0 or result.get("status") != "passed":
            raise _StageError(f"{stage}: exit={completed.returncode}, result={observed!r}")
        diagnostics = result.get("bridgeDiagnostics")
        if not isinstance(diagnostics, dict) or any(
            not isinstance(diagnostics.get(key), list)
            for key in ("roundTrips", "failures", "pending")
        ):
            raise _StageError(f"{stage}: complete bridge diagnostics required")
        if diagnostics["failures"] or diagnostics["pending"]:
            raise _StageError(f"{stage}: bridge diagnostics contain failures or pending requests")
    except Exception as exc:
        primary = exc
        result["status"] = "failed"
        result.setdefault("error", {"code": "REPLICA_STAGE_FAILED", "message": str(exc)})
        raise
    finally:
        if isinstance(primary, subprocess.TimeoutExpired) and result_path.is_file():
            try:
                partial = json.loads(result_path.read_text(encoding="utf-8"))
                if not isinstance(partial, dict) or (
                    partial.get("scenario") != SCENARIO_ID or partial.get("stage") != stage
                ):
                    raise ValueError("partial Node result identity mismatch")
                result["nodeResult"] = partial
                if isinstance(partial.get("bridgeDiagnostics"), dict):
                    result["bridgeDiagnostics"] = partial["bridgeDiagnostics"]
            except (OSError, ValueError) as read_error:
                primary.add_note(f"partial Node evidence unavailable: {read_error}")
        if not isinstance(result.get("bridgeDiagnostics"), dict):
            result["bridgeDiagnosticsError"] = "Node did not retain usable bridge diagnostics"
        try:
            for name, output in (("stdout", stdout), ("stderr", stderr)):
                # TimeoutExpired carries bytes even when subprocess.run uses text=True.
                (host.evidence_dir / f"runner-{name}.log").write_bytes(
                    output if isinstance(output, bytes) else (output or "").encode("utf-8")
                )
            (host.evidence_dir / "stage-result.json").write_text(
                json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
            )
        except OSError as write_error:
            if primary is None:
                raise
            primary.add_note(f"writing Node stage evidence also failed: {write_error}")


def run_directory_replica_conflict(
    *, package_root: Path, evidence_root: Path, node: str
) -> dict[str, object]:
    scenario_dir = (evidence_root / SCENARIO_ID).resolve()
    started = time.monotonic()
    runtime = (evidence_root / "_runtime" / "24").resolve()
    result: dict[str, object] = {
        "scenario": SCENARIO_ID,
        "status": "failed",
        "startedAt": datetime.now(UTC).isoformat(),
        "evidenceDirectory": str(scenario_dir),
    }
    stages: list[dict[str, object]] = []
    result["stages"] = stages
    try:
        scenario_dir.mkdir(parents=True)
        specs = tuple(
            ReplicaHostSpec(label, runtime / label, runtime / f"selected-{label}")
            for label in ("left", "right")
        )
        left, right = specs
        for spec in specs:
            spec.selected_root.mkdir(parents=True)
        state_path = scenario_dir / "journey-state.json"

        def run_phase(phase: str, steps: tuple[tuple[str, str], ...]) -> None:
            primary: Exception | None = None
            try:
                with opened_replica_hosts(
                    package_root, scenario_dir / phase, (left, right)
                ) as hosts:
                    try:
                        for stage, label in steps:
                            _run_stage(node, stage, hosts[label], state_path, stages)
                    except Exception as exc:
                        # Stop stage execution now, but let H0 request normal host close.
                        primary = exc
            except Exception as close_error:
                if primary is None:
                    raise
                primary.add_note(f"normal host close also failed: {close_error}")
                for note in getattr(close_error, "__notes__", ()):
                    primary.add_note(note)
            if primary is not None:
                raise primary

        run_phase("seed", (("seed", "left"),))
        result["seededPayloads"] = seed_replica_payloads(left.selected_root, right.selected_root)
        run_phase("fork", (("fork-left", "left"), ("fork-right", "right")))
        exchanged = exchange_replica_payloads(left.selected_root, right.selected_root)
        result["exchange"] = {
            "workspaceId": exchanged.workspace_id,
            "addedToLeft": exchanged.added_to_left,
            "addedToRight": exchanged.added_to_right,
        }
        run_phase("resolve", (("resolve", "left"),))
        # Reuse the same persisted product data after normal process exit, not a UI-only refresh.
        run_phase("reopen", (("verify-resolved", "left"),))
        result["status"] = "passed"
    except Exception as exc:
        result["error"] = {
            "code": "REPLICA_STAGE_FAILED"
            if isinstance(exc, _StageError)
            else "REPLICA_INFRASTRUCTURE_FAILED",
            "message": str(exc),
            "name": type(exc).__name__,
            "notes": list(getattr(exc, "__notes__", ())),
        }
    result["finishedAt"] = datetime.now(UTC).isoformat()
    result["durationMs"] = round((time.monotonic() - started) * 1000, 2)
    # Keep ordinary report consumers compatible; full stage results retain
    # which isolated host produced each diagnostic sample.
    diagnostics: dict[str, list[object]] = {
        "roundTrips": [],
        "failures": [],
        "pending": [],
        "acknowledgedFailures": [],
    }
    for stage_result in stages:
        observed = stage_result.get("bridgeDiagnostics")
        if isinstance(observed, dict):
            for key, values in diagnostics.items():
                samples = observed.get(key)
                if isinstance(samples, list):
                    values.extend(samples)
    result["bridgeDiagnostics"] = diagnostics
    result["bridgeDiagnosticsUnavailableStages"] = [
        stage["stage"] for stage in stages if not isinstance(stage.get("bridgeDiagnostics"), dict)
    ]
    if scenario_dir.is_dir():
        try:
            (scenario_dir / f"{SCENARIO_ID}-result.json").write_text(
                json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
            )
        except OSError as report_error:
            message = f"writing final scenario report failed: {report_error}"
            primary = result.get("error")
            if isinstance(primary, dict):
                notes = primary.get("notes")
                primary["notes"] = [*(notes if isinstance(notes, list) else []), message]
            else:
                result["status"] = "failed"
                result["error"] = {
                    "code": "REPLICA_INFRASTRUCTURE_FAILED",
                    "name": type(report_error).__name__,
                    "message": message,
                    "notes": [],
                }
    return result
