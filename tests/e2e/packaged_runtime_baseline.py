"""Measure one real packaged workspace lifecycle without performance thresholds."""

from __future__ import annotations

import argparse
import json
import subprocess
import time
from collections.abc import Callable
from pathlib import Path
from typing import Literal, TypedDict, cast

from scripts.node_toolchain import ensure_node
from scripts.qa.windows_process_scope import ProcessWorkingSetSnapshot
from tests.e2e.packaged_host_lifecycle import (
    WORKSPACE_ID,
    WorkspaceLifecycleEvidence,
    WorkspaceOpenEvidence,
    opened_packaged_workspace,
)
from tests.e2e.runtime_measurement_baseline import (
    BaselineErrorEvidence,
    BaselineMeasurementError,
    FoundationMeasurements,
    PackagedRuntimeIdentity,
    ReleaseCandidateEvidence,
    RuntimePhaseDurations,
    RuntimePhaseTimeline,
    VerifiedRuntimeCandidate,
    build_runtime_measurement_foundation_report,
    verify_runtime_candidate,
)

ROOT = Path(__file__).resolve().parents[2]
NODE_PROBE = Path(__file__).with_name("packaged_runtime_probe.mjs")
QUIET_WINDOW_SECONDS = 1.0


class FirstTableEvidence(TypedDict):
    status: Literal["passed"]
    tableId: str
    sameTableIdentity: Literal[True]
    tableSummaryVisible: Literal[True]
    errorOverlayVisible: Literal[False]
    rowCount: Literal[0]
    stableWindowMs: int


class PackagedWorkspaceReport(TypedDict):
    workspaceId: str
    sessionEpoch: int
    sessionState: Literal["openedWritable"]
    openOutcome: Literal["opened-writable"]
    writableSessionPublished: Literal[True]


class PackagedLifecycleReport(TypedDict):
    normalExitRequested: Literal[True]
    hostExitCode: Literal[0]
    membersAfterExit: list[dict[str, object]]
    portsReleased: Literal[True]
    ownerLeaseStableHandleClosed: Literal[True]
    errors: list[str]
    status: Literal["passed"]


class PackagedRuntimeCoverage(TypedDict):
    phaseTimeline: Literal["measured"]
    processWorkingSet: Literal["quiet-window-endpoint"]
    packageFootprint: Literal["measured"]
    packagedRun: Literal["measured"]
    rpcLatency: Literal["not-measured"]
    recovery: Literal["not-measured"]


class PackagedRuntimeSampling(TypedDict):
    processLifecycle: Literal["fresh"]
    webView2UserData: Literal["fresh"]
    osFileCache: Literal["not-cleared"]
    firstTableStableWindowMs: int
    workingSetQuietWindowMs: int


class PackagedRuntimeBaselineReport(TypedDict):
    contractVersion: Literal["1.0"]
    evidenceKind: Literal["packaged-runtime-baseline"]
    status: Literal["passed"]
    coverage: PackagedRuntimeCoverage
    identity: PackagedRuntimeIdentity
    releaseCandidate: ReleaseCandidateEvidence
    sampling: PackagedRuntimeSampling
    workspace: PackagedWorkspaceReport
    firstTable: FirstTableEvidence
    measurements: FoundationMeasurements
    lifecycle: PackagedLifecycleReport
    errors: list[BaselineErrorEvidence]


Probe = Callable[[str, Path], dict[str, object]]
ProbeFactory = Callable[[], Probe]


def _paths_overlap(left: Path, right: Path) -> bool:
    return left == right or left.is_relative_to(right) or right.is_relative_to(left)


def _validate_run_roots(
    *,
    package_root: Path,
    workspace_root: Path,
    evidence_root: Path,
) -> None:
    pairs = (
        ("package root", package_root, "workspace root", workspace_root),
        ("package root", package_root, "evidence root", evidence_root),
        ("workspace root", workspace_root, "evidence root", evidence_root),
    )
    for left_label, left, right_label, right in pairs:
        if _paths_overlap(left, right):
            raise BaselineMeasurementError(
                "RUNTIME_PATH_OVERLAP",
                f"{left_label} and {right_label} must not overlap",
            )


def _unsafe_report_path(
    report_path: Path,
    *,
    package_root: Path,
    package_archive: Path,
    build_identity_path: Path,
    workspace_root: Path,
    evidence_root: Path,
) -> bool:
    checksum_path = package_archive.with_suffix(package_archive.suffix + ".sha256").resolve()
    return any(
        _paths_overlap(report_path, root)
        for root in (
            package_root,
            package_archive,
            checksum_path,
            build_identity_path,
            workspace_root,
            evidence_root,
        )
    )


def _prepare_workspace(workspace_root: Path) -> None:
    if workspace_root.exists():
        raise BaselineMeasurementError(
            "WORKSPACE_ROOT_NOT_FRESH",
            "runtime baseline workspace root must be fresh",
        )
    metadata = workspace_root / ".vibetable"
    for name in (
        "data",
        "topology",
        "objects",
        "audit",
        "snapshots",
        "coordination",
        "quarantine",
        "temp",
    ):
        (metadata / name).mkdir(parents=True)
    manifest = {
        "contractVersion": "2.0",
        "formatVersion": 2,
        "workspaceId": WORKSPACE_ID,
        "displayName": "Packaged runtime baseline",
        "createdAt": "2026-08-29T00:00:00Z",
        "storageMode": "direct",
        "encryptionMode": "convenient",
        "repositoryFormat": "kopia-v3",
        "topologySchemaVersion": 1,
        "businessSchemaVersion": 1,
        "importedFromWorkspaceId": None,
        "sourceSnapshotId": None,
    }
    (metadata / "workspace.json").write_text(
        json.dumps(manifest, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )


def _read_probe_report(path: Path) -> dict[str, object]:
    try:
        decoded = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise BaselineMeasurementError(
            "FIRST_TABLE_PROBE_REPORT_INVALID",
            "first table probe did not write a valid JSON report",
        ) from exc
    if not isinstance(decoded, dict) or not all(isinstance(key, str) for key in decoded):
        raise BaselineMeasurementError(
            "FIRST_TABLE_PROBE_REPORT_INVALID",
            "first table probe report must be a JSON object",
        )
    return cast(dict[str, object], decoded)


def _strict_positive_int(value: object, *, label: str) -> int:
    if type(value) is not int or value <= 0:
        raise BaselineMeasurementError(
            "FIRST_TABLE_PROBE_REPORT_INVALID",
            f"{label} must be a positive integer",
        )
    return value


def _first_table_evidence(value: dict[str, object]) -> FirstTableEvidence:
    expected_fields = {
        "contractVersion",
        "evidenceKind",
        "status",
        "tableId",
        "sameTableIdentity",
        "tableSummaryVisible",
        "errorOverlayVisible",
        "rowCount",
        "stableWindowMs",
        "errors",
    }
    table_id = value.get("tableId")
    errors = value.get("errors")
    if (
        set(value) != expected_fields
        or value.get("contractVersion") != "1.0"
        or value.get("evidenceKind") != "packaged-runtime-ui-probe"
        or value.get("status") != "passed"
        or not isinstance(table_id, str)
        or not table_id.startswith("tbl_")
        or value.get("sameTableIdentity") is not True
        or value.get("tableSummaryVisible") is not True
        or value.get("errorOverlayVisible") is not False
        or type(value.get("rowCount")) is not int
        or value.get("rowCount") != 0
        or not isinstance(errors, list)
        or errors
    ):
        raise BaselineMeasurementError(
            "FIRST_TABLE_PROBE_REPORT_INVALID",
            "first table probe report does not match the closed success contract",
        )
    return {
        "status": "passed",
        "tableId": table_id,
        "sameTableIdentity": True,
        "tableSummaryVisible": True,
        "errorOverlayVisible": False,
        "rowCount": 0,
        "stableWindowMs": _strict_positive_int(
            value.get("stableWindowMs"), label="first table stable window"
        ),
    }


def run_first_table_probe(
    node_executable: Path,
    cdp_url: str,
    report_path: Path,
) -> dict[str, object]:
    try:
        completed = subprocess.run(
            [
                str(node_executable),
                str(NODE_PROBE),
                "--cdp-url",
                cdp_url,
                "--json-report",
                str(report_path),
            ],
            cwd=ROOT,
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=90,
        )
    except subprocess.TimeoutExpired as exc:
        raise BaselineMeasurementError(
            "FIRST_TABLE_PROBE_FAILED",
            "first table probe timed out",
        ) from exc
    if completed.returncode != 0:
        raise BaselineMeasurementError(
            "FIRST_TABLE_PROBE_FAILED",
            "first table probe did not prove a stable empty table",
        )
    report = _read_probe_report(report_path)
    _first_table_evidence(report)
    return report


def prepare_first_table_probe() -> Probe:
    """Resolve the Node toolchain before the measured Host lifecycle starts."""

    node_executable = ensure_node(ROOT)

    def prepared(cdp_url: str, report_path: Path) -> dict[str, object]:
        return run_first_table_probe(node_executable, cdp_url, report_path)

    return prepared


def _workspace_report(value: WorkspaceOpenEvidence) -> PackagedWorkspaceReport:
    workspace_id = value["workspaceId"]
    session_epoch = value["sessionEpoch"]
    if (
        not isinstance(workspace_id, str)
        or not workspace_id
        or type(session_epoch) is not int
        or session_epoch <= 0
        or value["sessionState"] != "openedWritable"
        or value["openOutcome"] != "opened-writable"
        or value["writableSessionPublished"] is not True
    ):
        raise BaselineMeasurementError(
            "WORKSPACE_EVIDENCE_INCOMPLETE",
            "packaged workspace was not verified writable",
        )
    return {
        "workspaceId": workspace_id,
        "sessionEpoch": session_epoch,
        "sessionState": "openedWritable",
        "openOutcome": "opened-writable",
        "writableSessionPublished": True,
    }


def _lifecycle_report(value: WorkspaceLifecycleEvidence) -> PackagedLifecycleReport:
    if (
        value["status"] != "passed"
        or value["normalExitRequested"] is not True
        or type(value["hostExitCode"]) is not int
        or value["hostExitCode"] != 0
        or value["membersAfterExit"]
        or value["portsReleased"] is not True
        or value["ownerLeaseStableHandleClosed"] is not True
        or value["errors"]
    ):
        raise BaselineMeasurementError(
            "HOST_LIFECYCLE_INCOMPLETE",
            "packaged Host did not complete a clean normal lifecycle",
        )
    return {
        "normalExitRequested": True,
        "hostExitCode": 0,
        "membersAfterExit": [],
        "portsReleased": True,
        "ownerLeaseStableHandleClosed": True,
        "errors": [],
        "status": "passed",
    }


def _passed_report(
    *,
    candidate: VerifiedRuntimeCandidate,
    phases: RuntimePhaseDurations,
    working_sets: ProcessWorkingSetSnapshot,
    workspace: WorkspaceOpenEvidence,
    first_table: FirstTableEvidence,
    lifecycle: WorkspaceLifecycleEvidence,
) -> PackagedRuntimeBaselineReport:
    foundation = build_runtime_measurement_foundation_report(
        package_root=candidate.package_root,
        phases=phases,
        working_sets=working_sets,
    )
    identity = candidate.identity
    if foundation["identity"] != {
        "release": identity["release"],
        "publishLayoutProtocolVersion": identity["publishLayoutProtocolVersion"],
    }:
        raise BaselineMeasurementError(
            "CANDIDATE_MEASUREMENT_MISMATCH",
            "runtime measurements do not describe the verified candidate",
        )
    return {
        "contractVersion": "1.0",
        "evidenceKind": "packaged-runtime-baseline",
        "status": "passed",
        "coverage": {
            "phaseTimeline": "measured",
            "processWorkingSet": "quiet-window-endpoint",
            "packageFootprint": "measured",
            "packagedRun": "measured",
            "rpcLatency": "not-measured",
            "recovery": "not-measured",
        },
        "identity": identity,
        "releaseCandidate": candidate.release_candidate,
        "sampling": {
            "processLifecycle": "fresh",
            "webView2UserData": "fresh",
            "osFileCache": "not-cleared",
            "firstTableStableWindowMs": first_table["stableWindowMs"],
            "workingSetQuietWindowMs": int(QUIET_WINDOW_SECONDS * 1_000),
        },
        "workspace": _workspace_report(workspace),
        "firstTable": first_table,
        "measurements": foundation["measurements"],
        "lifecycle": _lifecycle_report(lifecycle),
        "errors": [],
    }


def _failed_report(code: str, message: str) -> dict[str, object]:
    return {
        "contractVersion": "1.0",
        "evidenceKind": "packaged-runtime-baseline",
        "status": "failed",
        "coverage": {
            "phaseTimeline": "not-measured",
            "processWorkingSet": "not-measured",
            "packageFootprint": "not-measured",
            "packagedRun": "not-measured",
            "rpcLatency": "not-measured",
            "recovery": "not-measured",
        },
        "identity": None,
        "releaseCandidate": None,
        "sampling": {
            "processLifecycle": "not-measured",
            "webView2UserData": "not-measured",
            "osFileCache": "not-cleared",
            "firstTableStableWindowMs": None,
            "workingSetQuietWindowMs": None,
        },
        "workspace": None,
        "firstTable": None,
        "measurements": None,
        "lifecycle": None,
        "errors": [{"code": code, "message": message}],
    }


def _write_report(path: Path, report: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def run_packaged_runtime_baseline(
    *,
    package_root: Path,
    package_archive: Path,
    workspace_root: Path,
    evidence_root: Path,
    build_identity_path: Path,
    expected_source_sha: str,
    json_report: Path,
    monotonic_ns: Callable[[], int] = time.monotonic_ns,
    sleep: Callable[[float], None] = time.sleep,
    probe_factory: ProbeFactory = prepare_first_table_probe,
) -> dict[str, object]:
    """Own measurement freshness and persist passed only after normal cleanup."""

    package_root = package_root.resolve()
    package_archive = package_archive.resolve()
    build_identity_path = build_identity_path.resolve()
    workspace_root = workspace_root.resolve()
    evidence_root = evidence_root.resolve()
    json_report = json_report.resolve()
    if _unsafe_report_path(
        json_report,
        package_root=package_root,
        package_archive=package_archive,
        build_identity_path=build_identity_path,
        workspace_root=workspace_root,
        evidence_root=evidence_root,
    ):
        return _failed_report(
            "RUNTIME_REPORT_PATH_UNSAFE",
            "runtime baseline report must not overlap candidate assets or run roots",
        )

    try:
        candidate = verify_runtime_candidate(
            package_root=package_root,
            package_archive=package_archive,
            build_identity_path=build_identity_path,
            expected_source_sha=expected_source_sha,
        )
        _validate_run_roots(
            package_root=candidate.package_root,
            workspace_root=workspace_root,
            evidence_root=evidence_root,
        )
        probe = probe_factory()
        _prepare_workspace(workspace_root)
        timeline = RuntimePhaseTimeline(monotonic_ns)
        with opened_packaged_workspace(
            candidate.package_root,
            workspace_root,
            evidence_root,
            observer=timeline,
        ) as session:
            first_table = _first_table_evidence(
                probe(session.cdp_url, evidence_root / "ui-probe.json")
            )
            timeline.first_table_stable()
            sleep(QUIET_WINDOW_SECONDS)
            working_sets = session.working_set_snapshot()
        phases = timeline.finish()
        passed = _passed_report(
            candidate=candidate,
            phases=phases,
            working_sets=working_sets,
            workspace=session.workspace_evidence,
            first_table=first_table,
            lifecycle=session.lifecycle,
        )
        report: dict[str, object] = dict(passed)
    except Exception as exc:
        code = (
            exc.code
            if isinstance(exc, BaselineMeasurementError)
            else "PACKAGED_RUNTIME_BASELINE_FAILED"
        )
        report = _failed_report(code, str(exc))
    _write_report(json_report, report)
    return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-root", type=Path, required=True)
    parser.add_argument("--package-archive", type=Path, required=True)
    parser.add_argument("--workspace-root", type=Path, required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--build-identity", type=Path, required=True)
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--json-report", type=Path, required=True)
    args = parser.parse_args(argv)
    report = run_packaged_runtime_baseline(
        package_root=args.package_root.resolve(),
        package_archive=args.package_archive.resolve(),
        workspace_root=args.workspace_root.resolve(),
        evidence_root=args.evidence_root.resolve(),
        build_identity_path=args.build_identity.resolve(),
        expected_source_sha=args.source_sha,
        json_report=args.json_report.resolve(),
    )
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
